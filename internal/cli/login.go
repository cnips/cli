package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

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
	if token == "" && baseURL == "" && !cmd.Flags().Changed("api-url") {
		return fmt.Errorf("either --base-url (for browser login) or --token (for token login) is required\n\nExamples:\n  cnips login --base-url https://your-cnips-instance.com\n  cnips login --token \"$CNIPS_TOKEN\"")
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
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ID == "default" {
			return true
		}
		if out[j].ID == "default" {
			return false
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func chooseWorkspace(reader *bufio.Reader, workspaces []auth.Workspace) string {
	if len(workspaces) == 1 {
		return workspaces[0].ID
	}
	options := make([]string, len(workspaces))
	for i, ws := range workspaces {
		if ws.Name != "" && ws.Name != ws.ID {
			options[i] = ws.ID + " (" + ws.Name + ")"
		} else {
			options[i] = ws.ID
		}
	}
	idx := interactiveSelect("Select workspace", options)
	if idx >= 0 && idx < len(workspaces) {
		return workspaces[idx].ID
	}
	return workspaces[0].ID
}

// interactiveSelect presents an arrow-key navigable menu in the terminal.
// Falls back to a numbered prompt when the terminal does not support raw mode.
func interactiveSelect(title string, options []string) int {
	if len(options) == 0 {
		return -1
	}
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fallbackSelect(title, options)
	}
	defer term.Restore(fd, oldState)

	width, _, _ := term.GetSize(fd)
	if width <= 0 {
		width = 80
	}
	numDigits := len(strconv.Itoa(len(options)))
	prefixLen := 6 + numDigits
	maxOptLen := width - prefixLen
	display := make([]string, len(options))
	for i, opt := range options {
		if maxOptLen > 3 && len(opt) > maxOptLen {
			display[i] = opt[:maxOptLen-3] + "..."
		} else {
			display[i] = opt
		}
	}

	selected := 0
	numOpts := len(display)
	write := func(s string) { os.Stdout.WriteString(s) }

	renderOption := func(i int, highlighted bool) {
		write("\r\033[K")
		if highlighted {
			write(fmt.Sprintf("  \033[1;32m❯ %d. %s\033[0m\r\n", i+1, display[i]))
		} else {
			write(fmt.Sprintf("    %d. %s\r\n", i+1, display[i]))
		}
	}

	write("\r\n")
	write(fmt.Sprintf("  \033[1;36m%s\033[0m\r\n", title))
	for i := range display {
		renderOption(i, i == selected)
	}
	maxQuick := numOpts
	if maxQuick > 9 {
		maxQuick = 9
	}
	write(fmt.Sprintf("\r\n  \033[2m↑/↓ navigate • Enter select • 1-%d quick pick • Esc cancel\033[0m", maxQuick))
	write(fmt.Sprintf("\033[%dA\r", numOpts+1))

	renderAll := func() {
		write("\r")
		for i := range display {
			renderOption(i, i == selected)
		}
		write(fmt.Sprintf("\033[%dA\r", numOpts))
	}

	cleanup := func() {
		write(fmt.Sprintf("\033[%dB", numOpts))
		write("\r\033[K\r\n\033[K\r\n")
	}

	buf := make([]byte, 3)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			cleanup()
			return -1
		}
		if n == 3 && buf[0] == 27 && buf[1] == 91 {
			switch buf[2] {
			case 65: // Up
				if selected > 0 {
					selected--
					renderAll()
				}
			case 66: // Down
				if selected < numOpts-1 {
					selected++
					renderAll()
				}
			}
			continue
		}
		if n >= 1 {
			switch buf[0] {
			case 13, 10: // Enter
				cleanup()
				return selected
			case 27, 3: // Esc, Ctrl+C
				cleanup()
				return -1
			default:
				if buf[0] >= '1' && buf[0] <= '9' {
					idx := int(buf[0]-'0') - 1
					if idx < numOpts {
						selected = idx
						renderAll()
						cleanup()
						return idx
					}
				}
			}
		}
	}
}

// fallbackSelect is used when the terminal doesn't support raw mode (pipes, CI).
func fallbackSelect(title string, options []string) int {
	fmt.Printf("\n  %s\n", title)
	for i, opt := range options {
		fmt.Printf("    %d. %s\n", i+1, opt)
	}
	fmt.Printf("\n  Choose [1-%d]: ", len(options))
	reader := bufio.NewReader(os.Stdin)
	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 || n > len(options) {
		return 0
	}
	return n - 1
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
