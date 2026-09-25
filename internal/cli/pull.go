package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/platform"
	"github.com/cnips/cli/internal/project"
	"github.com/cnips/cli/internal/serializer"
	"github.com/spf13/cobra"
)

var pullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull pipelines, components, and functions from a local mgmt-srv instance",
	Long: `Fetches all pipelines, transformations, sources, destinations, and functions
from a running local mgmt-srv API and writes them as canonical YAML files into
the current cnips project directory.

Example:
  cnips pull --api-url http://localhost:8090 --workspace default \
             --tenant-key my-tenant --token <bearer-token>

The --token and --tenant-key flags are required only when the local mgmt-srv
has authentication enabled.  For a fully local dev setup with auth disabled,
omit them.`,
	RunE: runPull,
}

func init() {
	pullCmd.Flags().String("api-url", "http://localhost:8090", "Base URL of the local mgmt-srv (e.g. http://localhost:8090)")
	pullCmd.Flags().String("workspace", "default", "Workspace ID to pull from")
	pullCmd.Flags().String("tenant-key", "", "Value for the x-tenant-key header (required when auth is on)")
	pullCmd.Flags().String("token", "", "Bearer token for Authorization header (required when auth is on)")
	pullCmd.Flags().Bool("dry-run", false, "Print a summary of what would be written without writing any files")
	pullCmd.Flags().Bool("all-workspaces", false, "Pull from every workspace accessible to the current token")
	pullCmd.Flags().Bool("skip-versions", false, "Skip component version lookups and pull only current component sources")
	pullCmd.Flags().Int("version-workers", 32, "Number of concurrent component version lookups during pull")
	rootCmd.AddCommand(pullCmd)
}

func runPull(cmd *cobra.Command, _ []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if dryRun {
		return runPullInternal(cmd)
	}
	return runWithLoader("Pulling", true, func() error { return runPullInternal(cmd) })
}

