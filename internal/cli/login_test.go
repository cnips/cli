package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	if profile.WorkspaceName != "Workspace One" || profile.Directory == "" {
		t.Fatalf("workspace name and directory were not persisted: %#v", profile)
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

func TestRunLoginWithoutExistingConfigPromptsForWorkspace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workspace" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"status":  200,
			"data": []map[string]string{
				{"workspaceId": "ws-1", "workspaceName": "Workspace One"},
				{"workspaceId": "ws-2", "workspaceName": "Workspace Two"},
			},
		})
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)

	oldStdin := os.Stdin
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = write.WriteString("2\n")
	_ = write.Close()
	os.Stdin = read
	defer func() {
		os.Stdin = oldStdin
		_ = read.Close()
	}()

	cmd := newLoginTestCommand()
	_ = cmd.Flags().Set("api-url", server.URL)
	_ = cmd.Flags().Set("tenant-key", "cnips-local")
	_ = cmd.Flags().Set("token", "Bearer test-token")
	if err := runLogin(cmd, nil); err != nil {
		t.Fatalf("login execute: %v", err)
	}

	cfg, err := auth.Load()
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	profile, ok := cfg.Current()
	if !ok {
		t.Fatal("current profile missing")
	}
	if profile.WorkspaceID != "ws-2" {
		t.Fatalf("workspace = %q, want ws-2", profile.WorkspaceID)
	}
}

func TestRunLoginReusesExistingWorkspace(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "cnips.yaml", "apiVersion: cnips.io/v1\nkind: Project\nmetadata:\n  name: test\n")
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workspace" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"status":  200,
			"data": []map[string]string{
				{"workspaceId": "ws-1", "workspaceName": "Workspace One"},
				{"workspaceId": "ws-2", "workspaceName": "Workspace Two"},
			},
		})
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)
	cfg := &auth.Config{}
	cfg.Upsert(auth.Profile{
		Name:        "default",
		APIURL:      server.URL,
		TenantKey:   "cnips-local",
		WorkspaceID: "ws-2",
		Workspaces:  []auth.Workspace{{ID: "ws-1"}, {ID: "ws-2"}},
	})
	cfg.ClearToken("default")
	if err := auth.Save(cfg); err != nil {
		t.Fatalf("auth.Save: %v", err)
	}

	cmd := newLoginTestCommand()
	_ = cmd.Flags().Set("api-url", server.URL)
	_ = cmd.Flags().Set("tenant-key", "cnips-local")
	_ = cmd.Flags().Set("token", "Bearer new-token")
	if err := runLogin(cmd, nil); err != nil {
		t.Fatalf("login execute: %v", err)
	}

	loaded, err := auth.Load()
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	profile, ok := loaded.Current()
	if !ok {
		t.Fatal("current profile missing")
	}
	if profile.WorkspaceID != "ws-2" {
		t.Fatalf("workspace = %q, want ws-2", profile.WorkspaceID)
	}
	if profile.Token != "new-token" {
		t.Fatalf("token = %q, want new-token", profile.Token)
	}
}

func TestRunLoginWithExistingLoggedInConfigPromptsForWorkspace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workspace" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"status":  200,
			"data": []map[string]string{
				{"workspaceId": "ws-1", "workspaceName": "Workspace One"},
				{"workspaceId": "ws-2", "workspaceName": "Workspace Two"},
			},
		})
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)
	cfg := &auth.Config{}
	cfg.Upsert(auth.Profile{
		Name:        "default",
		APIURL:      server.URL,
		TenantKey:   "cnips-local",
		Token:       "old-token",
		WorkspaceID: "ws-1",
		Workspaces:  []auth.Workspace{{ID: "ws-1"}, {ID: "ws-2"}},
	})
	if err := auth.Save(cfg); err != nil {
		t.Fatalf("auth.Save: %v", err)
	}

	oldStdin := os.Stdin
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = write.WriteString("2\n")
	_ = write.Close()
	os.Stdin = read
	defer func() {
		os.Stdin = oldStdin
		_ = read.Close()
	}()

	cmd := newLoginTestCommand()
	_ = cmd.Flags().Set("api-url", server.URL)
	_ = cmd.Flags().Set("tenant-key", "cnips-local")
	_ = cmd.Flags().Set("token", "Bearer new-token")
	if err := runLogin(cmd, nil); err != nil {
		t.Fatalf("login execute: %v", err)
	}

	loaded, err := auth.Load()
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	profile, ok := loaded.Current()
	if !ok {
		t.Fatal("current profile missing")
	}
	if profile.WorkspaceID != "ws-2" {
		t.Fatalf("workspace = %q, want ws-2", profile.WorkspaceID)
	}
}

