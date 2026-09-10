package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cnips/cli/internal/auth"
	"github.com/cnips/cli/internal/platform"
)

var loginCmd = &cobra.Command{
	Use:     "login",
	Aliases: []string{"cnips-login"},
	Short:   "Authenticate the cnips CLI with mgmt-srv",
	Long: `Stores an authenticated cnips profile for later commands.

When --token is omitted, the CLI uses the same Cidaas PKCE browser flow as
api-tester-go: fetch /mgmt-srv/sso/clientid, discover OIDC metadata, open the
browser, capture the localhost redirect, exchange the code, and cache the token.

After login it calls GET /workspace to record the workspaces the user can
access, and it decodes the access token claims into the saved user profile.

Examples:
  cnips login --base-url https://local.cnips.eu --tenant-key cnips-local
  cnips login --token "$CNIPS_TOKEN" --tenant-key cnips-dev --workspace default`,
	RunE: runLogin,
}

func init() {
	loginCmd.Flags().String("base-url", "", "CNIPS origin URL used for browser login (e.g. https://local.cnips.eu)")
	loginCmd.Flags().String("api-url", "http://localhost:8090", "Base URL of mgmt-srv, kept for local/direct setups")
	loginCmd.Flags().String("tenant-key", "", "Value for the x-tenant-key header")
	loginCmd.Flags().String("token", "", "Bearer token or raw access token")
	loginCmd.Flags().String("workspace", "", "Workspace ID to select after login")
	loginCmd.Flags().String("profile", "default", "Local auth profile name")
	loginCmd.Flags().String("env", "default", "Environment name used for token cache key")
	loginCmd.Flags().String("auth-host", "localhost", "OIDC callback host")
	loginCmd.Flags().Int("auth-port", 3000, "OIDC callback port")
	loginCmd.Flags().String("auth-path", "/", "OIDC callback path")
	loginCmd.Flags().Bool("force", false, "Ignore cached browser token and force a fresh login")
	loginCmd.Flags().Bool("skip-verify", false, "Save credentials without calling GET /workspace")
	rootCmd.AddCommand(loginCmd)
}

