package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigStoresLoginArrayAndResolvesRepositoryWorkspace(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)
	repoOne, repoTwo := filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")
	cfg := &Config{}
	cfg.UpsertForDirectory(Profile{Name: "dev", UserID: "user-1", BaseURL: "https://cnips.example", APIURL: "https://cnips.example/mgmt-srv", Token: "one", WorkspaceID: "ws-one", WorkspaceName: "Workspace One"}, repoOne)
	cfg.UpsertForDirectory(Profile{Name: "dev", UserID: "user-1", BaseURL: "https://cnips.example", APIURL: "https://cnips.example/mgmt-srv", Token: "two", WorkspaceID: "ws-two", WorkspaceName: "Workspace Two"}, repoTwo)
	if len(cfg.Logins) != 2 {
		t.Fatalf("logins = %d, want two unique userId+baseUrl+workspaceName logins", len(cfg.Logins))
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if _, ok := stored["logins"]; !ok {
		t.Fatal("config does not contain logins array")
	}
	if _, ok := stored["profiles"]; ok {
		t.Fatal("legacy profiles map should not be written")
	}
	if !strings.Contains(string(data), `"directory"`) || !strings.Contains(string(data), `"workspaceName"`) {
		t.Fatalf("config should persist directory and workspace name: %s", data)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	first, ok := loaded.CurrentForDirectory(filepath.Join(repoOne, "nested"))
	if !ok || first.WorkspaceID != "ws-one" {
		t.Fatalf("repo one profile = %#v, %v", first, ok)
	}
	second, ok := loaded.CurrentForDirectory(repoTwo)
	if !ok || second.WorkspaceID != "ws-two" || second.Token != "two" {
		t.Fatalf("repo two profile = %#v, %v", second, ok)
	}
}

func TestLoginKeyIncludesWorkspaceName(t *testing.T) {
	base := Profile{UserID: "user-1", BaseURL: "https://cnips.example", WorkspaceID: "same-id"}
	one := base
	one.WorkspaceName = "Workspace One"
	two := base
	two.WorkspaceName = "Workspace Two"
	if LoginKey(one) == LoginKey(two) {
		t.Fatalf("workspace name must be part of login identity: %q", LoginKey(one))
	}
}

func TestLoadMigratesLegacyProfilesMap(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)
	legacy := `{"currentProfile":"dev","profiles":{"dev":{"name":"dev","apiUrl":"https://example.test/mgmt-srv","token":"token"}}}`
	if err := os.WriteFile(configPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Logins) != 1 || cfg.Logins[0].Name != "dev" {
		t.Fatalf("legacy config not migrated: %#v", cfg.Logins)
	}
}

func TestLatestLoginOwnsRepositoryMappingWithoutDeletingOtherAccount(t *testing.T) {
	repo := t.TempDir()
	cfg := &Config{}
	cfg.UpsertForDirectory(Profile{Name: "one", UserID: "user-1", BaseURL: "https://one.example", Token: "one"}, repo)
	cfg.UpsertForDirectory(Profile{Name: "two", UserID: "user-2", BaseURL: "https://two.example", Token: "two"}, repo)
	if len(cfg.Logins) != 2 {
		t.Fatalf("logins = %d, want 2", len(cfg.Logins))
	}
	profile, ok := cfg.ForDirectory(repo)
	if !ok || profile.UserID != "user-2" {
		t.Fatalf("repository login = %#v, %v", profile, ok)
	}
}

func TestSetWorkspaceForDirectoryMovesMappingToWorkspaceIdentity(t *testing.T) {
	repo := t.TempDir()
	cfg := &Config{}
	cfg.UpsertForDirectory(Profile{
		Name:          "dev",
		UserID:        "user-1",
		BaseURL:       "https://cnips.example",
		Token:         "token",
		WorkspaceID:   "ws-1",
		WorkspaceName: "Workspace One",
		Workspaces: []Workspace{
			{ID: "ws-1", Name: "Workspace One"},
			{ID: "ws-2", Name: "Workspace Two"},
		},
	}, repo)
	original, _ := cfg.ForDirectory(repo)

	if !cfg.SetWorkspaceForDirectory(LoginKey(original), repo, "ws-2") {
		t.Fatal("SetWorkspaceForDirectory returned false")
	}
	if len(cfg.Logins) != 2 {
		t.Fatalf("logins = %d, want one identity per workspace", len(cfg.Logins))
	}
	selected, ok := cfg.ForDirectory(repo)
	if !ok || selected.WorkspaceID != "ws-2" || selected.WorkspaceName != "Workspace Two" {
		t.Fatalf("directory login = %#v, %v", selected, ok)
	}
	if LoginKey(original) == LoginKey(selected) {
		t.Fatal("switching workspaces must change the login identity")
	}
}

