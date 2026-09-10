package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/auth"
	"github.com/cnips/cli/internal/platform"
	"github.com/cnips/cli/internal/project"
	"github.com/cnips/cli/internal/serializer"
	"github.com/spf13/cobra"
)

var pushCmd = &cobra.Command{
	Use:   "push",
	Short: "Apply local cnips files to a workspace",
	Long: `Applies the local canonical project files to a target cnips workspace.

This is a dev-loop command. It creates or updates workspace artifacts through
mgmt-srv and then applies pipelines after component IDs have been resolved.`,
	RunE: runPush,
}

func init() {
	pushCmd.Flags().String("api-url", "http://localhost:8090", "Base URL of mgmt-srv")
	pushCmd.Flags().String("workspace", "default", "Workspace ID to push to")
	pushCmd.Flags().String("tenant-key", "", "Value for the x-tenant-key header")
	pushCmd.Flags().String("token", "", "Bearer token for Authorization header")
	pushCmd.Flags().Bool("dry-run", false, "Print the push plan without applying changes")
	rootCmd.AddCommand(pushCmd)
}

func runPush(cmd *cobra.Command, _ []string) error {
	root := project.MustFindRoot()
	apiURL, workspace, tenantKey, token := platformFlags(cmd)
	dryRun, _ := cmd.Flags().GetBool("dry-run")

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
	if len(comparison.Conflicts) > 0 {
		fmt.Printf("Push stopped: cnips has changes not present locally for workspace=%s\n", workspace)
		fmt.Printf("  cnips-only: %d\n", len(comparison.RemoteOnly))
		fmt.Printf("  conflicts : %d\n", len(comparison.Conflicts))
		printHeadStatus(comparison)
		printSyncSummary(comparison)
		if err := updateLocalManifestForRemoteChanges(root, workspace, remoteAdvancingChanges(comparison)); err != nil {
			return err
		}
		return printSyncLinkWithResolver(root, apiURL, workspace, comparison)
	}
	if remoteChanges := remoteAdvancingChanges(comparison); len(remoteChanges) > 0 {
		fmt.Printf("Cnips has %d remote change(s); fast-forwarding them before push.\n", len(nonDerivedChanges(remoteChanges)))
		if err := applyRemoteOnly(root, comparison.RemoteOnly); err != nil {
			return err
		}
		if err := applyAutoMerged(root, comparison.AutoMerged); err != nil {
			return err
		}
		if err := updateLocalManifestForRemoteChanges(root, workspace, remoteChanges); err != nil {
			return err
		}
		if err := refreshLockFromLocal(root); err != nil {
			return err
		}
		manifest, _, err = readComparisonBase(root, client, workspace, true)
		if err != nil {
			return err
		}
		comparison, err = compareLocalBaseRemote(root, client, workspace, manifest)
		if err != nil {
			return err
		}
		if len(comparison.Conflicts) > 0 || len(remoteAdvancingChanges(comparison)) > 0 {
			fmt.Printf("Push stopped after fast-forward because cnips is still ahead for workspace=%s\n", workspace)
			fmt.Printf("  cnips-only: %d\n", len(comparison.RemoteOnly))
			fmt.Printf("  conflicts : %d\n", len(comparison.Conflicts))
			printHeadStatus(comparison)
			printSyncSummary(comparison)
			if err := updateLocalManifestForRemoteChanges(root, workspace, remoteAdvancingChanges(comparison)); err != nil {
				return err
			}
			return printSyncLinkWithResolver(root, apiURL, workspace, comparison)
		}
	}
	localChanges := nonDerivedChanges(comparison.LocalOnly)
	skippedDeletes := localDeleteChanges(localChanges)
	changes := pushableLocalChanges(localChanges)
	if len(changes) == 0 {
		printSkippedLocalDeletes(skippedDeletes)
		fmt.Printf("No local changes to push for workspace=%s\n", workspace)
		return nil
	}
	printSkippedLocalDeletes(skippedDeletes)
	state, err := loadWorkspaceState(client, workspace)
	if err != nil {
		return err
	}
	if err := indexRenameAliases(root, state); err != nil {
		return err
	}

	bundle, err := readLocalBundle(root, workspace)
	if err != nil {
		return err
	}
	indexLocalBundleAliases(state, bundle)
	bundle = filterBundleByChanges(bundle, changes)
	plan := buildPushPlan(bundle, state)
	printPushPlan(plan, apiURL, workspace, dryRun)
	if dryRun {
		return nil
	}

	progress := newTerminalProgress("Pushing", len(plan.Items)+1)
	journal := &pushRollbackJournal{}
	applied, err := applyBundle(client, workspace, bundle, state, progress, journal)
	if err != nil {
		fmt.Println()
		if rollbackErr := journal.Rollback(client, workspace); rollbackErr != nil {
			return fmt.Errorf("%w; rollback failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("%w; rollback completed", err)
	}
	if err := refreshLockFromLocal(root); err != nil {
		fmt.Fprintf(os.Stderr, "warn: refreshing cnips.lock after push: %v\n", err)
	}
	if err := pullWorkspaceSnapshot(root, client, workspace); err != nil {
		fmt.Fprintf(os.Stderr, "warn: pulling workspace after push: %v\n", err)
	}
	if err := clearConflictState(root, workspace, changes); err != nil {
		fmt.Fprintf(os.Stderr, "warn: clearing resolved conflict state after push: %v\n", err)
	}
	progress.Finish()
	fmt.Printf("\nPush complete.\n")
	fmt.Printf("  created: %d\n", applied.created)
	fmt.Printf("  updated: %d\n", applied.updated)
	fmt.Printf("  unchanged/skipped: %d\n", applied.skipped)
	return nil
}

type localBundle struct {
	Transformations []localComponent
	Sources         []localComponent
	Destinations    []localComponent
	Apps            []localComponent
	Functions       []localFunction
	GlobalVariables []artifact.GlobalVariable
	Configurations  []artifact.Configuration
	Pipelines       []artifact.Pipeline
}

type localComponent struct {
	Slug           string
	Dir            string
	BaseDir        string
	Artifact       artifact.Component
	Source         platform.SourceCode
	IsBunBuild     bool
	HasCodeChanges bool
}

type localFunction struct {
	Slug           string
	Dir            string
	Artifact       artifact.Function
	Source         platform.SourceCode
	IsBunBuild     bool
	HasCodeChanges bool
}

type workspaceState struct {
	Transformations map[string]platform.Transformation
	Sources         map[string]platform.Source
	Destinations    map[string]platform.Destination
	Apps            map[string]platform.App
	Functions       map[string]platform.Function
	GlobalVariables map[string]platform.GlobalVariable
	Configurations  map[string]platform.Configuration
	Pipelines       map[string]platform.Pipeline
}

type pushPlan struct {
	Items []pushPlanItem
}

type pushPlanItem struct {
	Kind   string
	Name   string
	Action string
	ID     string
}

type applyStats struct {
	created int
	updated int
	skipped int
}

type rollbackAction struct {
	label    string
	undo     func(*platform.Client, string) error
	updateID string
}

type pushRollbackJournal struct {
	actions []rollbackAction
}

func (j *pushRollbackJournal) Created(label string, undo func(*platform.Client, string) error) {
	if j == nil {
		return
	}
	j.actions = append(j.actions, rollbackAction{label: label, undo: undo})
}

func (j *pushRollbackJournal) Updated(label, id string, undo func(*platform.Client, string) error) {
	if j == nil {
		return
	}
	j.actions = append(j.actions, rollbackAction{label: label, updateID: id, undo: undo})
}

func (j *pushRollbackJournal) Rollback(client *platform.Client, workspace string) error {
	if j == nil || len(j.actions) == 0 {
		return nil
	}
	fmt.Println("Push failed; rolling back applied changes...")
	var errs []string
	for i := len(j.actions) - 1; i >= 0; i-- {
		action := j.actions[i]
		if action.undo == nil {
			continue
		}
		if err := action.undo(client, workspace); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", action.label, err))
			continue
		}
		fmt.Printf("  reverted %s\n", action.label)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func componentPlatformName(item localComponent) string {
	return firstNonEmpty(item.Artifact.Metadata.Name, item.Slug)
}

func functionPlatformName(item localFunction) string {
	return firstNonEmpty(item.Artifact.Metadata.Name, item.Slug)
}

func componentLookupKeys(item localComponent) []string {
	return lookupKeys(item.Artifact.Metadata.ID, componentPlatformName(item), item.Slug)
}

func functionLookupKeys(item localFunction) []string {
	return lookupKeys(item.Artifact.Metadata.ID, functionPlatformName(item), item.Slug)
}

func lookupKeys(values ...string) []string {
	keys := make([]string, 0, len(values)*2)
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		keys = append(keys, value)
		seen[value] = true
	}
	for _, value := range values {
		add(value)
		add(indexKey(value))
	}
	return keys
}

func findExistingTransformation(s *workspaceState, item localComponent) platform.Transformation {
	if s == nil {
		return platform.Transformation{}
	}
	for _, key := range componentLookupKeys(item) {
		if existing := s.Transformations[key]; existing.ID != "" {
			return existing
		}
	}
	return platform.Transformation{}
}

func findExistingSource(s *workspaceState, item localComponent) platform.Source {
	if s == nil {
		return platform.Source{}
	}
	for _, key := range componentLookupKeys(item) {
		if existing := s.Sources[key]; existing.ID != "" {
			return existing
		}
	}
	return platform.Source{}
}

func findExistingDestination(s *workspaceState, item localComponent) platform.Destination {
	if s == nil {
		return platform.Destination{}
	}
	for _, key := range componentLookupKeys(item) {
		if existing := s.Destinations[key]; existing.ID != "" {
			return existing
		}
	}
	return platform.Destination{}
}

func findExistingFunction(s *workspaceState, item localFunction) platform.Function {
	if s == nil {
		return platform.Function{}
	}
	for _, key := range functionLookupKeys(item) {
		if existing := s.Functions[key]; existing.ID != "" {
			return existing
		}
	}
	return platform.Function{}
}

func findExistingApp(s *workspaceState, name string) platform.App {
	if s == nil {
		return platform.App{}
	}
	for _, key := range lookupKeys(name) {
		if existing := s.Apps[key]; existing.ID != "" {
			return existing
		}
	}
	return platform.App{}
}

func findExistingPipeline(s *workspaceState, item artifact.Pipeline) platform.Pipeline {
	if s == nil {
		return platform.Pipeline{}
	}
	for _, key := range lookupKeys(item.Metadata.ID, item.Metadata.Name, item.Metadata.Slug) {
		if existing := s.Pipelines[key]; existing.ID != "" {
			return existing
		}
	}
	return platform.Pipeline{}
}

func indexLocalBundleAliases(s *workspaceState, b *localBundle) {
	if s == nil || b == nil {
		return
	}
	for _, item := range b.Transformations {
		if existing := findExistingTransformation(s, item); existing.ID != "" {
			indexTransformation(s.Transformations, existing, item.Slug)
		}
	}
	for _, item := range b.Sources {
		if existing := findExistingSource(s, item); existing.ID != "" {
			indexSource(s.Sources, existing, item.Slug)
		}
	}
	for _, item := range b.Destinations {
		if existing := findExistingDestination(s, item); existing.ID != "" {
			indexDestination(s.Destinations, existing, item.Slug)
		}
	}
	for _, item := range b.Apps {
		if existing := findExistingApp(s, componentPlatformName(item)); existing.ID != "" {
			indexApp(s.Apps, existing, item.Slug)
		}
	}
	for _, item := range b.Functions {
		if existing := findExistingFunction(s, item); existing.ID != "" {
			indexFunction(s.Functions, existing, item.Slug)
		}
	}
}

func indexRenameAliases(root string, s *workspaceState) error {
	if s == nil {
		return nil
	}
	state, err := readRenameState(root)
	if err != nil {
		return err
	}
	for _, record := range state.Entries {
		oldSlug := renamePrefixSlug(record.OldPrefix)
		newSlug := renamePrefixSlug(record.NewPrefix)
		if oldSlug == "" || newSlug == "" {
			continue
		}
		switch record.Kind {
		case "pipeline":
			if existing := s.Pipelines[indexKey(oldSlug)]; existing.ID != "" {
				indexPipeline(s.Pipelines, existing, newSlug)
			}
		case "transformation":
			if existing := s.Transformations[indexKey(oldSlug)]; existing.ID != "" {
				indexTransformation(s.Transformations, existing, newSlug)
			}
		case "approval", "switch", "decision":
			if existing := s.Transformations[indexKey(oldSlug)]; existing.ID != "" {
				indexTransformation(s.Transformations, existing, newSlug)
			}
		case "source":
			if existing := s.Sources[indexKey(oldSlug)]; existing.ID != "" {
				indexSource(s.Sources, existing, newSlug)
			}
		case "destination":
			if existing := s.Destinations[indexKey(oldSlug)]; existing.ID != "" {
				indexDestination(s.Destinations, existing, newSlug)
			}
		case "function":
			if existing := s.Functions[indexKey(oldSlug)]; existing.ID != "" {
				indexFunction(s.Functions, existing, newSlug)
			}
		}
	}
	return nil
}

func platformFlags(cmd *cobra.Command) (apiURL, workspace, tenantKey, token string) {
	apiURL, _ = cmd.Flags().GetString("api-url")
	workspace, _ = cmd.Flags().GetString("workspace")
	tenantKey, _ = cmd.Flags().GetString("tenant-key")
	token, _ = cmd.Flags().GetString("token")

	if cfg, err := auth.Load(); err == nil {
		if profile, ok := cfg.Current(); ok {
			if !cmd.Flags().Changed("api-url") && profile.APIURL != "" {
				apiURL = profile.APIURL
			}
			if !cmd.Flags().Changed("workspace") && profile.WorkspaceID != "" {
				workspace = profile.WorkspaceID
			}
			if !cmd.Flags().Changed("tenant-key") && profile.TenantKey != "" {
				tenantKey = profile.TenantKey
			}
			if !cmd.Flags().Changed("token") && profile.Token != "" {
				token = profile.Token
			}
		}
	}
	if tenantKey == "" {
		tenantKey = tenantKeyFromToken(token)
	}
	return strings.TrimRight(apiURL, "/"), workspace, tenantKey, normalizeToken(token)
}

func tenantKeyFromToken(token string) string {
	user := auth.UserFromToken(normalizeToken(token))
	if user == nil {
		return ""
	}
	for _, group := range user.Groups {
		if strings.EqualFold(group.GroupType, "tenant") && strings.TrimSpace(group.GroupID) != "" {
			return strings.TrimSpace(group.GroupID)
		}
	}
	if len(user.Groups) > 0 {
		return strings.TrimSpace(user.Groups[0].GroupID)
	}
	return ""
}

func pushableLocalChanges(changes []fileChange) []fileChange {
	out := make([]fileChange, 0, len(changes))
	for _, change := range changes {
		if isLocalDeleteChange(change) {
			continue
		}
		out = append(out, change)
	}
	return out
}

func localDeleteChanges(changes []fileChange) []fileChange {
	var out []fileChange
	for _, change := range changes {
		if isLocalDeleteChange(change) {
			out = append(out, change)
		}
	}
	return out
}

func isLocalDeleteChange(change fileChange) bool {
	return change.Status == "delete-remote" || (!change.LocalExists && change.RemoteExists)
}

func printSkippedLocalDeletes(changes []fileChange) {
	if len(changes) == 0 {
		return
	}
	fmt.Printf("Skipping %d local deletion(s); cnips resources are not deleted by push.\n", len(changes))
	for _, change := range changes {
		fmt.Printf("  skipped-delete %s\n", change.Path)
	}
}

func loadWorkspaceState(client *platform.Client, workspace string) (*workspaceState, error) {
	tx, src, dst, fn, gv, cfg, pl, err := fetchWorkspaceLists(client, []string{workspace})
	if err != nil {
		return nil, err
	}
	apps, err := client.ListApps()
	if err != nil && !isHTTPNotFound(err) {
		return nil, err
	}
	return &workspaceState{
		Transformations: indexTransformations(tx),
		Sources:         indexSources(src),
		Destinations:    indexDestinations(dst),
		Apps:            indexApps(apps),
		Functions:       indexFunctions(fn),
		GlobalVariables: indexGlobalVariables(gv),
		Configurations:  indexConfigurations(cfg),
		Pipelines:       indexPipelines(pl),
	}, nil
}

func readLocalBundle(root, workspace string) (*localBundle, error) {
	b := &localBundle{}
	var err error
	for _, base := range transformationFamilyBases {
		items, err := readLocalComponents(root, base.dir)
		if err != nil {
			return nil, err
		}
		b.Transformations = append(b.Transformations, items...)
	}
	if b.Sources, err = readLocalComponents(root, "sources"); err != nil {
		return nil, err
	}
	if b.Destinations, err = readLocalComponents(root, "destinations"); err != nil {
		return nil, err
	}
	if b.Apps, err = readLocalComponents(root, "apps"); err != nil {
		return nil, err
	}
	if b.Functions, err = readLocalFunctions(root); err != nil {
		return nil, err
	}
	gvs, err := artifact.ListGlobalVariables(root)
	if err != nil {
		return nil, err
	}
	for _, gv := range gvs {
		if gv.Spec.WorkspaceID == "" || gv.Spec.WorkspaceID == workspace {
			b.GlobalVariables = append(b.GlobalVariables, *gv)
		}
	}
	configs, err := artifact.ListConfigurations(root)
	if err != nil {
		return nil, err
	}
	for _, cfg := range configs {
		if cfg.Spec.WorkspaceID == "" || cfg.Spec.WorkspaceID == workspace {
			b.Configurations = append(b.Configurations, *cfg)
		}
	}
	pipelines, err := artifact.ListPipelines(root)
	if err != nil {
		return nil, err
	}
	for _, p := range pipelines {
		b.Pipelines = append(b.Pipelines, *p)
	}
	return b, nil
}

func readLocalComponents(root, base string) ([]localComponent, error) {
	baseDir := filepath.Join(root, base)
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, nil
	}
	var out []localComponent
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(baseDir, entry.Name())
		compDir := preferredComponentDir(dir)
		if !hasComponentManifest(compDir) {
			continue
		}
		comp, err := artifact.ParseComponent(compDir)
		if err != nil {
			return nil, err
		}
		source, isBun, err := readSourceCode(compDir, comp.Spec.Language)
		if err != nil {
			return nil, err
		}
		out = append(out, localComponent{Slug: entry.Name(), Dir: compDir, BaseDir: base, Artifact: *comp, Source: source, IsBunBuild: isBun})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func preferredComponentDir(parent string) string {
	for _, name := range []string{"latest", "vlatest"} {
		if info, err := os.Stat(filepath.Join(parent, name)); err == nil && info.IsDir() {
			return filepath.Join(parent, name)
		}
	}
	return parent
}

func hasComponentManifest(dir string) bool {
	for _, name := range []string{"component.yaml", "source.yaml", "destination.yaml", "transformation.yaml"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func readLocalFunctions(root string) ([]localFunction, error) {
	baseDir := filepath.Join(root, "functions")
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, nil
	}
	var out []localFunction
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(baseDir, entry.Name())
		if !hasFunctionManifest(dir) {
			continue
		}
		fn, err := artifact.ParseFunction(dir)
		if err != nil {
			return nil, err
		}
		source, isBun, err := readSourceCode(dir, fn.Spec.Runtime)
		if err != nil {
			return nil, err
		}
		out = append(out, localFunction{Slug: entry.Name(), Dir: dir, Artifact: *fn, Source: source, IsBunBuild: isBun})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func hasFunctionManifest(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "cnips.fn.yaml"))
	return err == nil && !info.IsDir()
}

func buildPushPlan(b *localBundle, s *workspaceState) pushPlan {
	var p pushPlan
	add := func(kind, name string, existingID string) {
		action := "create"
		if existingID != "" {
			action = "update"
		}
		p.Items = append(p.Items, pushPlanItem{Kind: kind, Name: name, Action: action, ID: existingID})
	}
	for _, item := range b.Transformations {
		add(componentLocalKind(item), componentPlatformName(item), findExistingTransformation(s, item).ID)
	}
	for _, item := range b.Sources {
		add("source", componentPlatformName(item), findExistingSource(s, item).ID)
	}
	for _, item := range b.Destinations {
		add("destination", componentPlatformName(item), findExistingDestination(s, item).ID)
	}
	for _, item := range b.Functions {
		add("function", functionPlatformName(item), findExistingFunction(s, item).ID)
	}
	for _, item := range b.GlobalVariables {
		add("globalvariable", item.Metadata.Name, s.GlobalVariables[indexKey(item.Metadata.Name)].ID)
	}
	for _, item := range b.Configurations {
		add("configuration", item.Metadata.Name, s.Configurations[indexKey(item.Metadata.Name)].ID)
	}
	for _, item := range b.Pipelines {
		if pipelineContainsAIConversationalAgent(item) {
			existing := findExistingPipeline(s, item)
			p.Items = append(p.Items, pushPlanItem{
				Kind:   "pipeline",
				Name:   item.Metadata.Name,
				Action: "skip",
				ID:     existing.ID,
			})
			continue
		}
		add("pipeline", item.Metadata.Name, findExistingPipeline(s, item).ID)
	}
	return p
}

func pipelineContainsAIConversationalAgent(item artifact.Pipeline) bool {
	for _, step := range item.Spec.Steps {
		if isAIConversationalAgentUses(step.Uses) {
			return true
		}
	}
	return false
}

func isAIConversationalAgentUses(uses string) bool {
	namespace, name, _ := artifact.UsesKind(uses)
	if namespace != "" && namespace != "native" {
		return false
	}
	name = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "_", "-"))
	return name == "ai-conversational-agent"
}