func TestRunSwitchDiscardsAndUpdatesWorkspace(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "cnips.yaml", "apiVersion: cnips.io/v1\nkind: Project\nmetadata:\n  name: test\n")
	writeTestFile(t, root, "cnips.lock", "apiVersion: cnips.io/v1\nkind: Lock\nspec: {}\n")
	writeTestFile(t, root, "pipelines/orders/pipeline.yaml", "kind: Pipeline\n")
	writeTestFile(t, root, ".cnips/state/workspaces/ws-1/base-manifest.json", "{}\n")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)
	cfg := &auth.Config{}
	cfg.Upsert(auth.Profile{
		Name:        "default",
		APIURL:      "http://localhost:8090",
		TenantKey:   "cnips-local",
		Token:       "token",
		WorkspaceID: "ws-1",
		Workspaces:  []auth.Workspace{{ID: "ws-1"}, {ID: "ws-2"}},
	})
	if err := auth.Save(cfg); err != nil {
		t.Fatalf("auth.Save: %v", err)
	}

	oldStdin := os.Stdin
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = write.WriteString("yes\n")
	_ = write.Close()
	os.Stdin = read
	defer func() {
		os.Stdin = oldStdin
		_ = read.Close()
	}()

	cmd := &cobra.Command{}
	cmd.Flags().String("profile", "", "")
	if err := runSwitch(cmd, nil); err != nil {
		t.Fatalf("runSwitch: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "pipelines", "orders", "pipeline.yaml")); !os.IsNotExist(err) {
		t.Fatalf("pipeline should be discarded, stat err=%v", err)
	}
	loaded, err := auth.Load()
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	profile, ok := loaded.Current()
	if !ok {
		t.Fatal("current profile missing")
	}
	if profile.WorkspaceID != "ws-2" {
		t.Fatalf("workspace = %q, want ws-2", profile.WorkspaceID)
	}
	if profile.Token != "token" {
		t.Fatalf("token should be preserved on switch, got %q", profile.Token)
	}
}

func TestRunLogoutClearsOnlyTokenFields(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)
	cfg := &auth.Config{}
	cfg.Upsert(auth.Profile{
		Name:         "default",
		BaseURL:      "https://local.cnips.eu",
		APIURL:       "https://local.cnips.eu/mgmt-srv",
		TenantKey:    "cnips-local",
		Token:        "token",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		WorkspaceID:  "ws-1",
		Workspaces:   []auth.Workspace{{ID: "ws-1"}, {ID: "ws-2"}},
	})
	if err := auth.Save(cfg); err != nil {
		t.Fatalf("auth.Save: %v", err)
	}

	cmd := &cobra.Command{}
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("env", "default", "")
	if err := runLogout(cmd, nil); err != nil {
		t.Fatalf("runLogout: %v", err)
	}

	loaded, err := auth.Load()
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	profile, ok := loaded.Current()
	if !ok {
		t.Fatal("current profile missing")
	}
	if profile.Token != "" || profile.RefreshToken != "" || profile.TokenType != "" {
		t.Fatalf("token fields should be cleared: %#v", profile)
	}
	if profile.APIURL != "https://local.cnips.eu/mgmt-srv" || profile.TenantKey != "cnips-local" || profile.WorkspaceID != "ws-1" || len(profile.Workspaces) != 2 {
		t.Fatalf("profile config should be preserved: %#v", profile)
	}
}

