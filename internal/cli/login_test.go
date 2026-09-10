package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/cnips/cli/internal/auth"
	"github.com/spf13/cobra"
)

func TestRunLoginStoresVerifiedWorkspaceProfile(t *testing.T) {
	var sawTenant, sawAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workspace" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		sawTenant = r.Header.Get("x-tenant-key")
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"status":  200,
			"data": []map[string]string{
				{"workspaceId": "ws-1", "workspaceName": "Workspace One"},
				{"id": "default", "workspaceName": "default"},
			},
		})
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)

	defer rootCmd.SetArgs(nil)
	rootCmd.SetArgs([]string{
		"login",
		"--api-url", server.URL,
		"--tenant-key", "cnips-local",
		"--token", "Bearer test-token",
		"--workspace", "ws-1",
		"--profile", "dev",
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("login execute: %v", err)
	}

	if sawTenant != "cnips-local" {
		t.Fatalf("tenant header = %q", sawTenant)
	}
	if sawAuth != "Bearer test-token" {
		t.Fatalf("auth header = %q", sawAuth)
	}

	cfg, err := auth.Load()
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	profile, ok := cfg.Current()
	if !ok {
		t.Fatal("current profile missing")
	}
	if profile.Name != "dev" || profile.WorkspaceID != "ws-1" || profile.TenantKey != "cnips-local" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
	if len(profile.Workspaces) != 2 {
		t.Fatalf("workspaces = %#v", profile.Workspaces)
	}
}

func TestRunLoginAllowsNginxResolvedTenantWithoutTenantKey(t *testing.T) {
	var sawTenant, sawAuth string
	var sawTenantHeader bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workspace" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		sawTenant = r.Header.Get("x-tenant-key")
		_, sawTenantHeader = r.Header["X-Tenant-Key"]
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"status":  200,
			"data": []map[string]string{
				{"workspaceId": "default", "workspaceName": "default"},
			},
		})
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)

	cmd := newLoginTestCommand()
	_ = cmd.Flags().Set("api-url", server.URL)
	_ = cmd.Flags().Set("token", "Bearer test-token")
	_ = cmd.Flags().Set("workspace", "default")
	if err := runLogin(cmd, nil); err != nil {
		t.Fatalf("login execute: %v", err)
	}

	if sawTenant != "" {
		t.Fatalf("tenant header = %q, want empty", sawTenant)
	}
	if sawTenantHeader {
		t.Fatal("tenant header was sent, want omitted")
	}
	if sawAuth != "Bearer test-token" {
		t.Fatalf("auth header = %q", sawAuth)
	}

	cfg, err := auth.Load()
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	profile, ok := cfg.Current()
	if !ok {
		t.Fatal("current profile missing")
	}
	if profile.TenantKey != "" {
		t.Fatalf("profile tenant = %q, want empty", profile.TenantKey)
	}
}

func newLoginTestCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("base-url", "", "")
	cmd.Flags().String("api-url", "http://localhost:8090", "")
	cmd.Flags().String("tenant-key", "", "")
	cmd.Flags().String("token", "", "")
	cmd.Flags().String("workspace", "", "")
	cmd.Flags().String("profile", "default", "")
	cmd.Flags().String("env", "default", "")
	cmd.Flags().String("auth-host", "localhost", "")
	cmd.Flags().Int("auth-port", 3000, "")
	cmd.Flags().String("auth-path", "/", "")
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().Bool("skip-verify", false, "")
	return cmd
}