func runPullInternal(cmd *cobra.Command) error {
	root := project.MustFindRoot()

	apiURL, workspace, tenantKey, token, err := resolveAuthenticatedPlatformFlags(cmd)
	if err != nil {
		return err
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	allWorkspaces, _ := cmd.Flags().GetBool("all-workspaces")
	skipVersions, _ := cmd.Flags().GetBool("skip-versions")
	versionWorkers, _ := cmd.Flags().GetInt("version-workers")

	apiURL = strings.TrimRight(apiURL, "/")
	token = normalizeToken(token)

	client := platform.NewClient(apiURL, tenantKey, token)
	workspaceIDs, workspaceLabel, err := resolvePullWorkspaces(client, workspace, allWorkspaces)
	if err != nil {
		return err
	}

	var previousManifest *pullManifest
	if len(workspaceIDs) == 1 {
		manifest, _, err := readComparisonBase(root, client, workspaceIDs[0], false)
		if err != nil {
			return err
		}
		if isFreshHydrationProject(root) {
			// Runtime manifests live outside the repository and can survive when a
			// project directory is deleted and initialized again at the same path.
			// A freshly initialized tree is authoritative evidence that this is a
			// hydration, so stale state must never suppress the download.
			fmt.Printf("Fresh cnips project detected; hydrating workspace=%s without reporting local conflicts.\n", workspaceIDs[0])
		} else {
			previousManifest = manifest
			comparison, err := compareLocalBaseRemote(root, client, workspaceIDs[0], manifest)
			if err != nil {
				return err
			}
			blockingLocal := nonDerivedChanges(comparison.LocalOnly)
			remoteChanges := remoteAdvancingChanges(comparison)
			if len(comparison.Conflicts) > 0 {
				fmt.Printf("Pull stopped: local and cnips changed the same file(s) for workspace=%s\n", workspaceIDs[0])
				fmt.Printf("  conflicts: %d\n", len(comparison.Conflicts))
				printHeadStatus(comparison)
				printSyncSummary(comparison)
				if err := updateLocalManifestForRemoteChanges(root, workspaceIDs[0], remoteChanges); err != nil {
					return err
				}
				return printSyncLinkWithResolver(root, apiURL, workspaceIDs[0], comparison)
			}
			if (len(blockingLocal) > 0 || len(comparison.AutoMerged) > 0) && len(remoteChanges) > 0 {
				fmt.Printf("Pull found local changes; applying %d non-conflicting cnips change(s) only.\n", len(nonDerivedChanges(remoteChanges)))
				printHeadStatus(comparison)
				printSyncSummary(comparison)
				if err := applyRemoteOnly(root, comparison.RemoteOnly); err != nil {
					return err
				}
				if err := applyAutoMerged(root, comparison.AutoMerged); err != nil {
					return err
				}
				if err := updateLocalManifestForRemoteChanges(root, workspaceIDs[0], remoteChanges); err != nil {
					return err
				}
				fmt.Println("\nLocal changes were left untouched. Run cnips status, then cnips push when ready.")
				return nil
			}
			if len(remoteChanges) == 0 {
				fmt.Printf("No cnips changes to pull for workspace=%s\n", workspaceIDs[0])
				return nil
			}
		}
	}

	if dryRun {
		fmt.Println("Dry-run mode — no files will be written.")
	}
	fmt.Printf("Pulling from %s  workspace=%s\n", apiURL, workspaceLabel)

	// ----------------------------------------------------------------
	// 1. Fetch all entities
	// ----------------------------------------------------------------
	txList, srcList, dstList, fnList, gvList, configList, pipelineList, err := fetchWorkspaceLists(client, workspaceIDs)
	if err != nil {
		return err
	}
	appList, appVersions, err := fetchApps(client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: apps could not be pulled: %v\n", err)
	}
	printFetchedCounts(txList, srcList, dstList, fnList, gvList, configList, pipelineList)
	fmt.Printf("  fetching apps...          %d\n", len(appList))
	if len(txList)+len(srcList)+len(dstList)+len(fnList)+len(gvList)+len(configList)+len(pipelineList) == 0 {
		fmt.Printf("\nWarning: workspace %q contains no workspace artifacts.\n", workspaceLabel)
		fmt.Println("If your components are in another workspace, run 'cnips switch' or pass '--workspace <id>'.")
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
	activePaths := pulledActivePaths(txList, srcList, dstList, fnList, gvList, configList, pipelineList, txSlugs, srcSlugs, dstSlugs, fnSlugs, gvSlugs, configSlugs, pipelineSlugs)

	if dryRun {
		fmt.Printf("\nWould write:\n")
		printDrySummary(txList, srcList, dstList, fnList, appList, pipelineList, txSlugs, srcSlugs, dstSlugs, fnSlugs, appSlugs, pipelineSlugs)
		for _, gv := range gvList {
			fmt.Printf("  globalvariables/%-29s  [%s]\n", gvSlugs[gv.ID], gv.WorkspaceID)
		}
		for _, config := range configList {
			fmt.Printf("  configurations/%-31s  [%s]\n", configSlugs[config.ID], config.Type)
		}
		return nil
	}

	var txVersions, srcVersions, dstVersions map[string][]platform.Version
	if skipVersions {
		fmt.Println("Skipping component version lookups.")
		txVersions = map[string][]platform.Version{}
		srcVersions = map[string][]platform.Version{}
		dstVersions = map[string][]platform.Version{}
	} else {
		fmt.Println("\nFetching component versions...")
		txVersions = fetchComponentVersions(client, "transformations", versionWorkers, txList, func(t platform.Transformation) (string, string, string) {
			return t.WorkspaceID, t.ID, platformTransformationType(t)
		})
		srcVersions = fetchComponentVersions(client, "sources", versionWorkers, srcList, func(s platform.Source) (string, string, string) {
			if !s.IsExtractor() {
				return "", "", ""
			}
			return s.WorkspaceID, s.ID, "EXTRACTOR"
		})
		dstVersions = fetchComponentVersions(client, "destinations", versionWorkers, dstList, func(d platform.Destination) (string, string, string) {
			return d.WorkspaceID, d.ID, "DESTINATION"
		})
	}

	// ----------------------------------------------------------------
	// 2. Write components: build entity-ID → slug maps as we go.
	// ----------------------------------------------------------------
	stats := struct{ tx, approval, sw, decision, src, dst, fn, app, gv, cfg, pl int }{}

	fmt.Println("\nWriting transformations...")
	for i := range txList {
		t := &txList[i]
		slug := txSlugs[t.ID]
		_, err := serializer.WriteTransformationVersionsAs(root, t, slug, txVersions[t.ID])
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn: transformation %q: %v\n", t.Name, err)
			continue
		}
		switch transformationSummaryKind(*t) {
		case "approval":
			stats.approval++
		case "switch":
			stats.sw++
		case "decision":
			stats.decision++
		default:
			stats.tx++
		}
	}

	fmt.Println("Writing sources...")
	for i := range srcList {
		s := &srcList[i]
		slug := srcSlugs[s.ID]
		_, err := serializer.WriteSourceVersionsAs(root, s, slug, srcVersions[s.ID])
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn: source %q: %v\n", s.Name, err)
			continue
		}
		stats.src++
	}

	fmt.Println("Writing destinations...")
	for i := range dstList {
		d := &dstList[i]
		slug := dstSlugs[d.ID]
		_, err := serializer.WriteDestinationVersionsAs(root, d, slug, dstVersions[d.ID])
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn: destination %q: %v\n", d.Name, err)
			continue
		}
		stats.dst++
	}

	fmt.Println("Writing functions...")
	for i := range fnList {
		f := &fnList[i]
		_, err := serializer.WriteFunctionAs(root, f, fnSlugs[f.ID])
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn: function %q: %v\n", f.Name, err)
			continue
		}
		stats.fn++
	}

	fmt.Println("Writing apps...")
	for i := range appList {
		app := &appList[i]
		slug := appSlugs[app.ID]
		_, err := serializer.WriteAppVersionsAs(root, app, slug, appVersions[app.ID])
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn: app %q: %v\n", app.Name, err)
			continue
		}
		stats.app++
	}

	fmt.Println("Writing global variables...")
	for i := range gvList {
		gv := &gvList[i]
		_, err := serializer.WriteGlobalVariableAs(root, gv, gvSlugs[gv.ID])
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn: global variable %q: %v\n", gv.Key, err)
			continue
		}
		stats.gv++
	}

	fmt.Println("Writing configurations...")
	for i := range configList {
		config := &configList[i]
		_, err := serializer.WriteConfigurationAs(root, config, configSlugs[config.ID])
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn: configuration %q: %v\n", config.Name, err)
			continue
		}
		stats.cfg++
	}

	// ----------------------------------------------------------------
	// 3. Write pipelines (reference the slugs built above).
	// ----------------------------------------------------------------
	fmt.Println("Writing pipelines...")
	for i := range pipelineList {
		p := &pipelineList[i]
		if err := serializer.WritePipelineAs(root, p, pipelineSlugs[p.ID], txSlugs, srcSlugs, dstSlugs, txConfig, srcConfig, dstConfig, appSlugs, appConfig, pipelineGlobalVariables(p, globalByID)); err != nil {
			fmt.Fprintf(os.Stderr, "  warn: pipeline %q: %v\n", p.Name, err)
			continue
		}
		stats.pl++
	}

	// ----------------------------------------------------------------
	// 4. Update cnips.lock with pulled components.
	// ----------------------------------------------------------------
	if err := updateLock(root, txList, srcList, dstList, fnList, appList, appVersions, txSlugs, srcSlugs, dstSlugs, fnSlugs, appSlugs); err != nil {
		fmt.Fprintf(os.Stderr, "warn: updating cnips.lock: %v\n", err)
	}
	if len(workspaceIDs) == 1 {
		if err := pruneStalePulledPaths(root, previousManifest, activePaths); err != nil {
			return err
		}
	}
	if err := refreshLockFromLocal(root); err != nil {
		fmt.Fprintf(os.Stderr, "warn: refreshing cnips.lock integrity: %v\n", err)
	}
	if len(workspaceIDs) == 1 {
		if err := writePullManifests(client, root, workspaceIDs[0]); err != nil {
			fmt.Fprintf(os.Stderr, "warn: writing pull manifest: %v\n", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "warn: skipping pull manifest for multi-workspace pull\n")
	}

	fmt.Printf("\nDone.\n")
	fmt.Printf("  transformations : %d\n", stats.tx)
	fmt.Printf("  approvals       : %d\n", stats.approval)
	fmt.Printf("  switches        : %d\n", stats.sw)
	fmt.Printf("  decisions       : %d\n", stats.decision)
	fmt.Printf("  sources         : %d\n", stats.src)
	fmt.Printf("  destinations    : %d\n", stats.dst)
	fmt.Printf("  functions       : %d\n", stats.fn)
	fmt.Printf("  apps            : %d\n", stats.app)
	fmt.Printf("  global variables: %d\n", stats.gv)
	fmt.Printf("  configurations  : %d\n", stats.cfg)
	fmt.Printf("  pipelines       : %d\n", stats.pl)
	return nil
}

