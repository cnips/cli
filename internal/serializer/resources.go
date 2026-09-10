package serializer

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/platform"
)

func WriteGlobalVariableAs(root string, gv *platform.GlobalVariable, slug string) (string, error) {
	dir := filepath.Join(root, "globalvariables")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir globalvariables: %w", err)
	}
	value := decodeHexString(gv.Value)
	ref := gv.ID
	if pulledSecretField(gv.Key) && strings.TrimSpace(value) != "" && !safePulledReference(value) {
		value = ""
		ref = "platform://globalvariables/" + firstNonEmpty(gv.ID, slug)
	}
	out := artifact.GlobalVariable{
		APIVersion: "cnips.io/v1",
		Kind:       artifact.KindGlobalVariable,
		Metadata:   artifact.ObjectMeta{Name: slug},
		Spec: artifact.GlobalVariableSpec{
			Key:         gv.Key,
			Value:       value,
			Ref:         ref,
			WorkspaceID: gv.WorkspaceID,
		},
	}
	return slug, artifact.WriteYAML(filepath.Join(dir, slug+".yaml"), out)
}

func WriteConfigurationAs(root string, config *platform.Configuration, slug string) (string, error) {
	dir := filepath.Join(root, "configurations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir configurations: %w", err)
	}
	value := config.Value
	if pulledSecretField(firstNonEmpty(config.Key, config.Type, config.Name)) && strings.TrimSpace(value) != "" && !safePulledReference(value) {
		value = "platform://configurations/" + firstNonEmpty(config.ID, slug) + "/value"
	}
	out := artifact.Configuration{
		APIVersion: "cnips.io/v1",
		Kind:       artifact.KindConfiguration,
		Metadata:   artifact.ObjectMeta{Name: slug, Description: config.Description},
		Spec: artifact.ConfigurationSpec{
			Type:        config.Type,
			Key:         config.Key,
			Value:       value,
			Ref:         config.ID,
			WorkspaceID: config.WorkspaceID,
			APIAccess:   sanitizePulledMap(config.APIAccess, "platform://configurations/"+firstNonEmpty(config.ID, slug)),
		},
	}
	return slug, artifact.WriteYAML(filepath.Join(dir, slug+".yaml"), out)
}

func decodeHexString(value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return value
	}
	return string(decoded)
}
