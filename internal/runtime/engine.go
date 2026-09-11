// Package runtime implements the local pipeline execution engine.
// It parses pipeline.yaml, traverses the DAG, executes native and
// local-component steps, and writes a trace file under .cnips/traces/.
package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	jg "github.com/blues/jsonata-go"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/builder"
	"github.com/cnips/cli/internal/fnrunner"
	"github.com/cnips/cli/internal/project"
)

// RunOptions controls how the pipeline is executed.
type RunOptions struct {
	Root         string
	Pipeline     string // pipeline name
	Payload      string // initial JSON payload (string)
	Environment  string // environment name (e.g. "dev")
	FromStep     string // optional step ID to resume execution from
	TraceRunID   string // optional previous trace used to seed resume context
	AutoApprove  bool
	AutoReject   bool
	Verbose      bool
	LogWriter    io.Writer
	Progress     io.Writer
	ExecutionDir string
	LogPath      string
}

// StepTrace holds execution data for one step.
type StepTrace struct {
	ID         string          `json:"id"`
	Uses       string          `json:"uses"`
	ParentID   string          `json:"parentId,omitempty"`
	LoopIndex  *int            `json:"loopIndex,omitempty"`
	Status     string          `json:"status"` // ok | error | skipped | approval
	DurationMs int64           `json:"durationMs"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	Logs       []string        `json:"logs,omitempty"`
	Children   []StepTrace     `json:"children,omitempty"`
}

// Trace is the full run record written to .cnips/traces/run_<id>.json.
type Trace struct {
	RunID      string      `json:"runId"`
	Pipeline   string      `json:"pipeline"`
	Status     string      `json:"status"`
	StartedAt  time.Time   `json:"startedAt"`
	DurationMs int64       `json:"durationMs"`
	Steps      []StepTrace `json:"steps"`
	Error      string      `json:"error,omitempty"`
	LogPath    string      `json:"logPath,omitempty"`
}

// Run executes the pipeline and returns the trace.
func Run(opts RunOptions) (*Trace, error) {
	if err := project.EnsureDirs(opts.Root); err != nil {
		return nil, err
	}

	// --- load pipeline ---
	pipeline, pipelineDir, err := artifact.ParsePipelineByName(opts.Root, opts.Pipeline)
	if err != nil {
		return nil, fmt.Errorf("load pipeline: %w", err)
	}

	// --- load environment (optional) ---
	envVars := make(map[string]string)
	loadProjectResourceVars(opts.Root, envVars)
	for _, v := range pipeline.Spec.Variables {
		if v.Name != "" {
			envVars[v.Name] = v.Value
		}
	}
	if opts.Environment != "" {
		env, err := artifact.ParseEnvironment(opts.Root, opts.Environment)
		if err == nil {
			for k, v := range env.Spec.Variables {
				envVars[k] = v
			}
		}
	}

	// --- build step index ---
	stepIndex := artifact.StepIndex(pipeline)

	// --- initialise state ---
	runID := "run_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	trace := &Trace{
		RunID:     runID,
		Pipeline:  pipeline.Metadata.Name,
		StartedAt: time.Now(),
		Status:    "running",
		LogPath:   opts.LogPath,
	}

	// step output context: stepID -> JSON string
	ctx := map[string]string{
		"_payload": opts.Payload,
	}

	mgr := fnrunner.NewManagerWithLogs(opts.LogWriter)
	defer mgr.StopAll()

	// --- find entry step ---
	currentID, label, err := findEntryStep(pipeline, stepIndex)
	if err != nil {
		return nil, err
	}
	if opts.TraceRunID != "" {
		resumePayload, err := seedContextFromTrace(opts.Root, opts.TraceRunID, opts.FromStep, ctx)
		if err != nil {
			return nil, err
		}
		if resumePayload != "" {
			ctx["_payload"] = resumePayload
		}
	}
	if opts.FromStep != "" {
		if stepIndex[opts.FromStep] == nil {
			return nil, fmt.Errorf("from step %q not found", opts.FromStep)
		}
		currentID = opts.FromStep
		label = ""
	}

	start := time.Now()

	for currentID != "" {
		step := stepIndex[currentID]
		if step == nil {
			trace.Error = fmt.Sprintf("step %q not found", currentID)
			trace.Status = "error"
			break
		}

		if opts.Verbose {
			fmt.Printf("  [run] step %-20s  uses=%s\n", step.ID, step.Uses)
		}

		stepStart := time.Now()
		st := StepTrace{ID: step.ID, Uses: step.Uses}

		// resolve the input payload
		inputPayload := resolveTemplate(ctx["_payload"], ctx, envVars)
		st.Input = rawJSON(inputPayload)

		// execute the step
		output, nextID, nextLabel, err := executeStep(step, inputPayload, ctx, envVars, pipelineDir, opts, mgr, &st)
		st.DurationMs = time.Since(stepStart).Milliseconds()

		if err != nil {
			st.Status = "error"
			st.Error = err.Error()
			trace.Steps = append(trace.Steps, st)
			trace.Status = "error"
			trace.Error = fmt.Sprintf("step %s failed: %v", step.ID, err)
			break
		}

		st.Status = "ok"
		st.Output = rawJSON(output)
		trace.Steps = append(trace.Steps, st)

		// store output for template resolution
		ctx[step.ID+".output"] = output
		ctx["_payload"] = output

		currentID = nextID
		label = nextLabel
	}

	trace.DurationMs = time.Since(start).Milliseconds()
	if trace.Status == "running" {
		trace.Status = "ok"
	}

	// write trace
	traceDir := project.TraceDir(opts.Root)
	if opts.ExecutionDir != "" {
		traceDir = opts.ExecutionDir
	}
	tracePath := filepath.Join(traceDir, runID+".json")
	if data, err := json.MarshalIndent(trace, "", "  "); err == nil {
		_ = os.WriteFile(tracePath, data, 0o644)
		fmt.Printf("\n  trace: %s\n", tracePath)
		if opts.LogPath != "" {
			fmt.Printf("  logs:  %s\n", opts.LogPath)
		}
	}

	_ = label // suppress unused warning; label drives next step already
	return trace, nil
}

func seedContextFromTrace(root, runID, fromStep string, ctx map[string]string) (string, error) {
	trace, err := LoadTrace(root, runID)
	if err != nil {
		return "", err
	}
	var resumePayload string
	foundFromStep := fromStep == ""
	for _, step := range flattenTraceSteps(trace.Steps) {
		if fromStep != "" && step.ID == fromStep {
			foundFromStep = true
			if len(step.Input) > 0 {
				resumePayload = string(step.Input)
			}
			break
		}
		if len(step.Output) > 0 {
			out := string(step.Output)
			ctx[step.ID+".output"] = out
			ctx["_payload"] = out
		}
	}
	if !foundFromStep {
		return "", fmt.Errorf("from step %q was not found in trace %q", fromStep, runID)
	}
	return resumePayload, nil
}

func flattenTraceSteps(steps []StepTrace) []StepTrace {
	var out []StepTrace
	var walk func([]StepTrace)
	walk = func(items []StepTrace) {
		for _, item := range items {
			out = append(out, item)
			if len(item.Children) > 0 {
				walk(item.Children)
			}
		}
	}
	walk(steps)
	return out
}

func LoadTrace(root, runID string) (*Trace, error) {
	path, err := FindTrace(root, runID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read trace %s: %w", runID, err)
	}
	var trace Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		return nil, fmt.Errorf("parse trace %s: %w", runID, err)
	}
	return &trace, nil
}

func FindTrace(root, runID string) (string, error) {
	name := strings.TrimSpace(runID)
	if name == "" {
		return "", fmt.Errorf("trace run id is required")
	}
	if info, err := os.Stat(name); err == nil && !info.IsDir() {
		return name, nil
	}
	if !filepath.IsAbs(name) {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && !info.IsDir() {
			return filepath.Join(root, name), nil
		}
	}
	if !strings.HasSuffix(name, ".json") {
		name += ".json"
	}
	candidates := []string{
		filepath.Join(project.TraceDir(root), name),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	pipelineRoot := filepath.Join(root, "pipelines")
	if info, err := os.Stat(pipelineRoot); err != nil || !info.IsDir() {
		return "", fmt.Errorf("trace %q not found under %s", runID, root)
	}
	var found string
	err := filepath.WalkDir(pipelineRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || filepath.Base(path) != name {
			return nil
		}
		found = path
		return filepath.SkipAll
	})
	if err != nil {
		return "", err
	}
	if found != "" {
		return found, nil
	}
	return "", fmt.Errorf("trace %q not found under %s", runID, root)
}

func loadProjectResourceVars(root string, envVars map[string]string) {
	globalVars, _ := artifact.ListGlobalVariables(root)
	for _, gv := range globalVars {
		aliases := []string{gv.Spec.Key, gv.Metadata.Name, gv.Spec.Ref}
		addAliases(envVars, aliases, gv.Spec.Value)
	}

	configs, _ := artifact.ListConfigurations(root)
	for _, config := range configs {
		aliases := []string{config.Spec.Ref, config.Metadata.Name, config.Spec.Key}
		if strings.EqualFold(config.Spec.Type, "KeyValue") {
			addAliases(envVars, aliases, config.Spec.Value)
			continue
		}
		if secret := localConfigurationSecret(config.Spec.APIAccess); secret != "" {
			addAliases(envVars, aliases, secret)
		}
	}
}

func logf(opts RunOptions, format string, args ...any) {
	if opts.LogWriter != nil {
		fmt.Fprintf(opts.LogWriter, format, args...)
		return
	}
	fmt.Printf("  "+format, args...)
}

type loopProgress struct {
	writer    io.Writer
	stepID    string
	total     int
	lastShown int
	lastAt    time.Time
}

func newLoopProgress(writer io.Writer, stepID string, total int) *loopProgress {
	p := &loopProgress{writer: writer, stepID: stepID, total: total}
	if writer != nil && total >= 10 {
		fmt.Fprintf(writer, "  [loop] %s 0/%d complete\n", stepID, total)
		p.lastAt = time.Now()
	}
	return p
}

func (p *loopProgress) Advance(done int) {
	if p == nil || p.writer == nil || p.total < 10 {
		return
	}
	now := time.Now()
	if done < p.total && done-p.lastShown < 10 && now.Sub(p.lastAt) < time.Second {
		return
	}
	p.lastShown = done
	p.lastAt = now
	fmt.Fprintf(p.writer, "  [loop] %s %d/%d complete\n", p.stepID, done, p.total)
}

func (p *loopProgress) Done() {
	if p == nil || p.writer == nil || p.total < 10 || p.lastShown == p.total {
		return
	}
	p.Advance(p.total)
}

func addAliases(envVars map[string]string, aliases []string, value string) {
	if value == "" {
		return
	}
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		envVars[alias] = value
	}
}

func localConfigurationSecret(apiAccess map[string]any) string {
	accessType, _ := apiAccess["apiAccessType"].(string)
	switch strings.ToUpper(strings.TrimSpace(accessType)) {
	case "APIKEY":
		return nestedString(apiAccess, "apikeyDetails", "apikey")
	case "TOTP":
		return nestedString(apiAccess, "totpDetails", "totpkey")
	case "GEN_OAUTH2", "CIDAAS_OAUTH2":
		return nestedString(apiAccess, "oAuthDetails", "client_secret")
	case "BASIC_AUTH":
		return nestedString(apiAccess, "basicAuthDetails", "password")
	default:
		return ""
	}
}

func nestedString(data map[string]any, objectKey, fieldKey string) string {
	nested, ok := data[objectKey].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := nested[fieldKey].(string)
	return value
}

// -----------------------------------------------------------------------
// Step execution
// -----------------------------------------------------------------------

// executeStep dispatches to the right handler and returns (output, nextID, nextLabel, err).
func executeStep(
	step *artifact.Step,
	payload string,
	ctx, envVars map[string]string,
	pipelineDir string,
	opts RunOptions,
	mgr *fnrunner.Manager,
	trace *StepTrace,
) (output, nextID, nextLabel string, err error) {
	ns, name, version := artifact.UsesKind(step.Uses)

	switch ns {
	case "native":
		return executeNative(step, name, payload, ctx, envVars, pipelineDir, opts, mgr, trace)
	case "function":
		out, err := executeFunction(step, name, payload, ctx, envVars, opts, mgr)
		return out, step.Next, "", err
	case "source":
		// Source/extractor step: run the component once and use its output as
		// the pipeline payload.  If the caller already provided a non-empty
		// payload we pass it through and skip the actual execution (useful for
		// cnips run --payload '...' where the event is already known).
		out, err := executeSource(step, name, "source", version, payload, ctx, envVars, opts, mgr)
		return out, step.Next, "", err
	case "transformation", "destination", "app":
		// Custom user-code components — run as HTTP service.
		out, err := executeLocalComponent(step, name, ns, version, payload, ctx, envVars, opts, mgr)
		return out, step.Next, "", err
	default:
		if local, ok := artifact.LocalComponentVersion(opts.Root, name, version); ok && local.Kind == "source" {
			out, err := executeSource(step, name, "source", version, payload, ctx, envVars, opts, mgr)
			return out, step.Next, "", err
		}

		// Unnamespaced reference: try local lookup (components/, destinations/, etc.).
		out, err := executeLocalComponent(step, name, "", version, payload, ctx, envVars, opts, mgr)
		return out, step.Next, "", err
	}
}

// -----------------------------------------------------------------------
// Native component handlers
// -----------------------------------------------------------------------

func executeNative(
	step *artifact.Step, name, payload string,
	ctx, envVars map[string]string,
	pipelineDir string,
	opts RunOptions,
	mgr *fnrunner.Manager,
	trace *StepTrace,
) (output, nextID, nextLabel string, err error) {
	with := step.With

	switch name {
	case "transformation":
		return nativeTransformation(step, with, payload, ctx, envVars, pipelineDir)

	case "decision":
		return nativeDecision(step, with, payload, ctx, envVars)

	case "switch":
		return nativeSwitch(step, with, payload, ctx, envVars)

	case "loop":
		return nativeLoop(step, with, payload, ctx, envVars, pipelineDir, opts, mgr, trace)

	case "approval":
		return nativeApproval(step, with, payload, opts)

	case "http-source":
		out, err := nativeHTTP(step, with, payload, ctx, envVars)
		return out, step.Next, "", err

	case "http-request":
		out, next, err := executeProcessorNative(step, name, payload, envVars)
		return out, next, "", err

	case "delay":
		return nativeDelay(step, with, payload, opts)

	case "stop-error":
		out, next, err := executeProcessorNative(step, name, payload, envVars)
		return out, next, "", err

	case "json-filter":
		out, next, err := executeProcessorNative(step, name, payload, envVars)
		return out, next, "", err

	case "template", "sort", "aggregator", "deduplicate", "markup-converter",
		"markup_converter", "data-mapper", "data_mapper", "regex-extractor",
		"regex_extractor", "json-schema-validator", "json_schema_validator",
		"crypto", "jwt":
		out, next, err := executeProcessorNative(step, name, payload, envVars)
		return out, next, "", err

	default:
		// unknown native — pass payload through with warning
		logf(opts, "[warn] unknown native component %q, passing payload through\n", name)
		return payload, step.Next, "", nil
	}
}

func nativeTransformation(
	step *artifact.Step, with map[string]any,
	payload string,
	ctx, envVars map[string]string,
	pipelineDir string,
) (string, string, string, error) {
	engine, _ := with["engine"].(string)
	if engine == "" {
		engine = "jsonata"
	}

	switch engine {
	case "jsonata":
		var exprStr string
		if scriptPath, ok := with["script"].(string); ok {
			// load script from file relative to pipeline dir
			abs := filepath.Join(pipelineDir, scriptPath)
			data, err := os.ReadFile(abs)
			if err != nil {
				return "", "", "", fmt.Errorf("read jsonata script %s: %w", scriptPath, err)
			}
			exprStr = string(data)
		} else if expr, ok := with["expression"].(string); ok {
			exprStr = expr
		} else {
			return "", "", "", fmt.Errorf("transformation step %s: missing 'script' or 'expression'", step.ID)
		}

		// resolve templates in payload
		resolvedPayload := resolveTemplate(payload, ctx, envVars)
		out, err := evalJSONata(exprStr, resolvedPayload)
		if err != nil {
			return "", "", "", fmt.Errorf("jsonata eval: %w", err)
		}
		return out, step.Next, "", nil

	default:
		return "", "", "", fmt.Errorf("unsupported transformation engine %q", engine)
	}
}

func nativeDecision(
	step *artifact.Step, with map[string]any,
	payload string,
	ctx, envVars map[string]string,
) (string, string, string, error) {
	condition, _ := with["condition"].(string)
	if condition == "" {
		return payload, step.Next, "", nil
	}

	// Prefer top-level step.Branches (set by pull serializer), fall back to with["branches"].
	trueBranch := step.Branches["true"]
	falseBranch := step.Branches["false"]
	if trueBranch == "" || falseBranch == "" {
		if branches, ok := with["branches"].(map[string]any); ok {
			if trueBranch == "" {
				trueBranch, _ = branches["true"].(string)
			}
			if falseBranch == "" {
				falseBranch, _ = branches["false"].(string)
			}
		}
	}

	result, err := evalJSONata(condition, resolveTemplate(payload, ctx, envVars))
	if err != nil {
		return "", "", "", fmt.Errorf("decision condition eval: %w", err)
	}

	var boolResult bool
	if err := json.Unmarshal([]byte(result), &boolResult); err != nil {
		// treat non-null/non-false/non-zero as truthy
		boolResult = result != "" && result != "null" && result != "false" && result != "0"
	}

	if boolResult {
		return payload, trueBranch, "true", nil
	}
	return payload, falseBranch, "false", nil
}

func nativeSwitch(
	step *artifact.Step, with map[string]any,
	payload string,
	ctx, envVars map[string]string,
) (string, string, string, error) {
	expression, _ := with["expression"].(string)

	// Prefer top-level step.Cases (set by pull serializer), fall back to with["cases"].
	cases := make(map[string]string)
	for k, v := range step.Cases {
		cases[k] = v
	}
	if len(cases) == 0 {
		if withCases, ok := with["cases"].(map[string]any); ok {
			for k, v := range withCases {
				if s, ok := v.(string); ok {
					cases[k] = s
				}
			}
		}
	}

	var caseValue string
	if expression != "" {
		val, err := evalJSONata(expression, resolveTemplate(payload, ctx, envVars))
		if err != nil {
			return "", "", "", fmt.Errorf("switch expression eval: %w", err)
		}
		caseValue = strings.Trim(val, `"`)
	}

	if next, ok := cases[caseValue]; ok {
		return payload, next, caseValue, nil
	}
	if def, ok := cases["default"]; ok {
		return payload, def, "default", nil
	}

	return payload, step.Next, "", nil
}

