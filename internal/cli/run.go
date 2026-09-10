package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cnips/cli/internal/executionlog"
	"github.com/cnips/cli/internal/project"
	"github.com/cnips/cli/internal/runtime"
)

var (
	runPayloadFile string
	runPayload     string
	runEnv         string
	runFrom        string
	runTraceID     string
	runAutoApprove bool
	runAutoReject  bool
	runVerbose     bool
)

var runCmd = &cobra.Command{
	Use:   "run <pipeline>",
	Short: "Execute a pipeline locally",
	Long: `Runs a pipeline from the local project files.

The pipeline is loaded from pipelines/<name>/pipeline.yaml.
Components and functions are built automatically if their source has changed.
A trace is written to .cnips/traces/run_<id>.json after the run.

Native components supported locally:
  native/transformation@1   JSONata (via script file or inline expression)
  native/decision@1         Boolean condition with true/false branches
  native/switch@1           Multi-way routing by expression value
  native/loop@1             Iterate over an array (sequential or concurrent)
  native/approval@1         Interactive terminal prompt (or --auto-approve/--auto-reject)
  native/http-source@1      Outbound HTTP call

Function and component steps:
  function/<name>@<version>  Built from ./functions/<name>/, run as local HTTP server
  <name>@<version>           Built from ./components/<name>/ (or sources/, destinations/, transformations/, approvals/, switches/, decisions/)

Examples:
  cnips run hello-world --payload '{"name":"Alice"}'
  cnips run order-sync  --payload-file ./test-events/order.json --env dev
  cnips run order-sync  --from normalize --trace run_01J8X
  cnips run my-pipeline --auto-approve --verbose`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pipelineName := args[0]
		root := project.MustFindRoot()

		// Load payload
		payload := "{}"
		switch {
		case runPayloadFile != "":
			data, err := os.ReadFile(runPayloadFile)
			if err != nil {
				return fmt.Errorf("read payload file: %w", err)
			}
			payload = string(data)
		case runPayload != "":
			payload = runPayload
		}

		// Validate JSON
		if !json.Valid([]byte(payload)) {
			return fmt.Errorf("payload is not valid JSON")
		}

		if runEnv == "" {
			runEnv = "dev"
		}
		if runAutoApprove && runAutoReject {
			return fmt.Errorf("--auto-approve and --auto-reject cannot be used together")
		}

		execLog, err := executionlog.Start(filepath.Join(root, "pipelines", pipelineName))
		if err != nil {
			return fmt.Errorf("create execution log: %w", err)
		}
		defer execLog.Close()

		fmt.Printf("Running pipeline: %s  (env=%s)\n", pipelineName, runEnv)
		fmt.Printf("Payload: %.80s", payload)
		if len(payload) > 80 {
			fmt.Printf("...")
		}
		fmt.Println()
		fmt.Println()

		trace, err := runtime.Run(runtime.RunOptions{
			Root:         root,
			Pipeline:     pipelineName,
			Payload:      payload,
			Environment:  runEnv,
			FromStep:     runFrom,
			TraceRunID:   runTraceID,
			AutoApprove:  runAutoApprove,
			AutoReject:   runAutoReject,
			Verbose:      runVerbose,
			LogWriter:    execLog.File,
			Progress:     os.Stdout,
			ExecutionDir: execLog.Dir,
			LogPath:      execLog.LogPath,
		})
		if err != nil {
			return fmt.Errorf("pipeline error: %w", err)
		}

		// Print summary
		fmt.Println()
		printTraceSummary(trace)

		if trace.Status == "error" {
			os.Exit(1)
		}
		return nil
	},
}

func init() {
	runCmd.Flags().StringVarP(&runPayloadFile, "payload-file", "f", "", "Path to a JSON file to use as the input payload")
	runCmd.Flags().StringVarP(&runPayload, "payload", "p", "", "JSON string to use as the input payload")
	runCmd.Flags().StringVarP(&runEnv, "env", "e", "dev", "Environment name to load from environments/")
	runCmd.Flags().StringVar(&runFrom, "from", "", "Start execution from the given step ID")
	runCmd.Flags().StringVar(&runTraceID, "trace", "", "Previous trace run ID or path used to seed --from execution")
	runCmd.Flags().BoolVar(&runAutoApprove, "auto-approve", false, "Automatically approve approval steps")
	runCmd.Flags().BoolVar(&runAutoReject, "auto-reject", false, "Automatically reject approval steps")
	runCmd.Flags().BoolVarP(&runVerbose, "verbose", "v", false, "Print each step as it executes")
	rootCmd.AddCommand(runCmd)
}

