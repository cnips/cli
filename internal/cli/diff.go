package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cnips/cli/internal/platform"
	"github.com/cnips/cli/internal/project"
	"github.com/cnips/cli/internal/serializer"
	"github.com/spf13/cobra"
)

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Show differences between local files and a cnips workspace",
	Long: `Shows the canonical file differences between the local project and the
current state of a target cnips workspace. This is intended to preview what a
dev-loop push would change.`,
	RunE: runDiff,
}

func init() {
	diffCmd.Flags().String("api-url", "http://localhost:8090", "Base URL of mgmt-srv")
	diffCmd.Flags().String("workspace", "default", "Workspace ID to compare against")
	diffCmd.Flags().String("tenant-key", "", "Value for the x-tenant-key header")
	diffCmd.Flags().String("token", "", "Bearer token for Authorization header")
	diffCmd.Flags().String("format", "unified", "Diff format: unified or summary")
	diffCmd.Flags().Bool("exit-code", false, "Exit with code 1 when differences are found")
	diffCmd.Flags().Bool("serve-resolver", false, "Deprecated: write editor conflict markers instead of only printing the diff")
	rootCmd.AddCommand(diffCmd)
}

func runDiff(cmd *cobra.Command, _ []string) error {
	root := project.MustFindRoot()
	apiURL, workspace, tenantKey, token, err := resolveAuthenticatedPlatformFlags(cmd)
	if err != nil {
		return err
	}
	format, _ := cmd.Flags().GetString("format")
	exitCode, _ := cmd.Flags().GetBool("exit-code")
	serveResolver, _ := cmd.Flags().GetBool("serve-resolver")

	if err := refreshLockFromLocal(root); err != nil {
		return err
	}
	client := platform.NewClient(apiURL, tenantKey, token)
	manifest, _, err := readComparisonBase(root, client, workspace, true)
	if err != nil {
		return err
	}
	comparison, err := compareLocalBaseRemote(root, client, workspace, manifest)
	if err != nil {
		return err
	}
	changes := comparison.All
	if len(changes) == 0 {
		fmt.Printf("No differences against %s workspace=%s\n", apiURL, workspace)
		return nil
	}
	fmt.Printf("Local-only: %d  Cnips-only: %d  Conflicts: %d\n", len(comparison.LocalOnly), len(comparison.RemoteOnly), len(comparison.Conflicts))
	if format == "summary" {
		printDiffSummary(changes)
	} else {
		printUnifiedDiff(changes)
	}
	if serveResolver && !exitCode {
		return printSyncLinkWithResolver(root, apiURL, workspace, comparison)
	}
	printSyncLink(apiURL, workspace, comparison)
	if exitCode {
		os.Exit(1)
	}
	return nil
}

type fileChange struct {
	Path              string
	Status            string
	LocalExists       bool
	RemoteExists      bool
	Local             []string
	Remote            []string
	Conflict          []string
	ConflictChunks    []conflictChunks
	RemoteDigest      string
	RemoteFingerprint string
}

func writeRemoteSnapshot(client *platform.Client, workspace, root string) error {
	workspaceIDs := []string{workspace}
	txList, srcList, dstList, fnList, gvList, configList, pipelineList, err := fetchWorkspaceLists(client, workspaceIDs)
	if err != nil {
		return err
	}
	appList, _, err := fetchApps(client)
	if err != nil {
		return err
	}

	txSlugs := uniqueSlugs(txList, func(t platform.Transformation) string { return t.ID }, func(t platform.Transformation) string { return t.Name })
	srcSlugs := uniqueSlugs(srcList, func(s platform.Source) string { return s.ID }, func(s platform.Source) string { return s.Name })
	dstSlugs := uniqueSlugs(dstList, func(d platform.Destination) string { return d.ID }, func(d platform.Destination) string { return d.Name })
	fnSlugs := uniqueSlugs(fnList, func(f platform.Function) string { return f.ID }, func(f platform.Function) string { return f.Name })
	appSlugs := uniqueSlugs(appList, func(a platform.App) string { return a.ID }, func(a platform.App) string { return a.Name })
	gvSlugs := uniqueSlugs(gvList, func(gv platform.GlobalVariable) string { return gv.ID }, func(gv platform.GlobalVariable) string { return gv.Key })
	configSlugs := uniqueSlugs(configList, func(c platform.Configuration) string { return c.ID }, func(c platform.Configuration) string { return c.Name })
	pipelineSlugs := uniqueSlugs(pipelineList, func(p platform.Pipeline) string { return p.ID }, func(p platform.Pipeline) string { return p.Name })
	txConfig := configMapByID(txList, func(t platform.Transformation) string { return t.ID }, func(t platform.Transformation) []platform.ConfigItem { return t.Config })
	srcConfig := configMapByID(srcList, func(s platform.Source) string { return s.ID }, func(s platform.Source) []platform.ConfigItem { return s.Config })
	dstConfig := configMapByID(dstList, func(d platform.Destination) string { return d.ID }, func(d platform.Destination) []platform.ConfigItem { return d.Config })
	appConfig := configMapByID(appList, func(a platform.App) string { return a.ID }, func(a platform.App) []platform.ConfigItem { return a.Config })
	globalByID := globalVariablesByID(gvList)
	const snapshotVersionWorkers = 32
	txVersions := fetchComponentVersions(client, "", snapshotVersionWorkers, txList, func(t platform.Transformation) (string, string, string) {
		return t.WorkspaceID, t.ID, platformTransformationType(t)
	})
	srcVersions := fetchComponentVersions(client, "", snapshotVersionWorkers, srcList, func(s platform.Source) (string, string, string) {
		if !s.IsExtractor() {
			return "", "", ""
		}
		return s.WorkspaceID, s.ID, "EXTRACTOR"
	})
	dstVersions := fetchComponentVersions(client, "", snapshotVersionWorkers, dstList, func(d platform.Destination) (string, string, string) {
		return d.WorkspaceID, d.ID, "DESTINATION"
	})

	for i := range txList {
		if _, err := serializer.WriteTransformationVersionsAs(root, &txList[i], txSlugs[txList[i].ID], txVersions[txList[i].ID]); err != nil {
			return err
		}
	}
	for i := range srcList {
		if _, err := serializer.WriteSourceVersionsAs(root, &srcList[i], srcSlugs[srcList[i].ID], srcVersions[srcList[i].ID]); err != nil {
			return err
		}
	}
	for i := range dstList {
		if _, err := serializer.WriteDestinationVersionsAs(root, &dstList[i], dstSlugs[dstList[i].ID], dstVersions[dstList[i].ID]); err != nil {
			return err
		}
	}
	for i := range fnList {
		if _, err := serializer.WriteFunctionAs(root, &fnList[i], fnSlugs[fnList[i].ID]); err != nil {
			return err
		}
	}
	for i := range gvList {
		if _, err := serializer.WriteGlobalVariableAs(root, &gvList[i], gvSlugs[gvList[i].ID]); err != nil {
			return err
		}
	}
	for i := range configList {
		if _, err := serializer.WriteConfigurationAs(root, &configList[i], configSlugs[configList[i].ID]); err != nil {
			return err
		}
	}
	for i := range pipelineList {
		p := &pipelineList[i]
		if err := serializer.WritePipelineAs(root, p, pipelineSlugs[p.ID], txSlugs, srcSlugs, dstSlugs, txConfig, srcConfig, dstConfig, appSlugs, appConfig, pipelineGlobalVariables(p, globalByID)); err != nil {
			return err
		}
	}
	return nil
}