func pulledActivePaths(
	txList []platform.Transformation,
	srcList []platform.Source,
	dstList []platform.Destination,
	fnList []platform.Function,
	gvList []platform.GlobalVariable,
	configList []platform.Configuration,
	pipelineList []platform.Pipeline,
	txSlugs, srcSlugs, dstSlugs, fnSlugs, gvSlugs, configSlugs, pipelineSlugs map[string]string,
) map[string]bool {
	active := map[string]bool{}
	for _, t := range txList {
		active[transformationFamilyBaseDir(transformationSummaryKind(t))+"/"+txSlugs[t.ID]] = true
	}
	for _, s := range srcList {
		active["sources/"+srcSlugs[s.ID]] = true
	}
	for _, d := range dstList {
		active["destinations/"+dstSlugs[d.ID]] = true
	}
	for _, f := range fnList {
		active["functions/"+fnSlugs[f.ID]] = true
	}
	for _, p := range pipelineList {
		active["pipelines/"+pipelineSlugs[p.ID]] = true
	}
	for _, gv := range gvList {
		active["globalvariables/"+gvSlugs[gv.ID]+".yaml"] = true
	}
	for _, config := range configList {
		active["configurations/"+configSlugs[config.ID]+".yaml"] = true
	}
	return active
}

func pruneStalePulledPaths(root string, previous *pullManifest, active map[string]bool) error {
	if previous == nil || len(previous.Files) == 0 {
		return nil
	}
	targets := map[string]bool{}
	for path := range previous.Files {
		target := stalePullTarget(path, active)
		if target == "" {
			continue
		}
		targets[target] = true
	}
	ordered := make([]string, 0, len(targets))
	for target := range targets {
		ordered = append(ordered, target)
	}
	sortPathsByDepthDesc(ordered)
	for _, target := range ordered {
		path, err := safeLocalPath(root, target)
		if err != nil {
			return err
		}
		if strings.HasSuffix(target, ".yaml") {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		pruneEmptyArtifactParents(root, filepath.Dir(path))
	}
	return nil
}

func stalePullTarget(path string, active map[string]bool) string {
	path = strings.Trim(filepath.ToSlash(path), "/")
	if path == "" || isDerivedComparableFile(path) {
		return ""
	}
	if strings.HasPrefix(path, "globalvariables/") || strings.HasPrefix(path, "configurations/") {
		if active[path] {
			return ""
		}
		return path
	}
	prefix := artifactPathPrefix(path)
	if prefix == "" || active[prefix] {
		return ""
	}
	return prefix
}

func pullWorkspaceSnapshot(root string, client *platform.Client, workspace string) error {
	txList, srcList, dstList, fnList, gvList, configList, pipelineList, err := fetchWorkspaceLists(client, []string{workspace})
	if err != nil {
		return err
	}
	appList, appVersions, err := fetchApps(client)
	if err != nil && !isHTTPNotFound(err) {
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

	txVersions := fetchComponentVersions(client, "", 32, txList, func(t platform.Transformation) (string, string, string) {
		return t.WorkspaceID, t.ID, platformTransformationType(t)
	})
	srcVersions := fetchComponentVersions(client, "", 32, srcList, func(s platform.Source) (string, string, string) {
		if !s.IsExtractor() {
			return "", "", ""
		}
		return s.WorkspaceID, s.ID, "EXTRACTOR"
	})
	dstVersions := fetchComponentVersions(client, "", 32, dstList, func(d platform.Destination) (string, string, string) {
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
	for i := range appList {
		if _, err := serializer.WriteAppVersionsAs(root, &appList[i], appSlugs[appList[i].ID], appVersions[appList[i].ID]); err != nil {
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
		if err := serializer.WritePipelineAs(root, &pipelineList[i], pipelineSlugs[pipelineList[i].ID], txSlugs, srcSlugs, dstSlugs, txConfig, srcConfig, dstConfig, appSlugs, appConfig, pipelineGlobalVariables(&pipelineList[i], globalByID)); err != nil {
			return err
		}
	}
	if err := updateLock(root, txList, srcList, dstList, fnList, appList, appVersions, txSlugs, srcSlugs, dstSlugs, fnSlugs, appSlugs); err != nil {
		return err
	}
	if err := refreshLockFromLocal(root); err != nil {
		return err
	}
	return writePullManifests(client, root, workspace)
}

func configMapByID[T any](items []T, idOf func(T) string, configOf func(T) []platform.ConfigItem) map[string]map[string]any {
	out := make(map[string]map[string]any, len(items))
	for _, item := range items {
		id := idOf(item)
		if id == "" {
			continue
		}
		config := make(map[string]any)
		for _, c := range configOf(item) {
			config[c.Key] = c.Value
		}
		if len(config) > 0 {
			out[id] = config
		}
	}
	return out
}

func globalVariablesByID(items []platform.GlobalVariable) map[string]platform.GlobalVariable {
	out := make(map[string]platform.GlobalVariable, len(items))
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func pipelineGlobalVariables(p *platform.Pipeline, byID map[string]platform.GlobalVariable) []platform.GlobalVariable {
	ids := append([]string{}, p.GlobalVariables...)
	ids = append(ids, p.Variables...)
	out := make([]platform.GlobalVariable, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if gv, ok := byID[id]; ok {
			out = append(out, gv)
			seen[id] = true
		}
	}
	return out
}

func fetchApps(client *platform.Client) ([]platform.App, map[string][]platform.AppVersion, error) {
	apps, err := client.ListApps()
	if err != nil {
		if isHTTPNotFound(err) {
			return nil, map[string][]platform.AppVersion{}, nil
		}
		return nil, nil, err
	}

	// ponytail: parallelize version lookups instead of sequential N+1.
	// Collect all (appIndex, versionID, versionMeta) tuples, then fan out.
	type versionLookup struct {
		appIdx    int
		versionID string
		// Fields from the list entry used to fill defaults.
		infoVersion       string
		infoVersionNumber string
		isFallback        bool // true when this is the latestVersionID fallback
	}
	var lookups []versionLookup
	for i := range apps {
		app := &apps[i]
		if app.ID == "" {
			app.ID = app.AltID
		}
		hasExplicit := false
		for _, info := range app.Versions {
			if info.VersionID == "" {
				continue
			}
			hasExplicit = true
			lookups = append(lookups, versionLookup{
				appIdx:            i,
				versionID:         info.VersionID,
				infoVersion:       info.Version,
				infoVersionNumber: info.VersionNumber,
			})
		}
		if !hasExplicit && app.LatestVersionID != "" {
			lookups = append(lookups, versionLookup{
				appIdx:     i,
				versionID:  app.LatestVersionID,
				isFallback: true,
			})
		}
	}

	if len(lookups) == 0 {
		return apps, map[string][]platform.AppVersion{}, nil
	}

	type versionResult struct {
		appIdx  int
		version platform.AppVersion
	}

	const workers = 16
	jobs := make(chan versionLookup)
	results := make(chan versionResult, len(lookups))
	var wg sync.WaitGroup
	workerCount := workers
	if len(lookups) < workerCount {
		workerCount = len(lookups)
	}
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for lookup := range jobs {
				app := &apps[lookup.appIdx]
				version, err := client.GetAppVersion(lookup.versionID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  warn: app %q version %q: %v\n", app.Name, lookup.versionID, err)
					continue
				}
				if version == nil {
					continue
				}
				if version.Ref == "" {
					version.Ref = app.ID
				}
				if version.Name == "" {
					version.Name = app.Name
				}
				if version.Version == "" {
					if lookup.isFallback {
						version.Version = app.LatestVersion
					} else {
						version.Version = firstNonEmpty(lookup.infoVersion, lookup.infoVersionNumber)
					}
				}
				results <- versionResult{appIdx: lookup.appIdx, version: *version}
			}
		}()
	}
	for _, l := range lookups {
		jobs <- l
	}
	close(jobs)
	wg.Wait()
	close(results)

	versions := make(map[string][]platform.AppVersion, len(apps))
	for r := range results {
		appID := apps[r.appIdx].ID
		versions[appID] = append(versions[appID], r.version)
	}
	return apps, versions, nil
}

func normalizePulledVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "latest"
	}
	if strings.EqualFold(version, "latest") {
		return "latest"
	}
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

func isHTTPNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "returned 404")
}

func fetchComponentVersions[T any](client *platform.Client, label string, workers int, items []T, refOf func(T) (workspaceID, ref, componentType string)) map[string][]platform.Version {
	if workers <= 0 {
		workers = 1
	}

	type lookup struct {
		workspaceID   string
		ref           string
		componentType string
	}
	var lookups []lookup
	for _, item := range items {
		workspaceID, ref, componentType := refOf(item)
		if workspaceID == "" || ref == "" || componentType == "" {
			continue
		}
		lookups = append(lookups, lookup{workspaceID: workspaceID, ref: ref, componentType: componentType})
	}

	out := make(map[string][]platform.Version)
	if len(lookups) == 0 {
		if label != "" {
			fmt.Printf("  %-15s 0\n", label+":")
		}
		return out
	}

	if label != "" {
		fmt.Printf("  %-15s %d", label+":", len(lookups))
	}
	if workers > len(lookups) {
		workers = len(lookups)
	}
	jobs := make(chan lookup)
	var wg sync.WaitGroup
	var mu sync.Mutex
	found := 0

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				versions, err := client.ListVersions(job.workspaceID, job.ref, job.componentType)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n  warn: versions for %s %s: %v\n", job.componentType, job.ref, err)
					continue
				}
				mu.Lock()
				out[job.ref] = versions
				found += len(versions)
				mu.Unlock()
			}
		}()
	}
	for _, job := range lookups {
		jobs <- job
	}
	close(jobs)
	wg.Wait()
	if label != "" {
		fmt.Printf("  (%d versions)\n", found)
	}
	return out
}