func TestRunLogoutUsesDirectoryMappedLogin(t *testing.T) {
	repo := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CNIPS_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	cfg := &auth.Config{}
	cfg.UpsertForDirectory(auth.Profile{UserID: "mapped", APIURL: "http://mapped.example", Token: "mapped-token", WorkspaceID: "ws-1", WorkspaceName: "One"}, repo)
	cfg.Upsert(auth.Profile{UserID: "other", APIURL: "http://other.example", Token: "other-token", WorkspaceID: "ws-2", WorkspaceName: "Two"})
	if err := auth.Save(cfg); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("env", "default", "")
	if err := runLogout(cmd, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := auth.Load()
	if err != nil {
		t.Fatal(err)
	}
	mapped, _ := loaded.ForDirectory(repo)
	other, _ := loaded.Find(auth.LoginKey(auth.Profile{UserID: "other", APIURL: "http://other.example", WorkspaceName: "Two"}))
	if mapped.Token != "" || other.Token != "other-token" {
		t.Fatalf("wrong login cleared: mapped=%#v other=%#v", mapped, other)
	}
}

func TestRunLogoutOutsideMappedDirectoryPromptsForLogin(t *testing.T) {
	outside := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CNIPS_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	cfg := &auth.Config{}
	cfg.UpsertForDirectory(auth.Profile{UserID: "one", APIURL: "http://one.example", Token: "one-token", WorkspaceID: "ws-1", WorkspaceName: "One"}, filepath.Join(t.TempDir(), "repo-one"))
	cfg.UpsertForDirectory(auth.Profile{UserID: "two", APIURL: "http://two.example", Token: "two-token", WorkspaceID: "ws-2", WorkspaceName: "Two"}, filepath.Join(t.TempDir(), "repo-two"))
	if err := auth.Save(cfg); err != nil {
		t.Fatal(err)
	}

	oldStdin := os.Stdin
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = write.WriteString("2\n")
	_ = write.Close()
	os.Stdin = read
	defer func() {
		os.Stdin = oldStdin
		_ = read.Close()
	}()

	cmd := &cobra.Command{}
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("env", "default", "")
	if err := runLogout(cmd, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := auth.Load()
	if err != nil {
		t.Fatal(err)
	}
	one, _ := loaded.Find(auth.LoginKey(auth.Profile{UserID: "one", APIURL: "http://one.example", WorkspaceName: "One"}))
	two, _ := loaded.Find(auth.LoginKey(auth.Profile{UserID: "two", APIURL: "http://two.example", WorkspaceName: "Two"}))
	if one.Token != "one-token" || two.Token != "" {
		t.Fatalf("selection cleared wrong login: one=%#v two=%#v", one, two)
	}
}

func TestRunLoginAppendsMgmtSrvPathToRemoteAPIURL(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)

	cmd := newLoginTestCommand()
	_ = cmd.Flags().Set("api-url", "https://dev.cnips.eu/")
	_ = cmd.Flags().Set("token", "Bearer test-token")
	_ = cmd.Flags().Set("workspace", "default")
	_ = cmd.Flags().Set("skip-verify", "true")
	if err := runLogin(cmd, nil); err != nil {
		t.Fatalf("login execute: %v", err)
	}

	cfg, err := auth.Load()
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	profile, ok := cfg.Current()
	if !ok {
		t.Fatal("current profile missing")
	}
	if profile.APIURL != "https://dev.cnips.eu/mgmt-srv" {
		t.Fatalf("apiURL = %q, want https://dev.cnips.eu/mgmt-srv", profile.APIURL)
	}
	if profile.BaseURL != "https://dev.cnips.eu" {
		t.Fatalf("baseURL = %q, want https://dev.cnips.eu", profile.BaseURL)
	}
}

func TestRunLoginPrefixesHTTPSWhenSchemeMissing(t *testing.T) {
	t.Setenv("CNIPS_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	cmd := newLoginTestCommand()
	_ = cmd.Flags().Set("api-url", "dev.cnips.eu")
	_ = cmd.Flags().Set("token", "test-token")
	_ = cmd.Flags().Set("workspace", "default")
	_ = cmd.Flags().Set("skip-verify", "true")
	if err := runLogin(cmd, nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := auth.Load()
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := cfg.Current()
	if !ok || profile.APIURL != "https://dev.cnips.eu/mgmt-srv" {
		t.Fatalf("profile = %#v, %v", profile, ok)
	}
}

func TestRunLoginMissingLoginMethodReturnsInvalidCommand(t *testing.T) {
	t.Setenv("CNIPS_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	cmd := newLoginTestCommand()
	err := runLogin(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid command") {
		t.Fatalf("error = %v, want invalid command", err)
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
