package serializer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/platform"
)

func TestWriteTransformationVersionsIncludesComponentConfig(t *testing.T) {
	root := t.TempDir()
	tx := &platform.Transformation{
		ID:          "tx-1",
		Name:        "Mapper",
		Language:    "JS",
		Description: "maps data",
		Config: []platform.ConfigItem{
			{Key: "threshold", Value: "10"},
		},
		SourceCode: platform.SourceCode{Script: "export async function execute() { return {}; }"},
	}
	versions := []platform.Version{
		{
			Version:    "latest",
			Language:   "JS",
			SourceCode: platform.SourceCode{Script: "export async function execute() { return {}; }"},
		},
		{
			Version:    "1.0.0",
			Language:   "JS",
			SourceCode: platform.SourceCode{Script: "export async function execute() { return {}; }"},
		},
	}

	if _, err := WriteTransformationVersionsAs(root, tx, "mapper", versions); err != nil {
		t.Fatalf("WriteTransformationVersionsAs: %v", err)
	}

	parent, err := artifact.ParseComponent(filepath.Join(root, "transformations", "mapper"))
	if err != nil {
		t.Fatalf("parse parent component: %v", err)
	}
	if got := parent.Spec.Config["threshold"]; got != "10" {
		t.Fatalf("parent config threshold = %#v, want 10", got)
	}

	versioned, err := artifact.ParseComponent(filepath.Join(root, "transformations", "mapper", "v1.0.0"))
	if err != nil {
		t.Fatalf("parse version component: %v", err)
	}
	if got := versioned.Spec.Config["threshold"]; got != "10" {
		t.Fatalf("version config threshold = %#v, want 10", got)
	}
}

func TestWriteSwitchIncludesSwitchLabels(t *testing.T) {
	root := t.TempDir()
	sw := &platform.Transformation{
		ID:           "sw-1",
		Name:         "Route Orders",
		Type:         "SWITCH",
		SwitchLabels: []string{"express", "standard", "default"},
	}

	if _, err := WriteTransformationAs(root, sw, "route-orders"); err != nil {
		t.Fatalf("WriteTransformationAs: %v", err)
	}

	comp, err := artifact.ParseComponent(filepath.Join(root, "switches", "route-orders"))
	if err != nil {
		t.Fatalf("parse switch component: %v", err)
	}
	want := []string{"express", "standard", "default"}
	if strings.Join(comp.Spec.SwitchLabels, ",") != strings.Join(want, ",") {
		t.Fatalf("SwitchLabels=%v, want %v", comp.Spec.SwitchLabels, want)
	}
}

func TestWriteTransformationVersionsRedactsSecretConfig(t *testing.T) {
	root := t.TempDir()
	tx := &platform.Transformation{
		ID:       "tx-1",
		Name:     "Mapper",
		Language: "JS",
		Config: []platform.ConfigItem{
			{Key: "apiKey", Value: "8FhK2sL9pQW4xT0rYvA7MZcEJ1N6bR"},
			{Key: "apikey_placeholder", Value: "X-API-Key"},
		},
		SourceCode: platform.SourceCode{Script: "export async function execute() { return {}; }"},
	}

	if _, err := WriteTransformationVersionsAs(root, tx, "mapper", nil); err != nil {
		t.Fatalf("WriteTransformationVersionsAs: %v", err)
	}

	comp, err := artifact.ParseComponent(filepath.Join(root, "transformations", "mapper"))
	if err != nil {
		t.Fatalf("parse component: %v", err)
	}
	if got := comp.Spec.Config["apiKey"]; got != "platform://component-config/apikey" {
		t.Fatalf("secret config = %#v, want redacted platform ref", got)
	}
	if got := comp.Spec.Config["apikey_placeholder"]; got != "X-API-Key" {
		t.Fatalf("placeholder config = %#v, want preserved value", got)
	}
}

func TestWriteAppVersionsAsComponentSourceVersions(t *testing.T) {
	root := t.TempDir()
	app := &platform.App{
		ID:          "app-1",
		Name:        "Send Mail",
		Type:        "DESTINATION",
		AppType:     "public",
		Language:    "javascript",
		Description: "sends mail",
		Config: []platform.ConfigItem{
			{Key: "region", Value: "eu"},
		},
		SourceCode: platform.SourceCode{
			Script: "export default async function execute() { return 'latest' }\n",
			JavaScript: &platform.JavaScript{
				PackageJSON: `{"type":"module"}`,
			},
		},
	}
	versions := []platform.AppVersion{
		{
			ID:               "ver-1",
			Ref:              "app-1",
			Name:             "Send Mail",
			Version:          "1.2.3",
			Type:             "DESTINATION",
			Language:         "javascript",
			SignatureVersion: "v3",
			SourceCode: platform.SourceCode{
				Script: "export default async function execute() { return 'v1.2.3' }\n",
			},
		},
	}

	if _, err := WriteAppVersionsAs(root, app, "send-mail", versions); err != nil {
		t.Fatalf("WriteAppVersionsAs: %v", err)
	}

	comp, err := artifact.ParseComponent(filepath.Join(root, "apps", "send-mail", "v1.2.3"))
	if err != nil {
		t.Fatalf("parse app component: %v", err)
	}
	if comp.Spec.AppType != "public" {
		t.Fatalf("appType=%q, want public", comp.Spec.AppType)
	}
	data, err := os.ReadFile(filepath.Join(root, "apps", "send-mail", "v1.2.3", "handler.js"))
	if err != nil {
		t.Fatalf("read app handler.js: %v", err)
	}
	if !strings.Contains(string(data), "v1.2.3") {
		t.Fatalf("handler.js should contain version source, got %q", data)
	}
}