func resolvePullWorkspaces(client *platform.Client, workspace string, allWorkspaces bool) ([]string, string, error) {
	if !allWorkspaces && workspace != "all" && workspace != "*" {
		return []string{workspace}, workspace, nil
	}

	workspaces, err := client.ListWorkspaces()
	if err != nil {
		return nil, "", fmt.Errorf("list workspaces for all-workspaces pull: %w", err)
	}
	ids := make([]string, 0, len(workspaces))
	seen := map[string]bool{}
	for _, workspace := range workspaces {
		id := workspace.WorkspaceID
		if id == "" {
			id = workspace.ID
		}
		if id == "" || seen[id] {
			continue
		}
		ids = append(ids, id)
		seen[id] = true
	}
	if len(ids) == 0 {
		return nil, "", fmt.Errorf("no accessible workspaces returned by mgmt-srv")
	}
	return ids, fmt.Sprintf("all (%d workspaces)", len(ids)), nil
}

func collectByID[T any](workspaceIDs []string, fetch func(string) ([]T, error), idOf func(T) string) ([]T, error) {
	out := make([]T, 0)
	seen := map[string]bool{}
	for _, workspaceID := range workspaceIDs {
		items, err := fetch(workspaceID)
		if err != nil {
			return nil, fmt.Errorf("workspace %s: %w", workspaceID, err)
		}
		for i, item := range items {
			setWorkspaceID(&item, workspaceID)
			id := idOf(item)
			if id == "" {
				id = fmt.Sprintf("%s/%d", workspaceID, i)
			}
			if seen[id] {
				continue
			}
			out = append(out, item)
			seen[id] = true
		}
	}
	return out, nil
}

