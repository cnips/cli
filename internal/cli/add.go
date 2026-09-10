package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/project"
)

var addOpts struct {
	ComponentType string
	Language      string
	GoFramework   string
	Description   string
}

var addCmd = &cobra.Command{
	Use:   "add <component-name>",
	Short: "Scaffold a local component or function",
	Long: `Creates a local component or function under the standard cnips project layout.

Examples:
  cnips add normalize-order --type transformation --language javascript
  cnips add http-orders --type source --language go
  cnips add enrich-customer --type function --language go --go-framework fiber`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		root, err := project.FindRoot(cwd)
		if err != nil {
			return err
		}
		created, err := addComponent(root, args[0], addOptions{
			ComponentType: addOpts.ComponentType,
			Language:      addOpts.Language,
			GoFramework:   addOpts.GoFramework,
			Description:   addOpts.Description,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Created %s %q at %s\n", created.Kind, created.Name, relPath(root, created.Dir))
		fmt.Printf("  language: %s\n", created.Language)
		fmt.Printf("  signatureVersion: %s\n", created.SignatureVersion)
		if created.TemplateVersion != "" {
			fmt.Printf("  templateVersion: %s\n", created.TemplateVersion)
		}
		return nil
	},
}

func init() {
	addCmd.Flags().StringVarP(&addOpts.ComponentType, "type", "t", "", "Component type: source, destination, transformation, approval, switch, decision, component, or function")
	addCmd.Flags().StringVarP(&addOpts.Language, "language", "l", "", "Language: javascript, python, or go")
	addCmd.Flags().StringVar(&addOpts.GoFramework, "go-framework", "", "Go function framework: http or fiber (Go functions only; default http)")
	addCmd.Flags().StringVarP(&addOpts.Description, "description", "d", "", "Description for the generated manifest")
	rootCmd.AddCommand(addCmd)
}

type addOptions struct {
	ComponentType string
	Language      string
	GoFramework   string
	Description   string
}

type addResult struct {
	Name             string
	Kind             string
	Language         string
	SignatureVersion string
	TemplateVersion  string
	Dir              string
}

type scaffoldTemplate struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	ScriptType string `json:"scriptType"`
	Type       string `json:"type"`
	Language   string `json:"language"`
	Script     string `json:"script"`
	Golang     *struct {
		Main string `json:"main"`
		Mod  string `json:"mod"`
	} `json:"golang"`
	Python *struct {
		Main         string `json:"main"`
		Requirements string `json:"requirements"`
	} `json:"python"`
	JavaScript *struct {
		PackageJSON string `json:"packageJson"`
	} `json:"javascript"`
}

func addComponent(root, name string, opts addOptions) (addResult, error) {
	slug := slugify(strings.TrimSpace(name))
	if slug == "" {
		return addResult{}, fmt.Errorf("component name is required")
	}

	kind, err := normalizeComponentType(opts.ComponentType)
	if err != nil {
		return addResult{}, err
	}
	lang, err := normalizeAddLanguage(opts.Language)
	if err != nil {
		return addResult{}, err
	}
	tmpl, err := selectScaffoldTemplate(kind, lang, opts.GoFramework)
	if err != nil {
		return addResult{}, err
	}
	signatureVersion, templateVersion := templateVersions(kind, lang, opts.GoFramework, tmpl)

	dir := filepath.Join(root, addBaseDir(kind), slug)
	if _, err := os.Stat(dir); err == nil {
		return addResult{}, fmt.Errorf("%s %q already exists at %s", kind, slug, relPath(root, dir))
	} else if !os.IsNotExist(err) {
		return addResult{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return addResult{}, err
	}

	if kind == "function" {
		if err := writeFunctionScaffold(dir, slug, lang, signatureVersion, templateVersion, opts, tmpl); err != nil {
			return addResult{}, err
		}
	} else {
		if err := writeComponentScaffold(dir, slug, kind, lang, signatureVersion, templateVersion, opts, tmpl); err != nil {
			return addResult{}, err
		}
	}

	return addResult{
		Name:             slug,
		Kind:             kind,
		Language:         lang,
		SignatureVersion: signatureVersion,
		TemplateVersion:  templateVersion,
		Dir:              dir,
	}, nil
}

func normalizeComponentType(kind string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "source", "src", "extractor":
		return "source", nil
	case "destination", "dest":
		return "destination", nil
	case "transformation", "transform", "tx":
		return "transformation", nil
	case "approval", "switch", "decision":
		return strings.ToLower(strings.TrimSpace(kind)), nil
	case "component":
		return "component", nil
	case "function", "fn":
		return "function", nil
	case "":
		return "", fmt.Errorf("--type is required")
	default:
		return "", fmt.Errorf("unsupported component type %q (use source, destination, transformation, approval, switch, decision, component, or function)", kind)
	}
}

