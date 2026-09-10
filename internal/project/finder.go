// Package project locates the cnips project root and provides
// helpers for navigating the standard project layout.
package project

import (
	"fmt"
	"os"
	"path/filepath"
)

// FindRoot walks up from dir until it finds cnips.yaml.
// Returns the directory containing cnips.yaml, or an error.
func FindRoot(dir string) (string, error) {
	current := dir
	for {
		if _, err := os.Stat(filepath.Join(current, "cnips.yaml")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("cnips project not found (no cnips.yaml found walking up from %s)", dir)
		}
		current = parent
	}
}

// MustFindRoot is like FindRoot but exits with a helpful message.
func MustFindRoot() string {
	cwd, _ := os.Getwd()
	root, err := FindRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Run 'cnips init' to create a project in the current directory.\n")
		os.Exit(1)
	}
	return root
}

// CacheDir returns the path of the .cnips/cache directory.
func CacheDir(root string) string { return filepath.Join(root, ".cnips", "cache") }

// ArtifactCacheDir returns the path where built component artifacts are stored.
func ArtifactCacheDir(root string) string { return filepath.Join(root, ".cnips", "cache", "artifacts") }

// TraceDir returns the path where local run traces are stored.
func TraceDir(root string) string { return filepath.Join(root, ".cnips", "traces") }

// StateDir returns the path of the .cnips/state directory.
func StateDir(root string) string { return filepath.Join(root, ".cnips", "state") }

// EnsureDirs creates the standard .cnips runtime directories.
func EnsureDirs(root string) error {
	for _, d := range []string{
		CacheDir(root),
		ArtifactCacheDir(root),
		TraceDir(root),
		StateDir(root),
		filepath.Join(root, ".cnips", "cache", "schemas"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
