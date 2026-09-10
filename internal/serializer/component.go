// Package serializer converts platform model objects into local YAML files
// and source code files under the cnips project directory layout.
package serializer

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/platform"
)

var reSlug = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// Slug converts a display name to a lowercase filesystem-safe slug.
func Slug(name string) string {
	s := strings.ToLower(reSlug.ReplaceAllString(name, "-"))
	return strings.Trim(s, "-")
}

// -----------------------------------------------------------------------
// Transformation serializer
// -----------------------------------------------------------------------

// WriteTransformation writes a transformation-family component to its local
// folder with source code and a component.yaml manifest.
func WriteTransformation(root string, t *platform.Transformation) (slug string, err error) {
	return WriteTransformationAs(root, t, Slug(t.Name))
}

func WriteTransformationAs(root string, t *platform.Transformation, slug string) (string, error) {
	return WriteTransformationVersionsAs(root, t, slug, nil)
}

func WriteTransformationVersionsAs(root string, t *platform.Transformation, slug string, versions []platform.Version) (string, error) {
	kind := transformationKind(t.Type)
	return writeComponentVersions(root, transformationBaseDir(kind), slug, componentWriteSpec{
		ID:               firstNonEmpty(t.ID, t.AltID),
		Name:             t.Name,
		Kind:             kind,
		Language:         t.Language,
		Description:      t.Description,
		Config:           configItemsToMap(t.Config),
		SignatureVersion: t.SignatureVersion,
		TemplateVersion:  t.TemplateVersion,
		SwitchLabels:     t.SwitchLabels,
		SourceCode:       t.SourceCode,
	}, versions)
}

func transformationKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "approval", "switch", "decision":
		return strings.ToLower(strings.TrimSpace(kind))
	default:
		return "transformation"
	}
}

func transformationBaseDir(kind string) string {
	switch kind {
	case "approval":
		return "approvals"
	case "switch":
		return "switches"
	case "decision":
		return "decisions"
	default:
		return "transformations"
	}
}

// -----------------------------------------------------------------------
// Source (extractor) serializer
// -----------------------------------------------------------------------

// WriteSource writes any source to sources/<slug>/.
// Extractor sources include code; webhook/Kafka sources produce metadata so
// pipelines can keep sourceRef associations locally.
func WriteSource(root string, s *platform.Source) (slug string, err error) {
	return WriteSourceAs(root, s, Slug(s.Name))
}

func WriteSourceAs(root string, s *platform.Source, slug string) (string, error) {
	return WriteSourceVersionsAs(root, s, slug, nil)
}

func WriteSourceVersionsAs(root string, s *platform.Source, slug string, versions []platform.Version) (string, error) {
	return writeComponentVersions(root, "sources", slug, componentWriteSpec{
		ID:               firstNonEmpty(s.ID, s.AltID),
		Name:             s.Name,
		Kind:             "source",
		SourceType:       s.NormalizedSourceType(),
		Language:         s.Language,
		Description:      s.Description,
		Config:           configItemsToMap(s.Config),
		APIAccessRef:     s.APIAccessRef,
		SignatureVersion: s.SignatureVersion,
		TemplateVersion:  s.TemplateVersion,
		SourceCode:       s.SourceCode,
	}, versions)
}

// -----------------------------------------------------------------------
// Destination serializer
// -----------------------------------------------------------------------

// WriteDestination writes a destination to destinations/<slug>/.
func WriteDestination(root string, d *platform.Destination) (slug string, err error) {
	return WriteDestinationAs(root, d, Slug(d.Name))
}

func WriteDestinationAs(root string, d *platform.Destination, slug string) (string, error) {
	return WriteDestinationVersionsAs(root, d, slug, nil)
}

func WriteDestinationVersionsAs(root string, d *platform.Destination, slug string, versions []platform.Version) (string, error) {
	return writeComponentVersions(root, "destinations", slug, componentWriteSpec{
		ID:               firstNonEmpty(d.ID, d.AltID),
		Name:             d.Name,
		Kind:             "destination",
		Language:         d.Language,
		Description:      d.Description,
		Config:           configItemsToMap(d.Config),
		SignatureVersion: d.SignatureVersion,
		TemplateVersion:  d.TemplateVersion,
		SourceCode:       d.SourceCode,
	}, versions)
}