func setWorkspaceID[T any](item *T, workspaceID string) {
	switch v := any(item).(type) {
	case *platform.Pipeline:
		if v.WorkspaceID == "" {
			v.WorkspaceID = workspaceID
		}
	case *platform.Source:
		if v.WorkspaceID == "" {
			v.WorkspaceID = workspaceID
		}
	case *platform.Transformation:
		if v.WorkspaceID == "" {
			v.WorkspaceID = workspaceID
		}
	case *platform.Destination:
		if v.WorkspaceID == "" {
			v.WorkspaceID = workspaceID
		}
	case *platform.Function:
		if v.WorkspaceID == "" {
			v.WorkspaceID = workspaceID
		}
	case *platform.GlobalVariable:
		if v.WorkspaceID == "" {
			v.WorkspaceID = workspaceID
		}
	case *platform.Configuration:
		if v.WorkspaceID == "" {
			v.WorkspaceID = workspaceID
		}
	}
}

func uniqueSlugs[T any](items []T, idOf func(T) string, nameOf func(T) string) map[string]string {
	counts := map[string]int{}
	for _, item := range items {
		base := serializer.Slug(nameOf(item))
		if base == "" {
			base = "unnamed"
		}
		counts[base]++
	}

	used := map[string]bool{}
	out := map[string]string{}
	for i, item := range items {
		id := idOf(item)
		if id == "" {
			id = fmt.Sprintf("item-%d", i)
		}
		base := serializer.Slug(nameOf(item))
		if base == "" {
			base = "unnamed"
		}
		slug := base
		if counts[base] > 1 {
			slug = base + "-" + shortID(id)
		}
		if used[slug] {
			for n := 2; ; n++ {
				candidate := fmt.Sprintf("%s-%d", slug, n)
				if !used[candidate] {
					slug = candidate
					break
				}
			}
		}
		out[id] = slug
		used[slug] = true
	}
	return out
}

