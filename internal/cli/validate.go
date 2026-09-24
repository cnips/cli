package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/project"
)

var validateOpts struct {
	Workspace   string
	AllowLatest bool
	Type        string
}

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate local cnips project files before push",
	Long: `Validates local cnips project files before push.

Checks performed:
  - Project, component, function, pipeline, environment, configuration, and variable YAML parse correctly
  - Pipeline graph, trigger, component/function, schema-file, and lockfile references resolve locally
  - Secret-like YAML fields must use references instead of literal values
  - cnips.lock matches local component integrity and required dependency metadata`,
	RunE: runValidate,
}

func init() {
	validateCmd.Flags().StringVar(&validateOpts.Workspace, "workspace", "default", "Workspace ID whose local base manifest should be used")
	validateCmd.Flags().BoolVar(&validateOpts.AllowLatest, "allow-latest", false, "Accepted for compatibility; latest references are allowed")
	validateCmd.Flags().StringVarP(&validateOpts.Type, "type", "t", "", "Optional component type to validate")
	rootCmd.AddCommand(validateCmd)
}

type validationReport struct {
	mu       sync.Mutex
	errors   []string
	warnings []string
}

func (r *validationReport) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *validationReport) Warnf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

func (r *validationReport) snapshot() (errs, warns []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	errs = append(errs, r.errors...)
	warns = append(warns, r.warnings...)
	sort.Strings(errs)
	sort.Strings(warns)
	return errs, warns
}

func runValidate(cmd *cobra.Command, _ []string) error {
	root := project.MustFindRoot()
	workspace, _ := cmd.Flags().GetString("workspace")
	report := &validationReport{}
	selectedType, err := normalizeValidationType(validateOpts.Type)
	if err != nil {
		return err
	}

	tasks := []func() error{func() error { validateProjectManifest(root, report); return nil }}
	if selectedType == "" || selectedType == "pipeline" {
		tasks = append(tasks, func() error { validatePipelines(root, report); return nil })
	}
	if selectedType == "" || selectedType == "function" {
		tasks = append(tasks, func() error { validateFunctions(root, report); return nil })
	}
	if selectedType == "" || validationComponentType(selectedType) {
		tasks = append(tasks, func() error { validateComponents(root, selectedType, report); return nil })
	}
	if selectedType == "" || selectedType == "globalvariable" || selectedType == "configuration" {
		tasks = append(tasks, func() error { validateWorkspaceFiles(root, workspace, report); return nil })
	}
	if selectedType == "" {
		tasks = append(tasks,
			func() error { validateLocalReferences(root, validateOpts.AllowLatest, report); return nil },
			func() error { validateSecretLeaks(root, report); return nil },
			func() error { validateEnvironments(root, report); return nil },
			func() error { validateLockFile(root, report); return nil },
		)
	}
	if err := runConcurrent(tasks...); err != nil {
		return err
	}

	errs, warns := report.snapshot()
	for _, w := range warns {
		fmt.Printf("  WARN  %s\n", w)
	}
	for _, e := range errs {
		fmt.Printf("  ERROR %s\n", e)
	}
	if len(errs) > 0 {
		return fmt.Errorf("validation failed: %d error(s), %d warning(s)", len(errs), len(warns))
	}
	fmt.Printf("✓ Validation passed: %d warning(s)\n", len(warns))
	return nil
}

func validateProjectManifest(root string, report *validationReport) {
	proj, err := artifact.ParseProject(root)
	if err != nil {
		report.Errorf("cnips.yaml: %v", err)
		return
	}
	if proj.Metadata.Name == "" {
		report.Errorf("cnips.yaml: metadata.name is empty")
	}
	if proj.Spec.Runtime == "" {
		report.Warnf("cnips.yaml: spec.runtime not set")
	}
}

func validatePipelines(root string, report *validationReport) {
	base := filepath.Join(root, "pipelines")
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	var tasks []func() error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		entry := entry
		tasks = append(tasks, func() error {
			dir := filepath.Join(base, entry.Name())
			p, err := artifact.ParsePipeline(dir)
			if err != nil {
				report.Errorf("pipelines/%s/pipeline.yaml: %v", entry.Name(), err)
				return nil
			}
			if p.Metadata.Name != entry.Name() {
				report.Errorf("pipelines/%s: metadata.name %q must exactly match directory name %q", entry.Name(), p.Metadata.Name, entry.Name())
			}
			for _, validationErr := range validatePipeline(p) {
				report.Errorf("%s", validationErr)
			}
			return nil
		})
	}
	_ = runConcurrent(tasks...)
}