func componentLocalKind(item localComponent) string {
	kind := strings.ToLower(strings.TrimSpace(item.Artifact.Spec.Type))
	if transformationFamilyKind(kind) {
		return kind
	}
	if kind == "source" {
		return "source"
	}
	if kind == "destination" {
		return "destination"
	}
	if item.BaseDir != "" {
		return kindForComponentBase(item.BaseDir)
	}
	return "transformation"
}

func printPushPlan(plan pushPlan, apiURL, workspace string, dryRun bool) {
	fmt.Printf("Push target: %s  workspace=%s\n", apiURL, workspace)
	if dryRun {
		fmt.Println("Dry-run mode: no changes will be applied.")
	}
	if len(plan.Items) == 0 {
		fmt.Println("No local artifacts found.")
		return
	}
	fmt.Println("\nPlan:")
	for _, item := range plan.Items {
		id := ""
		if item.ID != "" {
			id = " " + item.ID
		}
		fmt.Printf("  %-13s %-7s %s%s\n", item.Kind, item.Action, item.Name, id)
	}
}

func applyBundle(client *platform.Client, workspace string, b *localBundle, s *workspaceState, progress *terminalProgress, journal *pushRollbackJournal) (applyStats, error) {
	var stats applyStats
	populateSwitchLabelsFromPipelines(b)
	for _, item := range b.Transformations {
		localKind := componentLocalKind(item)
		body := componentToTransformation(item, workspace)
		if existing := findExistingTransformation(s, item); existing.ID != "" {
			body.ID = existing.ID
			updated, err := updateTransformationWhenReady(client, workspace, localKind, item.Slug, existing.ID, body, b, s)
			if err != nil {
				return stats, fmt.Errorf("update %s %s: %w", localKind, item.Slug, err)
			}
			before := existing
			journal.Updated(localKind+" "+item.Slug, existing.ID, func(client *platform.Client, workspace string) error {
				_, err := client.UpdateTransformation(workspace, before.ID, before)
				return err
			})
			if updated != nil {
				indexTransformation(s.Transformations, *updated, item.Slug)
			}
			if componentNeedsServerBuild(item) {
				if err := waitForTransformationBuild(client, workspace, item, s); err != nil {
					return stats, err
				}
			}
			stats.updated++
		} else if created, err := client.CreateTransformation(workspace, body); err != nil {
			return stats, fmt.Errorf("create %s %s: %w", localKind, item.Slug, err)
		} else if created != nil {
			indexTransformation(s.Transformations, *created, item.Slug)
			createdID := created.ID
			journal.Created(localKind+" "+item.Slug, func(client *platform.Client, workspace string) error {
				return client.DeleteTransformation(workspace, createdID)
			})
			if componentNeedsServerBuild(item) {
				if err := waitForTransformationBuild(client, workspace, item, s); err != nil {
					return stats, err
				}
			}
			stats.created++
		}
		progress.Advance()
	}
	for _, item := range b.Sources {
		body := componentToSource(item, workspace)
		if existing := findExistingSource(s, item); existing.ID != "" {
			body.ID = existing.ID
			updated, err := updateSourceWhenReady(client, workspace, item.Slug, existing.ID, body, b, s)
			if err != nil {
				return stats, fmt.Errorf("update source %s: %w", item.Slug, err)
			}
			before := existing
			journal.Updated("source "+item.Slug, existing.ID, func(client *platform.Client, workspace string) error {
				_, err := client.UpdateSource(workspace, before.ID, before)
				return err
			})
			if updated != nil {
				indexSource(s.Sources, *updated, item.Slug)
			}
			if componentNeedsServerBuild(item) {
				if err := waitForSourceBuild(client, workspace, item, s); err != nil {
					return stats, err
				}
			}
			stats.updated++
		} else if created, err := client.CreateSource(workspace, body); err != nil {
			return stats, fmt.Errorf("create source %s: %w", item.Slug, err)
		} else if created != nil {
			indexSource(s.Sources, *created, item.Slug)
			createdID := created.ID
			journal.Created("source "+item.Slug, func(client *platform.Client, workspace string) error {
				return client.DeleteSource(workspace, createdID)
			})
			if componentNeedsServerBuild(item) {
				if err := waitForSourceBuild(client, workspace, item, s); err != nil {
					return stats, err
				}
			}
			stats.created++
		}
		progress.Advance()
	}
	for _, item := range b.Destinations {
		body := componentToDestination(item, workspace)
		if existing := findExistingDestination(s, item); existing.ID != "" {
			body.ID = existing.ID
			updated, err := updateDestinationWhenReady(client, workspace, item.Slug, existing.ID, body, b, s)
			if err != nil {
				return stats, fmt.Errorf("update destination %s: %w", item.Slug, err)
			}
			before := existing
			journal.Updated("destination "+item.Slug, existing.ID, func(client *platform.Client, workspace string) error {
				_, err := client.UpdateDestination(workspace, before.ID, before)
				return err
			})
			if updated != nil {
				indexDestination(s.Destinations, *updated, item.Slug)
			}
			if componentNeedsServerBuild(item) {
				if err := waitForDestinationBuild(client, workspace, item, s); err != nil {
					return stats, err
				}
			}
			stats.updated++
		} else if created, err := client.CreateDestination(workspace, body); err != nil {
			return stats, fmt.Errorf("create destination %s: %w", item.Slug, err)
		} else if created != nil {
			indexDestination(s.Destinations, *created, item.Slug)
			createdID := created.ID
			journal.Created("destination "+item.Slug, func(client *platform.Client, workspace string) error {
				return client.DeleteDestination(workspace, createdID)
			})
			if componentNeedsServerBuild(item) {
				if err := waitForDestinationBuild(client, workspace, item, s); err != nil {
					return stats, err
				}
			}
			stats.created++
		}
		progress.Advance()
	}
	for _, item := range b.Functions {
		body := functionToPlatform(item, workspace)
		if existing := findExistingFunction(s, item); existing.ID != "" {
			body.ID = existing.ID
			updated, err := updateFunctionWhenReady(client, workspace, item.Slug, existing.ID, body, b, s)
			if err != nil {
				return stats, fmt.Errorf("update function %s: %w", item.Slug, err)
			}
			before := existing
			journal.Updated("function "+item.Slug, existing.ID, func(client *platform.Client, workspace string) error {
				_, err := client.UpdateFunction(workspace, before.ID, before)
				return err
			})
			if updated != nil {
				indexFunction(s.Functions, *updated, item.Slug)
			}
			if functionNeedsServerBuild(item) {
				if err := waitForFunctionBuild(client, workspace, item, s); err != nil {
					return stats, err
				}
			}
			stats.updated++
		} else if created, err := client.CreateFunction(workspace, body); err != nil {
			return stats, fmt.Errorf("create function %s: %w", item.Slug, err)
		} else if created != nil {
			indexFunction(s.Functions, *created, item.Slug)
			createdID := created.ID
			journal.Created("function "+item.Slug, func(client *platform.Client, workspace string) error {
				return client.DeleteFunction(workspace, createdID)
			})
			if functionNeedsServerBuild(item) {
				if err := waitForFunctionBuild(client, workspace, item, s); err != nil {
					return stats, err
				}
			}
			stats.created++
		}
		progress.Advance()
	}
	for _, item := range b.GlobalVariables {
		body := globalVariableToPlatform(item, workspace)
		key := indexKey(item.Metadata.Name)
		if existing := s.GlobalVariables[key]; existing.ID != "" {
			body.ID = existing.ID
			if updated, err := client.UpdateGlobalVariable(workspace, existing.ID, body); err != nil {
				return stats, fmt.Errorf("update global variable %s: %w", item.Metadata.Name, err)
			} else if updated != nil {
				before := existing
				journal.Updated("global variable "+item.Metadata.Name, existing.ID, func(client *platform.Client, workspace string) error {
					_, err := client.UpdateGlobalVariable(workspace, before.ID, before)
					return err
				})
				s.GlobalVariables[key] = *updated
			}
			stats.updated++
		} else if created, err := client.CreateGlobalVariable(workspace, body); err != nil {
			return stats, fmt.Errorf("create global variable %s: %w", item.Metadata.Name, err)
		} else if created != nil {
			s.GlobalVariables[key] = *created
			createdID := created.ID
			journal.Created("global variable "+item.Metadata.Name, func(client *platform.Client, workspace string) error {
				return client.DeleteGlobalVariable(workspace, createdID)
			})
			stats.created++
		}
		progress.Advance()
	}
	for _, item := range b.Configurations {
		body := configurationToPlatform(item, workspace)
		key := indexKey(item.Metadata.Name)
		if existing := s.Configurations[key]; existing.ID != "" {
			body.ID = existing.ID
			if updated, err := client.UpdateConfiguration(workspace, existing.ID, body); err != nil {
				return stats, fmt.Errorf("update configuration %s: %w", item.Metadata.Name, err)
			} else if updated != nil {
				before := existing
				journal.Updated("configuration "+item.Metadata.Name, existing.ID, func(client *platform.Client, workspace string) error {
					_, err := client.UpdateConfiguration(workspace, before.ID, before)
					return err
				})
				s.Configurations[key] = *updated
			}
			stats.updated++
		} else if created, err := client.CreateConfiguration(workspace, body); err != nil {
			return stats, fmt.Errorf("create configuration %s: %w", item.Metadata.Name, err)
		} else if created != nil {
			s.Configurations[key] = *created
			createdID := created.ID
			journal.Created("configuration "+item.Metadata.Name, func(client *platform.Client, workspace string) error {
				return client.DeleteConfiguration(workspace, createdID)
			})
			stats.created++
		}
		progress.Advance()
	}
	if hasBuildableChanges(b) {
		refreshed, err := waitForServerBuilds(client, workspace, b)
		if err != nil {
			return stats, err
		}
		*s = *refreshed
	}
	indexLocalBundleAliases(s, b)
	for _, item := range b.Pipelines {
		if pipelineContainsAIConversationalAgent(item) {
			stats.skipped++
			progress.Advance()
			continue
		}
		body, err := pipelineToPlatform(item, workspace, s)
		if err != nil {
			return stats, err
		}
		if existing := findExistingPipeline(s, item); existing.ID != "" {
			body.ID = existing.ID
			if updated, err := client.UpdatePipeline(workspace, existing.ID, body); err != nil {
				return stats, fmt.Errorf("update pipeline %s: %w", item.Metadata.Name, err)
			} else if updated != nil {
				before := existing
				journal.Updated("pipeline "+item.Metadata.Name, existing.ID, func(client *platform.Client, workspace string) error {
					_, err := client.UpdatePipeline(workspace, before.ID, before)
					return err
				})
				indexPipeline(s.Pipelines, *updated, item.Metadata.Name)
			}
			stats.updated++
		} else if created, err := client.CreatePipeline(workspace, body); err != nil {
			return stats, fmt.Errorf("create pipeline %s: %w", item.Metadata.Name, err)
		} else if created != nil {
			indexPipeline(s.Pipelines, *created, item.Metadata.Name)
			createdID := created.ID
			journal.Created("pipeline "+item.Metadata.Name, func(client *platform.Client, workspace string) error {
				return client.DeletePipeline(workspace, createdID)
			})
			stats.created++
		}
		progress.Advance()
	}
	return stats, nil
}

