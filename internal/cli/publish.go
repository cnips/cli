package cli

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cnips/cli/internal/platform"
	"github.com/cnips/cli/internal/project"
)

var publishCmd = &cobra.Command{
	Use:   "publish <type> <name>",
	Short: "Publish a pushed component to the marketplace",
	Long: `Submits a pushed component version for marketplace review.

Supported types: transformation, destination, switch, approval, decision.
The component must already exist in the target workspace and have a successful
platform build, because marketplace publish copies the built artifact from the
tenant bucket.`,
	Args: cobra.ExactArgs(2),
	RunE: runPublish,
}

func init() {
	publishCmd.Flags().String("api-url", "http://localhost:8090", "Base URL of mgmt-srv")
	publishCmd.Flags().String("marketplace-url", "", "Base URL of mplace-srv (or CNIPS_MARKETPLACE_URL)")
	publishCmd.Flags().String("workspace", "default", "Workspace ID containing the component")
	publishCmd.Flags().String("tenant-key", "", "Value for the x-tenant-key header (defaults to the token tenant)")
	publishCmd.Flags().String("marketplace-tenant-key", "", "Value for the marketplace x-tenant-key header (defaults to the token tenant or CNIPS_MARKETPLACE_TENANT_KEY)")
	publishCmd.Flags().String("token", "", "Bearer token for Authorization header")
	publishCmd.Flags().String("component-version", "", "CNIPS component version to publish (defaults to latest successful version)")
	publishCmd.Flags().String("marketplace-version", "", "Marketplace semantic version (defaults to next patch on the server)")
	publishCmd.Flags().String("description", "", "Marketplace description")
	publishCmd.Flags().String("logo-url", "", "Marketplace logo URL")
	publishCmd.Flags().Bool("mask-publisher-name", false, "Publish using the masked publisher name")
	publishCmd.Flags().Bool("wait", true, "Wait for marketplace artifact upload to finish")
	rootCmd.AddCommand(publishCmd)
}

func runPublish(cmd *cobra.Command, args []string) error {
	_ = project.MustFindRoot()
	kind, name := normalizePublishKind(args[0]), strings.TrimSpace(args[1])
	if kind == "" {
		return fmt.Errorf("unsupported publish type %q; use transformation, destination, switch, approval, or decision", args[0])
	}
	apiURL, workspace, tenantKey, token := platformFlags(cmd)
	marketplaceURL, err := resolveMarketplaceURL(cmd, apiURL)
	if err != nil {
		return err
	}
	marketplaceVersion, _ := cmd.Flags().GetString("marketplace-version")
	componentVersion, _ := cmd.Flags().GetString("component-version")
	description, _ := cmd.Flags().GetString("description")
	logoURL, _ := cmd.Flags().GetString("logo-url")
	maskPublisherName, _ := cmd.Flags().GetBool("mask-publisher-name")
	wait, _ := cmd.Flags().GetBool("wait")

	mgmt := platform.NewClient(apiURL, tenantKey, token)
	state, err := loadWorkspaceState(mgmt, workspace)
	if err != nil {
		return err
	}
	target, err := resolvePublishTarget(state, kind, name)
	if err != nil {
		return err
	}
	target, err = hydratePublishTarget(mgmt, workspace, target)
	if err != nil {
		return err
	}
	versions, err := mgmt.ListVersions(workspace, target.id, target.versionType)
	if err != nil {
		return fmt.Errorf("list versions for %s %s: %w", kind, name, err)
	}
	version, err := selectPublishableVersion(versions, target, componentVersion)
	if err != nil {
		return err
	}
	pluginFileName := target.pluginFileNameFor(version)

	body := platform.PublishRequest{
		ComponentID:        target.id,
		ComponentName:      target.name,
		ComponentType:      target.componentType,
		WorkspaceID:        workspace,
		VersionID:          version.ID,
		Version:            version.Version,
		MarketplaceVersion: marketplaceVersion,
		Description:        firstNonEmpty(description, target.description),
		LogoURL:            logoURL,
		UserConfigs:        userConfigsFromConfig(target.config),
		Language:           firstNonEmpty(version.Language, target.language),
		PluginFileName:     pluginFileName,
		BuildStatus:        "success",
		Code:               codeSnapshot(version.SourceCode),
		SignatureVersion:   firstNonEmpty(version.SignatureVersion, target.signatureVersion),
		IsBunBuild:         version.IsBunBuild,
		MaskPublisherName:  maskPublisherName,
		SwitchLabels:       target.switchLabels,
	}
	if body.PluginFileName == "" {
		return fmt.Errorf("%s %q version %q has no pluginFileName in the component detail; run cnips push with a successful build before publishing", kind, name, version.Version)
	}

	marketplaceTenantKey := resolveMarketplaceTenantKey(cmd, marketplaceURL, tenantKey)
	if err := validatePublishTenantKeys(tenantKey, marketplaceTenantKey); err != nil {
		return err
	}
	marketplace := platform.NewClient(marketplaceURL, marketplaceTenantKey, token)
	resp, err := marketplace.SubmitPublish(body)
	if err != nil {
		return fmt.Errorf("submit marketplace publish: %w", err)
	}
	requestID := resp.ID
	fmt.Printf("Publish submitted: %s %q version=%s marketplaceVersion=%s artifact=%s request=%s\n",
		strings.ToLower(body.ComponentType), body.ComponentName, body.Version, resp.Version, body.PluginFileName, requestID)
	if !wait || requestID == "" {
		return nil
	}
	return waitForPublishUpload(marketplace, requestID)
}