func validateFunctions(root string, report *validationReport) {
	functions, err := readLocalFunctions(root)
	if err != nil {
		report.Errorf("functions: %v", err)
		return
	}
	var tasks []func() error
	for _, fn := range functions {
		fn := fn
		tasks = append(tasks, func() error {
			if fn.Artifact.Spec.Runtime == "" {
				report.Errorf("functions/%s: spec.runtime is required", fn.Slug)
			}
			if fn.Artifact.Metadata.Name != fn.Slug {
				report.Errorf("functions/%s: metadata.name %q must exactly match directory name %q", fn.Slug, fn.Artifact.Metadata.Name, fn.Slug)
			}
			if _, err := normalizeFunctionMethod(fn.Artifact.Spec.Method()); err != nil {
				report.Errorf("functions/%s: spec.type must be GET, POST, PUT, or DELETE", fn.Slug)
			}
			return nil
		})
	}
	_ = runConcurrent(tasks...)
}

func validateComponents(root, selectedType string, report *validationReport) {
	var tasks []func() error
	for _, base := range localComponentBases {
		if selectedType != "" && selectedType != base.kind {
			continue
		}
		base := base
		tasks = append(tasks, func() error {
			items, err := readValidationComponents(root, base.dir)
			if err != nil {
				report.Errorf("%s: %v", base.dir, err)
				return nil
			}
			for _, item := range items {
				if item.Artifact.Metadata.Name == "" {
					report.Errorf("%s: metadata.name is required", relPath(root, item.Dir))
				}
				if item.Artifact.Spec.Language == "" && componentRequiresLanguage(base.dir, item) {
					report.Errorf("%s: spec.language is required", relPath(root, item.Dir))
				}
				if item.Artifact.Metadata.Name != item.Slug {
					report.Errorf("%s/%s: metadata.name %q must exactly match directory name %q", base.dir, item.Slug, item.Artifact.Metadata.Name, item.Slug)
				}
			}
			return nil
		})
	}
	_ = runConcurrent(tasks...)
}

func normalizeValidationType(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	supported := map[string]bool{"pipeline": true, "function": true, "component": true, "source": true, "destination": true, "transformation": true, "approval": true, "switch": true, "decision": true, "globalvariable": true, "configuration": true}
	if !supported[value] {
		return "", fmt.Errorf("unsupported --type %q", value)
	}
	return value, nil
}

func validationComponentType(value string) bool {
	switch value {
	case "component", "source", "destination", "transformation", "approval", "switch", "decision":
		return true
	default:
		return false
	}
}

func validateWorkspaceFiles(root, workspace string, report *validationReport) {
	gvs, err := artifact.ListGlobalVariables(root)
	if err != nil {
		report.Errorf("globalvariables: %v", err)
	}
	configs, err := artifact.ListConfigurations(root)
	if err != nil {
		report.Errorf("configurations: %v", err)
	}
	for _, gv := range gvs {
		path := filepath.ToSlash(filepath.Join("globalvariables", gv.Metadata.Name+".yaml"))
		if gv.Metadata.Name == "" {
			report.Errorf("%s: metadata.name is required", path)
		}
		if gv.Spec.Key == "" {
			report.Errorf("%s: spec.key is required", path)
		}
		if gv.Spec.WorkspaceID != "" && gv.Spec.WorkspaceID != workspace {
			continue
		}
	}
	for _, cfg := range configs {
		path := filepath.ToSlash(filepath.Join("configurations", cfg.Metadata.Name+".yaml"))
		if cfg.Metadata.Name == "" {
			report.Errorf("%s: metadata.name is required", path)
		}
		if cfg.Spec.Type == "" {
			report.Errorf("%s: spec.type is required", path)
		}
		if cfg.Spec.WorkspaceID != "" && cfg.Spec.WorkspaceID != workspace {
			continue
		}
	}
}

