package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cnips/cli/internal/auth"
)

func TestDiscardProjectRootResetsArtifactsAndState(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "cnips.yaml", "apiVersion: cnips.io/v1\nkind: Project\nmetadata:\n  name: test\n")
	writeTestFile(t, root, "cnips.lock", "old: lock\n")
	writeTestFile(t, root, "pipelines/orders/pipeline.yaml", "kind: Pipeline\n")
	writeTestFile(t, root, "transformations/normalize/component.yaml", "kind: Component\n")
	writeTestFile(t, root, "globalvariables/api-key.yaml", "kind: GlobalVariable\n")
	writeTestFile(t, root, ".cnips/state/workspaces/ws/base-manifest.json", "{}\n")
	writeTestFile(t, root, "environments/dev.yaml", "keep: me\n")
	writeTestFile(t, root, "README.md", "keep me\n")

	if err := discardProjectRoot(root); err != nil {
		t.Fatalf("discardProjectRoot: %v", err)
	}

	for _, rel := range []string{
		"pipelines/orders/pipeline.yaml",
		"transformations/normalize/component.yaml",
		"globalvariables/api-key.yaml",
		".cnips/state/workspaces/ws/base-manifest.json",
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
		".cnips/state",
		".cnips/cache/artifacts",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("%s missing after discard: %v", rel, err)
		}
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
