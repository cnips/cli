package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cnips/cli/internal/platform"
	"github.com/cnips/cli/internal/serializer"
	"github.com/spf13/cobra"
)

func TestPullHydratesFreshProjectDespiteStaleExternalManifest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CNIPS_DATA_DIR", t.TempDir())
	writeTestFile(t, root, "cnips.yaml", "apiVersion: cnips.io/v1\nkind: Project\nmetadata:\n  name: fresh\n")
	writeTestFile(t, root, "cnips.lock", "apiVersion: cnips.io/v1\nkind: Lock\nspec:\n  components: []\n")

	remoteFunction := platform.Function{
		ID:          "fn-1",
		Name:        "example",
		Language:    "JAVASCRIPT",
		Description: "remote function",
		SourceCode:  platform.SourceCode{Script: "module.exports = 'remote'\n"},
	}
	remoteRoot := t.TempDir()
	if _, err := serializer.WriteFunctionAs(remoteRoot, &remoteFunction, "example"); err != nil {
		t.Fatalf("write remote fixture: %v", err)
	}
	remoteFiles, err := collectComparableFiles(remoteRoot)
	if err != nil {
		t.Fatalf("collect remote fixture: %v", err)
	}
	staleBaseFiles, err := manifestFilesForRoot(remoteRoot, remoteFiles)
	if err != nil {
		t.Fatalf("build stale manifest: %v", err)
	}
	if err := writeLocalPullManifest(root, "ws-1", &pullManifest{Files: staleBaseFiles}); err != nil {
		t.Fatalf("write stale external manifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": http.StatusOK, "data": map[string]any{}})
			return
		}
		var list any = []any{}
		if strings.HasSuffix(r.URL.Path, "/functions") {
			list = []platform.Function{remoteFunction}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"status":  http.StatusOK,
			"data": map[string]any{
				"list":  list,
				"count": 0,
			},
		})
	}))
	defer server.Close()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	cmd := newPullTestCommand()
	_ = cmd.Flags().Set("api-url", server.URL)
	_ = cmd.Flags().Set("workspace", "ws-1")
	_ = cmd.Flags().Set("token", "test-token")
	_ = cmd.Flags().Set("skip-versions", "true")
	if err := runPullInternal(cmd); err != nil {
		t.Fatalf("runPullInternal: %v", err)
	}

	handler, err := os.ReadFile(filepath.Join(root, "functions", "example", "handler.js"))
	if err != nil {
		t.Fatalf("read pulled handler: %v", err)
	}
	if string(handler) != remoteFunction.Script {
		t.Fatalf("handler = %q, want %q", handler, remoteFunction.Script)
	}
	lock, err := os.ReadFile(filepath.Join(root, "cnips.lock"))
	if err != nil {
		t.Fatalf("read cnips.lock: %v", err)
	}
	if !strings.Contains(string(lock), "example") {
		t.Fatalf("cnips.lock was not refreshed with the pulled function:\n%s", lock)
	}
}

func newPullTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "pull"}
	cmd.Flags().String("api-url", "http://localhost:8090", "")
	cmd.Flags().String("workspace", "default", "")
	cmd.Flags().String("tenant-key", "", "")
	cmd.Flags().String("token", "", "")
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("all-workspaces", false, "")
	cmd.Flags().Bool("skip-versions", false, "")
	cmd.Flags().Int("version-workers", 32, "")
	return cmd
}