func compareTrees(localRoot, remoteRoot string) ([]fileChange, error) {
	local, err := collectComparableFiles(localRoot)
	if err != nil {
		return nil, err
	}
	remote, err := collectComparableFiles(remoteRoot)
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for path := range local {
		paths[path] = true
	}
	for path := range remote {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)

	var changes []fileChange
	for _, path := range ordered {
		l, lok := local[path]
		r, rok := remote[path]
		switch {
		case !lok:
			changes = append(changes, fileChange{Path: path, Status: "add-remote", RemoteExists: true, Remote: splitLines(r)})
		case !rok:
			changes = append(changes, fileChange{Path: path, Status: "add-local", LocalExists: true, Local: splitLines(l)})
		case l != r:
			changes = append(changes, fileChange{Path: path, Status: "modify", LocalExists: true, RemoteExists: true, Local: splitLines(l), Remote: splitLines(r)})
		}
	}
	return changes, nil
}

func collectComparableFiles(root string) (map[string]string, error) {
	out := map[string]string{}
	allowed := []string{"pipelines", "transformations", "approvals", "switches", "decisions", "sources", "destinations", "functions", "globalvariables", "configurations", "cnips.lock"}
	for _, rel := range allowed {
		path := filepath.Join(root, rel)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		if err := filepath.WalkDir(path, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == ".cnips" || d.Name() == "node_modules" || d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if !isComparableFile(path) {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relPath, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(relPath)] = strings.ReplaceAll(string(data), "\r\n", "\n")
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func isComparableFile(path string) bool {
	switch filepath.Base(path) {
	case "go.sum", "package-lock.json":
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml", ".js", ".ts", ".go", ".py", ".json", ".txt", ".mod":
		return true
	default:
		return false
	}
}

func printDiffSummary(changes []fileChange) {
	fmt.Printf("%d file(s) differ\n", len(changes))
	for _, change := range changes {
		fmt.Printf("  %-10s %s\n", change.Status, change.Path)
	}
}

func printUnifiedDiff(changes []fileChange) {
	for _, change := range changes {
		fmt.Printf("diff --cnips %s\n", change.Path)
		fmt.Printf("--- remote/%s\n", change.Path)
		fmt.Printf("+++ local/%s\n", change.Path)
		switch change.Status {
		case "add-local":
			for _, line := range change.Local {
				fmt.Printf("+%s\n", line)
			}
		case "add-remote":
			for _, line := range change.Remote {
				fmt.Printf("-%s\n", line)
			}
		default:
			printSimpleLineDiff(change.Remote, change.Local)
		}
	}
}

func printSimpleLineDiff(remote, local []string) {
	max := len(remote)
	if len(local) > max {
		max = len(local)
	}
	for i := 0; i < max; i++ {
		var r, l string
		if i < len(remote) {
			r = remote[i]
		}
		if i < len(local) {
			l = local[i]
		}
		if r == l {
			fmt.Printf(" %s\n", r)
			continue
		}
		if i < len(remote) {
			fmt.Printf("-%s\n", r)
		}
		if i < len(local) {
			fmt.Printf("+%s\n", l)
		}
	}
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
