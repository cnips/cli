package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cnips/cli/internal/project"
	"github.com/cnips/cli/internal/runtime"
)

var traceJSON bool

var traceCmd = &cobra.Command{
	Use:   "trace <run-id>",
	Short: "Show a local pipeline run trace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := project.MustFindRoot()
		trace, err := runtime.LoadTrace(root, args[0])
		if err != nil {
			return err
		}
		if traceJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(trace)
		}
		printTraceSummary(trace)
		if trace.LogPath != "" {
			fmt.Printf("\nlogs: %s\n", trace.LogPath)
		}
		return nil
	},
}

func init() {
	traceCmd.Flags().BoolVar(&traceJSON, "json", false, "Print the full trace JSON")
	rootCmd.AddCommand(traceCmd)
}
