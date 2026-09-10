package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
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
}

// Execute is the entry point called by main.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