func nativeLoop(
	step *artifact.Step, with map[string]any,
	payload string,
	ctx, envVars map[string]string,
	pipelineDir string,
	opts RunOptions,
	mgr *fnrunner.Manager,
	trace *StepTrace,
) (string, string, string, error) {
	config := loopConfigFromWith(with)
	resolvedPayload := resolveTemplate(payload, ctx, envVars)

	items, parentEvent, err := extractLoopItems(resolvedPayload, config)
	if err != nil {
		return "", "", "", err
	}

	bodySteps, nextAfterLoop, err := resolveLoopBodySteps(step, with, pipelineDir)
	if err != nil {
		return "", "", "", fmt.Errorf("loop body resolve: %w", err)
	}

	ownManager := false
	if mgr == nil {
		mgr = fnrunner.NewManagerWithLogs(opts.LogWriter)
		ownManager = true
	}
	if ownManager {
		defer mgr.StopAll()
	}

	results := make([]any, 0, len(items))
	progress := newLoopProgress(opts.Progress, step.ID, len(items))
	for i, item := range items {
		loopCtx := copyMap(ctx)
		itemPayload, err := buildLoopItemPayload(parentEvent, item, i, i%config.BatchSize, i/config.BatchSize, len(items))
		if err != nil {
			return "", "", "", fmt.Errorf("loop item %d payload: %w", i, err)
		}

		itemJSON, _ := json.Marshal(item)
		loopCtx["loop.item"] = string(itemJSON)
		loopCtx["loop.index"] = fmt.Sprintf("%d", i)
		loopCtx["_payload"] = itemPayload

		loopPayload := itemPayload
		bodyEndedInLoop := false
		for _, bs := range bodySteps {
			bodyEndedInLoop = isNativeLoopStep(&bs)
			childStart := time.Now()
			loopIndex := i
			childTrace := StepTrace{
				ID:        bs.ID,
				Uses:      bs.Uses,
				ParentID:  step.ID,
				LoopIndex: &loopIndex,
				Input:     rawJSON(resolveTemplate(loopPayload, loopCtx, envVars)),
			}

			out, nextID, _, err := executeStep(&bs, loopPayload, loopCtx, envVars, pipelineDir, opts, mgr, &childTrace)
			childTrace.DurationMs = time.Since(childStart).Milliseconds()
			if err != nil {
				childTrace.Status = "error"
				childTrace.Error = err.Error()
				if trace != nil {
					trace.Children = append(trace.Children, childTrace)
				}
				if config.AllowFailures {
					logf(opts, "[warn] loop item %d step %s error: %v\n", i, bs.ID, err)
					break
				}
				return "", "", "", fmt.Errorf("loop item %d step %s: %w", i, bs.ID, err)
			}
			childTrace.Status = "ok"
			childTrace.Output = rawJSON(out)
			if trace != nil {
				trace.Children = append(trace.Children, childTrace)
			}
			loopPayload = out
			loopCtx[bs.ID+".output"] = out
			if nextID == "" {
				break
			}
		}
		var result any
		if err := json.Unmarshal([]byte(loopPayload), &result); err != nil {
			result = loopPayload
		}
		if bodyEndedInLoop {
			if nestedResults, ok := result.([]any); ok {
				results = append(results, nestedResults...)
				progress.Advance(i + 1)
				continue
			}
		}
		results = append(results, result)
		progress.Advance(i + 1)
	}
	progress.Done()

	// combine results as array
	combined, _ := json.Marshal(results)
	return string(combined), nextAfterLoop, "", nil
}

