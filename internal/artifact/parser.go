package artifact

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseProject reads and parses cnips.yaml from the given project root.
func ParseProject(root string) (*Project, error) {
	return parseFile[Project](filepath.Join(root, "cnips.yaml"))
}

// ParseLock reads cnips.lock from the project root.
func ParseLock(root string) (*Lock, error) {
	path := filepath.Join(root, "cnips.lock")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &Lock{APIVersion: "cnips.io/v1", Kind: KindLock, Spec: LockSpec{}}, nil
	}
	return parseFile[Lock](path)
}

// ParsePipeline reads a pipeline.yaml given a pipeline directory.
func ParsePipeline(pipelineDir string) (*Pipeline, error) {
	p, err := parseFile[Pipeline](filepath.Join(pipelineDir, "pipeline.yaml"))
	if err != nil {
		return nil, err
	}
	layout, err := ParsePipelineLayout(pipelineDir)
	if err != nil {
		return nil, err
	}
	p.Layout = layout
	return p, nil
}

// ParsePipelineLayout reads layout.yaml from a pipeline directory when present.
func ParsePipelineLayout(pipelineDir string) (*PipelineLayout, error) {
	path := filepath.Join(pipelineDir, "layout.yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	return parseFile[PipelineLayout](path)
}

// ParsePipelineByName finds and parses a pipeline by name under the project root.
func ParsePipelineByName(root, name string) (*Pipeline, string, error) {
	dir := filepath.Join(root, "pipelines", name)
	p, err := ParsePipeline(dir)
	if err != nil {
		return nil, "", err
	}
	return p, dir, nil
}

// ParseFunction reads cnips.fn.yaml from a function directory.
func ParseFunction(fnDir string) (*Function, error) {
	return parseFile[Function](filepath.Join(fnDir, "cnips.fn.yaml"))
}

// ParseFunctionByName finds a function by name under the project root.
func ParseFunctionByName(root, name string) (*Function, string, error) {
	dir := filepath.Join(root, "functions", name)
	fn, err := ParseFunction(dir)
	if err != nil {
		return nil, "", err
	}
	return fn, dir, nil
}

// ParseComponent reads component.yaml (or source.yaml / destination.yaml) from a component directory.
func ParseComponent(compDir string) (*Component, error) {
	for _, name := range []string{"component.yaml", "source.yaml", "destination.yaml", "transformation.yaml"} {
		path := filepath.Join(compDir, name)
		if _, err := os.Stat(path); err == nil {
			return parseFile[Component](path)
		}
	}
	return nil, fmt.Errorf("no component manifest found in %s", compDir)
}

// ParseEnvironment reads an environment yaml by name (e.g. "dev" loads environments/dev.yaml).
func ParseEnvironment(root, name string) (*Environment, error) {
	return parseFile[Environment](filepath.Join(root, "environments", name+".yaml"))
}

func ListGlobalVariables(root string) ([]*GlobalVariable, error) {
	return listArtifacts[GlobalVariable](filepath.Join(root, "globalvariables"))
}

func ListConfigurations(root string) ([]*Configuration, error) {
	return listArtifacts[Configuration](filepath.Join(root, "configurations"))
}

// StepIndex builds a map of step ID -> Step for fast lookup.
func StepIndex(pipeline *Pipeline) map[string]*Step {
	m := make(map[string]*Step, len(pipeline.Spec.Steps))
	for i := range pipeline.Spec.Steps {
		s := &pipeline.Spec.Steps[i]
		m[s.ID] = s
	}
	return m
}

// UsesKind extracts the component kind from a uses string like "native/transformation@1".
// Returns ("native", "transformation", "1") for the above.
// Returns ("function", "enrich-customer", "1") for "function/enrich-customer@1".
// Returns ("", "sap-destination", "3") for "sap-destination@3".
func UsesKind(uses string) (namespace, name, version string) {
	// strip version
	atIdx := strings.LastIndex(uses, "@")
	if atIdx >= 0 {
		version = uses[atIdx+1:]
		uses = uses[:atIdx]
	}
	slashIdx := strings.Index(uses, "/")
	if slashIdx >= 0 {
		namespace = uses[:slashIdx]
		name = uses[slashIdx+1:]
	} else {
		name = uses
	}
	return
}

// LocalComponentRef describes a local component found under the project root.
type LocalComponentRef struct {
	Dir  string
	Kind string
}

// LocalComponent resolves the local directory and kind for a component reference.
// Searches components/, apps/, sources/, destinations/, transformations/,
// approvals/, switches/, and decisions/ under the root.
func LocalComponent(root, name string) (LocalComponentRef, bool) {
	return LocalComponentVersion(root, name, "")
}

// LocalComponentVersion resolves the local directory and kind for a component
// reference, selecting a version subdirectory when the component has versioned
// local sources.
func LocalComponentVersion(root, name, version string) (LocalComponentRef, bool) {
	return LocalComponentKindVersion(root, name, "", version)
}

// LocalComponentKindVersion resolves a local component constrained to one kind
// when kind is set. Kind may be component, app, source, destination,
// transformation, approval, switch, or decision.
func LocalComponentKindVersion(root, name, kind, version string) (LocalComponentRef, bool) {
	candidates := localComponentCandidates(root, name, kind)
	if len(candidates) == 0 {
		return LocalComponentRef{}, false
	}
	for _, c := range candidates {
		if info, err := os.Stat(c.dir); err == nil && info.IsDir() {
			if versionDir, ok := resolveComponentVersionDir(c.dir, version); ok {
				return LocalComponentRef{Dir: versionDir, Kind: c.kind}, true
			}
			if hasVersionDirs(c.dir) {
				continue
			}
			return LocalComponentRef{Dir: c.dir, Kind: c.kind}, true
		}
	}
	return LocalComponentRef{}, false
}

func localComponentCandidates(root, name, kind string) []struct {
	dir  string
	kind string
} {
	candidates := []struct {
		dir  string
		kind string
	}{
		{dir: filepath.Join(root, "components", name), kind: "component"},
		{dir: filepath.Join(root, "apps", name), kind: "app"},
		{dir: filepath.Join(root, "sources", name), kind: "source"},
		{dir: filepath.Join(root, "destinations", name), kind: "destination"},
		{dir: filepath.Join(root, "transformations", name), kind: "transformation"},
		{dir: filepath.Join(root, "approvals", name), kind: "approval"},
		{dir: filepath.Join(root, "switches", name), kind: "switch"},
		{dir: filepath.Join(root, "decisions", name), kind: "decision"},
	}
	if kind == "" {
		return candidates
	}
	for _, candidate := range candidates {
		if candidate.kind == kind {
			return []struct {
				dir  string
				kind string
			}{candidate}
		}
	}
	return nil
}

// LocalComponentDir resolves the local directory for a component reference.
func LocalComponentDir(root, name string) (string, bool) {
	return LocalComponentDirVersion(root, name, "")
}

// LocalComponentDirVersion resolves the local directory for a component reference version.
func LocalComponentDirVersion(root, name, version string) (string, bool) {
	ref, ok := LocalComponentVersion(root, name, version)
	if !ok {
		return "", false
	}
	return ref.Dir, true
}

// LocalComponentDirKindVersion resolves a component directory constrained to one kind.
func LocalComponentDirKindVersion(root, name, kind, version string) (string, bool) {
	ref, ok := LocalComponentKindVersion(root, name, kind, version)
	if !ok {
		return "", false
	}
	return ref.Dir, true
}

func resolveComponentVersionDir(componentDir, version string) (string, bool) {
	version = normalizeUsesVersion(version)
	if version != "" {
		if dir, ok := existingVersionDir(componentDir, version); ok {
			return dir, true
		}
		return "", false
	}
	if dir, ok := existingVersionDir(componentDir, "latest"); ok {
		return dir, true
	}
	return "", false
}

func existingVersionDir(componentDir, version string) (string, bool) {
	for _, candidate := range versionCandidates(version) {
		dir := filepath.Join(componentDir, candidate)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, true
		}
	}
	return "", false
}