func resolveMarketplaceTenantKey(cmd *cobra.Command, marketplaceURL, tenantKey string) string {
	if explicit, _ := cmd.Flags().GetString("marketplace-tenant-key"); strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	if env := strings.TrimSpace(os.Getenv("CNIPS_MARKETPLACE_TENANT_KEY")); env != "" {
		return env
	}
	return tenantKey
}

func validatePublishTenantKeys(mgmtTenantKey, marketplaceTenantKey string) error {
	mgmtTenantKey = strings.TrimSpace(mgmtTenantKey)
	marketplaceTenantKey = strings.TrimSpace(marketplaceTenantKey)
	if mgmtTenantKey == "" || marketplaceTenantKey == "" || strings.EqualFold(mgmtTenantKey, marketplaceTenantKey) {
		return nil
	}
	return fmt.Errorf("publish tenant mismatch: mgmt uses tenant %q but marketplace uses tenant %q; mplace-srv copies the artifact from the marketplace tenant bucket, so push and publish must use the same tenant key", mgmtTenantKey, marketplaceTenantKey)
}

func resolveMarketplaceURL(cmd *cobra.Command, apiURL string) (string, error) {
	if explicit, _ := cmd.Flags().GetString("marketplace-url"); strings.TrimSpace(explicit) != "" {
		return strings.TrimRight(strings.TrimSpace(explicit), "/"), nil
	}
	if env := strings.TrimSpace(os.Getenv("CNIPS_MARKETPLACE_URL")); env != "" {
		return strings.TrimRight(env, "/"), nil
	}

	candidates := marketplaceURLCandidates(apiURL)
	for _, candidate := range candidates {
		client := platform.NewClient(candidate, "", "")
		if err := client.Ping(); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("mplace-srv is not reachable; start mplace-srv and pass --marketplace-url, for example --marketplace-url http://localhost:<port>/mplace-srv")
}

func marketplaceURLCandidates(apiURL string) []string {
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	var candidates []string
	add := func(value string) {
		value = strings.TrimRight(strings.TrimSpace(value), "/")
		if value == "" {
			return
		}
		for _, existing := range candidates {
			if existing == value {
				return
			}
		}
		candidates = append(candidates, value)
	}

	if strings.Contains(apiURL, "/mgmt-srv") {
		add(strings.Replace(apiURL, "/mgmt-srv", "/mplace-srv", 1))
	}
	if strings.Contains(apiURL, "localhost:8090") {
		add("http://localhost:3000/mplace-srv")
		add("https://local.cnips.eu/mplace-srv")
	}
	return candidates
}

type publishTarget struct {
	id               string
	name             string
	description      string
	componentType    string
	versionType      string
	language         string
	signatureVersion string
	buildStatus      string
	pluginFileName   string
	versions         []platform.VersionInfo
	config           []platform.ConfigItem
	switchLabels     []string
}

func (t publishTarget) pluginFileNameFor(version platform.Version) string {
	for _, info := range t.versions {
		if info.PluginFileName == "" {
			continue
		}
		infoVersion := firstNonEmpty(info.VersionNumber, info.Version)
		if info.VersionID == version.ID || strings.EqualFold(infoVersion, version.Version) {
			return info.PluginFileName
		}
	}
	return firstNonEmpty(t.pluginFileName, version.PluginFileName)
}

func normalizePublishKind(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "transformation", "destination", "switch", "approval", "decision":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func resolvePublishTarget(s *workspaceState, kind, name string) (publishTarget, error) {
	key := indexKey(name)
	if kind == "destination" {
		d := s.Destinations[key]
		if d.ID == "" {
			return publishTarget{}, fmt.Errorf("destination %q not found in workspace", name)
		}
		return publishTarget{id: d.ID, name: d.Name, description: d.Description, componentType: "DESTINATION", versionType: "DESTINATION", language: d.Language, signatureVersion: d.SignatureVersion, buildStatus: d.BuildStatus, pluginFileName: d.PluginFileName, versions: d.Versions, config: d.Config}, nil
	}
	t := s.Transformations[key]
	if t.ID == "" {
		return publishTarget{}, fmt.Errorf("%s %q not found in workspace transformations", kind, name)
	}
	wantType := strings.ToUpper(kind)
	if wantType == "TRANSFORMATION" && t.Type == "" {
		t.Type = "TRANSFORMATION"
	}
	if !strings.EqualFold(t.Type, wantType) {
		return publishTarget{}, fmt.Errorf("%q is type %q, not %q", name, t.Type, wantType)
	}
	return publishTarget{id: t.ID, name: t.Name, description: t.Description, componentType: wantType, versionType: wantType, language: t.Language, signatureVersion: t.SignatureVersion, buildStatus: t.BuildStatus, pluginFileName: t.PluginFileName, versions: t.Versions, config: t.Config, switchLabels: t.SwitchLabels}, nil
}

func hydratePublishTarget(client *platform.Client, workspace string, target publishTarget) (publishTarget, error) {
	switch target.componentType {
	case "DESTINATION":
		d, err := client.GetDestination(workspace, target.id)
		if err != nil {
			return target, fmt.Errorf("get destination %s before publish: %w", target.name, err)
		}
		return publishTarget{id: d.ID, name: d.Name, description: d.Description, componentType: "DESTINATION", versionType: "DESTINATION", language: d.Language, signatureVersion: d.SignatureVersion, buildStatus: d.BuildStatus, pluginFileName: d.PluginFileName, versions: d.Versions, config: d.Config}, nil
	case "TRANSFORMATION", "SWITCH", "APPROVAL", "DECISION":
		t, err := client.GetTransformation(workspace, target.id)
		if err != nil {
			return target, fmt.Errorf("get transformation %s before publish: %w", target.name, err)
		}
		return publishTarget{id: t.ID, name: t.Name, description: t.Description, componentType: target.componentType, versionType: target.versionType, language: t.Language, signatureVersion: t.SignatureVersion, buildStatus: t.BuildStatus, pluginFileName: t.PluginFileName, versions: t.Versions, config: t.Config, switchLabels: t.SwitchLabels}, nil
	default:
		return target, nil
	}
}

func selectPublishableVersion(versions []platform.Version, target publishTarget, requested string) (platform.Version, error) {
	if len(versions) == 0 {
		return platform.Version{}, fmt.Errorf("no platform versions found; run cnips push first")
	}
	requested = strings.TrimSpace(requested)
	if requested != "" {
		for _, version := range versions {
			if version.ID == requested || strings.EqualFold(version.Version, requested) {
				return validatePublishableVersion(version, target)
			}
		}
		return platform.Version{}, fmt.Errorf("component version %q not found; available versions: %s", requested, availableVersionList(versions))
	}
	sort.SliceStable(versions, func(i, j int) bool {
		return normalizeVersionSortKey(versions[i].Version) > normalizeVersionSortKey(versions[j].Version)
	})
	return validatePublishableVersion(versions[0], target)
}

func validatePublishableVersion(version platform.Version, target publishTarget) (platform.Version, error) {
	status := strings.ToLower(firstNonEmpty(version.BuildStatus, target.buildStatus))
	if status == "" {
		status = "success"
	}
	if status != "success" {
		return platform.Version{}, fmt.Errorf("component version %q build status is %q; wait for a successful build before publishing", version.Version, status)
	}
	if version.ID == "" {
		return platform.Version{}, fmt.Errorf("component version %q has no version id", version.Version)
	}
	return version, nil
}

func availableVersionList(versions []platform.Version) string {
	values := make([]string, 0, len(versions))
	for _, version := range versions {
		label := strings.TrimSpace(version.Version)
		if label == "" {
			label = version.ID
		}
		if label != "" {
			values = append(values, label)
		}
	}
	if len(values) == 0 {
		return "<none>"
	}
	sort.SliceStable(values, func(i, j int) bool {
		return normalizeVersionSortKey(values[i]) > normalizeVersionSortKey(values[j])
	})
	return strings.Join(values, ", ")
}

func normalizeVersionSortKey(version string) string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if strings.EqualFold(version, "latest") || version == "" {
		return "999999.999999.999999"
	}
	parts := strings.Split(version, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			n = 0
		}
		parts[i] = fmt.Sprintf("%06d", n)
	}
	return strings.Join(parts, ".")
}

