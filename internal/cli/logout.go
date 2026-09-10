package cli

import (
	"fmt"
	"os"

	"github.com/cnips/cli/internal/auth"
	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out of the cnips CLI",
	Long: `Removes the saved access token for the current profile.

The profile configuration, tenant, available workspaces, and selected workspace
are kept so the next cnips login can reuse the same workspace automatically.`,
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
	name := profileName
	if name == "" {
		name = cfg.CurrentProfile
	}
	profile, ok := cfg.Profiles[name]
	if !ok {
		if name == "" {
			fmt.Println("Already logged out.")
			return nil
		}
		return fmt.Errorf("profile %q does not exist", name)
	}
	clearProfileTokenCache(profile, envName)
	if !cfg.ClearToken(name) {
		return fmt.Errorf("profile %q does not exist", name)
	}
	if err := auth.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("Logged out of profile %q. Workspace configuration was kept.\n", name)
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