func hasVersionDirs(componentDir string) bool {
	entries, err := os.ReadDir(componentDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "latest" || strings.HasPrefix(name, "v") {
			return true
		}
	}
	return false
}

func versionCandidates(version string) []string {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	if strings.EqualFold(version, "latest") {
		return []string{"latest"}
	}
	if strings.HasPrefix(version, "v") {
		return []string{version, strings.TrimPrefix(version, "v")}
	}
	return []string{"v" + version, version}
}

func normalizeUsesVersion(version string) string {
	version = strings.TrimSpace(version)
	if strings.EqualFold(version, "latest") {
		return "latest"
	}
	return version
}

// parseFile is a generic YAML file parser.
func parseFile[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var v T
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &v, nil
}

func listArtifacts[T any](base string) ([]*T, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, nil
	}
	var out []*T
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		artifact, err := parseFile[T](filepath.Join(base, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, artifact)
	}
	return out, nil
}

// ListPipelines returns all parsed pipelines under <root>/pipelines/.
func ListPipelines(root string) ([]*Pipeline, error) {
	base := filepath.Join(root, "pipelines")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, nil // no pipelines directory
	}
	var out []*Pipeline
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := ParsePipeline(filepath.Join(base, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// WriteYAML marshals v to YAML and writes to path.
func WriteYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
