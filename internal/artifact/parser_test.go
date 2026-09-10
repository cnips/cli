package artifact

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePipelineLoadsLayoutWhenPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pipeline.yaml"), []byte(`apiVersion: cnips.io/v1
kind: Pipeline
metadata:
  name: orders
spec:
  steps:
    - id: normalize
      uses: transformation/normalize@latest
`), 0o644); err != nil {
		t.Fatalf("write pipeline.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "layout.yaml"), []byte(`apiVersion: cnips.io/v1
kind: PipelineLayout
positions:
  normalize:
    x: 123
    y: 456
`), 0o644); err != nil {
		t.Fatalf("write layout.yaml: %v", err)
	}

	pipeline, err := ParsePipeline(dir)
	if err != nil {
		t.Fatalf("ParsePipeline: %v", err)
	}
	if pipeline.Layout == nil {
		t.Fatal("Layout was not loaded")
	}
	if got := pipeline.Layout.Positions["normalize"]; got.X != 123 || got.Y != 456 {
		t.Fatalf("unexpected layout position: %#v", got)
	}
}

func TestLocalComponentReturnsKind(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "sources", "general-src"),
		filepath.Join(root, "destinations", "general-dest"),
		filepath.Join(root, "transformations", "mapper"),
		filepath.Join(root, "approvals", "gate"),
		filepath.Join(root, "switches", "router"),
		filepath.Join(root, "decisions", "fraud"),
		filepath.Join(root, "components", "custom"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}

	tests := []struct {
		name string
		kind string
	}{
		{name: "general-src", kind: "source"},
		{name: "general-dest", kind: "destination"},
		{name: "mapper", kind: "transformation"},
		{name: "gate", kind: "approval"},
		{name: "router", kind: "switch"},
		{name: "fraud", kind: "decision"},
		{name: "custom", kind: "component"},
	}

	for _, tt := range tests {
		ref, ok := LocalComponent(root, tt.name)
		if !ok {
			t.Fatalf("LocalComponent(%q) was not found", tt.name)
		}
		if ref.Kind != tt.kind {
			t.Fatalf("LocalComponent(%q).Kind = %q, want %q", tt.name, ref.Kind, tt.kind)
		}
	}
}

func TestLocalComponentKindVersionFindsTransformationFamilyFolders(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "decisions", "fraud-check", "latest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir decision: %v", err)
	}

	ref, ok := LocalComponentKindVersion(root, "fraud-check", "decision", "latest")
	if !ok {
		t.Fatal("LocalComponentKindVersion was not found")
	}
	if ref.Dir != dir || ref.Kind != "decision" {
		t.Fatalf("unexpected ref: %#v", ref)
	}
}

func TestLocalComponentVersionPrefersRequestedVersionDirectory(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "transformations", "mapper")
	for _, path := range []string{
		filepath.Join(base, "latest"),
		filepath.Join(base, "v1.0.0"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}

	ref, ok := LocalComponentVersion(root, "mapper", "1.0.0")
	if !ok {
		t.Fatalf("LocalComponentVersion was not found")
	}
	want := filepath.Join(base, "v1.0.0")
	if ref.Dir != want {
		t.Fatalf("Dir = %q, want %q", ref.Dir, want)
	}
}

func TestLocalComponentVersionDefaultsToLatestDirectory(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "sources", "extract")
	latest := filepath.Join(base, "latest")
	if err := os.MkdirAll(latest, 0o755); err != nil {
		t.Fatalf("mkdir latest: %v", err)
	}

	ref, ok := LocalComponent(root, "extract")
	if !ok {
		t.Fatalf("LocalComponent was not found")
	}
	if ref.Dir != latest {
		t.Fatalf("Dir = %q, want %q", ref.Dir, latest)
	}
}

func TestLocalComponentVersionFallsBackToFlatDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "destinations", "sink")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sink: %v", err)
	}

	ref, ok := LocalComponentVersion(root, "sink", "latest")
	if !ok {
		t.Fatalf("LocalComponentVersion was not found")
	}
	if ref.Dir != dir {
		t.Fatalf("Dir = %q, want %q", ref.Dir, dir)
	}
}

func TestLocalComponentVersionDoesNotFallBackWhenVersionedDirectoryExists(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "transformations", "mapper", "v1.0.0"), 0o755); err != nil {
		t.Fatalf("mkdir version: %v", err)
	}

	if ref, ok := LocalComponentVersion(root, "mapper", "2.0.0"); ok {
		t.Fatalf("LocalComponentVersion unexpectedly resolved to %q", ref.Dir)
	}
}