func runLogin(cmd *cobra.Command, _ []string) error {
	reader := bufio.NewReader(os.Stdin)

	baseURL, _ := cmd.Flags().GetString("base-url")
	apiURL, _ := cmd.Flags().GetString("api-url")
	tenantKey, _ := cmd.Flags().GetString("tenant-key")
	token, _ := cmd.Flags().GetString("token")
	workspaceID, _ := cmd.Flags().GetString("workspace")
	profileName, _ := cmd.Flags().GetString("profile")
	envName, _ := cmd.Flags().GetString("env")
	authHost, _ := cmd.Flags().GetString("auth-host")
	authPort, _ := cmd.Flags().GetInt("auth-port")
	authPath, _ := cmd.Flags().GetString("auth-path")
	force, _ := cmd.Flags().GetBool("force")
	skipVerify, _ := cmd.Flags().GetBool("skip-verify")

	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	tenantKey = strings.TrimSpace(tenantKey)
	token = normalizeToken(strings.TrimSpace(token))
	workspaceID = strings.TrimSpace(workspaceID)
	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		profileName = "default"
	}
	if baseURL == "" {
		baseURL = auth.OriginFromAPIURL(apiURL)
	}
	if !cmd.Flags().Changed("api-url") && baseURL != "" {
		apiURL = auth.APIURLFromOrigin(baseURL)
	}

	if apiURL == "" {
		apiURL = prompt(reader, "mgmt-srv URL", auth.APIURLFromOrigin(baseURL))
	}
	var tokenMeta *auth.Token
	if token == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		opts := auth.LoginOptions{
			BaseURL: baseURL,
			EnvName: envName,
			Host:    authHost,
			Port:    authPort,
			Path:    authPath,
		}
		var err error
		if force {
			_ = auth.ClearTokenCache(opts)
			tokenMeta, err = auth.BrowserLogin(ctx, opts)
		} else {
			tokenMeta, err = auth.GetToken(ctx, opts)
		}
		if err != nil {
			return err
		}
		token = tokenMeta.AccessToken
	}
	user := auth.UserFromToken(token)
	if token == "" {
		return fmt.Errorf("access token is required")
	}

	var savedWorkspaces []auth.Workspace
	if !skipVerify {
		client := platform.NewClient(apiURL, tenantKey, token)
		workspaces, err := client.ListWorkspaces()
		if err != nil {
			return fmt.Errorf("login verification failed: %w\nUse --skip-verify to save credentials without contacting mgmt-srv", err)
		}
		savedWorkspaces = convertWorkspaces(workspaces)
		if len(savedWorkspaces) == 0 {
			savedWorkspaces = []auth.Workspace{{ID: "default", Name: "default"}}
		}
		if workspaceID == "" {
			workspaceID = chooseWorkspace(reader, savedWorkspaces)
		}
		if !workspaceExists(savedWorkspaces, workspaceID) {
			return fmt.Errorf("workspace %q is not in the authenticated workspace list", workspaceID)
		}
	} else if workspaceID == "" {
		workspaceID = "default"
	}

	cfg, err := auth.Load()
	if err != nil {
		return err
	}
	cfg.Upsert(auth.Profile{
		Name:        profileName,
		BaseURL:     baseURL,
		APIURL:      apiURL,
		TenantKey:   tenantKey,
		Token:       token,
		User:        user,
		WorkspaceID: workspaceID,
		Workspaces:  savedWorkspaces,
	})
	if tokenMeta != nil {
		profile := cfg.Profiles[profileName]
		profile.RefreshToken = tokenMeta.RefreshToken
		profile.TokenType = tokenMeta.TokenType
		profile.ExpiresAt = tokenMeta.ExpiresAt
		cfg.Profiles[profileName] = profile
	}
	if err := auth.Save(cfg); err != nil {
		return err
	}
	path, _ := auth.Path()

	fmt.Printf("Logged in to %s\n", apiURL)
	fmt.Printf("  profile:   %s\n", profileName)
	if tenantKey != "" {
		fmt.Printf("  tenant:    %s\n", tenantKey)
	}
	fmt.Printf("  workspace: %s\n", workspaceID)
	if user != nil {
		fmt.Printf("  user:      %s\n", firstNonEmpty(user.Email, user.Name, user.Subject))
	}
	if len(savedWorkspaces) > 0 {
		fmt.Printf("  access:    %d workspace(s)\n", len(savedWorkspaces))
	}
	fmt.Printf("  config:    %s\n", path)
	return nil
}

func prompt(reader *bufio.Reader, label, fallback string) string {
	if fallback == "" {
		fmt.Printf("%s: ", label)
	} else {
		fmt.Printf("%s [%s]: ", label, fallback)
	}
	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback
	}
	return text
}

func normalizeToken(token string) string {
	return strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
}

func convertWorkspaces(workspaces []platform.Workspace) []auth.Workspace {
	out := make([]auth.Workspace, 0, len(workspaces))
	for _, ws := range workspaces {
		id := firstNonEmpty(ws.WorkspaceID, ws.ID)
		name := firstNonEmpty(ws.WorkspaceName, ws.Name, id)
		if id == "" {
			continue
		}
		out = append(out, auth.Workspace{ID: id, Name: name})
	}
	return out
}

func chooseWorkspace(reader *bufio.Reader, workspaces []auth.Workspace) string {
	if len(workspaces) == 1 {
		return workspaces[0].ID
	}
	fmt.Println("Available workspaces:")
	for i, ws := range workspaces {
		label := ws.ID
		if ws.Name != "" && ws.Name != ws.ID {
			label += " (" + ws.Name + ")"
		}
		fmt.Printf("  %d. %s\n", i+1, label)
	}
	selected := prompt(reader, "Workspace", workspaces[0].ID)
	for {
		if workspaceExists(workspaces, selected) {
			return selected
		}
		fmt.Printf("Workspace %q is not available.\n", selected)
		selected = prompt(reader, "Workspace", workspaces[0].ID)
	}
}

func workspaceExists(workspaces []auth.Workspace, workspaceID string) bool {
	if workspaceID == "" {
		return false
	}
	for _, ws := range workspaces {
		if ws.ID == workspaceID {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