func TestWriteTransformationPreservesPlatformIdentity(t *testing.T) {
	root := t.TempDir()
	tx := &platform.Transformation{
		ID:       "tx-special",
		Name:     "Order Mapper @ v2!",
		Language: "JS",
		SourceCode: platform.SourceCode{
			Script: "export async function execute() { return {}; }",
		},
	}

	if _, err := WriteTransformationVersionsAs(root, tx, "order-mapper-v2", nil); err != nil {
		t.Fatalf("WriteTransformationVersionsAs: %v", err)
	}

	comp, err := artifact.ParseComponent(filepath.Join(root, "transformations", "order-mapper-v2"))
	if err != nil {
		t.Fatalf("parse component: %v", err)
	}
	if comp.Metadata.ID != "tx-special" {
		t.Fatalf("metadata.id=%q, want tx-special", comp.Metadata.ID)
	}
	if comp.Metadata.Name != "Order Mapper @ v2!" {
		t.Fatalf("metadata.name=%q, want original platform name", comp.Metadata.Name)
	}
	if comp.Metadata.Slug != "order-mapper-v2" {
		t.Fatalf("metadata.slug=%q, want local slug", comp.Metadata.Slug)
	}
}

func TestWriteTransformationFamilyUsesOwnFolderAndType(t *testing.T) {
	root := t.TempDir()
	tx := &platform.Transformation{
		ID:       "decision-1",
		Name:     "Fraud Decision",
		Type:     "DECISION",
		Language: "JS",
		SourceCode: platform.SourceCode{
			Script: "export async function execute() { return true; }",
		},
	}

	if _, err := WriteTransformationVersionsAs(root, tx, "fraud-decision", nil); err != nil {
		t.Fatalf("WriteTransformationVersionsAs: %v", err)
	}

	comp, err := artifact.ParseComponent(filepath.Join(root, "decisions", "fraud-decision"))
	if err != nil {
		t.Fatalf("parse component: %v", err)
	}
	if comp.Spec.Type != "decision" {
		t.Fatalf("spec.type=%q, want decision", comp.Spec.Type)
	}
}

func TestWriteSourceVersionsWritesStandardSourceMetadata(t *testing.T) {
	root := t.TempDir()
	src := &platform.Source{
		ID:          "src-1",
		Name:        "Webhook Source",
		SourceType:  "STANDARD",
		Description: "pipeline trigger",
		Config: []platform.ConfigItem{
			{Key: "path", Value: "/hook"},
		},
	}

	if _, err := WriteSourceVersionsAs(root, src, "webhook-source", nil); err != nil {
		t.Fatalf("WriteSourceVersionsAs: %v", err)
	}

	comp, err := artifact.ParseComponent(filepath.Join(root, "sources", "webhook-source"))
	if err != nil {
		t.Fatalf("parse source component: %v", err)
	}
	if comp.Spec.SourceType != "STANDARD" {
		t.Fatalf("sourceType = %q, want STANDARD", comp.Spec.SourceType)
	}
	if got := comp.Spec.Config["path"]; got != "/hook" {
		t.Fatalf("config path = %#v, want /hook", got)
	}
}

func TestWriteSourceVersionsNormalizesExtractorSourceType(t *testing.T) {
	root := t.TempDir()
	src := &platform.Source{
		ID:         "src-2",
		Name:       "General Native Component Src",
		SourceType: "extractor",
		Language:   "javascript",
		SourceCode: platform.SourceCode{Script: "export default async function handler() {}"},
	}

	if _, err := WriteSourceVersionsAs(root, src, "general-native-component-src", nil); err != nil {
		t.Fatalf("WriteSourceVersionsAs: %v", err)
	}

	comp, err := artifact.ParseComponent(filepath.Join(root, "sources", "general-native-component-src"))
	if err != nil {
		t.Fatalf("parse source component: %v", err)
	}
	if comp.Spec.SourceType != "EXTRACTOR" {
		t.Fatalf("sourceType = %q, want EXTRACTOR", comp.Spec.SourceType)
	}
	if comp.Spec.Type != "source" {
		t.Fatalf("type = %q, want source", comp.Spec.Type)
	}
}
