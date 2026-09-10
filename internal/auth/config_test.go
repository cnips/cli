package auth

import (
	"os"
	"path/filepath"
	"testing"
)

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
