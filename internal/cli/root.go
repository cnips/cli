package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/cnips/cli/internal/auth"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var rootCmd = &cobra.Command{
	Use:   "cnips",
	Short: "cnips local dev CLI — init, build, run pipelines and functions locally",
	Long: `cnips is the local development CLI for the cnips integration platform.

Commands:
  init      Scaffold a new project with the standard folder structure
  add       Scaffold a local component or function
  rename    Rename a local component, function, or pipeline
  login     Store mgmt-srv authentication and workspace access
  logout    Remove the saved authentication token
  switch    Switch workspace after discarding local cnips artifacts
  discard   Reset local cnips artifacts to a clean init-style project
  status    Show local and cnips changes against the tracked base
  pull      Export workspace artifacts into canonical local files
  push      Apply canonical local files to a workspace
  rebase    Replay local cnips work on top of a base workspace
  stash     Temporarily save local cnips changes
  diff      Show differences between local files and a workspace
  fmt       Format canonical cnips YAML files
  publish   Submit pushed components for marketplace review
  build     Compile local components and functions
  run       Execute a pipeline locally with a JSON payload
  trace     Show a local pipeline run trace
  fn        Function development commands (fn dev)
  fun       Run a function locally (alias for fn dev)
  validate  Validate local cnips project files before push

Run 'cnips <command> --help' for more information.`,
	PersistentPreRunE: requireTokenForCommand,
}

func init() {
	defaultHelp := rootCmd.HelpFunc()
	defaultUsage := rootCmd.UsageFunc()
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		labelFlagRequirements(cmd)
		defaultHelp(cmd, args)
	})
	rootCmd.SetUsageFunc(func(cmd *cobra.Command) error {
		labelFlagRequirements(cmd)
		return defaultUsage(cmd)
	})
}

func labelFlagRequirements(cmd *cobra.Command) {
	for current := cmd; current != nil; current = current.Parent() {
		current.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
			if strings.HasPrefix(flag.Usage, "[mandatory]") || strings.HasPrefix(flag.Usage, "[optional]") || strings.HasPrefix(flag.Usage, "[one required]") {
				return
			}
			label := "[optional] "
			if flag.Annotations != nil && len(flag.Annotations["cnips_mandatory"]) > 0 {
				label = "[mandatory] "
			} else if flag.Annotations != nil && len(flag.Annotations["cnips_one_required"]) > 0 {
				label = "[one required] "
			}
			flag.Usage = label + flag.Usage
		})
	}
}

// Execute is the entry point called by main.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func requireTokenForCommand(cmd *cobra.Command, _ []string) error {
	if commandAllowedWithoutToken(cmd) {
		return nil
	}
	if tokenFlag := cmd.Flags().Lookup("token"); tokenFlag != nil && cmd.Flags().Changed("token") && normalizeToken(tokenFlag.Value.String()) != "" {
		return nil
	}
	cfg, err := auth.Load()
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	profile, ok := cfg.CurrentForDirectory(cwd)
	if !ok || strings.TrimSpace(profile.Token) == "" {
		return fmt.Errorf("not logged in; run cnips login first")
	}
	return nil
}

func commandAllowedWithoutToken(cmd *cobra.Command) bool {
	if cmd == nil {
		return true
	}
	switch cmd.Name() {
	case "init", "login", "logout", "discard", "add", "rename", "fmt", "help", "completion":
		return true
	default:
		return false
	}
}