func componentRequiresLanguage(base string, item localComponent) bool {
	if base != "sources" {
		return true
	}
	sourceType := strings.ToUpper(item.Artifact.Spec.SourceType)
	if sourceType == "STANDARD" || sourceType == "SOURCE" || sourceType == "" {
		return componentHasSourceFiles(item.Dir)
	}
	return true
}

func componentHasSourceFiles(dir string) bool {
	for _, name := range []string{"handler.js", "main.go", "handler.py", "handler.script", "package.json", "go.mod"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func readValidationComponents(root, base string) ([]localComponent, error) {
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
		parent := filepath.Join(baseDir, entry.Name())
		for _, dir := range componentManifestDirs(parent) {
			comp, err := artifact.ParseComponent(dir)
			if err != nil {
				return nil, err
			}
			source, isBun, err := readSourceCode(dir, comp.Spec.Language)
			if err != nil {
				return nil, err
			}
			slug := entry.Name()
			if dir != parent {
				slug = filepath.ToSlash(filepath.Join(entry.Name(), filepath.Base(dir)))
			}
			out = append(out, localComponent{Slug: slug, Dir: dir, Artifact: *comp, Source: source, IsBunBuild: isBun})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}

func componentManifestDirs(parent string) []string {
	var dirs []string
	if hasComponentManifest(parent) {
		dirs = append(dirs, parent)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return dirs
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(parent, entry.Name())
		if hasComponentManifest(dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func validateLocalReferences(root string, allowLatest bool, report *validationReport) {
	bundle, err := readLocalBundle(root, validateOpts.Workspace)
	if err != nil {
		report.Errorf("local bundle: %v", err)
		return
	}
	lock, err := artifact.ParseLock(root)
	if err != nil {
		report.Errorf("cnips.lock: %v", err)
		return
	}
	lockEntries := lockEntrySet(lock)
	localEntries := localReferenceSet(bundle)
	validateArtifactIdentity(root, bundle, report)
	validatePipelineReferences(root, bundle, lockEntries, localEntries, allowLatest, report)
	validateSchemaReferences(root, bundle, report)
}

func lockEntrySet(lock *artifact.Lock) map[string]artifact.LockComponent {
	out := map[string]artifact.LockComponent{}
	for _, component := range lock.Spec.Components {
		out[lockKey(strings.ToLower(component.Kind), component.Name)] = component
	}
	return out
}

func localReferenceSet(bundle *localBundle) map[string]bool {
	out := map[string]bool{}
	addComponent := func(kind string, items []localComponent) {
		for _, item := range items {
			names := []string{item.Slug, item.Artifact.Metadata.Name, item.Artifact.Metadata.Slug}
			for _, name := range names {
				if name != "" {
					out[lockKey(kind, name)] = true
				}
			}
		}
	}
	for _, item := range bundle.Transformations {
		kind := componentLocalKind(item)
		addComponent(kind, []localComponent{item})
		if kind != "transformation" {
			addComponent("transformation", []localComponent{item})
		}
	}
	addComponent("source", bundle.Sources)
	addComponent("destination", bundle.Destinations)
	for _, item := range bundle.Functions {
		for _, name := range []string{item.Slug, item.Artifact.Metadata.Name, item.Artifact.Metadata.Slug} {
			if name != "" {
				out[lockKey("function", name)] = true
			}
		}
	}
	return out
}

func validateArtifactIdentity(root string, bundle *localBundle, report *validationReport) {
	seen := map[string]string{}
	check := func(kind, slug, dir, name, metaSlug string) {
		rel := relPath(root, dir)
		if name == "" {
			report.Errorf("%s: metadata.name is required", rel)
		} else if slug != "" && name != slug && metaSlug == "" {
			report.Warnf("%s: directory slug %q differs from metadata.name %q; set metadata.slug if this is intentional", rel, slug, name)
		}
		canonical := firstNonEmpty(metaSlug, name, slug)
		key := lockKey(kind, strings.ToLower(canonical))
		if prev, ok := seen[key]; ok {
			report.Errorf("%s: duplicate %s identity %q also declared in %s", rel, kind, canonical, prev)
		}
		seen[key] = rel
	}
	for _, item := range bundle.Transformations {
		check(componentLocalKind(item), item.Slug, item.Dir, item.Artifact.Metadata.Name, item.Artifact.Metadata.Slug)
	}
	for _, item := range bundle.Sources {
		check("source", item.Slug, item.Dir, item.Artifact.Metadata.Name, item.Artifact.Metadata.Slug)
	}
	for _, item := range bundle.Destinations {
		check("destination", item.Slug, item.Dir, item.Artifact.Metadata.Name, item.Artifact.Metadata.Slug)
	}
	for _, item := range bundle.Functions {
		check("function", item.Slug, item.Dir, item.Artifact.Metadata.Name, item.Artifact.Metadata.Slug)
	}
	for _, p := range bundle.Pipelines {
		if p.Metadata.Name == "" {
			report.Errorf("pipelines: pipeline metadata.name is required")
		}
	}
}

func validatePipelineReferences(root string, bundle *localBundle, lockEntries map[string]artifact.LockComponent, localEntries map[string]bool, allowLatest bool, report *validationReport) {
	pipelinesByName := map[string]string{}
	for _, p := range bundle.Pipelines {
		name := p.Metadata.Name
		if name == "" {
			continue
		}
		path := filepath.ToSlash(filepath.Join("pipelines", name, "pipeline.yaml"))
		if prev, ok := pipelinesByName[strings.ToLower(name)]; ok {
			report.Errorf("%s: duplicate pipeline name also declared in %s", path, prev)
		}
		pipelinesByName[strings.ToLower(name)] = path
		if p.Spec.Trigger != nil && p.Spec.Trigger.SourceRef != "" {
			if !referenceExists("source", p.Spec.Trigger.SourceRef, lockEntries, localEntries) {
				report.Errorf("%s: trigger sourceRef %q does not resolve to a local or locked source", path, p.Spec.Trigger.SourceRef)
			}
		}
		for _, step := range p.Spec.Steps {
			validateStepUses(path, step.ID, step.Uses, lockEntries, localEntries, allowLatest, report)
			validateNestedSteps(path, step.ID, step.With, lockEntries, localEntries, allowLatest, report)
		}
	}
	_ = root
}

func validateNestedSteps(path, parent string, with map[string]any, lockEntries map[string]artifact.LockComponent, localEntries map[string]bool, allowLatest bool, report *validationReport) {
	body, ok := with["body"]
	if !ok {
		return
	}
	steps, ok := body.([]any)
	if !ok {
		return
	}
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := step["id"].(string)
		uses, _ := step["uses"].(string)
		validateStepUses(path, firstNonEmpty(id, parent+".body"), uses, lockEntries, localEntries, allowLatest, report)
	}
}

func validateStepUses(path, stepID, uses string, lockEntries map[string]artifact.LockComponent, localEntries map[string]bool, allowLatest bool, report *validationReport) {
	if uses == "" {
		return
	}
	namespace, name, version := artifact.UsesKind(uses)
	if name == "" {
		report.Errorf("%s step %q: uses %q is invalid", path, stepID, uses)
		return
	}
	if namespace == "native" {
		if version == "" {
			report.Warnf("%s step %q: native component %q has no version pin", path, stepID, uses)
		}
		return
	}
	_ = allowLatest
	kind := namespace
	if kind == "" {
		kind = inferReferencedKind(name, lockEntries, localEntries)
	}
	if kind == "" {
		if namespace == "" {
			return
		}
		report.Errorf("%s step %q: uses %q does not identify a known component kind", path, stepID, uses)
		return
	}
	if !referenceExists(kind, name, lockEntries, localEntries) {
		report.Errorf("%s step %q: uses %q does not resolve to a local artifact or cnips.lock entry", path, stepID, uses)
	}
}

func inferReferencedKind(name string, lockEntries map[string]artifact.LockComponent, localEntries map[string]bool) string {
	for _, kind := range []string{"source", "transformation", "approval", "switch", "decision", "destination", "function", "app"} {
		if referenceExists(kind, name, lockEntries, localEntries) {
			return kind
		}
	}
	return ""
}

func referenceExists(kind, name string, lockEntries map[string]artifact.LockComponent, localEntries map[string]bool) bool {
	key := lockKey(strings.ToLower(kind), name)
	if localEntries[key] {
		return true
	}
	_, ok := lockEntries[key]
	return ok
}

func validateSchemaReferences(root string, bundle *localBundle, report *validationReport) {
	check := func(owner, dir string, ref *artifact.SchemaRef) {
		if ref == nil || strings.TrimSpace(ref.Schema) == "" {
			return
		}
		schemaPath := ref.Schema
		if strings.Contains(schemaPath, "://") {
			return
		}
		if !filepath.IsAbs(schemaPath) {
			schemaPath = filepath.Join(dir, schemaPath)
		}
		info, err := os.Stat(schemaPath)
		if err != nil || info.IsDir() {
			report.Errorf("%s: schema reference %q does not exist", owner, ref.Schema)
		}
	}
	for _, item := range bundle.Transformations {
		owner := relPath(root, item.Dir)
		check(owner, item.Dir, item.Artifact.Spec.Input)
		check(owner, item.Dir, item.Artifact.Spec.Output)
	}
	for _, item := range bundle.Sources {
		owner := relPath(root, item.Dir)
		check(owner, item.Dir, item.Artifact.Spec.Input)
		check(owner, item.Dir, item.Artifact.Spec.Output)
	}
	for _, item := range bundle.Destinations {
		owner := relPath(root, item.Dir)
		check(owner, item.Dir, item.Artifact.Spec.Input)
		check(owner, item.Dir, item.Artifact.Spec.Output)
	}
	for _, item := range bundle.Functions {
		owner := relPath(root, item.Dir)
		check(owner, item.Dir, item.Artifact.Spec.Input)
		check(owner, item.Dir, item.Artifact.Spec.Output)
	}
}

func validateEnvironments(root string, report *validationReport) {
	envBase := filepath.Join(root, "environments")
	entries, err := os.ReadDir(envBase)
	if err != nil {
		return
	}
	var tasks []func() error
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		e := e
		tasks = append(tasks, func() error {
			if err := validateEnvFile(filepath.Join(envBase, e.Name())); err != nil {
				report.Errorf("%s", err.Error())
			}
			return nil
		})
	}
	_ = runConcurrent(tasks...)
}

func validateSecretLeaks(root string, report *validationReport) {
	files, err := collectValidationFiles(root)
	if err != nil {
		report.Errorf("secret scan: %v", err)
		return
	}
	for path, body := range files {
		for _, finding := range secretFindings(path, body) {
			report.Errorf("%s", finding)
		}
	}
}

func collectValidationFiles(root string) (map[string]string, error) {
	out := map[string]string{}
	allowed := []string{
		"cnips.yaml",
		"cnips.lock",
		"pipelines",
		"transformations",
		"approvals",
		"switches",
		"decisions",
		"sources",
		"destinations",
		"functions",
		"globalvariables",
		"configurations",
		"environments",
	}
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
				switch d.Name() {
				case ".cnips", ".git", "node_modules", ".venv", "vendor", "dist", "build":
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

func secretFindings(path, body string) []string {
	var findings []string
	if strings.Contains(path, "cnips.lock") {
		return findings
	}
	if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(body), &node); err == nil && len(node.Content) > 0 {
			secretYAMLFindings(path, "", node.Content[0], &findings)
		}
		return dedupeStrings(findings)
	}
	return dedupeStrings(findings)
}

func secretYAMLFindings(path, keyPath string, node *yaml.Node, findings *[]string) {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			nextPath := key
			if keyPath != "" {
				nextPath = keyPath + "." + key
			}
			value := node.Content[i+1]
			if forbiddenSecretField(key) && scalarLiteralSecretValue(value) {
				*findings = append(*findings, fmt.Sprintf("%s: field %s contains a literal credential; use ref instead", path, nextPath))
			} else if forbiddenSecretField(key) && mappingLiteralValue(value) {
				*findings = append(*findings, fmt.Sprintf("%s: field %s.value contains a literal credential; use ref instead", path, nextPath))
			}
			secretYAMLFindings(path, nextPath, value, findings)
		}
	case yaml.SequenceNode:
		for i, item := range node.Content {
			secretYAMLFindings(path, fmt.Sprintf("%s[%d]", keyPath, i), item, findings)
		}
	}
}

func forbiddenSecretField(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, safe := range []string{"placeholder", "placement", "location", "method", "type", "url", "wellknown", "scope"} {
		if strings.Contains(key, safe) {
			return false
		}
	}
	for _, needle := range []string{"password", "passwd", "secret", "token", "api_key", "apikey", "access_key", "private_key", "client_secret"} {
		if strings.Contains(key, needle) {
			return true
		}
	}
	return false
}

func mappingLiteralValue(node *yaml.Node) bool {
	value := mappingValue(node, "value")
	return scalarLiteralSecretValue(value)
}

func scalarLiteralSecretValue(node *yaml.Node) bool {
	if node == nil || node.Kind != yaml.ScalarNode {
		return false
	}
	value := strings.TrimSpace(node.Value)
	if value == "" || safeReferenceValue(value) {
		return false
	}
	return true
}

func safeReferenceValue(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "${{") ||
		strings.HasPrefix(value, "$") ||
		strings.HasPrefix(value, "env:") ||
		strings.HasPrefix(value, "platform://") ||
		strings.HasPrefix(value, "secret://") ||
		strings.HasPrefix(value, "ref:") {
		return true
	}
	return false
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func validateLockFile(root string, report *validationReport) {
	lock, err := artifact.ParseLock(root)
	if err != nil {
		report.Errorf("cnips.lock: %v", err)
		return
	}
	expected, err := expectedLockComponents(root, lock)
	if err != nil {
		report.Errorf("cnips.lock: %v", err)
		return
	}
	actual := map[string]artifact.LockComponent{}
	for _, component := range lock.Spec.Components {
		key := lockKey(strings.ToLower(component.Kind), component.Name)
		if _, exists := actual[key]; exists {
			report.Errorf("cnips.lock: duplicate entry for %s %q", component.Kind, component.Name)
			continue
		}
		validateLockEntry(component, report)
		actual[key] = component
	}
	for key, want := range expected {
		got, ok := actual[key]
		if !ok {
			report.Errorf("cnips.lock: missing entry for %s %q", want.Kind, want.Name)
			continue
		}
		if got.Language != want.Language || got.SignatureVersion != want.SignatureVersion || got.TemplateVersion != want.TemplateVersion || got.Integrity != want.Integrity {
			report.Errorf("cnips.lock: entry for %s %q does not match local files; regenerate the lock file", want.Kind, want.Name)
		}
	}
	for key, got := range actual {
		if _, ok := expected[key]; !ok {
			if strings.EqualFold(got.Source, "local") {
				report.Errorf("cnips.lock: unexpected local entry for %s %q", got.Kind, got.Name)
			}
		}
	}
}

func validateLockEntry(component artifact.LockComponent, report *validationReport) {
	if component.Name == "" {
		report.Errorf("cnips.lock: component entry missing name")
	}
	if component.Kind == "" {
		report.Errorf("cnips.lock: component %q missing kind", component.Name)
	}
	if component.Version == "" {
		report.Errorf("cnips.lock: %s %q missing version", component.Kind, component.Name)
	}
	if component.Source == "" {
		report.Errorf("cnips.lock: %s %q missing source", component.Kind, component.Name)
		return
	}
	switch strings.ToLower(component.Source) {
	case "local":
		if component.Integrity == "" {
			report.Errorf("cnips.lock: local %s %q missing integrity", component.Kind, component.Name)
		}
	case "marketplace":
		return
	case "native":
		if component.Version == "" {
			report.Errorf("cnips.lock: native %s %q missing runtime version", component.Kind, component.Name)
		}
	default:
		report.Errorf("cnips.lock: %s %q has unsupported source %q", component.Kind, component.Name, component.Source)
	}
	if component.Integrity != "" && !strings.HasPrefix(component.Integrity, "sha256:") && !strings.HasPrefix(component.Integrity, "sha256-") {
		report.Errorf("cnips.lock: %s %q integrity must use sha256", component.Kind, component.Name)
	}
}

func expectedLockComponents(root string, current *artifact.Lock) (map[string]artifact.LockComponent, error) {
	expected := map[string]artifact.LockComponent{}
	add := func(name, kind, lang, sigVer, tmplVer, dir string) error {
		integrity, err := dirIntegrity(dir)
		if err != nil {
			return err
		}
		component := artifact.LockComponent{
			Name:             name,
			Kind:             kind,
			Version:          "local",
			Source:           "local",
			Language:         lang,
			SignatureVersion: sigVer,
			TemplateVersion:  tmplVer,
			Integrity:        integrity,
		}
		for _, existing := range current.Spec.Components {
			if lockKey(existing.Kind, existing.Name) != lockKey(kind, name) {
				continue
			}
			component.Version = firstNonEmpty(existing.Version, component.Version)
			component.Source = firstNonEmpty(existing.Source, component.Source)
			component.Path = existing.Path
			component.ArtifactID = existing.ArtifactID
			component.PluginFileName = existing.PluginFileName
			component.IsBunBuild = existing.IsBunBuild
			component.Publisher = existing.Publisher
			break
		}
		expected[lockKey(kind, name)] = component
		return nil
	}
	for _, base := range localComponentBases {
		items, err := readLocalComponents(root, base.dir)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			kind := componentLocalKind(item)
			if err := add(item.Slug, kind, item.Artifact.Spec.Language, item.Artifact.Spec.SignatureVersion, item.Artifact.Spec.TemplateVersion, item.Dir); err != nil {
				return nil, fmt.Errorf("%s %s: %w", kind, item.Slug, err)
			}
		}
	}
	functions, err := readLocalFunctions(root)
	if err != nil {
		return nil, err
	}
	for _, item := range functions {
		if err := add(item.Slug, "function", item.Artifact.Spec.Runtime, item.Artifact.Spec.SignatureVersion, item.Artifact.Spec.TemplateVersion, item.Dir); err != nil {
			return nil, fmt.Errorf("function %s: %w", item.Slug, err)
		}
	}
	return expected, nil
}

func validatePipeline(p *artifact.Pipeline) []string {
	var errs []string
	name := p.Metadata.Name
	if name == "" {
		errs = append(errs, "pipeline: metadata.name is empty")
		name = "<unknown>"
	}
	if len(p.Spec.Steps) == 0 {
		return errs
	}

	ids := make(map[string]bool)
	for _, s := range p.Spec.Steps {
		if s.ID == "" {
			errs = append(errs, fmt.Sprintf("pipeline %s: step missing 'id' field", name))
		} else {
			ids[s.ID] = true
		}
		if s.Uses == "" {
			errs = append(errs, fmt.Sprintf("pipeline %s step %q: 'uses' is required", name, s.ID))
		}
	}

	for _, s := range p.Spec.Steps {
		if s.Next != "" && !ids[s.Next] {
			errs = append(errs, fmt.Sprintf("pipeline %s step %q: next=%q not found", name, s.ID, s.Next))
		}
		if s.With != nil {
			errs = append(errs, checkWithRefs(name, s.ID, s.With, ids)...)
		}
		for label, target := range s.Branches {
			if target != "" && !ids[target] {
				errs = append(errs, fmt.Sprintf("pipeline %s step %q: branches.%s=%q not found", name, s.ID, label, target))
			}
		}
		for label, target := range s.Cases {
			if target != "" && !ids[target] {
				errs = append(errs, fmt.Sprintf("pipeline %s step %q: cases.%s=%q not found", name, s.ID, label, target))
			}
		}
	}
	return errs
}

func checkWithRefs(pipeline, stepID string, with map[string]any, ids map[string]bool) []string {
	var errs []string
	if branches, ok := with["branches"].(map[string]any); ok {
		for label, target := range branches {
			if t, ok := target.(string); ok && t != "" && !ids[t] {
				errs = append(errs, fmt.Sprintf("pipeline %s step %q: branches.%s=%q not found", pipeline, stepID, label, t))
			}
		}
	}
	if cases, ok := with["cases"].(map[string]any); ok {
		for label, target := range cases {
			if t, ok := target.(string); ok && t != "" && !ids[t] {
				errs = append(errs, fmt.Sprintf("pipeline %s step %q: cases.%s=%q not found", pipeline, stepID, label, t))
			}
		}
	}
	return errs
}

func validateEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("environments/%s: %w", filepath.Base(path), err)
	}
	var env artifact.Environment
	if err := yaml.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("environments/%s: %w", filepath.Base(path), err)
	}
	if env.Metadata.Name != "dev" && env.Metadata.Name != "local" {
		for name, s := range env.Spec.Secrets {
			if s.Value != "" && s.Ref == "" {
				return fmt.Errorf("environments/%s: secret %q has a literal value (use 'ref: platform://...' instead)", filepath.Base(path), name)
			}
		}
	}
	return nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
