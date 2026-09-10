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