func nativeApproval(
	step *artifact.Step, with map[string]any,
	payload string,
	opts RunOptions,
) (string, string, string, error) {
	if opts.AutoApprove && opts.AutoReject {
		return "", "", "", fmt.Errorf("approval step %s cannot be both auto-approved and auto-rejected", step.ID)
	}
	if opts.AutoApprove {
		logf(opts, "[approval] step %s auto-approved\n", step.ID)
		return payload, step.Next, "", nil
	}
	if opts.AutoReject {
		logf(opts, "[approval] step %s auto-rejected\n", step.ID)
		return "", "", "", fmt.Errorf("approval rejected by --auto-reject")
	}

	fmt.Printf("\n  [approval] step %s requires approval.\n", step.ID)
	fmt.Printf("  Press ENTER to approve, type 'reject' to reject: ")
	var input string
	fmt.Scanln(&input)
	if strings.ToLower(strings.TrimSpace(input)) == "reject" {
		return "", "", "", fmt.Errorf("approval rejected by user")
	}
	return payload, step.Next, "", nil
}

func nativeDelay(step *artifact.Step, with map[string]any, payload string, opts RunOptions) (string, string, string, error) {
	ms := 0
	switch v := with["durationMs"].(type) {
	case int:
		ms = v
	case float64:
		ms = int(v)
	case string:
		fmt.Sscanf(v, "%d", &ms)
	}
	if ms <= 0 {
		// also accept "duration" key as a fallback
		switch v := with["duration"].(type) {
		case int:
			ms = v
		case float64:
			ms = int(v)
		}
	}
	if ms > 0 {
		logf(opts, "[delay] sleeping %dms\n", ms)
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	return payload, step.Next, "", nil
}

func nativeJSONFilter(
	step *artifact.Step, with map[string]any,
	payload string,
	ctx, envVars map[string]string,
) (string, string, string, error) {
	expr, _ := with["expression"].(string)
	if expr == "" {
		return payload, step.Next, "", nil
	}
	out, err := evalJSONata(expr, resolveTemplate(payload, ctx, envVars))
	if err != nil {
		return "", "", "", fmt.Errorf("json-filter eval: %w", err)
	}
	return out, step.Next, "", nil
}

func nativeHTTP(
	_ *artifact.Step, with map[string]any,
	payload string,
	ctx, envVars map[string]string,
) (string, error) {
	method, _ := with["method"].(string)
	urlStr, _ := with["url"].(string)

	if method == "" {
		method = "GET"
	}
	urlStr = resolveTemplate(urlStr, ctx, envVars)

	import_http, err := doHTTPRequest(method, urlStr, payload)
	if err != nil {
		return "", fmt.Errorf("http-source %s %s: %w", method, urlStr, err)
	}
	return import_http, nil
}

// -----------------------------------------------------------------------
// Function and component execution
// -----------------------------------------------------------------------

func executeFunction(
	step *artifact.Step, fnName, payload string,
	ctx, envVars map[string]string,
	opts RunOptions,
	mgr *fnrunner.Manager,
) (string, error) {
	fnDir := filepath.Join(opts.Root, "functions", fnName)
	if _, err := os.Stat(fnDir); os.IsNotExist(err) {
		return "", fmt.Errorf("function %q not found at %s", fnName, fnDir)
	}

	resolvedPayload := resolveTemplate(payload, ctx, envVars)
	with := cloneStepWith(step.With)
	method := "POST"
	if fn, err := artifact.ParseFunction(fnDir); err == nil {
		if !artifact.IsSupportedFunctionMethod(fn.Spec.Method()) {
			return "", fmt.Errorf("function %q has unsupported method %q (use GET, POST, PUT, or DELETE)", fnName, fn.Spec.Method())
		}
		method = fn.Spec.NormalizedMethod()
		for k, v := range fn.Spec.Config {
			if _, exists := with[k]; !exists {
				with[k] = v
			}
		}
		if fn.Spec.APIAccessRef != "" {
			with["apiAccessRef"] = fn.Spec.APIAccessRef
		}
	}

	// build if needed
	var build *builder.Result
	if builder.NeedsBuild(opts.Root, fnName, fnDir) {
		logf(opts, "[build] building function %s...\n", fnName)
		var err error
		build, err = builder.BuildWithOptions(opts.Root, fnName, fnDir, builder.Options{LogWriter: opts.LogWriter})
		if err != nil {
			return "", fmt.Errorf("build function %s: %w", fnName, err)
		}
		_ = builder.SaveDigest(opts.Root, fnName, fnDir)
	} else {
		// reconstruct result from cache
		outDir := filepath.Join(opts.Root, ".cnips", "cache", "artifacts", fnName)
		build = inferBuildResult(fnName, fnDir, outDir)
	}

	envelope := buildEnvelope(resolvedPayload, with, envVars)
	return mgr.ExecuteWithMethod(fnName, build, method, envelope)
}

func executeLocalComponent(
	step *artifact.Step, name, kind, version, payload string,
	ctx, envVars map[string]string,
	opts RunOptions,
	mgr *fnrunner.Manager,
) (string, error) {
	compDir, found := artifact.LocalComponentDirKindVersion(opts.Root, name, kind, version)
	if !found {
		logf(opts, "[warn] component %q version %q not found locally, passing payload through\n", name, displayVersion(version))
		return payload, nil
	}

	resolvedPayload := resolveTemplate(payload, ctx, envVars)
	with := componentWith(compDir, step.With)
	buildName := componentBuildName(name, version)

	var build *builder.Result
	if builder.NeedsBuild(opts.Root, buildName, compDir) {
		logf(opts, "[build] building component %s@%s...\n", name, displayVersion(version))
		var err error
		build, err = builder.BuildWithOptions(opts.Root, buildName, compDir, builder.Options{LogWriter: opts.LogWriter})
		if err != nil {
			return "", fmt.Errorf("build component %s@%s: %w", name, displayVersion(version), err)
		}
		_ = builder.SaveDigest(opts.Root, buildName, compDir)
	} else {
		outDir := filepath.Join(opts.Root, ".cnips", "cache", "artifacts", buildName)
		build = inferBuildResult(buildName, compDir, outDir)
	}

	// Wrap payload with config/vars envelope so server.mjs can pass them to execute().
	envelope := buildEnvelope(resolvedPayload, with, envVars)
	return mgr.Execute(buildName, build, envelope)
}

// executeSource handles steps with namespace "source/" (pulled extractors).
// When a non-empty payload is already available (provided via --payload or a
// previous step), we use it directly — the source is the pipeline trigger and
// the event was already supplied externally.
// When no payload is available we invoke the extractor's execute() function
// in one-shot mode and use the result as the pipeline payload.
func executeSource(
	step *artifact.Step, name, kind, version, payload string,
	ctx, envVars map[string]string,
	opts RunOptions,
	mgr *fnrunner.Manager,
) (string, error) {
	trimmed := strings.TrimSpace(payload)
	nonEmpty := trimmed != "" && trimmed != "{}" && trimmed != "null" && trimmed != "[]"

	if nonEmpty {
		logf(opts, "[source] %s using provided payload (skipping extractor)\n", name)
		return payload, nil
	}

	// No payload — actually run the extractor.
	compDir, found := artifact.LocalComponentDirKindVersion(opts.Root, name, kind, version)
	if !found {
		return "", fmt.Errorf("source %q version %q not found locally; provide a non-empty --payload or pull/add the source component under sources/%s", name, displayVersion(version), name)
	}

	buildName := componentBuildName(name, version)
	var build *builder.Result
	if builder.NeedsBuild(opts.Root, buildName, compDir) {
		logf(opts, "[build] building source %s@%s...\n", name, displayVersion(version))
		var err error
		build, err = builder.BuildWithOptions(opts.Root, buildName, compDir, builder.Options{LogWriter: opts.LogWriter})
		if err != nil {
			return "", fmt.Errorf("build source %s@%s: %w", name, displayVersion(version), err)
		}
		_ = builder.SaveDigest(opts.Root, buildName, compDir)
	} else {
		outDir := filepath.Join(opts.Root, ".cnips", "cache", "artifacts", buildName)
		build = inferBuildResult(buildName, compDir, outDir)
	}
	if build.Language == "javascript" && build.RunnerScript == "" && !builder.JSUsesDirectServer(compDir) {
		runner := filepath.Join(build.OutDir, "runner.mjs")
		if _, err := os.Stat(runner); err == nil {
			build.RunnerScript = runner
		} else {
			_, runner, err := builder.EnsureJSWrappers(build.OutDir)
			if err != nil {
				return "", fmt.Errorf("prepare source runner %s: %w", name, err)
			}
			build.RunnerScript = runner
		}
	}

	config := componentWith(compDir, step.With)
	vars := make(map[string]any)
	for k, v := range envVars {
		vars[k] = v
	}

	out, err := fnrunner.ExecuteOneShotWithLogs(buildName, build, config, vars, opts.LogWriter)
	if err != nil {
		return "", fmt.Errorf("source %s: %w", name, err)
	}
	return out, nil
}

func componentBuildName(name, version string) string {
	version = normalizeBuildVersion(version)
	if version == "latest" {
		return name + "@latest"
	}
	return name + "@" + version
}

func displayVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "latest"
	}
	return version
}

