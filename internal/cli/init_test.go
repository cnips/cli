package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitDoesNotCreateSampleFunctionOrPipeline(t *testing.T) {
	root := t.TempDir()
	cmd := initCmd
	cmd.SetArgs([]string{})
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(oldWd)
		cmd.SetArgs(nil)
	}()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Execute(); err != nil {
		t.Fatalf("init execute: %v", err)
	}
	for _, rel := range []string{
		"functions/hello-fn/cnips.fn.yaml",
		"pipelines/hello-world/pipeline.yaml",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Fatalf("sample artifact %s should not be created", rel)
		}
	}
}