func normalizeAddLanguage(lang string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "js", "node", "nodejs":
		return "javascript", nil
	case "python", "py":
		return "python", nil
	case "go", "golang":
		return "go", nil
	case "":
		return "", fmt.Errorf("--language is required")
	default:
		return "", fmt.Errorf("unsupported language %q (use javascript, python, or go)", lang)
	}
}

func selectScaffoldTemplate(kind, lang, goFramework string) (scaffoldTemplate, error) {
	signatureVersion, _, err := defaultTemplateVersions(kind, lang, goFramework)
	if err != nil {
		return scaffoldTemplate{}, err
	}
	templates, err := loadScaffoldTemplates(kind == "function")
	if err != nil {
		return scaffoldTemplate{}, err
	}
	for _, tmpl := range templates {
		if normalizeTemplateLanguage(tmpl.Language) != lang {
			continue
		}
		if kind == "function" {
			if functionTemplateMatches(tmpl, lang, signatureVersion) {
				return tmpl, nil
			}
			continue
		}
		if strings.EqualFold(tmpl.ScriptType, componentScriptType(kind)) && strings.EqualFold(tmpl.Version, signatureVersion) {
			return tmpl, nil
		}
	}
	return scaffoldTemplate{}, fmt.Errorf("template not found for type=%s language=%s version=%s", kind, lang, signatureVersion)
}

func defaultTemplateVersions(kind, lang, goFramework string) (signatureVersion, templateVersion string, err error) {
	if kind == "function" {
		switch lang {
		case "javascript":
			if strings.TrimSpace(goFramework) != "" {
				return "", "", fmt.Errorf("--go-framework is only supported for Go functions")
			}
			return "express-v3", "js-express-v3", nil
		case "python":
			if strings.TrimSpace(goFramework) != "" {
				return "", "", fmt.Errorf("--go-framework is only supported for Go functions")
			}
			return "python-v1", "python-v1", nil
		case "go":
			switch strings.ToLower(strings.TrimSpace(goFramework)) {
			case "", "http", "go-http":
				return "go-http", "http-v1", nil
			case "fiber", "go-fiber":
				return "go-fiber", "fiber-v2", nil
			default:
				return "", "", fmt.Errorf("unsupported --go-framework %q (use http or fiber)", goFramework)
			}
		}
	}
	if strings.TrimSpace(goFramework) != "" {
		return "", "", fmt.Errorf("--go-framework is only supported for Go functions")
	}
	switch lang {
	case "javascript":
		if kind == "source" {
			return "v2", "jsext-v2", nil
		}
		return "v3", "jsv3", nil
	case "python":
		return "v1", "python-v1", nil
	case "go":
		return "v2", "gov2", nil
	}
	return "", "", fmt.Errorf("unsupported language %q", lang)
}

func templateVersions(kind, lang, goFramework string, tmpl scaffoldTemplate) (signatureVersion, templateVersion string) {
	signatureVersion, templateVersion, _ = defaultTemplateVersions(kind, lang, goFramework)
	if kind != "function" {
		return strings.ToLower(tmpl.Version), strings.ToLower(tmpl.Version)
	}
	if tmpl.ID != "" {
		return tmpl.ID, tmpl.ID
	}
	switch lang {
	case "python":
		return "python-v1", "python-v1"
	case "go":
		return signatureVersion, signatureVersion
	default:
		return signatureVersion, templateVersion
	}
}

func functionTemplateMatches(tmpl scaffoldTemplate, lang, signatureVersion string) bool {
	if tmpl.ID != "" && strings.EqualFold(tmpl.ID, signatureVersion) {
		return true
	}
	switch lang {
	case "python":
		return strings.EqualFold(tmpl.ID, "python-v1") || strings.EqualFold(tmpl.Type, "python")
	case "go":
		if signatureVersion == "go-fiber" {
			return strings.EqualFold(tmpl.Type, "fiber")
		}
		return strings.EqualFold(tmpl.Type, "http")
	default:
		return false
	}
}

func componentScriptType(kind string) string {
	switch kind {
	case "source":
		return "EXTRACTOR"
	default:
		return strings.ToUpper(kind)
	}
}

func normalizeTemplateLanguage(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "golang":
		return "go"
	case "js", "node", "nodejs":
		return "javascript"
	default:
		return strings.ToLower(strings.TrimSpace(lang))
	}
}

func addBaseDir(kind string) string {
	switch kind {
	case "function":
		return "functions"
	case "source":
		return "sources"
	case "destination":
		return "destinations"
	case "transformation", "approval", "switch", "decision":
		return transformationFamilyBaseDir(kind)
	default:
		return "components"
	}
}