func userConfigsFromConfig(config []platform.ConfigItem) []platform.UserConfig {
	out := make([]platform.UserConfig, 0, len(config))
	for _, item := range config {
		if item.Key == "" {
			continue
		}
		out = append(out, platform.UserConfig{Key: item.Key, Value: item.Value, Secret: looksSecretKey(item.Key)})
	}
	return out
}

func looksSecretKey(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "secret") || strings.Contains(key, "token") || strings.Contains(key, "password") || strings.Contains(key, "api_key") || strings.Contains(key, "apikey")
}

func codeSnapshot(source platform.SourceCode) *platform.CodeSnapshot {
	packageJSON := ""
	if source.JavaScript != nil {
		packageJSON = source.JavaScript.PackageJSON
	}
	return &platform.CodeSnapshot{
		Script: source.Script,
		Golang: source.Golang,
		Python: source.Python,
		JavaScript: &platform.JavaScriptCode{
			Script:      source.Script,
			PackageJSON: packageJSON,
		},
	}
}

func waitForPublishUpload(client *platform.Client, requestID string) error {
	const (
		timeout      = 2 * time.Minute
		pollInterval = 2 * time.Second
	)
	deadline := time.Now().Add(timeout)
	for {
		status, err := client.GetPublishStatus(requestID)
		if err != nil {
			return err
		}
		switch strings.ToLower(status.ArtifactStatus) {
		case "ready":
			fmt.Println("Marketplace artifact upload ready.")
			return nil
		case "failed":
			return fmt.Errorf("marketplace artifact upload failed: %s", status.ArtifactError)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for marketplace artifact upload")
		}
		fmt.Fprintf(os.Stderr, "  waiting for marketplace upload: %s\n", status.ArtifactStatus)
		time.Sleep(pollInterval)
	}
}