// WriteAppVersionsAs writes app source under apps/<slug>/ using the same
// versioned layout as other component types.
func WriteAppVersionsAs(root string, app *platform.App, slug string, versions []platform.AppVersion) (string, error) {
	return writeAppVersions(root, "apps", slug, app, versions)
}

type componentWriteSpec struct {
	ID               string
	Name             string
	Kind             string
	SourceType       string
	Version          string
	Language         string
	Description      string
	Config           map[string]any
	APIAccessRef     string
	SignatureVersion string
	TemplateVersion  string
	SwitchLabels     []string
	AppType          string
	SourceCode       platform.SourceCode
}

func writeComponentVersions(root, base, slug string, spec componentWriteSpec, versions []platform.Version) (string, error) {
	dir := filepath.Join(root, base, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s/%s: %w", base, slug, err)
	}

	versionSpecs := normalizeVersionSpecs(spec, versions)
	multiVersion := len(versionSpecs) > 1

	parent := componentArtifact(slug, "", spec)
	if err := artifact.WriteYAML(filepath.Join(dir, "component.yaml"), parent); err != nil {
		return "", err
	}

	for _, versionSpec := range versionSpecs {
		versionDir := dir
		if multiVersion {
			versionDir = filepath.Join(dir, versionSpec.Version)
			if err := os.MkdirAll(versionDir, 0o755); err != nil {
				return "", err
			}
		}
		if err := writeSourceCode(versionDir, versionSpec.Language, versionSpec.Name, versionSpec.SourceCode); err != nil {
			return "", err
		}
		if multiVersion {
			comp := componentArtifact(slug, versionSpec.Version, versionSpec)
			if err := artifact.WriteYAML(filepath.Join(versionDir, "component.yaml"), comp); err != nil {
				return "", err
			}
		}
	}
	return slug, nil
}

func writeAppVersions(root, base, slug string, app *platform.App, versions []platform.AppVersion) (string, error) {
	dir := filepath.Join(root, base, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s/%s: %w", base, slug, err)
	}

	versionSpecs := normalizeAppVersionSpecs(app, versions)
	multiVersion := len(versionSpecs) > 1
	parent := componentArtifact(slug, "", versionSpecs[0])
	if err := artifact.WriteYAML(filepath.Join(dir, "component.yaml"), parent); err != nil {
		return "", err
	}

	for _, versionSpec := range versionSpecs {
		versionDir := dir
		if multiVersion {
			versionDir = filepath.Join(dir, versionSpec.Version)
			if err := os.MkdirAll(versionDir, 0o755); err != nil {
				return "", err
			}
		}
		if err := writeSourceCode(versionDir, versionSpec.Language, versionSpec.Name, versionSpec.SourceCode); err != nil {
			return "", err
		}
		if multiVersion {
			comp := componentArtifact(slug, versionSpec.Version, versionSpec)
			if err := artifact.WriteYAML(filepath.Join(versionDir, "component.yaml"), comp); err != nil {
				return "", err
			}
		}
	}
	return slug, nil
}

func normalizeVersionSpecs(parent componentWriteSpec, versions []platform.Version) []componentWriteSpec {
	out := make([]componentWriteSpec, 0, len(versions)+1)
	seen := map[string]bool{}
	for _, version := range versions {
		number := normalizeComponentVersion(version.Version)
		if number == "" || seen[number] {
			continue
		}
		spec := parent
		spec.Version = number
		if version.Name != "" {
			spec.Name = version.Name
		}
		if version.Language != "" {
			spec.Language = version.Language
		}
		if version.SignatureVersion != "" {
			spec.SignatureVersion = version.SignatureVersion
		}
		spec.SourceCode = version.SourceCode
		out = append(out, spec)
		seen[number] = true
	}
	if !seen["latest"] && hasSourceCode(parent.SourceCode) {
		latest := parent
		latest.Version = "latest"
		out = append([]componentWriteSpec{latest}, out...)
		seen["latest"] = true
	}
	if len(out) == 0 {
		parent.Version = "latest"
		out = append(out, parent)
	}
	return out
}