func updateTransformationWhenReady(client *platform.Client, workspace, kind, slug, id string, body platform.Transformation, b *localBundle, s *workspaceState) (*platform.Transformation, error) {
	return retryAfterPendingBuild(client, workspace, kind+"/"+slug, b, s, func() (*platform.Transformation, error) {
		return client.UpdateTransformation(workspace, id, body)
	})
}

func updateSourceWhenReady(client *platform.Client, workspace, slug, id string, body platform.Source, b *localBundle, s *workspaceState) (*platform.Source, error) {
	return retryAfterPendingBuild(client, workspace, "source/"+slug, b, s, func() (*platform.Source, error) {
		return client.UpdateSource(workspace, id, body)
	})
}

func updateDestinationWhenReady(client *platform.Client, workspace, slug, id string, body platform.Destination, b *localBundle, s *workspaceState) (*platform.Destination, error) {
	return retryAfterPendingBuild(client, workspace, "destination/"+slug, b, s, func() (*platform.Destination, error) {
		return client.UpdateDestination(workspace, id, body)
	})
}

func updateFunctionWhenReady(client *platform.Client, workspace, slug, id string, body platform.Function, b *localBundle, s *workspaceState) (*platform.Function, error) {
	return retryAfterPendingBuild(client, workspace, "function/"+slug, b, s, func() (*platform.Function, error) {
		return client.UpdateFunction(workspace, id, body)
	})
}