func printTraceSummary(trace *runtime.Trace) {
	status := trace.Status
	statusIcon := "✓"
	if status == "error" {
		statusIcon = "✗"
	}

	fmt.Printf("%s Pipeline %q  status=%s  duration=%dms\n\n", statusIcon, trace.Pipeline, status, trace.DurationMs)

	steps := summarizeTraceSteps(trace.Steps)
	maxID := 0
	for _, s := range steps {
		maxID = max(maxID, maxTraceIDLen(s))
	}
	if maxID < 6 {
		maxID = 6
	}

	fmt.Printf("  %-*s  %-8s  %-5s  %s\n", maxID, "STEP", "STATUS", "COUNT", "DURATION")
	fmt.Printf("  %s  %s  %s  %s\n", dashes(maxID), dashes(8), dashes(5), dashes(10))
	for _, s := range steps {
		printTraceStep(s, maxID)
	}

	if trace.Error != "" {
		fmt.Printf("\nError: %s\n", trace.Error)
	}
}

type traceSummaryStep struct {
	runtime.StepTrace
	Count int
}

func summarizeTraceSteps(steps []runtime.StepTrace) []traceSummaryStep {
	groupByKey := make(map[string]int)
	summary := make([]traceSummaryStep, 0, len(steps))

	for _, step := range steps {
		key := strings.Join([]string{step.ParentID, step.ID, step.Uses}, "\x00")
		if index, ok := groupByKey[key]; ok {
			mergeTraceSummaryStep(&summary[index], step)
			continue
		}

		groupByKey[key] = len(summary)
		next := traceSummaryStep{
			StepTrace: step,
			Count:     1,
		}
		markNestedTraceError(&next.StepTrace)
		summary = append(summary, next)
	}

	return summary
}

func mergeTraceSummaryStep(summary *traceSummaryStep, step runtime.StepTrace) {
	markNestedTraceError(&step)
	summary.Count++
	summary.DurationMs += step.DurationMs
	summary.StepTrace.Children = append(summary.StepTrace.Children, step.Children...)
	if step.Status == "error" {
		summary.Status = "error"
		if summary.Error == "" {
			summary.Error = step.Error
		}
	}
}

func markNestedTraceError(step *runtime.StepTrace) {
	if step.Status == "error" {
		return
	}
	for _, child := range step.Children {
		if child.Status == "error" {
			step.Status = "error"
			if step.Error == "" {
				step.Error = fmt.Sprintf("nested step %s failed: %s", displayStepName(child.ID), child.Error)
			}
			return
		}
		nested := child
		markNestedTraceError(&nested)
		if nested.Status == "error" {
			step.Status = "error"
			if step.Error == "" {
				step.Error = fmt.Sprintf("nested step %s failed: %s", displayStepName(nested.ID), nested.Error)
			}
			return
		}
	}
}

func printTraceStep(s traceSummaryStep, maxID int) {
	icon := "ok     "
	if s.Status == "error" {
		icon = "ERROR  "
	}
	fmt.Printf("  %-*s  %s  %-5d  %dms\n", maxID, traceStepID(s), icon, s.Count, s.DurationMs)
	if s.Error != "" {
		fmt.Printf("  %s  error: %s\n", spaces(maxID), s.Error)
	}
	for _, child := range summarizeTraceSteps(s.Children) {
		printTraceStep(child, maxID)
	}
}

func maxTraceIDLen(s traceSummaryStep) int {
	maxLen := len(traceStepID(s))
	for _, child := range summarizeTraceSteps(s.Children) {
		maxLen = max(maxLen, maxTraceIDLen(child))
	}
	return maxLen
}

func traceStepID(s traceSummaryStep) string {
	return displayStepName(s.ID)
}

func displayStepName(id string) string {
	if slash := strings.LastIndex(id, "/"); slash >= 0 {
		return id[slash+1:]
	}
	return id
}

func dashes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}

func spaces(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}