func TestLoadSplitsLegacyRepositoryWorkspaceMappings(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)
	repoOne, repoTwo := filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")
	legacy := Config{
		CurrentProfile: "dev",
		Logins: []Profile{{
			Name:        "dev",
			UserID:      "user-1",
			BaseURL:     "https://cnips.example",
			WorkspaceID: "ws-2",
			Workspaces:  []Workspace{{ID: "ws-1", Name: "Workspace One"}, {ID: "ws-2", Name: "Workspace Two"}},
			Repositories: []Repository{
				{Directory: repoOne, WorkspaceID: "ws-1"},
				{Directory: repoTwo, WorkspaceID: "ws-2"},
			},
		}},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Logins) != 2 {
		t.Fatalf("migrated logins = %d, want 2", len(cfg.Logins))
	}
	first, firstOK := cfg.ForDirectory(repoOne)
	second, secondOK := cfg.ForDirectory(repoTwo)
	if !firstOK || first.WorkspaceName != "Workspace One" || !secondOK || second.WorkspaceName != "Workspace Two" {
		t.Fatalf("migrated mappings: first=%#v (%v), second=%#v (%v)", first, firstOK, second, secondOK)
	}
}

func TestSaveLoadCurrentProfile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)

	cfg := &Config{}
	cfg.Upsert(Profile{
		Name:        "dev",
		APIURL:      "https://local.cnips.eu/mgmt-srv/",
		TenantKey:   "cnips-local",
		Token:       "token",
		WorkspaceID: "default",
		Workspaces:  []Workspace{{ID: "default", Name: "default"}},
	})

	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	profile, ok := loaded.Current()
	if !ok {
		t.Fatal("Current profile missing")
	}
	if profile.APIURL != "https://local.cnips.eu/mgmt-srv" {
		t.Fatalf("APIURL = %q", profile.APIURL)
	}
	if profile.TenantKey != "cnips-local" || profile.WorkspaceID != "default" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestClearTokenKeepsProfileConfiguration(t *testing.T) {
	cfg := &Config{}
	cfg.Upsert(Profile{
		Name:         "dev",
		BaseURL:      "https://local.cnips.eu",
		APIURL:       "https://local.cnips.eu/mgmt-srv",
		TenantKey:    "cnips-local",
		Token:        "token",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		WorkspaceID:  "ws-1",
		Workspaces:   []Workspace{{ID: "ws-1"}, {ID: "ws-2"}},
	})

	if !cfg.ClearToken("dev") {
		t.Fatal("ClearToken returned false")
	}
	profile, ok := cfg.Current()
	if !ok {
		t.Fatal("current profile missing")
	}
	if profile.Token != "" || profile.RefreshToken != "" || profile.TokenType != "" || !profile.ExpiresAt.IsZero() {
		t.Fatalf("token fields were not cleared: %#v", profile)
	}
	if profile.APIURL != "https://local.cnips.eu/mgmt-srv" || profile.TenantKey != "cnips-local" || profile.WorkspaceID != "ws-1" || len(profile.Workspaces) != 2 {
		t.Fatalf("profile configuration was not preserved: %#v", profile)
	}
}

func TestRemoveDeletesConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CNIPS_CONFIG", configPath)
	cfg := &Config{}
	cfg.Upsert(Profile{Name: "default", APIURL: "http://localhost:8090", Token: "token"})
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config should be removed, stat err=%v", err)
	}
	if err := Remove(); err != nil {
		t.Fatalf("Remove missing config: %v", err)
	}
}