func retryAfterPendingBuild[T any](client *platform.Client, workspace, label string, b *localBundle, s *workspaceState, update func() (*T, error)) (*T, error) {
	updated, err := update()
	if err == nil {
		return updated, nil
	}
	if !isBuildPendingError(err) {
		return nil, err
	}
	if !hasBuildableChanges(b) {
		return nil, err
	}
	fmt.Printf("\n  %s build is pending; waiting before retrying update.\n", label)
	refreshed, waitErr := waitForServerBuilds(client, workspace, b)
	if waitErr != nil {
		return nil, waitErr
	}
	*s = *refreshed
	return update()
}

func isBuildPendingError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "build is pending")
}

func hasBuildableChanges(b *localBundle) bool {
	if b == nil {
		return false
	}
	for _, item := range b.Transformations {
		if componentNeedsServerBuild(item) {
			return true
		}
	}
	for _, item := range b.Sources {
		if componentNeedsServerBuild(item) {
			return true
		}
	}
	for _, item := range b.Destinations {
		if componentNeedsServerBuild(item) {
			return true
		}
	}
	for _, item := range b.Functions {
		if functionNeedsServerBuild(item) {
			return true
		}
	}
	return false
}

func componentNeedsServerBuild(item localComponent) bool {
	if !item.HasCodeChanges {
		return false
	}
	kind := componentLocalKind(item)
	if !isExtractorSourceComponent(item) && strings.EqualFold(kind, "source") {
		return false
	}
	return componentBuildStatus(kind, item.Artifact.Spec.SignatureVersion, item.Artifact.Spec.Language) == "pending"
}