func componentWith(compDir string, stepWith map[string]any) map[string]any {
	with := make(map[string]any)
	if comp, err := artifact.ParseComponent(compDir); err == nil {
		for k, v := range comp.Spec.Config {
			with[k] = v
		}
		if comp.Spec.APIAccessRef != "" {
			with["apiAccessRef"] = comp.Spec.APIAccessRef
		}
	}
	for k, v := range stepWith {
		with[k] = v
	}
	return with
}

func normalizeBuildVersion(version string) string {
	version = displayVersion(version)
	if version == "latest" || strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

func cloneStepWith(stepWith map[string]any) map[string]any {
	out := make(map[string]any, len(stepWith))
	for k, v := range stepWith {
		out[k] = v
	}
	return out
}

// buildEnvelope wraps a raw payload JSON with config and vars for server.mjs.
// { "data": <payload>, "config": <step.With>, "vars": <envVars> }
func buildEnvelope(payload string, with map[string]any, envVars map[string]string) string {
	config := with
	if config == nil {
		config = map[string]any{}
	}
	vars := make(map[string]any, len(envVars))
	for k, v := range envVars {
		vars[k] = v
	}

	var dataNode any
	if err := json.Unmarshal([]byte(payload), &dataNode); err != nil {
		dataNode = payload
	}

	env := map[string]any{
		"data":   dataNode,
		"config": config,
		"vars":   vars,
	}
	b, _ := json.Marshal(env)
	return string(b)
}

var templateRe = regexp.MustCompile(`\$\{\{\s*([^}]+?)\s*\}\}`)

func resolveTemplate(s string, ctx, envVars map[string]string) string {
	return templateRe.ReplaceAllStringFunc(s, func(match string) string {
		inner := templateRe.FindStringSubmatch(match)[1]
		inner = strings.TrimSpace(inner)

		// env.xxx
		if strings.HasPrefix(inner, "env.") {
			key := inner[4:]
			if v, ok := envVars[key]; ok {
				return v
			}
			return ""
		}

		// trigger.body.xxx
		if strings.HasPrefix(inner, "trigger.body.") {
			key := inner[len("trigger.body."):]
			return gjson.Get(ctx["_payload"], key).String()
		}

		// stepId.output.xxx  or  loop.item.xxx
		dotIdx := strings.Index(inner, ".")
		if dotIdx > 0 {
			prefix := inner[:dotIdx]
			rest := inner[dotIdx+1:]

			// check for step output context keys like "normalize.output"
			contextKey := prefix + "." + strings.Split(rest, ".")[0]
			if val, ok := ctx[contextKey]; ok {
				// further path after "output"
				subPath := strings.Join(strings.Split(rest, ".")[1:], ".")
				if subPath == "" {
					return val
				}
				return gjson.Get(val, subPath).String()
			}

			// direct context key
			if val, ok := ctx[inner]; ok {
				return val
			}
		}

		// plain key in ctx
		if val, ok := ctx[inner]; ok {
			return val
		}
		return match // unresolved, keep as-is
	})
}

// -----------------------------------------------------------------------
// JSONata evaluation
// -----------------------------------------------------------------------

func evalJSONata(expression, payload string) (string, error) {
	e, err := jg.Compile(expression)
	if err != nil {
		return "", fmt.Errorf("compile expression: %w", err)
	}

	var data interface{}
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			// treat as raw string
			data = payload
		}
	}

	result, err := e.Eval(data)
	if err != nil {
		return "", fmt.Errorf("eval: %w", err)
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// -----------------------------------------------------------------------
// DAG entry point detection
// -----------------------------------------------------------------------

func findEntryStep(pipeline *artifact.Pipeline, idx map[string]*artifact.Step) (string, any, error) {
	if len(pipeline.Spec.Steps) == 0 {
		return "", nil, fmt.Errorf("pipeline %q has no steps", pipeline.Metadata.Name)
	}

	// Build set of all steps that are referenced as "next" from some step.
	referenced := make(map[string]bool)
	for _, s := range pipeline.Spec.Steps {
		if s.Next != "" {
			referenced[s.Next] = true
		}
		// also walk branches/cases inside with
		addNextRefs(s.With, referenced)
	}

	// Entry step = not referenced from anywhere.
	for _, s := range pipeline.Spec.Steps {
		if !referenced[s.ID] {
			return s.ID, true, nil
		}
	}

	// Fallback: first step.
	return pipeline.Spec.Steps[0].ID, true, nil
}

func addNextRefs(with map[string]any, refs map[string]bool) {
	if with == nil {
		return
	}
	if branches, ok := with["branches"].(map[string]any); ok {
		for _, v := range branches {
			if s, ok := v.(string); ok {
				refs[s] = true
			}
		}
	}
	if cases, ok := with["cases"].(map[string]any); ok {
		for _, v := range cases {
			if s, ok := v.(string); ok {
				refs[s] = true
			}
		}
	}
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

func rawJSON(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

type loopConfig struct {
	ItemsExpr     string
	ArrayField    string
	Concurrency   int
	BatchSize     int
	AllowFailures bool
}

func loopConfigFromWith(with map[string]any) loopConfig {
	cfg := loopConfig{
		BatchSize:   1,
		Concurrency: 1,
	}
	if with == nil {
		return cfg
	}
	cfg.ItemsExpr, _ = with["items"].(string)
	cfg.ArrayField, _ = with["arrayField"].(string)
	cfg.Concurrency = intFromAny(with["concurrency"], 1)
	cfg.BatchSize = intFromAny(with["batchSize"], 1)
	cfg.AllowFailures, _ = with["allowFailures"].(bool)
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.BatchSize < 1 {
		cfg.BatchSize = 1
	}
	return cfg
}

func intFromAny(v any, fallback int) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return int(i)
		}
	}
	return fallback
}