func shortID(id string) string {
	id = serializer.Slug(id)
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return "unknown"
	}
	return id
}

// -----------------------------------------------------------------------
// lock file update
// -----------------------------------------------------------------------

func updateLock(
	root string,
	txList []platform.Transformation,
	srcList []platform.Source,
	dstList []platform.Destination,
	fnList []platform.Function,
	appList []platform.App,
	appVersions map[string][]platform.AppVersion,
	txSlugs map[string]string,
	srcSlugs map[string]string,
	dstSlugs map[string]string,
	fnSlugs map[string]string,
	appSlugs map[string]string,
) error {
	lock, err := artifact.ParseLock(root)
	if err != nil {
		// Start fresh if no lock yet.
		lock = &artifact.Lock{APIVersion: "cnips.io/v1", Kind: "Lock"}
	}

	existing := make(map[string]bool)
	for _, c := range lock.Spec.Components {
		existing[c.Kind+"/"+c.Name] = true
	}

	add := func(slug, kind, lang, sigVer string) {
		key := kind + "/" + slug
		if !existing[key] {
			lock.Spec.Components = append(lock.Spec.Components, artifact.LockComponent{
				Name:             slug,
				Kind:             kind,
				Version:          "local",
				Source:           "local",
				Language:         lang,
				SignatureVersion: sigVer,
			})
			existing[key] = true
		}
	}
	addApp := func(slug string, app platform.App, version platform.AppVersion) {
		key := "app/" + slug
		if existing[key] {
			return
		}
		lock.Spec.Components = append(lock.Spec.Components, artifact.LockComponent{
			Name:             slug,
			Kind:             "app",
			Version:          firstNonEmpty(normalizePulledVersion(version.Version), normalizePulledVersion(app.LatestVersion)),
			Source:           "marketplace",
			Language:         firstNonEmpty(version.Language, app.Language),
			SignatureVersion: version.SignatureVersion,
			TemplateVersion:  version.TemplateVersion,
		})
		existing[key] = true
	}

	for _, t := range txList {
		add(txSlugs[t.ID], transformationSummaryKind(t), t.Language, t.SignatureVersion)
	}
	for _, s := range srcList {
		add(srcSlugs[s.ID], "source", s.Language, s.SignatureVersion)
	}
	for _, d := range dstList {
		add(dstSlugs[d.ID], "destination", d.Language, d.SignatureVersion)
	}
	for _, f := range fnList {
		add(fnSlugs[f.ID], "function", f.Language, f.SignatureVersion)
	}
	for _, app := range appList {
		versions := appVersions[app.ID]
		if len(versions) > 0 {
			addApp(appSlugs[app.ID], app, versions[0])
			continue
		}
		addApp(appSlugs[app.ID], app, platform.AppVersion{})
	}

	return artifact.WriteYAML(root+"/cnips.lock", lock)
}