func isExtractorSourceComponent(item localComponent) bool {
	sourceType := strings.ToUpper(strings.TrimSpace(item.Artifact.Spec.SourceType))
	return sourceType == "" || sourceType == "EXTRACTOR"
}

func functionNeedsServerBuild(item localFunction) bool {
	if !item.HasCodeChanges {
		return false
	}
	return componentBuildStatus("FUNCTION", item.Artifact.Spec.SignatureVersion, item.Artifact.Spec.Runtime) == "pending"
}

func waitForTransformationBuild(client *platform.Client, workspace string, item localComponent, s *workspaceState) error {
	return waitForServerBuild(client, workspace, componentLocalKind(item)+"/"+item.Slug, s, func(state *workspaceState) string {
		existing := findExistingTransformation(state, item)
		if existing.ID == "" {
			return "pending"
		}
		return existing.BuildStatus
	})
}

func waitForSourceBuild(client *platform.Client, workspace string, item localComponent, s *workspaceState) error {
	return waitForServerBuild(client, workspace, "source/"+item.Slug, s, func(state *workspaceState) string {
		existing := findExistingSource(state, item)
		if existing.ID == "" {
			return "pending"
		}
		return existing.BuildStatus
	})
}

func waitForDestinationBuild(client *platform.Client, workspace string, item localComponent, s *workspaceState) error {
	return waitForServerBuild(client, workspace, "destination/"+item.Slug, s, func(state *workspaceState) string {
		existing := findExistingDestination(state, item)
		if existing.ID == "" {
			return "pending"
		}
		return existing.BuildStatus
	})
}

func waitForFunctionBuild(client *platform.Client, workspace string, item localFunction, s *workspaceState) error {
	return waitForServerBuild(client, workspace, "function/"+item.Slug, s, func(state *workspaceState) string {
		existing := findExistingFunction(state, item)
		if existing.ID == "" {
			return "pending"
		}
		return existing.BuildStatus
	})
}

func waitForServerBuild(client *platform.Client, workspace, label string, s *workspaceState, statusOf func(*workspaceState) string) error {
	const (
		timeout      = 2 * time.Minute
		pollInterval = 2 * time.Second
	)
	deadline := time.Now().Add(timeout)
	for {
		state, err := loadWorkspaceState(client, workspace)
		if err != nil {
			return fmt.Errorf("reload workspace state after %s push: %w", label, err)
		}
		*s = *state
		status := strings.ToLower(strings.TrimSpace(statusOf(state)))
		switch status {
		case "", "success":
			return nil
		case "failed":
			return fmt.Errorf("server-side build failed for %s", label)
		}
		if time.Now().After(deadline) {
			if status == "pending" {
				return fmt.Errorf("timed out waiting for server-side build: %s", label)
			}
			return fmt.Errorf("timed out waiting for server-side build: %s=%s", label, status)
		}
		if status == "pending" {
			fmt.Printf("  waiting for cnips build: %s\n", label)
		} else {
			fmt.Printf("  waiting for cnips build: %s=%s\n", label, status)
		}
		time.Sleep(pollInterval)
	}
}