func writeFunctionScaffold(dir, slug, lang, signatureVersion, templateVersion string, opts addOptions, tmpl scaffoldTemplate) error {
	fn := artifact.Function{
		APIVersion: "cnips.io/v1",
		Kind:       artifact.KindFunction,
		Metadata: artifact.ObjectMeta{
			Name: slug,
		},
		Spec: artifact.FunctionSpec{
			Runtime:          functionRuntime(lang),
			Description:      opts.Description,
			Handler:          functionHandler(lang),
			Timeout:          "30s",
			SignatureVersion: signatureVersion,
			TemplateVersion:  templateVersion,
		},
	}
	if err := artifact.WriteYAML(filepath.Join(dir, "cnips.fn.yaml"), fn); err != nil {
		return err
	}
	return writeTemplateFiles(dir, lang, tmpl)
}

func writeComponentScaffold(dir, slug, kind, lang, signatureVersion, templateVersion string, opts addOptions, tmpl scaffoldTemplate) error {
	comp := artifact.Component{
		APIVersion: "cnips.io/v1",
		Kind:       artifact.KindComponent,
		Metadata: artifact.ObjectMeta{
			Name: slug,
		},
		Spec: artifact.ComponentSpec{
			Type:             kind,
			Language:         lang,
			Description:      opts.Description,
			SignatureVersion: signatureVersion,
			TemplateVersion:  templateVersion,
		},
	}
	if err := artifact.WriteYAML(filepath.Join(dir, "component.yaml"), comp); err != nil {
		return err
	}
	return writeTemplateFiles(dir, lang, tmpl)
}

func functionRuntime(lang string) string {
	switch lang {
	case "javascript":
		return "nodejs22"
	case "python":
		return "python3.12"
	case "go":
		return "go"
	default:
		return lang
	}
}

func functionHandler(lang string) string {
	switch lang {
	case "javascript":
		return "./handler.js#handleRequest"
	case "python":
		return "./handler.py#execute"
	case "go":
		return "./main.go#HandleRequest"
	default:
		return ""
	}
}

func writeTemplateFiles(dir, lang string, tmpl scaffoldTemplate) error {
	switch lang {
	case "javascript":
		if err := writeFileNoOverwrite(filepath.Join(dir, "handler.js"), tmpl.Script); err != nil {
			return err
		}
		if tmpl.JavaScript == nil {
			return nil
		}
		return writeFileNoOverwrite(filepath.Join(dir, "package.json"), tmpl.JavaScript.PackageJSON)
	case "python":
		if tmpl.Python == nil {
			return fmt.Errorf("python template body is missing")
		}
		if err := writeFileNoOverwrite(filepath.Join(dir, "handler.py"), tmpl.Python.Main); err != nil {
			return err
		}
		return writeFileNoOverwrite(filepath.Join(dir, "requirements.txt"), tmpl.Python.Requirements)
	case "go":
		if tmpl.Golang == nil {
			return fmt.Errorf("go template body is missing")
		}
		if err := writeFileNoOverwrite(filepath.Join(dir, "go.mod"), tmpl.Golang.Mod); err != nil {
			return err
		}
		return writeFileNoOverwrite(filepath.Join(dir, "main.go"), tmpl.Golang.Main)
	default:
		return fmt.Errorf("unsupported language %q", lang)
	}
}

func loadScaffoldTemplates(functions bool) ([]scaffoldTemplate, error) {
	fileName := "templates.json"
	if functions {
		fileName = "function-templates.json"
	}
	path, err := templateResourcePath(fileName)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var templates []scaffoldTemplate
	if err := json.Unmarshal(data, &templates); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return templates, nil
}

func templateResourcePath(fileName string) (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CNIPS_TEMPLATE_RESOURCES")); dir != "" {
		path := filepath.Join(dir, fileName)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	for _, start := range templateSearchStarts() {
		if path, ok := findTemplateResource(start, fileName); ok {
			return path, nil
		}
	}
	return "", fmt.Errorf("template resource %s not found (set CNIPS_TEMPLATE_RESOURCES to mgmt-srv/resources)", fileName)
}

func templateSearchStarts() []string {
	starts := []string{}
	if cwd, err := os.Getwd(); err == nil {
		starts = append(starts, cwd)
	}
	if _, sourceFile, _, ok := runtime.Caller(0); ok {
		starts = append(starts, filepath.Dir(sourceFile))
	}
	return starts
}

func findTemplateResource(start, fileName string) (string, bool) {
	current, err := filepath.Abs(start)
	if err != nil {
		current = start
	}
	for {
		for _, candidate := range []string{
			filepath.Join(current, "mgmt-srv", "resources", fileName),
			filepath.Join(current, "..", "mgmt-srv", "resources", fileName),
		} {
			if _, err := os.Stat(candidate); err == nil {
				return filepath.Clean(candidate), true
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}

func writeFileNoOverwrite(path, content string) error {
	if content == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	if rel == "." {
		return "."
	}
	return rel
}