func normalizeAppVersionSpecs(app *platform.App, versions []platform.AppVersion) []componentWriteSpec {
	parent := componentWriteSpec{
		ID:          firstNonEmpty(app.ID, app.AltID),
		Name:        app.Name,
		Kind:        strings.ToLower(firstNonEmpty(app.Type, "app")),
		Language:    app.Language,
		Description: app.Description,
		Config:      configItemsToMap(app.Config),
		AppType:     firstNonEmpty(app.AppType, "public"),
		SourceCode:  app.SourceCode,
	}
	out := make([]componentWriteSpec, 0, len(versions)+1)
	seen := map[string]bool{}
	for _, version := range versions {
		number := normalizeComponentVersion(version.Version)
		if number == "" || seen[number] {
			continue
		}
		spec := parent
		spec.ID = firstNonEmpty(version.Ref, parent.ID)
		spec.Version = number
		spec.Name = firstNonEmpty(version.Name, parent.Name)
		spec.Kind = strings.ToLower(firstNonEmpty(version.Type, parent.Kind, "app"))
		spec.Language = firstNonEmpty(version.Language, parent.Language)
		spec.Description = firstNonEmpty(version.Description, parent.Description)
		spec.Config = configItemsToMap(version.Config)
		if spec.Config == nil {
			spec.Config = parent.Config
		}
		spec.SignatureVersion = version.SignatureVersion
		spec.TemplateVersion = version.TemplateVersion
		spec.AppType = parent.AppType
		spec.SourceCode = version.SourceCode
		out = append(out, spec)
		seen[number] = true
	}
	if !seen["latest"] && hasSourceCode(parent.SourceCode) {
		latest := parent
		latest.Version = "latest"
		out = append([]componentWriteSpec{latest}, out...)
		seen["latest"] = true
	}
	if len(out) == 0 {
		parent.Version = "latest"
		out = append(out, parent)
	}
	return out
}

func componentArtifact(slug, version string, spec componentWriteSpec) artifact.Component {
	name := firstNonEmpty(spec.Name, slug)
	localSlug := ""
	if slug != name {
		localSlug = slug
	}
	return artifact.Component{
		APIVersion: "cnips.io/v1",
		Kind:       "Component",
		Metadata: artifact.ObjectMeta{
			ID:      spec.ID,
			Name:    name,
			Slug:    localSlug,
			Version: version,
		},
		Spec: artifact.ComponentSpec{
			Type:             spec.Kind,
			SourceType:       spec.SourceType,
			Language:         spec.Language,
			Description:      spec.Description,
			Config:           spec.Config,
			APIAccessRef:     spec.APIAccessRef,
			SignatureVersion: spec.SignatureVersion,
			TemplateVersion:  spec.TemplateVersion,
			SwitchLabels:     switchLabelsForComponent(spec),
			AppType:          spec.AppType,
		},
	}
}

func switchLabelsForComponent(spec componentWriteSpec) []string {
	if !strings.EqualFold(spec.Kind, "switch") {
		return nil
	}
	return normalizedSwitchLabels(spec.SwitchLabels)
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
	return out
}

func normalizeComponentVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	if strings.EqualFold(version, "latest") {
		return "latest"
	}
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

func hasSourceCode(sc platform.SourceCode) bool {
	return sc.Script != "" ||
		sc.Golang != nil ||
		sc.Python != nil ||
		sc.JavaScript != nil
}

func configItemsToMap(items []platform.ConfigItem) map[string]any {
	if len(items) == 0 {
		return nil
	}
	config := make(map[string]any, len(items))
	for _, item := range items {
		if item.Key == "" {
			continue
		}
		config[item.Key] = sanitizePulledValue(item.Key, item.Value, "platform://component-config/"+slugPart(item.Key))
	}
	if len(config) == 0 {
		return nil
	}
	return config
}

// -----------------------------------------------------------------------
// Function serializer
// -----------------------------------------------------------------------

