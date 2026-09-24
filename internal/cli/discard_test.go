package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cnips/cli/internal/auth"
	"github.com/cnips/cli/internal/project"
)

func TestDiscardProjectRootResetsArtifactsAndState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CNIPS_DATA_DIR", t.TempDir())
	writeTestFile(t, root, "cnips.yaml", "apiVersion: cnips.io/v1\nkind: Project\nmetadata:\n  name: test\n")
	writeTestFile(t, root, "cnips.lock", "old: lock\n")
	writeTestFile(t, root, "pipelines/orders/pipeline.yaml", "kind: Pipeline\n")
	writeTestFile(t, root, "transformations/normalize/component.yaml", "kind: Component\n")
	writeTestFile(t, root, "globalvariables/api-key.yaml", "kind: GlobalVariable\n")
	writeTestFile(t, project.DataDir(root), "state/workspaces/ws/base-manifest.json", "{}\n")
	writeTestFile(t, root, "environments/dev.yaml", "keep: me\n")
	writeTestFile(t, root, "README.md", "keep me\n")

	if err := discardProjectRoot(root); err != nil {
		t.Fatalf("discardProjectRoot: %v", err)
	}

	for _, rel := range []string{
		"pipelines/orders/pipeline.yaml",
		"transformations/normalize/component.yaml",
		"globalvariables/api-key.yaml",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after discard", rel)
		}
	}
	for _, rel := range []string{
		"cnips.yaml",
		"cnips.lock",
		"environments/dev.yaml",
		"README.md",
		"pipelines",
		"transformations",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("%s missing after discard: %v", rel, err)
		}
	}
	for _, path := range []string{project.StateDir(root), project.ArtifactCacheDir(root)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("runtime directory missing after discard: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".cnips")); !os.IsNotExist(err) {
		t.Fatalf("legacy .cnips directory should not be recreated")
	}
	lock, err := os.ReadFile(filepath.Join(root, "cnips.lock"))
	if err != nil {
		t.Fatalf("read cnips.lock: %v", err)
	}
	if string(lock) == "old: lock\n" {
		t.Fatal("cnips.lock was not reset")
	}
}

func TestRunDiscardRemovesSavedConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CNIPS_DATA_DIR", t.TempDir())
	writeTestFile(t, root, "cnips.yaml", "apiVersion: cnips.io/v1\nkind: Project\nmetadata:\n  name: test\n")
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)
	cfg := &auth.Config{}
	cfg.Upsert(auth.Profile{Name: "default", APIURL: "http://localhost:8090", Token: "token", WorkspaceID: "ws-1"})
	if err := auth.Save(cfg); err != nil {
		t.Fatalf("auth.Save: %v", err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	if err := discardCmd.RunE(discardCmd, nil); err != nil {
		t.Fatalf("discard run: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config should be removed, stat err=%v", err)
	}
}
