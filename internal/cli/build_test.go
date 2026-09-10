package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectBuildTargetsForVersionedComponentBuildsAllVersions(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"latest", "v1.0.0"} {
		dir := filepath.Join(root, "transformations", "mapper", version)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", version, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "handler.js"), []byte("export async function execute() { return {}; }"), 0o644); err != nil {
			t.Fatalf("write handler: %v", err)
		}
	}

	targets, err := collectBuildTargets(root, []string{"transformation/mapper"})
	if err != nil {
		t.Fatalf("collectBuildTargets: %v", err)
	}

	got := map[string]bool{}
	for _, target := range targets {
		got[target.name] = true
	}
	for _, want := range []string{"mapper@latest", "mapper@v1.0.0"} {
		if !got[want] {
			t.Fatalf("target %q missing from %#v", want, got)
		}
	}
}
