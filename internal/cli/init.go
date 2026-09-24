package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cnips/cli/internal/project"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var initCmd = &cobra.Command{
	Use:   "init [project-name]",
	Short: "Scaffold a new cnips project with the standard folder structure",
	Long: `Creates a cnips project in the current directory (or a new sub-directory
when a project name is given).

Folder structure created:
  cnips.yaml              Project manifest
  cnips.lock              Resolved dependency graph (empty)
  pipelines/              Pipeline definitions
  functions/              FaaS functions
  sources/                Local source components
  destinations/           Local destination components
  transformations/        Local transformation components
  approvals/              Local approval components
  switches/               Local switch components
  decisions/              Local decision components
  environments/dev.yaml   Dev environment definition
  CLI runtime data is stored per-project in the user config directory`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var name string
		targetDir := "."
		if len(args) > 0 {
			name = args[0]
			targetDir = slugify(name)
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			name = filepath.Base(cwd)
		}

		if targetDir != "." {
			if _, err := os.Stat(targetDir); err == nil {
				return fmt.Errorf("directory %q already exists", targetDir)
			}
		}
		existingManifest := fileExists(filepath.Join(targetDir, "cnips.yaml"))

		if err := ensureInitProjectDirs(targetDir); err != nil {
			return err
		}
		if err := project.EnsureDirs(targetDir); err != nil {
			return err
		}

		// cnips.yaml
		manifest := projectManifest(name)
		if err := writeYAMLIfMissing(filepath.Join(targetDir, "cnips.yaml"), manifest); err != nil {
			return err
		}

		// cnips.lock
		lock := emptyLock()
		if err := writeYAMLIfMissing(filepath.Join(targetDir, "cnips.lock"), lock); err != nil {
			return err
		}

		// environments/dev.yaml
		devEnv := devEnvironment()
		if err := writeYAMLIfMissing(filepath.Join(targetDir, "environments", "dev.yaml"), devEnv); err != nil {
			return err
		}

		// .gitignore
		gitignore := "node_modules/\n*.pyc\n__pycache__/\n"
		if err := writeFileIfMissing(filepath.Join(targetDir, ".gitignore"), []byte(gitignore)); err != nil {
			return err
		}

		fmt.Printf("✓ Initialized cnips project: %s\n\n", name)
		fmt.Printf("Project layout:\n")
		fmt.Printf("  %-35s  project manifest\n", "cnips.yaml")
		fmt.Printf("  %-35s  dependency lockfile\n", "cnips.lock")
		fmt.Printf("  %-35s  pipeline definitions\n", "pipelines/")
		fmt.Printf("  %-35s  FaaS functions\n", "functions/")
		fmt.Printf("  %-35s  source, destination, transformation-family components\n", "sources/ destinations/ transformations/")
		fmt.Printf("  %-35s  approval, switch, decision components\n", "approvals/ switches/ decisions/")
		fmt.Printf("  %-35s  environment bindings\n", "environments/")
		fmt.Printf("  %-35s  runtime state outside the repository\n", "user config directory")
		fmt.Println()
		fmt.Printf("Next steps:\n")
		if targetDir != "." {
			fmt.Printf("  cd %s\n", targetDir)
		}
		if existingManifest {
			fmt.Printf("  cnips pull\n")
			fmt.Printf("  cnips status\n")
		} else {
			fmt.Printf("  cnips pull\n")
			fmt.Printf("  cnips status\n")
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func writeYAMLIfMissing(path string, value any) error {
	if fileExists(path) {
		return nil
	}
	return writeYAML(path, value)
}

func writeFileIfMissing(path string, data []byte) error {
	if fileExists(path) {
		return nil
	}
	return os.WriteFile(path, data, 0o644)
}

func projectManifest(name string) map[string]any {
	return map[string]any{
		"apiVersion": "cnips.io/v1",
		"kind":       "Project",
		"metadata": map[string]any{
			"name": name,
		},
		"spec": map[string]any{
			"runtime":  "2026.8",
			"registry": "https://marketplace.cnips.io",
		},
	}
}

func emptyLock() map[string]any {
	return map[string]any{
		"apiVersion": "cnips.io/v1",
		"kind":       "Lock",
		"spec": map[string]any{
			"runtime":    "2026.8",
			"components": []any{},
		},
	}
}

func devEnvironment() map[string]any {
	return map[string]any{
		"apiVersion": "cnips.io/v1",
		"kind":       "Environment",
		"metadata": map[string]any{
			"name": "dev",
		},
		"spec": map[string]any{
			"target": map[string]any{
				"workspace": "local",
			},
			"variables": map[string]any{
				"region": "local",
			},
		},
	}
}

// -----------------------------------------------------------------------
// Util
// -----------------------------------------------------------------------

func writeYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func slugify(s string) string {
	return strings.NewReplacer(" ", "-", "_", "-").Replace(strings.ToLower(s))
}
