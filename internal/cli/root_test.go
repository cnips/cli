package cli

import (
	"path/filepath"
	"testing"

	"github.com/cnips/cli/internal/auth"
	"github.com/spf13/cobra"
)

func TestRequireTokenForCommandBlocksWithoutToken(t *testing.T) {
	t.Setenv("CNIPS_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	cmd := &cobra.Command{Use: "build"}
	if err := requireTokenForCommand(cmd, nil); err == nil {
		t.Fatal("expected login error without saved token")
	}
}

func TestRequireTokenForCommandAllowsSavedToken(t *testing.T) {
	t.Setenv("CNIPS_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	cfg := &auth.Config{}
	cfg.Upsert(auth.Profile{Name: "default", Token: "token"})
	if err := auth.Save(cfg); err != nil {
		t.Fatalf("auth.Save: %v", err)
	}

	cmd := &cobra.Command{Use: "build"}
	if err := requireTokenForCommand(cmd, nil); err != nil {
		t.Fatalf("requireTokenForCommand: %v", err)
	}
}

func TestRequireTokenForCommandAllowsExplicitTokenFlag(t *testing.T) {
	t.Setenv("CNIPS_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	cmd := &cobra.Command{Use: "pull"}
	cmd.Flags().String("token", "", "")
	if err := cmd.Flags().Set("token", "Bearer token"); err != nil {
		t.Fatalf("set token: %v", err)
	}
	if err := requireTokenForCommand(cmd, nil); err != nil {
		t.Fatalf("requireTokenForCommand: %v", err)
	}
}

func TestRequireTokenForCommandAllowsAuthLifecycleCommands(t *testing.T) {
	t.Setenv("CNIPS_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	for _, name := range []string{"init", "login", "logout", "discard", "add", "rename", "fmt", "help"} {
		cmd := &cobra.Command{Use: name}
		if err := requireTokenForCommand(cmd, nil); err != nil {
			t.Fatalf("%s should be allowed without token: %v", name, err)
		}
	}
}