func waitForServerBuilds(client *platform.Client, workspace string, b *localBundle) (*workspaceState, error) {
	const (
		timeout      = 2 * time.Minute
		pollInterval = 2 * time.Second
	)
	deadline := time.Now().Add(timeout)
	for {
		state, err := loadWorkspaceState(client, workspace)
		if err != nil {
			return nil, fmt.Errorf("reload workspace state after component push: %w", err)
		}
		pending, failed := buildStatusSummary(state, b)
		if len(failed) > 0 {
			return nil, fmt.Errorf("server-side build failed for %s", strings.Join(failed, ", "))
		}
		if len(pending) == 0 {
			return state, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for server-side build: %s", strings.Join(pending, ", "))
		}
		fmt.Printf("  waiting for cnips build: %s\n", strings.Join(pending, ", "))
		time.Sleep(pollInterval)
	}
}

func buildStatusSummary(s *workspaceState, b *localBundle) (pending, failed []string) {
	check := func(label, status string) {
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "", "success":
			return
		case "failed":
			failed = append(failed, label)
		case "pending":
			pending = append(pending, label)
		default:
			pending = append(pending, label+"="+status)
		}
	}
	for _, item := range b.Transformations {
		check(componentLocalKind(item)+"/"+item.Slug, findExistingTransformation(s, item).BuildStatus)
	}
	for _, item := range b.Sources {
		check("source/"+item.Slug, findExistingSource(s, item).BuildStatus)
	}
	for _, item := range b.Destinations {
		check("destination/"+item.Slug, findExistingDestination(s, item).BuildStatus)
	}
	for _, item := range b.Functions {
		check("function/"+item.Slug, findExistingFunction(s, item).BuildStatus)
	}
	return pending, failed
}

func componentToTransformation(item localComponent, workspace string) platform.Transformation {
	comp := item.Artifact
	return platform.Transformation{
		Name:             componentPlatformName(item),
		WorkspaceID:      workspace,
		Type:             strings.ToUpper(firstNonEmpty(comp.Spec.Type, componentLocalKind(item))),
		Description:      firstNonEmpty(comp.Metadata.Description, comp.Spec.Description),
		Language:         platformLanguage(comp.Spec.Language),
		SignatureVersion: comp.Spec.SignatureVersion,
		TemplateVersion:  comp.Spec.TemplateVersion,
		Config:           configMapToItems(comp.Spec.Config),
		SourceCode:       sourceCodePayload(item.Source, componentBuildStatusForChange(item.HasCodeChanges, comp.Spec.Type, comp.Spec.SignatureVersion, comp.Spec.Language)),
		SwitchLabels:     switchLabelsForLocalComponent(item),
	}
}

func populateSwitchLabelsFromPipelines(b *localBundle) {
	if b == nil || len(b.Pipelines) == 0 || len(b.Transformations) == 0 {
		return
	}
	labelsBySlug := pipelineSwitchLabels(b.Pipelines)
	for i := range b.Transformations {
		if componentLocalKind(b.Transformations[i]) != "switch" {
			continue
		}
		keys := lookupKeys(b.Transformations[i].Slug, b.Transformations[i].Artifact.Metadata.Name, b.Transformations[i].Artifact.Metadata.Slug)
		var labels []string
		labels = append(labels, b.Transformations[i].Artifact.Spec.SwitchLabels...)
		for _, key := range keys {
			labels = append(labels, labelsBySlug[key]...)
		}
		b.Transformations[i].Artifact.Spec.SwitchLabels = normalizedSwitchLabels(labels)
	}
}

func pipelineSwitchLabels(pipelines []artifact.Pipeline) map[string][]string {
	out := map[string][]string{}
	for _, pipeline := range pipelines {
		for _, step := range pipeline.Spec.Steps {
			namespace, name, _ := artifact.UsesKind(step.Uses)
			if namespace != "switch" {
				continue
			}
			labels := switchLabelsFromStep(step)
			if len(labels) == 0 {
				continue
			}
			key := indexKey(name)
			out[key] = normalizedSwitchLabels(append(out[key], labels...))
		}
	}
	return out
}

func switchLabelsForLocalComponent(item localComponent) []string {
	if componentLocalKind(item) != "switch" {
		return nil
	}
	return normalizedSwitchLabels(item.Artifact.Spec.SwitchLabels)
}

func normalizedSwitchLabels(labels []string) []string {
	if len(labels) == 0 {
		return nil
	}
	out := make([]string, 0, len(labels))
	seen := map[string]bool{}
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" || seen[label] {
			continue
		}
		out = append(out, label)
		seen[label] = true
	}
	sort.Strings(out)
	return out
}

func switchLabelsFromStep(step artifact.Step) []string {
	var labels []string
	for label := range step.Cases {
		labels = append(labels, label)
	}
	if withCases, ok := step.With["cases"].(map[string]any); ok {
		for label := range withCases {
			labels = append(labels, label)
		}
	}
	if withCases, ok := step.With["cases"].(map[string]string); ok {
		for label := range withCases {
			labels = append(labels, label)
		}
	}
	return normalizedSwitchLabels(labels)
}

func componentToSource(item localComponent, workspace string) platform.Source {
	comp := item.Artifact
	sourceType := strings.ToUpper(strings.TrimSpace(comp.Spec.SourceType))
	if sourceType == "" {
		sourceType = "EXTRACTOR"
	}
	return platform.Source{
		Name:             componentPlatformName(item),
		WorkspaceID:      workspace,
		SourceType:       sourceType,
		Description:      firstNonEmpty(comp.Metadata.Description, comp.Spec.Description),
		Language:         platformLanguage(comp.Spec.Language),
		SignatureVersion: comp.Spec.SignatureVersion,
		TemplateVersion:  comp.Spec.TemplateVersion,
		APIAccessRef:     comp.Spec.APIAccessRef,
		Config:           configMapToItems(comp.Spec.Config),
		SourceCode:       sourceCodePayload(item.Source, sourceBuildStatusForChange(item.HasCodeChanges, sourceType, comp.Spec.SignatureVersion, comp.Spec.Language)),
		Active:           true,
	}
}

func componentToDestination(item localComponent, workspace string) platform.Destination {
	comp := item.Artifact
	return platform.Destination{
		Name:             componentPlatformName(item),
		WorkspaceID:      workspace,
		Description:      firstNonEmpty(comp.Metadata.Description, comp.Spec.Description),
		Language:         platformLanguage(comp.Spec.Language),
		SignatureVersion: comp.Spec.SignatureVersion,
		TemplateVersion:  comp.Spec.TemplateVersion,
		Config:           configMapToItems(comp.Spec.Config),
		SourceCode:       sourceCodePayload(item.Source, componentBuildStatusForChange(item.HasCodeChanges, "DESTINATION", comp.Spec.SignatureVersion, comp.Spec.Language)),
	}
}

func functionToPlatform(item localFunction, workspace string) platform.Function {
	fn := item.Artifact
	return platform.Function{
		Name:             functionPlatformName(item),
		WorkspaceID:      workspace,
		Description:      firstNonEmpty(fn.Metadata.Description, fn.Spec.Description),
		Language:         platformLanguage(fn.Spec.Runtime),
		SignatureVersion: fn.Spec.SignatureVersion,
		TemplateVersion:  fn.Spec.TemplateVersion,
		APIAccessRef:     fn.Spec.APIAccessRef,
		Config:           stringMapToItems(fn.Spec.Config),
		SourceCode:       sourceCodePayload(item.Source, componentBuildStatusForChange(item.HasCodeChanges, "FUNCTION", fn.Spec.SignatureVersion, fn.Spec.Runtime)),
		Active:           true,
	}
}

func globalVariableToPlatform(item artifact.GlobalVariable, workspace string) platform.GlobalVariable {
	return platform.GlobalVariable{
		Key:         firstNonEmpty(item.Spec.Key, item.Metadata.Name),
		Value:       item.Spec.Value,
		WorkspaceID: workspace,
	}
}

