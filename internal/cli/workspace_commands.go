package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cnips/cli/internal/platform"
	"github.com/cnips/cli/internal/project"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show local and cnips changes against the tracked base",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runWithLoader("Checking status", true, func() error { return runStatus(cmd) })
	},
}

func runStatus(cmd *cobra.Command) error {
	root := project.MustFindRoot()
	apiURL, workspace, tenantKey, token, err := resolveAuthenticatedPlatformFlags(cmd)
	if err != nil {
		return err
	}
	client := platform.NewClient(apiURL, tenantKey, token)
	manifest, hasLocalBase, err := readComparisonBase(root, client, workspace, true)
	if err != nil {
		return err
	}
	comparison, err := compareLocalBaseRemote(root, client, workspace, manifest)
	if err != nil {
		return err
	}
	fmt.Printf("On cnips workspace %s\n", workspace)
	if hasLocalBase {
		fmt.Println("Base: local tracking manifest")
	} else {
		fmt.Println("Base: workspace manifest fallback")
	}
	printHeadStatus(comparison)
	if len(comparison.All) == 0 {
		fmt.Println("nothing to commit, working tree clean")
		return nil
	}
	fmt.Printf("\nLocal changes: %d  Cnips changes: %d  Conflicts: %d\n", len(nonDerivedChanges(comparison.LocalOnly)), len(comparison.RemoteOnly), len(comparison.Conflicts))
	printSyncSummary(comparison)
	return nil
}

func printHeadStatus(comparison *syncComparison) {
	trackedLocal := firstNonEmpty(comparison.TrackedLocalHead, comparison.BaseHead)
	trackedRemote := firstNonEmpty(comparison.TrackedRemoteHead, comparison.BaseHead)
	fmt.Printf("HEADs: base=%s local=%s remote=%s\n", shortHead(comparison.BaseHead), shortHead(comparison.LocalHead), shortHead(comparison.RemoteHead))
	if trackedLocal != comparison.LocalHead {
		fmt.Printf("Local HEAD moved from %s to %s\n", shortHead(trackedLocal), shortHead(comparison.LocalHead))
	}
	if trackedRemote != comparison.RemoteHead {
		fmt.Printf("Remote HEAD moved from %s to %s\n", shortHead(trackedRemote), shortHead(comparison.RemoteHead))
	}
}