// WriteFunction writes a function to functions/<slug>/ with source code and
// a cnips.fn.yaml manifest.
func WriteFunction(root string, f *platform.Function) (slug string, err error) {
	return WriteFunctionAs(root, f, Slug(f.Name))
}

func WriteFunctionAs(root string, f *platform.Function, slug string) (string, error) {
	dir := filepath.Join(root, "functions", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir functions/%s: %w", slug, err)
	}

	if err := writeSourceCode(dir, f.Language, f.Name, f.SourceCode); err != nil {
		return "", err
	}

	config := make(map[string]string)
	for _, c := range f.Config {
		value := sanitizePulledValue(c.Key, c.Value, "platform://function-config/"+slugPart(c.Key))
		if text, ok := value.(string); ok {
			config[c.Key] = text
		}
	}

	fn := artifact.Function{
		APIVersion: "cnips.io/v1",
		Kind:       "Function",
		Metadata: artifact.ObjectMeta{
			ID:   firstNonEmpty(f.ID, f.AltID),
			Name: firstNonEmpty(f.Name, slug),
			Slug: functionLocalSlug(f.Name, slug),
		},
		Spec: artifact.FunctionSpec{
			Runtime:          languageRuntime(f.Language),
			Description:      f.Description,
			Config:           config,
			APIAccessRef:     f.APIAccessRef,
			SignatureVersion: f.SignatureVersion,
			TemplateVersion:  f.TemplateVersion,
		},
	}
	return slug, artifact.WriteYAML(filepath.Join(dir, "cnips.fn.yaml"), fn)
}

func functionLocalSlug(name, slug string) string {
	if name == "" || name == slug {
		return ""
	}
	return slug
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// -----------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------

// writeSourceCode writes the appropriate source files based on language.
func writeSourceCode(dir, lang, name string, sc platform.SourceCode) error {
	switch strings.ToUpper(lang) {
	case "JS", "JAVASCRIPT":
		return writeJSSource(dir, sc)
	case "GO", "GOLANG":
		return writeGoSource(dir, name, sc)
	case "PY", "PYTHON":
		return writePythonSource(dir, sc)
	default:
		// Unknown language — write the script if present and do not error.
		if sc.Script != "" {
			return writeFile(filepath.Join(dir, "handler.script"), sc.Script)
		}
	}
	return nil
}

func writeJSSource(dir string, sc platform.SourceCode) error {
	if sc.Script != "" {
		if err := writeFile(filepath.Join(dir, "handler.js"), sc.Script); err != nil {
			return err
		}
	}
	if sc.JavaScript != nil && sc.JavaScript.PackageJSON != "" {
		return writeFile(filepath.Join(dir, "package.json"), sc.JavaScript.PackageJSON)
	}
	return nil
}

func writeGoSource(dir, name string, sc platform.SourceCode) error {
	if sc.Golang == nil {
		return nil
	}
	if sc.Golang.Main != "" {
		if err := writeFile(filepath.Join(dir, "main.go"), sc.Golang.Main); err != nil {
			return err
		}
	}
	if sc.Golang.Mod != "" {
		return writeFile(filepath.Join(dir, "go.mod"), sc.Golang.Mod)
	}
	return nil
}

func writePythonSource(dir string, sc platform.SourceCode) error {
	if sc.Python == nil {
		return nil
	}
	if sc.Python.Main != "" {
		if err := writeFile(filepath.Join(dir, "handler.py"), sc.Python.Main); err != nil {
			return err
		}
	}
	if sc.Python.Requirements != "" {
		return writeFile(filepath.Join(dir, "requirements.txt"), sc.Python.Requirements)
	}
	return nil
}

// writeFile writes content to path, creating parent dirs as needed.
// Skips if content is empty to avoid creating blank placeholder files.
func writeFile(path, content string) error {
	if content == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// languageRuntime maps platform language codes to local runtime identifiers.
func languageRuntime(lang string) string {
	switch strings.ToUpper(lang) {
	case "JS", "JAVASCRIPT":
		return "js"
	case "GO", "GOLANG":
		return "go"
	case "PY", "PYTHON":
		return "python"
	default:
		return strings.ToLower(lang)
	}
}