func configurationToPlatform(item artifact.Configuration, workspace string) platform.Configuration {
	return platform.Configuration{
		Name:        item.Metadata.Name,
		Description: item.Metadata.Description,
		Type:        item.Spec.Type,
		Key:         item.Spec.Key,
		Value:       item.Spec.Value,
		APIAccess:   item.Spec.APIAccess,
		WorkspaceID: workspace,
		Active:      true,
	}
}

func pipelineToPlatform(p artifact.Pipeline, workspace string, s *workspaceState) (platform.Pipeline, error) {
	nodeIDByStep := make(map[string]string, len(p.Spec.Steps))
	for _, step := range p.Spec.Steps {
		nodeIDByStep[step.ID] = step.ID
	}
	existingPipeline := findExistingPipeline(s, p)
	out := platform.Pipeline{
		Name:        p.Metadata.Name,
		Description: firstNonEmpty(p.Metadata.Description, p.Spec.Description),
		WorkspaceID: workspace,
		Active:      true,
		TransformationMap: &platform.TransformationMap{
			EdgeMap:       nil,
			NodePositions: map[string]platform.Position{},
		},
	}
	if p.Spec.Schedule != "" {
		out.Schedule = &platform.Schedule{CronExpression: p.Spec.Schedule, TimeZone: "UTC", Active: true}
	}
	if p.Spec.Retry != nil {
		out.RetrySchedule = &platform.RetrySchedule{AfterSeconds: p.Spec.Retry.BackoffSec, MaxRetries: p.Spec.Retry.MaxAttempts}
	}
	for _, v := range p.Spec.Variables {
		ref := firstNonEmpty(v.Ref, s.GlobalVariables[indexKey(v.Name)].ID)
		if ref != "" {
			out.GlobalVariables = append(out.GlobalVariables, ref)
		}
	}

	for _, step := range p.Spec.Steps {
		comp, err := pipelineComponentForStep(step, s)
		if err != nil {
			return out, fmt.Errorf("pipeline %s step %s: %w", p.Metadata.Name, step.ID, err)
		}
		comp.ID = nodeIDByStep[step.ID]
		if strings.EqualFold(comp.Type, "SWITCH") {
			comp.SwitchLabels = normalizedSwitchLabels(append(comp.SwitchLabels, switchLabelsFromStep(step)...))
		}
		out.TransformationMap.NodePositions[comp.ID] = pipelineNodePosition(p, step.ID, comp, existingPipeline)
		switch strings.ToUpper(comp.Type) {
		case "SOURCE", "EXTRACTOR", "STANDARD":
			out.SourceList = append(out.SourceList, comp)
		case "DESTINATION":
			out.DestinationList = append(out.DestinationList, comp)
		case "APPROVAL":
			out.ApprovalList = append(out.ApprovalList, comp)
		default:
			out.TransformationList = append(out.TransformationList, comp)
		}
		addStepEdges(&out, step, nodeIDByStep)
	}
	return out, nil
}

func pipelineNodePosition(p artifact.Pipeline, stepID string, comp platform.PipelineComponent, existing platform.Pipeline) platform.Position {
	if p.Layout != nil && p.Layout.Positions != nil {
		if pos, ok := p.Layout.Positions[stepID]; ok {
			return platform.Position{X: pos.X, Y: pos.Y}
		}
	}
	if existing.TransformationMap == nil || existing.TransformationMap.NodePositions == nil {
		return platform.Position{}
	}
	if pos, ok := existing.TransformationMap.NodePositions[comp.ID]; ok {
		return pos
	}
	if existingID, ok := uniqueExistingPipelineNodeID(existing, comp.NodeID); ok {
		if pos, ok := existing.TransformationMap.NodePositions[existingID]; ok {
			return pos
		}
	}
	return platform.Position{}
}

func uniqueExistingPipelineNodeID(p platform.Pipeline, nodeID string) (string, bool) {
	if nodeID == "" {
		return "", false
	}
	var match string
	for _, comp := range pipelineComponents(p) {
		if comp.NodeID != nodeID {
			continue
		}
		if match != "" && match != comp.ID {
			return "", false
		}
		match = comp.ID
	}
	return match, match != ""
}

func pipelineComponents(p platform.Pipeline) []platform.PipelineComponent {
	total := len(p.SourceList) + len(p.TransformationList) + len(p.DestinationList) + len(p.ApprovalList) + len(p.AIConvAgentList)
	out := make([]platform.PipelineComponent, 0, total)
	out = append(out, p.SourceList...)
	out = append(out, p.TransformationList...)
	out = append(out, p.DestinationList...)
	out = append(out, p.ApprovalList...)
	out = append(out, p.AIConvAgentList...)
	return out
}

func pipelineComponentForStep(step artifact.Step, s *workspaceState) (platform.PipelineComponent, error) {
	namespace, name, version := artifact.UsesKind(step.Uses)
	if version == "" {
		version = "latest"
	}
	comp := platform.PipelineComponent{
		Name:        firstNonEmpty(name, step.ID),
		UsedVersion: version,
		Config:      step.With,
	}
	if namespace == "native" {
		comp.Type = nativeStepType(name)
		comp.NodeID = step.ID
		comp.NativeConfig = step.With
		return comp, nil
	}
	switch namespace {
	case "app":
		existing := findExistingApp(s, name)
		if existing.ID == "" {
			return comp, fmt.Errorf("unknown app %q", name)
		}
		comp.Type = firstNonEmpty(existing.Type, "APP")
		comp.NodeID = existing.ID
		comp.Name = existing.Name
		comp.IsApp = true
		comp.AppType = firstNonEmpty(existing.AppType, "public")
	case "transformation":
		existing := s.Transformations[name]
		if existing.ID == "" {
			return comp, fmt.Errorf("unknown transformation %q", name)
		}
		comp.Type = firstNonEmpty(existing.Type, "TRANSFORMATION")
		comp.NodeID = existing.ID
		comp.Name = existing.Name
		comp.SwitchLabels = existing.SwitchLabels
	case "approval", "switch", "decision":
		existing := s.Transformations[name]
		if existing.ID == "" {
			return comp, fmt.Errorf("unknown %s %q", namespace, name)
		}
		comp.Type = firstNonEmpty(existing.Type, strings.ToUpper(namespace))
		comp.NodeID = existing.ID
		comp.Name = existing.Name
		comp.SwitchLabels = existing.SwitchLabels
	case "source":
		existing := s.Sources[name]
		if existing.ID == "" {
			return comp, fmt.Errorf("unknown source %q", name)
		}
		comp.Type = firstNonEmpty(existing.SourceType, "SOURCE")
		comp.NodeID = existing.ID
		comp.Name = existing.Name
	case "destination":
		existing := s.Destinations[name]
		if existing.ID == "" {
			return comp, fmt.Errorf("unknown destination %q", name)
		}
		comp.Type = "DESTINATION"
		comp.NodeID = existing.ID
		comp.Name = existing.Name
	case "function":
		return comp, fmt.Errorf("function pipeline steps are not supported by the current mgmt-srv pipeline model")
	default:
		return comp, fmt.Errorf("unsupported uses %q", step.Uses)
	}
	return comp, nil
}

func addStepEdges(out *platform.Pipeline, step artifact.Step, nodeIDByStep map[string]string) {
	add := func(target string, label any) {
		if target == "" {
			return
		}
		out.TransformationMap.EdgeMap = append(out.TransformationMap.EdgeMap, platform.EdgeMap{
			Source: nodeIDByStep[step.ID],
			Target: nodeIDByStep[target],
			Label:  label,
		})
	}
	add(step.Next, nil)
	for label, target := range step.Branches {
		add(target, label)
	}
	for label, target := range step.Cases {
		add(target, label)
	}
}

func nativeStepType(name string) string {
	switch strings.ToLower(name) {
	case "decision":
		return "DECISION"
	case "switch":
		return "SWITCH"
	case "loop":
		return "LOOP"
	case "approval":
		return "APPROVAL"
	default:
		return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	}
}

