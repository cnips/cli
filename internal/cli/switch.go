package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/cnips/cli/internal/auth"
	"github.com/cnips/cli/internal/platform"
	"github.com/cnips/cli/internal/project"
	"github.com/spf13/cobra"
)

var switchCmd = &cobra.Command{
	Use:   "switch",
	Short: "Switch the selected cnips workspace",
	Long: `Switches the current profile to another available workspace.

Switching workspaces discards all local cnips artifacts and sync state. The new
workspace is selected in the profile, but no pull is performed automatically.`,
	RunE: runSwitch,
}

func init() {
	switchCmd.Flags().String("profile", "", "Local auth profile name (defaults to current)")
	rootCmd.AddCommand(switchCmd)
}

func runSwitch(cmd *cobra.Command, _ []string) error {
	reader := bufio.NewReader(os.Stdin)
	profileName, _ := cmd.Flags().GetString("profile")
	cfg, err := auth.Load()
	if err != nil {
		return err
	}
	name := strings.TrimSpace(profileName)
	var profile auth.Profile
	var ok bool
	if name != "" {
		profile, ok = cfg.Find(name)
	} else if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		profile, ok = cfg.CurrentForDirectory(cwd)
	}
	if !ok {
		return fmt.Errorf("no logged in profile found; run cnips login first")
	}
	if strings.TrimSpace(profile.Token) == "" {
		return fmt.Errorf("profile %q is logged out; run cnips login before switching workspaces", name)
	}

	fmt.Println("Switching workspaces will discard all local cnips components and sync state.")
	if !confirm(reader, "Continue?") {
		fmt.Println("Workspace switch cancelled.")
		return nil
	}

	workspaces, err := switchableWorkspaces(profile)
	if err != nil {
		return err
	}
	current := strings.TrimSpace(profile.WorkspaceID)
	options := make([]auth.Workspace, 0, len(workspaces))
	for _, ws := range workspaces {
		if ws.ID == "" || ws.ID == current {
			continue
		}
		options = append(options, ws)
	}
	if len(options) == 0 {
		return fmt.Errorf("no other workspaces are available for profile %q", name)
	}

	selected := chooseWorkspace(reader, options)
	root := project.MustFindRoot()
	if err := discardProjectRoot(root); err != nil {
		return err
	}
	if !cfg.SetWorkspaceForDirectory(auth.LoginKey(profile), root, selected) {
		return fmt.Errorf("profile %q does not exist", name)
	}
	if err := auth.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("Switched workspace from %q to %q.\n", current, selected)
	fmt.Println("Local cnips files were discarded. Run cnips pull when you are ready to hydrate this workspace.")
	return nil
}

func switchableWorkspaces(profile auth.Profile) ([]auth.Workspace, error) {
	if len(profile.Workspaces) > 0 {
		return profile.Workspaces, nil
	}
	client := platform.NewClient(profile.APIURL, profile.TenantKey, profile.Token)
	workspaces, err := client.ListWorkspaces()
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	return convertWorkspaces(workspaces), nil
}

func confirm(reader *bufio.Reader, label string) bool {
	fmt.Printf("%s Type yes to confirm: ", label)
	text, _ := reader.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(text), "yes")
}
