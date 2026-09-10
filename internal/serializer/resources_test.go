package serializer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/platform"
	"gopkg.in/yaml.v3"
)

func TestWriteConfigurationRedactsAPIAccessSecrets(t *testing.T) {
	root := t.TempDir()
	config := &platform.Configuration{
		ID:          "config-1",
		Name:        "OAuth",
		Type:        "APIAccess",
		WorkspaceID: "default",
		APIAccess: map[string]any{
			"oAuthDetails": map[string]any{
				"client_id":     "app_9f3a2c7b1d84e6",
				"client_secret": "8FhK2sL9pQW4xT0rYvA7MZcEJ1N6bR",
				"token_url":     "https://local.cnips.eu/token",
			},
		},
	}

	if _, err := WriteConfigurationAs(root, config, "oauth"); err != nil {
		t.Fatalf("WriteConfigurationAs: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "configurations", "oauth.yaml"))
	if err != nil {
		t.Fatalf("read configuration: %v", err)
	}
	var out artifact.Configuration
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal configuration: %v", err)
	}
	details := out.Spec.APIAccess["oAuthDetails"].(map[string]any)
	if got := details["client_secret"]; got != "platform://configurations/config-1/oauthdetails/client-secret" {
		t.Fatalf("client_secret = %#v, want redacted platform ref", got)
	}
	if got := details["token_url"]; got != "https://local.cnips.eu/token" {
		t.Fatalf("token_url = %#v, want preserved URL", got)
	}
}