func readSourceCode(dir, runtime string) (platform.SourceCode, bool, error) {
	var source platform.SourceCode
	lang := sourceCodeLanguage(runtime)
	switch lang {
	case "JS":
		script, err := readOptional(filepath.Join(dir, "handler.js"))
		if err != nil {
			return source, false, err
		}
		pkg, err := readOptional(filepath.Join(dir, "package.json"))
		if err != nil {
			return source, false, err
		}
		source.Script = script
		if pkg != "" {
			source.JavaScript = &platform.JavaScript{PackageJSON: pkg}
		}
		return source, pkg != "", nil
	case "GO":
		main, err := readOptional(filepath.Join(dir, "main.go"))
		if err != nil {
			return source, false, err
		}
		mod, err := readOptional(filepath.Join(dir, "go.mod"))
		if err != nil {
			return source, false, err
		}
		source.Golang = &platform.Golang{Main: main, Mod: mod}
	case "PYTHON":
		main, err := readOptional(filepath.Join(dir, "handler.py"))
		if err != nil {
			return source, false, err
		}
		req, err := readOptional(filepath.Join(dir, "requirements.txt"))
		if err != nil {
			return source, false, err
		}
		source.Python = &platform.Python{Main: main, Requirements: req}
	default:
		script, err := readOptional(filepath.Join(dir, "handler.script"))
		if err != nil {
			return source, false, err
		}
		source.Script = script
	}
	return source, false, nil
}

func sourceCodePayload(source platform.SourceCode, buildStatus string) platform.SourceCode {
	source.BuildStatus = buildStatus
	return source
}

func sourceBuildStatus(sourceType, signatureVersion, runtime string) string {
	return sourceBuildStatusForChange(true, sourceType, signatureVersion, runtime)
}

func sourceBuildStatusForChange(hasCodeChanges bool, sourceType, signatureVersion, runtime string) string {
	if !hasCodeChanges {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(sourceType), "EXTRACTOR") {
		return ""
	}
	return componentBuildStatus("EXTRACTOR", signatureVersion, runtime)
}

func componentBuildStatusForChange(hasCodeChanges bool, kind, signatureVersion, runtime string) string {
	if !hasCodeChanges {
		return ""
	}
	return componentBuildStatus(kind, signatureVersion, runtime)
}

func componentBuildStatus(kind, signatureVersion, runtime string) string {
	if noServerBuildRequired(kind, signatureVersion, platformLanguage(runtime)) {
		return ""
	}
	return "pending"
}

func noServerBuildRequired(kind, signatureVersion, language string) bool {
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "SOURCE":
		kind = "EXTRACTOR"
	default:
		kind = strings.ToUpper(strings.TrimSpace(kind))
	}
	signatureVersion = strings.TrimSpace(signatureVersion)
	language = platformLanguage(language)
	switch {
	case language == "javascript" && signatureVersion == "v1" && (kind == "TRANSFORMATION" || kind == "DECISION" || kind == "DESTINATION" || kind == "EXTRACTOR"):
		return true
	case language == "javascript" && signatureVersion == "v2" && (kind == "TRANSFORMATION" || kind == "DECISION" || kind == "DESTINATION"):
		return true
	case language == "javascript" && signatureVersion == "express-v1" && kind == "FUNCTION":
		return true
	default:
		return false
	}
}

func readOptional(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func configMapToItems(config map[string]any) []platform.ConfigItem {
	if len(config) == 0 {
		return nil
	}
	keys := make([]string, 0, len(config))
	for key := range config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]platform.ConfigItem, 0, len(keys))
	for _, key := range keys {
		items = append(items, platform.ConfigItem{Key: key, Value: fmt.Sprint(config[key])})
	}
	return items
}

func stringMapToItems(config map[string]string) []platform.ConfigItem {
	if len(config) == 0 {
		return nil
	}
	keys := make([]string, 0, len(config))
	for key := range config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]platform.ConfigItem, 0, len(keys))
	for _, key := range keys {
		items = append(items, platform.ConfigItem{Key: key, Value: config[key]})
	}
	return items
}

func platformLanguage(runtime string) string {
	switch strings.ToUpper(runtime) {
	case "JS", "JAVASCRIPT", "NODE", "NODEJS", "NODEJS22":
		return "javascript"
	case "GO", "GOLANG":
		return "golang"
	case "PY", "PYTHON", "PYTHON3":
		return "python"
	default:
		return strings.ToLower(strings.TrimSpace(runtime))
	}
}

func sourceCodeLanguage(runtime string) string {
	switch strings.ToUpper(runtime) {
	case "JS", "JAVASCRIPT", "NODE", "NODEJS", "NODEJS22":
		return "JS"
	case "GO", "GOLANG":
		return "GO"
	case "PY", "PYTHON", "PYTHON3":
		return "PYTHON"
	default:
		return strings.ToUpper(strings.TrimSpace(runtime))
	}
}

func indexTransformations(items []platform.Transformation) map[string]platform.Transformation {
	out := map[string]platform.Transformation{}
	for _, item := range items {
		indexTransformation(out, item)
	}
	return out
}

func indexSources(items []platform.Source) map[string]platform.Source {
	out := map[string]platform.Source{}
	for _, item := range items {
		indexSource(out, item)
	}
	return out
}

func indexDestinations(items []platform.Destination) map[string]platform.Destination {
	out := map[string]platform.Destination{}
	for _, item := range items {
		indexDestination(out, item)
	}
	return out
}

func indexFunctions(items []platform.Function) map[string]platform.Function {
	out := map[string]platform.Function{}
	for _, item := range items {
		indexFunction(out, item)
	}
	return out
}

func indexApps(items []platform.App) map[string]platform.App {
	out := map[string]platform.App{}
	for _, item := range items {
		indexApp(out, item)
	}
	return out
}

func indexGlobalVariables(items []platform.GlobalVariable) map[string]platform.GlobalVariable {
	out := map[string]platform.GlobalVariable{}
	for _, item := range items {
		out[indexKey(firstNonEmpty(item.Key, item.ID))] = item
	}
	return out
}

func indexConfigurations(items []platform.Configuration) map[string]platform.Configuration {
	out := map[string]platform.Configuration{}
	for _, item := range items {
		out[indexKey(firstNonEmpty(item.Name, item.Key, item.ID))] = item
	}
	return out
}

func indexPipelines(items []platform.Pipeline) map[string]platform.Pipeline {
	out := map[string]platform.Pipeline{}
	for _, item := range items {
		indexPipeline(out, item)
	}
	return out
}

func indexKey(name string) string {
	return serializer.Slug(name)
}

func indexTransformation(out map[string]platform.Transformation, item platform.Transformation, aliases ...string) {
	for _, key := range lookupKeys(append([]string{item.ID, item.AltID, item.Name}, aliases...)...) {
		out[key] = item
	}
}

func indexSource(out map[string]platform.Source, item platform.Source, aliases ...string) {
	for _, key := range lookupKeys(append([]string{item.ID, item.AltID, item.Name}, aliases...)...) {
		out[key] = item
	}
}

func indexDestination(out map[string]platform.Destination, item platform.Destination, aliases ...string) {
	for _, key := range lookupKeys(append([]string{item.ID, item.AltID, item.Name}, aliases...)...) {
		out[key] = item
	}
}

func indexPipeline(out map[string]platform.Pipeline, item platform.Pipeline, aliases ...string) {
	for _, key := range lookupKeys(append([]string{item.ID, item.Name}, aliases...)...) {
		out[key] = item
	}
}

func indexFunction(out map[string]platform.Function, item platform.Function, aliases ...string) {
	for _, key := range lookupKeys(append([]string{item.ID, item.AltID, item.Name}, aliases...)...) {
		out[key] = item
	}
}

func indexApp(out map[string]platform.App, item platform.App, aliases ...string) {
	for _, key := range lookupKeys(append([]string{item.ID, item.AltID, item.Name}, aliases...)...) {
		out[key] = item
	}
}
