package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/cnips/cli/internal/auth"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out of the cnips CLI",
	Long: `Removes the saved access token for a login.

Inside a directory mapped by cnips login, that directory's login is logged out
directly. Outside a mapped directory, an interactive list shows every logged-in
user, base URL, and workspace.

The login configuration and directory mapping are kept so the next cnips login
can reuse the same workspace automatically.`,
	RunE: runLogout,
}

func init() {
	logoutCmd.Flags().String("profile", "", "Local auth profile name (defaults to current)")
	logoutCmd.Flags().String("env", "default", "Environment name used for browser token cache key")
	rootCmd.AddCommand(logoutCmd)
}

func runLogout(cmd *cobra.Command, _ []string) error {
	profileName, _ := cmd.Flags().GetString("profile")
	envName, _ := cmd.Flags().GetString("env")

	cfg, err := auth.Load()
	if err != nil {
		return err
	}
	var profile auth.Profile
	var ok bool
	if strings.TrimSpace(profileName) != "" {
		profile, ok = cfg.Find(profileName)
	} else if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		profile, ok = cfg.ForDirectory(cwd)
	}
	if !ok && strings.TrimSpace(profileName) == "" {
		available := make([]auth.Profile, 0, len(cfg.Logins))
		options := make([]string, 0, len(cfg.Logins))
		for _, candidate := range cfg.Logins {
			if strings.TrimSpace(candidate.Token) == "" {
				continue
			}
			available = append(available, candidate)
			user := candidate.UserID
			if candidate.User != nil {
				user = firstNonEmpty(candidate.User.Email, candidate.User.PreferredUsername, candidate.User.Name, candidate.User.Subject, user)
			}
			options = append(options, fmt.Sprintf("%s — %s — %s", firstNonEmpty(user, candidate.Name), firstNonEmpty(candidate.BaseURL, candidate.APIURL), firstNonEmpty(candidate.WorkspaceName, candidate.WorkspaceID, "default")))
		}
		if len(available) > 0 {
			idx := -1
			if term.IsTerminal(int(os.Stdin.Fd())) {
				idx = interactiveSelect("Select login to log out", options)
			} else {
				idx = fallbackSelectWithReader(bufio.NewReader(os.Stdin), "Select login to log out", options)
			}
			if idx < 0 || idx >= len(available) {
				return fmt.Errorf("logout cancelled")
			}
			profile, ok = available[idx], true
		}
	}
	if !ok {
		if strings.TrimSpace(profileName) == "" {
			fmt.Println("Already logged out.")
			return nil
		}
		return fmt.Errorf("profile %q does not exist", profileName)
	}
	clearProfileTokenCache(profile, envName)
	if !cfg.ClearToken(auth.LoginKey(profile)) {
		return fmt.Errorf("login no longer exists")
	}
	if err := auth.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("Logged out %s from %s (workspace %s). Workspace configuration was kept.\n", firstNonEmpty(profile.UserID, profile.Name), firstNonEmpty(profile.BaseURL, profile.APIURL), firstNonEmpty(profile.WorkspaceName, profile.WorkspaceID, "default"))
	return nil
}

func clearProfileTokenCache(profile auth.Profile, envName string) {
	baseURL := firstNonEmpty(profile.BaseURL, auth.OriginFromAPIURL(profile.APIURL))
	if baseURL == "" {
		return
	}
	if err := auth.ClearTokenCache(auth.LoginOptions{BaseURL: baseURL, EnvName: envName}); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warn: clearing token cache for profile %q: %v\n", profile.Name, err)
	}
}
