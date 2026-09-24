// Package project locates the cnips project root and provides
// helpers for navigating the standard project layout.
package project

import (
	"crypto/sha256"
	"encoding/json"
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

// DataDir keeps generated state outside the repository. This prevents multiple
// initialized repositories from sharing or accidentally committing CLI state.
func DataDir(root string) string {
	base := os.Getenv("CNIPS_DATA_DIR")
	if base == "" {
		if configDir, err := os.UserConfigDir(); err == nil {
			base = filepath.Join(configDir, "cnips", "projects")
		} else {
			base = filepath.Join(os.TempDir(), "cnips", "projects")
		}
	}
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return filepath.Join(base, fmt.Sprintf("%x", sum[:12]))
}

// CacheDir returns the per-project cache directory in the user's config area.
func CacheDir(root string) string { return filepath.Join(DataDir(root), "cache") }

// ArtifactCacheDir returns the path where built component artifacts are stored.
func ArtifactCacheDir(root string) string { return filepath.Join(CacheDir(root), "artifacts") }

// TraceDir returns the path where local run traces are stored.
func TraceDir(root string) string { return filepath.Join(DataDir(root), "traces") }

// StateDir returns the per-project state directory in the user's config area.
func StateDir(root string) string { return filepath.Join(DataDir(root), "state") }

// ManifestDir separates sync manifests from other mutable state.
func ManifestDir(root string) string { return filepath.Join(DataDir(root), "manifests") }

// EnsureDirs creates the standard .cnips runtime directories.
func EnsureDirs(root string) error {
	for _, d := range []string{
		CacheDir(root),
		ArtifactCacheDir(root),
		TraceDir(root),
		StateDir(root),
		filepath.Join(CacheDir(root), "schemas"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	metadata, err := json.MarshalIndent(map[string]string{"projectPath": filepath.Clean(root)}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(DataDir(root), "project.json"), append(metadata, '\n'), 0o600)
}

func RemoveData(root string) error { return os.RemoveAll(DataDir(root)) }
