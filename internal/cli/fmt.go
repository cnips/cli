package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/project"
)

var fmtCheck bool
var fmtType string

var fmtCmd = &cobra.Command{
	Use:   "fmt [path]",
	Short: "Format cnips YAML files canonically",
	Long: `Formats cnips project YAML files using the canonical structs and key order.

Without a path, formats cnips.yaml, cnips.lock, pipelines, components,
functions, environments, configurations, and global variables under the
current project.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := project.MustFindRoot()
		target := root
		if len(args) == 1 {
			target = args[0]
			if !filepath.IsAbs(target) {
				target = filepath.Join(root, target)
			}
		}
		files, err := collectFmtFiles(root, target)
		if err != nil {
			return err
		}
		selectedType, err := normalizeValidationType(fmtType)
		if err != nil {
			return err
		}
		if selectedType != "" {
			files = filterFmtFilesByType(root, files, selectedType)
		}
		changed := 0
		for _, path := range files {
			ok, err := formatCnipsYAML(path, fmtCheck)
			if err != nil {
				return err
			}
			if ok {
				changed++
				if fmtCheck {
					fmt.Printf("needs format: %s\n", relPath(root, path))
				} else {
					fmt.Printf("formatted: %s\n", relPath(root, path))
				}
			}
		}
		if fmtCheck && changed > 0 {
			return fmt.Errorf("%d file(s) need formatting", changed)
		}
		fmt.Printf("cnips fmt complete: %d file(s), %d changed\n", len(files), changed)
		return nil
	},
}

func init() {
	fmtCmd.Flags().BoolVar(&fmtCheck, "check", false, "Report files that would change without writing them")
	fmtCmd.Flags().StringVarP(&fmtType, "type", "t", "", "Optional component type to format")
	rootCmd.AddCommand(fmtCmd)
}

func filterFmtFilesByType(root string, files []string, selectedType string) []string {
	dirByType := map[string]string{"pipeline": "pipelines", "function": "functions", "component": "components", "source": "sources", "destination": "destinations", "transformation": "transformations", "approval": "approvals", "switch": "switches", "decision": "decisions", "globalvariable": "globalvariables", "configuration": "configurations"}
	prefix := dirByType[selectedType] + "/"
	var out []string
	for _, path := range files {
		rel := filepath.ToSlash(relPath(root, path))
		if strings.HasPrefix(rel, prefix) {
			out = append(out, path)
		}
	}
	return out
}

func collectFmtFiles(root, target string) ([]string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if isFmtYAML(target) {
			return []string{target}, nil
		}
		return nil, fmt.Errorf("%s is not a cnips YAML file", target)
	}
	var files []string
	err = filepath.WalkDir(target, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := filepath.Base(path)
			if base == ".git" || base == ".cnips" {
				return filepath.SkipDir
			}
			return nil
		}
		if isFmtYAML(path) {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func isFmtYAML(path string) bool {
	base := filepath.Base(path)
	if base == "cnips.yaml" || base == "cnips.lock" || base == "pipeline.yaml" || base == "component.yaml" || base == "source.yaml" || base == "destination.yaml" || base == "transformation.yaml" || base == "cnips.fn.yaml" {
		return true
	}
	return strings.HasSuffix(base, ".yaml") && (strings.Contains(path, "/environments/") || strings.Contains(path, "/globalvariables/") || strings.Contains(path, "/configurations/"))
}

func formatCnipsYAML(path string, check bool) (bool, error) {
	before, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var value any
	switch filepath.Base(path) {
	case "cnips.yaml":
		value, err = artifact.ParseProject(filepath.Dir(path))
	case "cnips.lock":
		value, err = artifact.ParseLock(filepath.Dir(path))
	case "pipeline.yaml":
		value, err = artifact.ParsePipeline(filepath.Dir(path))
	case "component.yaml", "source.yaml", "destination.yaml", "transformation.yaml":
		value, err = artifact.ParseComponent(filepath.Dir(path))
	case "cnips.fn.yaml":
		value, err = artifact.ParseFunction(filepath.Dir(path))
	default:
		if strings.Contains(path, "/environments/") {
			name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			value, err = artifact.ParseEnvironment(filepath.Dir(filepath.Dir(path)), name)
		} else if strings.Contains(path, "/globalvariables/") {
			var values []*artifact.GlobalVariable
			values, err = artifact.ListGlobalVariables(filepath.Dir(filepath.Dir(path)))
			value = findNamedYAML(values, path)
		} else if strings.Contains(path, "/configurations/") {
			var values []*artifact.Configuration
			values, err = artifact.ListConfigurations(filepath.Dir(filepath.Dir(path)))
			value = findNamedYAML(values, path)
		}
	}
	if err != nil {
		return false, err
	}
	if value == nil {
		return false, fmt.Errorf("unsupported cnips YAML file %s", path)
	}
	tmp := path + ".fmt-tmp"
	if err := artifact.WriteYAML(tmp, value); err != nil {
		return false, err
	}
	after, err := os.ReadFile(tmp)
	_ = os.Remove(tmp)
	if err != nil {
		return false, err
	}
	if string(before) == string(after) {
		return false, nil
	}
	if check {
		return true, nil
	}
	return true, os.WriteFile(path, after, 0o644)
}

func findNamedYAML[T any](items []*T, path string) *T {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for _, item := range items {
		switch v := any(item).(type) {
		case *artifact.GlobalVariable:
			if v.Metadata.Name == name || v.Spec.Key == name {
				return item
			}
		case *artifact.Configuration:
			if v.Metadata.Name == name {
				return item
			}
		}
	}
	return nil
}