func extractLoopItems(payload string, cfg loopConfig) ([]any, any, error) {
	var eventData any
	if err := json.Unmarshal([]byte(payload), &eventData); err != nil {
		return nil, nil, fmt.Errorf("loop payload parse: %w", err)
	}

	if cfg.ItemsExpr != "" {
		itemsJSON, err := evalJSONata(cfg.ItemsExpr, payload)
		if err != nil {
			return nil, nil, fmt.Errorf("loop items eval: %w", err)
		}
		items, err := anySliceFromJSON(itemsJSON)
		if err != nil {
			return nil, nil, fmt.Errorf("loop items decode: %w", err)
		}
		return items, eventData, nil
	}

	if cfg.ArrayField != "" {
		items, err := loopItemsFromPath(eventData, cfg.ArrayField)
		if err != nil {
			return nil, nil, fmt.Errorf("loop arrayField %q: %w", cfg.ArrayField, err)
		}
		return items, eventData, nil
	}

	items, err := inferLoopItems(eventData)
	if err != nil {
		return nil, nil, err
	}
	return items, eventData, nil
}

func anySliceFromJSON(data string) ([]any, error) {
	var value any
	if err := json.Unmarshal([]byte(data), &value); err != nil {
		return nil, err
	}
	if items, ok := value.([]any); ok {
		return items, nil
	}
	return []any{value}, nil
}