var rebaseCmd = &cobra.Command{
	Use:   "rebase",
	Short: "Replay local cnips work on top of a base workspace",
	Long: `Rebases the local cnips working tree onto a base workspace by running the
same three-way sync guard as pull. Use --base-workspace to pull from a shared
base workspace instead of the current workspace.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		baseWorkspace, _ := cmd.Flags().GetString("base-workspace")
		if strings.TrimSpace(baseWorkspace) != "" && !cmd.Flags().Changed("workspace") {
			_ = cmd.Flags().Set("workspace", baseWorkspace)
		}
		return runPull(cmd, args)
	},
}

var stashCmd = &cobra.Command{
	Use:   "stash [push|list|apply|pop] [stash-id]",
	Short: "Temporarily save local cnips changes",
	Args:  cobra.RangeArgs(0, 2),
	RunE:  runStash,
}

func init() {
	addPlatformFlags(statusCmd)
	addPlatformFlags(rebaseCmd)
	rebaseCmd.Flags().String("base-workspace", "", "Workspace to use as the rebase base")
	rebaseCmd.Flags().Bool("dry-run", false, "Print a summary of what would be written without writing any files")
	rebaseCmd.Flags().Bool("all-workspaces", false, "Pull from every workspace accessible to the current token")
	rebaseCmd.Flags().Bool("skip-versions", false, "Skip component version lookups and pull only current component sources")
	rebaseCmd.Flags().Int("version-workers", 32, "Number of concurrent component version lookups during pull")
	addPlatformFlags(stashCmd)
	rootCmd.AddCommand(statusCmd, rebaseCmd, stashCmd)
}

func addPlatformFlags(cmd *cobra.Command) {
	cmd.Flags().String("api-url", "http://localhost:8090", "Base URL of mgmt-srv")
	cmd.Flags().String("workspace", "default", "Workspace ID")
	cmd.Flags().String("tenant-key", "", "Value for the x-tenant-key header")
	cmd.Flags().String("token", "", "Bearer token for Authorization header")
}

func runStash(cmd *cobra.Command, args []string) error {
	action := "push"
	if len(args) > 0 {
		action = args[0]
	}
	root := project.MustFindRoot()
	switch action {
	case "push":
		return stashPush(cmd, root)
	case "list":
		return stashList(root)
	case "apply", "pop":
		id := ""
		if len(args) > 1 {
			id = args[1]
		}
		return stashApply(root, id, action == "pop")
	default:
		return fmt.Errorf("unknown stash action %q", action)
	}
}

type cnipsStash struct {
	ID        string            `json:"id"`
	CreatedAt string            `json:"createdAt"`
	Workspace string            `json:"workspace"`
	Files     []syncSessionFile `json:"files"`
}

func stashPush(cmd *cobra.Command, root string) error {
	apiURL, workspace, tenantKey, token, err := resolveAuthenticatedPlatformFlags(cmd)
	if err != nil {
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
	files := syncFilesForStash(comparison)
	if len(files) == 0 {
		fmt.Println("No local changes to stash.")
		return nil
	}
	now := time.Now().UTC()
	stash := cnipsStash{
		ID:        now.Format("20060102T150405Z"),
		CreatedAt: now.Format(time.RFC3339),
		Workspace: workspace,
		Files:     files,
	}
	path := filepath.Join(stashDir(root), stash.ID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(stash, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	resolver := &syncResolver{root: root}
	for _, file := range files {
		if err := resolver.applyFile(file, "remote"); err != nil {
			return err
		}
	}
	fmt.Printf("Saved working tree state as stash@{%s}\n", stash.ID)
	return nil
}

func syncFilesForStash(comparison *syncComparison) []syncSessionFile {
	var out []syncSessionFile
	changes := append(nonDerivedChanges(comparison.LocalOnly), nonDerivedChanges(comparison.Conflicts)...)
	for _, change := range changes {
		out = append(out, syncSessionFile{
			Path:         change.Path,
			Status:       change.Status,
			LocalExists:  change.LocalExists,
			RemoteExists: change.RemoteExists,
			Local:        change.Local,
			Remote:       change.Remote,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func stashList(root string) error {
	stashes, err := readStashes(root)
	if err != nil {
		return err
	}
	if len(stashes) == 0 {
		fmt.Println("No stashes.")
		return nil
	}
	for _, stash := range stashes {
		fmt.Printf("stash@{%s}: workspace=%s files=%d created=%s\n", stash.ID, stash.Workspace, len(stash.Files), stash.CreatedAt)
	}
	return nil
}

func stashApply(root, id string, drop bool) error {
	stash, path, err := loadStash(root, id)
	if err != nil {
		return err
	}
	resolver := &syncResolver{root: root}
	for _, file := range stash.Files {
		if err := resolver.applyFile(file, "local"); err != nil {
			return err
		}
	}
	if drop {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	fmt.Printf("Applied stash@{%s}\n", stash.ID)
	return nil
}

func readStashes(root string) ([]cnipsStash, error) {
	entries, err := os.ReadDir(stashDir(root))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []cnipsStash
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(stashDir(root), entry.Name()))
		if err != nil {
			return nil, err
		}
		var stash cnipsStash
		if err := json.Unmarshal(data, &stash); err != nil {
			return nil, err
		}
		out = append(out, stash)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func loadStash(root, id string) (*cnipsStash, string, error) {
	stashes, err := readStashes(root)
	if err != nil {
		return nil, "", err
	}
	if len(stashes) == 0 {
		return nil, "", fmt.Errorf("no stashes found")
	}
	if id == "" {
		id = stashes[0].ID
	}
	for _, stash := range stashes {
		if stash.ID == id {
			return &stash, filepath.Join(stashDir(root), stash.ID+".json"), nil
		}
	}
	return nil, "", fmt.Errorf("stash %q not found", id)
}

func stashDir(root string) string {
	return filepath.Join(project.StateDir(root), "stash")
}
