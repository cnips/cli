package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cnips/cli/internal/auth"
	"github.com/cnips/cli/internal/project"
	"github.com/spf13/cobra"
)

var discardCmd = &cobra.Command{
	Use:   "discard",
	Short: "Discard local cnips artifacts",
	Long: `Resets the current cnips project back to a clean init-style layout.

The command removes canonical cnips artifact directories and runtime sync state,
then recreates the standard empty project folders and cnips.lock. It also
removes the saved cnips CLI config. It leaves cnips.yaml, environments, .git,
and unrelated repository files untouched.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		root := project.MustFindRoot()
		if err := discardProjectRoot(root); err != nil {
			return err
		}
		if err := auth.Remove(); err != nil {
			return err
		}
		fmt.Println("Discarded cnips artifacts, reset local sync state, and removed saved cnips config.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(discardCmd)
}

var initProjectDirs = []string{
	"pipelines",
	"components",
	"functions",
	"sources",
	"destinations",
	"transformations",
	"approvals",
	"switches",
	"decisions",
	"environments",
	".cnips/cache/artifacts",
	".cnips/cache/schemas",
	".cnips/traces",
	".cnips/state",
}

var discardArtifactDirs = []string{
	"pipelines",
	"components",
	"functions",
	"sources",
	"destinations",
	"transformations",
	"approvals",
	"switches",
	"decisions",
	"globalvariables",
	"configurations",
	"apps",
}

func discardProjectRoot(root string) error {
	if root == "" {
		return fmt.Errorf("project root is required")
	}
	for _, rel := range discardArtifactDirs {
		path, err := safeProjectChild(root, rel)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	cnipsDir, err := safeProjectChild(root, ".cnips")
	if err != nil {
		return err
	}
	if err := os.RemoveAll(cnipsDir); err != nil {
		return err
	}
	if err := ensureInitProjectDirs(root); err != nil {
		return err
	}
	return writeYAML(filepath.Join(root, "cnips.lock"), emptyLock())
}

func ensureInitProjectDirs(root string) error {
	for _, rel := range initProjectDirs {
		path, err := safeProjectChild(root, rel)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func safeProjectChild(root, rel string) (string, error) {
	root = filepath.Clean(root)
	path := filepath.Clean(filepath.Join(root, rel))
	if path == root {
		return "", fmt.Errorf("refusing to operate on project root")
	}
	parent := filepath.Dir(path)
	if relToRoot, err := filepath.Rel(root, parent); err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, "../") {
		return "", fmt.Errorf("refusing path outside project root: %s", rel)
	}
	return path, nil
}