func loopItemsFromPath(data any, path string) ([]any, error) {
	value, err := valueByDottedPath(data, path)
	if err != nil {
		return nil, err
	}
	if path == "." {
		if items, ok := value.([]any); ok {
			return limitLoopItems(items)
		}
		return []any{value}, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("expected array, got %T", value)
	}
	if len(items) > 1000 {
		return nil, fmt.Errorf("array contains too many items (%d), max allowed is 1000", len(items))
	}
	return items, nil
}

func valueByDottedPath(data any, path string) (any, error) {
	if path == "." {
		return currentLoopValue(data), nil
	}
	current := data
	segments := strings.Split(path, ".")
	for segmentIndex, segment := range segments {
		if segment == "" {
			continue
		}
		if array, ok := current.([]any); ok && len(array) == 1 {
			if _, ok := array[0].(map[string]any); ok {
				current = array[0]
			}
		}
		if array, ok := current.([]any); ok {
			return valuesByDottedPathFromArray(array, strings.Join(segments[segmentIndex:], "."))
		}
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected object at segment %q, got %T", segment, current)
		}
		value, ok := obj[segment]
		if !ok {
			return nil, fmt.Errorf("field %q not found", segment)
		}
		current = value
	}
	return current, nil
}

func valuesByDottedPathFromArray(items []any, path string) ([]any, error) {
	values := make([]any, 0, len(items))
	for i, item := range items {
		value, err := valueByDottedPath(item, path)
		if err != nil {
			return nil, fmt.Errorf("array item %d: %w", i, err)
		}
		if nested, ok := value.([]any); ok {
			values = append(values, nested...)
			continue
		}
		values = append(values, value)
	}
	return values, nil
}