// -----------------------------------------------------------------------
// dry-run summary
// -----------------------------------------------------------------------

func printDrySummary(
	txList []platform.Transformation,
	srcList []platform.Source,
	dstList []platform.Destination,
	fnList []platform.Function,
	appList []platform.App,
	pipelineList []platform.Pipeline,
	txSlugs map[string]string,
	srcSlugs map[string]string,
	dstSlugs map[string]string,
	fnSlugs map[string]string,
	appSlugs map[string]string,
	pipelineSlugs map[string]string,
) {
	for _, t := range txList {
		base := transformationFamilyBaseDir(transformationSummaryKind(t))
		fmt.Printf("  %s/%-30s  [%s]\n", base, txSlugs[t.ID], t.Language)
	}
	for _, s := range srcList {
		fmt.Printf("  sources/%-35s  [%s]\n", srcSlugs[s.ID], s.NormalizedSourceType())
	}
	for _, d := range dstList {
		fmt.Printf("  destinations/%-32s  [%s]\n", dstSlugs[d.ID], d.Language)
	}
	for _, f := range fnList {
		fmt.Printf("  functions/%-35s  [%s]\n", fnSlugs[f.ID], f.Language)
	}
	for _, app := range appList {
		fmt.Printf("  apps/%-40s  [%s]\n", appSlugs[app.ID], app.AppType)
	}
	for _, p := range pipelineList {
		fmt.Printf("  pipelines/%-35s\n", pipelineSlugs[p.ID])
	}
}

func transformationSummaryKind(t platform.Transformation) string {
	switch strings.ToLower(strings.TrimSpace(t.Type)) {
	case "approval", "switch", "decision":
		return strings.ToLower(strings.TrimSpace(t.Type))
	default:
		return "transformation"
	}
}

func platformTransformationType(t platform.Transformation) string {
	return strings.ToUpper(transformationSummaryKind(t))
}