func currentLoopValue(data any) any {
	obj, ok := data.(map[string]any)
	if !ok {
		return data
	}
	if metadata, ok := obj["loopMetadata"].(map[string]any); ok {
		if current, ok := metadata["current"]; ok {
			return current
		}
	}
	if item, ok := obj["item"].(map[string]any); ok {
		if data, ok := item["data"]; ok {
			return data
		}
	}
	return data
}

func inferLoopItems(data any) ([]any, error) {
	if items, ok := data.([]any); ok {
		return limitLoopItems(items)
	}
	for _, path := range []string{
		"items",
		"testItems",
		"originalItem.items",
		"originalEvent.items",
		"item.data.items",
		"loopMetadata.current.items",
	} {
		items, err := loopItemsFromPath(data, path)
		if err == nil {
			return items, nil
		}
	}
	return nil, fmt.Errorf("loop config missing: set with.arrayField or with.items")
}

func limitLoopItems(items []any) ([]any, error) {
	if len(items) > 1000 {
		return nil, fmt.Errorf("array contains too many items (%d), max allowed is 1000", len(items))
	}
	return items, nil
}

func buildLoopItemPayload(parentEvent any, item any, index, batchIndex, batchNumber, totalItems int) (string, error) {
	payload := map[string]any{}
	if eventMap, ok := parentEvent.(map[string]any); ok {
		for k, v := range eventMap {
			payload[k] = v
		}
	}
	if itemMap, ok := item.(map[string]any); ok {
		for k, v := range itemMap {
			payload[k] = v
		}
	}

	itemContext := map[string]any{
		"data":        item,
		"index":       index,
		"batchIndex":  batchIndex,
		"batchNumber": batchNumber,
		"totalItems":  totalItems,
	}
	payload["item"] = itemContext
	payload["loopMetadata"] = map[string]any{
		"index":       index,
		"batchIndex":  batchIndex,
		"batchNumber": batchNumber,
		"totalItems":  totalItems,
		"current":     item,
	}

	out, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func resolveLoopBodySteps(step *artifact.Step, with map[string]any, pipelineDir string) ([]artifact.Step, string, error) {
	if body, ok := with["body"].([]any); ok && len(body) > 0 {
		bodySteps, err := decodeBodySteps(body)
		return bodySteps, step.Next, err
	}

	pipeline, err := artifact.ParsePipeline(pipelineDir)
	if err != nil {
		return nil, "", err
	}

	stepsByID := artifact.StepIndex(pipeline)
	if isDestinationStep(stepsByID[step.Next]) {
		bodyStart := loopBodyStartAfterDestination(pipeline, step.ID, step.Next)
		if bodyStart != "" {
			bodySteps := loopBodyStepsFromNext(step.ID, bodyStart, stepsByID)
			if len(bodySteps) > 0 {
				return bodySteps, step.Next, nil
			}
		}
	}

	bodyIDs := loopBodyIDsFromBackEdges(step.ID, pipeline.Spec.Steps)
	if len(bodyIDs) > 0 {
		bodySteps := make([]artifact.Step, 0, len(bodyIDs))
		for _, id := range bodyIDs {
			if bodyStep, ok := stepsByID[id]; ok {
				bodySteps = append(bodySteps, *bodyStep)
			}
		}
		return bodySteps, step.Next, nil
	}

	if step.Next == "" {
		return nil, "", fmt.Errorf("loop has no body")
	}

	bodySteps := loopBodyStepsFromNext(step.ID, step.Next, stepsByID)
	if len(bodySteps) == 0 {
		return nil, "", fmt.Errorf("loop has no body")
	}
	return bodySteps, loopNextAfterBody(pipeline, step.ID, bodySteps), nil
}

func loopBodyStartAfterDestination(pipeline *artifact.Pipeline, loopID, destinationID string) string {
	loopIndex := -1
	for i, step := range pipeline.Spec.Steps {
		if step.ID == loopID {
			loopIndex = i
			break
		}
	}
	if loopIndex < 0 {
		return ""
	}

	for _, step := range pipeline.Spec.Steps[loopIndex+1:] {
		if step.ID == destinationID || isDestinationStep(&step) {
			continue
		}
		return step.ID
	}
	return ""
}

func loopNextAfterBody(pipeline *artifact.Pipeline, loopID string, bodySteps []artifact.Step) string {
	bodyIDs := make(map[string]bool, len(bodySteps))
	for _, step := range bodySteps {
		bodyIDs[step.ID] = true
	}

	loopIndex := -1
	for i, step := range pipeline.Spec.Steps {
		if step.ID == loopID {
			loopIndex = i
			break
		}
	}
	if loopIndex < 0 {
		return ""
	}

	for _, step := range pipeline.Spec.Steps[loopIndex+1:] {
		if bodyIDs[step.ID] {
			continue
		}
		ns, _, _ := artifact.UsesKind(step.Uses)
		if ns == "destination" {
			return step.ID
		}
	}
	return ""
}

func isDestinationStep(step *artifact.Step) bool {
	if step == nil {
		return false
	}
	ns, _, _ := artifact.UsesKind(step.Uses)
	return ns == "destination"
}

func loopBodyIDsFromBackEdges(loopID string, steps []artifact.Step) []string {
	body := make([]string, 0)
	loopIndex := -1
	for i, candidate := range steps {
		if candidate.ID == loopID {
			loopIndex = i
			break
		}
	}
	for i, candidate := range steps {
		if loopIndex >= 0 && i <= loopIndex {
			continue
		}
		if candidate.Next == loopID {
			body = append(body, candidate.ID)
		}
	}
	return body
}

func loopBodyStepsFromNext(loopID, startID string, stepsByID map[string]*artifact.Step) []artifact.Step {
	visited := map[string]bool{loopID: true}
	var body []artifact.Step
	currentID := startID
	for currentID != "" && !visited[currentID] {
		current, ok := stepsByID[currentID]
		if !ok {
			break
		}
		visited[currentID] = true
		body = append(body, *current)
		if current.ID != loopID && isNativeLoopStep(current) {
			break
		}
		currentID = current.Next
	}
	return body
}

func isNativeLoopStep(step *artifact.Step) bool {
	ns, name, _ := artifact.UsesKind(step.Uses)
	return ns == "native" && name == "loop"
}

// inferBuildResult recreates a Result from a cached artifact directory.
func inferBuildResult(name, srcDir, outDir string) *builder.Result {
	// Check for each possible output
	for _, candidate := range []struct {
		file string
		lang string
		bun  bool
	}{
		{"main.mjs", "javascript", true},
		{"main.js", "javascript", false},
		{"main", "go", false},
		{"main.py", "python", false},
	} {
		path := filepath.Join(outDir, candidate.file)
		if _, err := os.Stat(path); err == nil {
			exec := path
			if candidate.lang == "python" {
				venvPy := filepath.Join(outDir, ".venv", "bin", "python3")
				if _, err := os.Stat(venvPy); err == nil {
					exec = venvPy + " " + path
				}
			}
			result := &builder.Result{
				Name:       name,
				Language:   candidate.lang,
				OutDir:     outDir,
				Executable: exec,
				Config:     functionConfig(srcDir),
				IsBun:      candidate.bun,
			}
			if candidate.lang == "go" && builder.GoUsesUnixSocket(srcDir) {
				result.Protocol = "unix"
			}
			if candidate.lang == "go" && builder.GoUsesExecuteHandler(srcDir) {
				result.RunnerScript = path
			}
			if candidate.lang == "javascript" {
				if builder.JSUsesUnixSocket(srcDir) {
					result.Protocol = "unix"
				} else {
					result.Protocol = "http"
				}
			}
			// Ensure cached JS artifacts have wrappers required by local runs.
			if candidate.lang == "javascript" && !builder.JSUsesDirectServer(srcDir) {
				server, runner, err := builder.EnsureJSWrappers(outDir)
				if err == nil {
					result.ServerScript = server
					result.RunnerScript = runner
				}
			}
			if candidate.lang == "python" {
				server, runner, err := builder.EnsurePythonWrappers(outDir)
				if err == nil {
					result.ServerScript = server
					result.RunnerScript = runner
				}
			}
			return result
		}
	}
	return &builder.Result{Name: name, Language: "javascript", OutDir: outDir, Executable: filepath.Join(outDir, "main.mjs"), Config: functionConfig(srcDir), IsBun: true}
}

func functionConfig(srcDir string) map[string]string {
	fn, err := artifact.ParseFunction(srcDir)
	if err != nil || len(fn.Spec.Config) == 0 {
		return nil
	}
	config := make(map[string]string, len(fn.Spec.Config))
	for key, value := range fn.Spec.Config {
		config[key] = value
	}
	return config
}

// decodeBodySteps converts []any (from YAML) back to []artifact.Step.
func decodeBodySteps(raw []any) ([]artifact.Step, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var steps []artifact.Step
	if err := json.Unmarshal(data, &steps); err != nil {
		return nil, err
	}
	return steps, nil
}
