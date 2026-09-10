package serializer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/platform"
)

// -----------------------------------------------------------------------
// Pipeline serializer
// -----------------------------------------------------------------------

// WritePipeline converts a platform Pipeline into:
//   - pipelines/<slug>/pipeline.yaml
//   - pipelines/<slug>/layout.yaml  (node positions for the visual editor)
//
// txSlugs, srcSlugs, dstSlugs map platform entity UUIDs → local slug names so
// the serializer can build correct `uses:` references.
func WritePipeline(
	root string,
	p *platform.Pipeline,
	txSlugs map[string]string, // nodeID (Transformation ID) → slug
	srcSlugs map[string]string, // nodeID (Source ID) → slug
	dstSlugs map[string]string, // nodeID (Destination ID) → slug
) error {
	return WritePipelineAs(root, p, Slug(p.Name), txSlugs, srcSlugs, dstSlugs, nil, nil, nil, nil, nil, nil)
}

func WritePipelineAs(
	root string,
	p *platform.Pipeline,
	slug string,
	txSlugs map[string]string,
	srcSlugs map[string]string,
	dstSlugs map[string]string,
	txConfig map[string]map[string]any,
	srcConfig map[string]map[string]any,
	dstConfig map[string]map[string]any,
	appSlugs map[string]string,
	appConfig map[string]map[string]any,
	globalVars []platform.GlobalVariable,
) error {
	dir := filepath.Join(root, "pipelines", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir pipelines/%s: %w", slug, err)
	}

	// Build a mapping: nodeUUID → PipelineComponent for all component types.
	nodeByID := make(map[string]*platform.PipelineComponent)
	for i := range p.SourceList {
		c := &p.SourceList[i]
		nodeByID[c.ID] = c
	}
	for i := range p.TransformationList {
		c := &p.TransformationList[i]
		nodeByID[c.ID] = c
	}
	for i := range p.DestinationList {
		c := &p.DestinationList[i]
		nodeByID[c.ID] = c
	}
	for i := range p.ApprovalList {
		c := &p.ApprovalList[i]
		nodeByID[c.ID] = c
	}
	for i := range p.AIConvAgentList {
		c := &p.AIConvAgentList[i]
		nodeByID[c.ID] = c
	}

	// Build edge lookup: nodeID → outgoing edges.
	edgesFrom := make(map[string][]platform.EdgeMap)
	if p.TransformationMap != nil {
		for _, e := range p.TransformationMap.EdgeMap {
			edgesFrom[e.Source] = append(edgesFrom[e.Source], e)
		}
	}

	ordered := bfsOrder(p, edgesFrom)

	// Determine stable step IDs: use Slug(component.Name).
	// We need uniqueness within a pipeline, so suffix duplicates. Assign these
	// in traversal order; ranging over nodeByID would make identical remote
	// pipelines serialize differently between process runs.
	stepIDCount := make(map[string]int)
	stepIDFor := make(map[string]string) // nodeUUID → stepID
	for _, id := range ordered {
		c, ok := nodeByID[id]
		if !ok {
			continue
		}
		base := Slug(c.Name)
		if base == "" {
			base = "step"
		}
		stepIDCount[base]++
		if stepIDCount[base] == 1 {
			stepIDFor[id] = base
		} else {
			stepIDFor[id] = fmt.Sprintf("%s-%d", base, stepIDCount[base])
		}
	}

	// Build the steps list.
	var steps []artifact.Step
	for _, nodeID := range ordered {
		c, ok := nodeByID[nodeID]
		if !ok {
			continue
		}

		step, err := buildStep(c, nodeID, stepIDFor, edgesFrom, txSlugs, srcSlugs, dstSlugs, appSlugs, txConfig, srcConfig, dstConfig, appConfig)
		if err != nil {
			return err
		}
		steps = append(steps, step)
	}

	// Build trigger spec from the first standard (webhook) source, if any.
	var trigger *artifact.TriggerSpec
	for _, src := range p.SourceList {
		if strings.EqualFold(src.Type, "STANDARD") || src.Type == "" {
			trigger = &artifact.TriggerSpec{
				Type:      "http",
				SourceRef: srcSlugs[src.NodeID],
			}
			break
		}
	}

	// Retry config.
	var retry *artifact.RetryConfig
	if r := p.RetrySchedule; r != nil && r.MaxRetries > 0 {
		retry = &artifact.RetryConfig{
			MaxAttempts: r.MaxRetries,
			BackoffSec:  r.AfterSeconds,
		}
	}

	// Cron schedule.
	var schedule string
	if p.Schedule != nil && p.Schedule.CronExpression != "" {
		schedule = p.Schedule.CronExpression
	}

	pipeline := artifact.Pipeline{
		APIVersion: "cnips.io/v1",
		Kind:       "Pipeline",
		Metadata:   artifact.ObjectMeta{Name: slug},
		Spec: artifact.PipelineSpec{
			Description: p.Description,
			Trigger:     trigger,
			Schedule:    schedule,
			Retry:       retry,
			Steps:       steps,
			Variables:   buildVarRefs(p, globalVars),
		},
	}
	if err := artifact.WriteYAML(filepath.Join(dir, "pipeline.yaml"), pipeline); err != nil {
		return err
	}

	// Write layout.yaml (node positions for visual re-import).
	if p.TransformationMap != nil && len(p.TransformationMap.NodePositions) > 0 {
		layout := buildLayout(p.TransformationMap.NodePositions, stepIDFor)
		if err := artifact.WriteYAML(filepath.Join(dir, "layout.yaml"), layout); err != nil {
			return err
		}
	}

	return nil
}

// -----------------------------------------------------------------------
// step builder
// -----------------------------------------------------------------------

// nativeUsesMap maps ScriptType strings to the `uses:` prefix used in YAML.
var nativeUsesMap = map[string]string{
	"DECISION":              "native/decision",
	"SWITCH":                "native/switch",
	"LOOP":                  "native/loop",
	"APPROVAL":              "native/approval",
	"HTTP_REQUEST":          "native/http-request",
	"JSON_FILTER":           "native/json-filter",
	"JSON_SCHEMA_VALIDATOR": "native/json-schema-validator",
	"DATA_MAPPER":           "native/data-mapper",
	"CRYPTO":                "native/crypto",
	"REGEX_EXTRACTOR":       "native/regex-extractor",
	"STOP_ERROR":            "native/stop-error",
	"TEMPLATE":              "native/template",
	"SORT":                  "native/sort",
	"JWT":                   "native/jwt",
	"AGGREGATOR":            "native/aggregator",
	"DEDUPLICATE":           "native/deduplicate",
	"DELAY":                 "native/delay",
	"MARKUP_CONVERTER":      "native/markup-converter",
}

func buildStep(
	c *platform.PipelineComponent,
	nodeID string,
	stepIDFor map[string]string,
	edgesFrom map[string][]platform.EdgeMap,
	txSlugs, srcSlugs, dstSlugs map[string]string,
	appSlugs map[string]string,
	txConfig, srcConfig, dstConfig map[string]map[string]any,
	appConfig map[string]map[string]any,
) (artifact.Step, error) {
	stepID := stepIDFor[nodeID]
	uses := resolveUses(c, txSlugs, srcSlugs, dstSlugs, appSlugs)

	step := artifact.Step{
		ID:   stepID,
		Uses: uses,
	}

	with := stepConfig(stepID, c, txConfig, srcConfig, dstConfig, appConfig)
	if len(with) > 0 {
		step.With = with
	}

	// Outgoing edges → next / branches / cases.
	if outEdges, ok := edgesFrom[nodeID]; ok {
		step = attachEdges(step, c.Type, outEdges, stepIDFor)
	}

	return step, nil
}

func stepConfig(
	stepID string,
	c *platform.PipelineComponent,
	txConfig, srcConfig, dstConfig map[string]map[string]any,
	appConfig map[string]map[string]any,
) map[string]any {
	with := make(map[string]any)
	merge := func(values map[string]any) {
		for k, v := range values {
			with[k] = v
		}
	}

	if c.IsApp {
		merge(appConfig[c.NodeID])
	} else {
		switch strings.ToUpper(c.Type) {
		case "APP":
			merge(appConfig[c.NodeID])
		case "TRANSFORMATION":
			merge(txConfig[c.NodeID])
		case "DESTINATION":
			merge(dstConfig[c.NodeID])
		case "EXTRACTOR", "SOURCE":
			merge(srcConfig[c.NodeID])
		default:
			if config, ok := txConfig[c.NodeID]; ok {
				merge(config)
			}
			if config, ok := srcConfig[c.NodeID]; ok {
				merge(config)
			}
			if config, ok := dstConfig[c.NodeID]; ok {
				merge(config)
			}
			if config, ok := appConfig[c.NodeID]; ok {
				merge(config)
			}
		}
	}

	if native, ok := normalizeConfig(c.NativeConfig); ok {
		merge(native)
	}
	merge(c.Config)

	if len(with) == 0 {
		return nil
	}
	for key, value := range with {
		with[key] = sanitizePulledValue(key, value, "platform://pipeline-step-config/"+slugPart(stepID)+"/"+slugPart(key))
	}
	return with
}

func normalizeConfig(config any) (map[string]any, bool) {
	switch v := config.(type) {
	case nil:
		return nil, false
	case map[string]any:
		return v, len(v) > 0
	case map[string]string:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = val
		}
		return out, len(out) > 0
	default:
		return map[string]any{"config": v}, true
	}
}

func buildVarRefs(p *platform.Pipeline, globalVars []platform.GlobalVariable) []artifact.VarRef {
	var refs []artifact.VarRef
	seen := map[string]bool{}
	for _, gv := range globalVars {
		name := gv.Key
		if name == "" {
			name = gv.ID
		}
		if name == "" || seen[name] {
			continue
		}
		value := gv.Value
		ref := gv.ID
		if pulledSecretField(name) && strings.TrimSpace(value) != "" && !safePulledReference(value) {
			value = ""
			ref = "platform://globalvariables/" + firstNonEmpty(gv.ID, name)
		}
		refs = append(refs, artifact.VarRef{Name: name, Value: value, Ref: ref})
		seen[name] = true
	}
	addIDs := func(ids []string) {
		for _, id := range ids {
			if id == "" || seen[id] {
				continue
			}
			refs = append(refs, artifact.VarRef{Name: id, Ref: id})
			seen[id] = true
		}
	}
	addIDs(p.GlobalVariables)
	addIDs(p.Variables)
	return refs
}

// resolveUses builds the `uses:` string for a step.
func resolveUses(
	c *platform.PipelineComponent,
	txSlugs, srcSlugs, dstSlugs map[string]string,
	appSlugs map[string]string,
) string {
	t := strings.ToUpper(c.Type)

	version := c.UsedVersion
	if version == "" {
		version = "latest"
	}

	if c.IsApp {
		if slug, ok := appSlugs[c.NodeID]; ok {
			return fmt.Sprintf("app/%s@%s", slug, version)
		}
		return fmt.Sprintf("app/%s@%s", Slug(c.Name), version)
	}

	switch t {
	case "TRANSFORMATION", "APPROVAL", "SWITCH", "DECISION":
		namespace := strings.ToLower(t)
		if slug, ok := txSlugs[c.NodeID]; ok {
			return fmt.Sprintf("%s/%s@%s", namespace, slug, version)
		}
		if t != "TRANSFORMATION" {
			if prefix, ok := nativeUsesMap[t]; ok {
				return prefix + "@1"
			}
		}
		return fmt.Sprintf("%s/%s@%s", namespace, Slug(c.Name), version)
	case "DESTINATION":
		if slug, ok := dstSlugs[c.NodeID]; ok {
			return fmt.Sprintf("destination/%s@%s", slug, version)
		}
		return fmt.Sprintf("destination/%s@%s", Slug(c.Name), version)
	case "EXTRACTOR", "SOURCE":
		if slug, ok := srcSlugs[c.NodeID]; ok {
			return fmt.Sprintf("source/%s@%s", slug, version)
		}
		return fmt.Sprintf("source/%s@%s", Slug(c.Name), version)
	case "APP":
		if slug, ok := appSlugs[c.NodeID]; ok {
			return fmt.Sprintf("app/%s@%s", slug, version)
		}
		return fmt.Sprintf("app/%s@%s", Slug(c.Name), version)
	case "AI_CONV_AGENT":
		return "native/ai-agent@1"
	default:
		if prefix, ok := nativeUsesMap[t]; ok {
			return prefix + "@1"
		}
		// Fallback: check if the NodeID is known in any of the slug maps regardless
		// of the Type field (which is sometimes empty or non-standard on pulled data).
		if slug, ok := srcSlugs[c.NodeID]; ok {
			return fmt.Sprintf("source/%s@%s", slug, version)
		}
		if slug, ok := dstSlugs[c.NodeID]; ok {
			return fmt.Sprintf("destination/%s@%s", slug, version)
		}
		if slug, ok := txSlugs[c.NodeID]; ok {
			return fmt.Sprintf("transformation/%s@%s", slug, version)
		}
		if slug, ok := appSlugs[c.NodeID]; ok {
			return fmt.Sprintf("app/%s@%s", slug, version)
		}
		return fmt.Sprintf("%s@%s", Slug(c.Name), version)
	}
}

// attachEdges adds next/branches/cases to a step based on outgoing EdgeMaps.
// compType is the platform ScriptType string (TRANSFORMATION, DECISION, SWITCH, …).
func attachEdges(step artifact.Step, compType string, edges []platform.EdgeMap, stepIDFor map[string]string) artifact.Step {
	if len(edges) == 0 {
		return step
	}

	t := strings.ToUpper(compType)

	// SWITCH: every labelled outgoing edge is a case value, even when there is
	// only one edge.
	if t == "SWITCH" {
		cases := make(map[string]string)
		for _, e := range edges {
			key, ok := edgeLabelString(e.Label)
			if ok {
				cases[key] = stepIDFor[e.Target]
			}
		}
		if len(cases) > 0 {
			step.Cases = cases
			return step
		}
		step.Next = stepIDFor[edges[0].Target]
		return step
	}

	// Single outgoing edge: ALWAYS use `next`, regardless of the edge label.
	// Platform edge labels on non-decision nodes (e.g. source→first-tx) are
	// often `true` from the graph library but do not imply a decision branch.
	if len(edges) == 1 {
		step.Next = stepIDFor[edges[0].Target]
		return step
	}

	// DECISION: two bool-labelled edges → branches { true: …, false: … }.
	if t == "DECISION" {
		branches := make(map[string]string)
		for _, e := range edges {
			var key string
			switch v := e.Label.(type) {
			case bool:
				if v {
					key = "true"
				} else {
					key = "false"
				}
			default:
				key = fmt.Sprintf("%v", v)
			}
			branches[key] = stepIDFor[e.Target]
		}
		step.Branches = branches
		return step
	}

	// Everything else with multiple outgoing edges: use first as next.
	step.Next = stepIDFor[edges[0].Target]
	return step
}

func edgeLabelString(label any) (string, bool) {
	switch v := label.(type) {
	case nil:
		return "", false
	case string:
		v = strings.TrimSpace(v)
		return v, v != ""
	default:
		return fmt.Sprintf("%v", v), true
	}
}

// -----------------------------------------------------------------------
// BFS ordering
// -----------------------------------------------------------------------

// bfsOrder returns node IDs in breadth-first execution order, starting from
// source nodes (those with no incoming edges from non-root edges).
func bfsOrder(p *platform.Pipeline, edgesFrom map[string][]platform.EdgeMap) []string {
	// Collect all node IDs.
	allIDs := make(map[string]bool)
	for _, c := range p.SourceList {
		allIDs[c.ID] = true
	}
	for _, c := range p.TransformationList {
		allIDs[c.ID] = true
	}
	for _, c := range p.DestinationList {
		allIDs[c.ID] = true
	}
	for _, c := range p.ApprovalList {
		allIDs[c.ID] = true
	}
	for _, c := range p.AIConvAgentList {
		allIDs[c.ID] = true
	}

	// Determine which nodes have incoming edges.
	hasIncoming := make(map[string]bool)
	if p.TransformationMap != nil {
		for _, e := range p.TransformationMap.EdgeMap {
			hasIncoming[e.Target] = true
		}
	}

	// Roots = all nodes with no incoming edges.
	var roots []string
	// Prefer sources as entry points.
	for _, c := range p.SourceList {
		if !hasIncoming[c.ID] {
			roots = append(roots, c.ID)
		}
	}
	// Then other rootless nodes (transformations that act as entry points).
	var otherRoots []string
	for id := range allIDs {
		if !hasIncoming[id] {
			isSource := false
			for _, c := range p.SourceList {
				if c.ID == id {
					isSource = true
					break
				}
			}
			if !isSource {
				otherRoots = append(otherRoots, id)
			}
		}
	}
	sort.Strings(otherRoots)
	roots = append(roots, otherRoots...)
	// Fallback: if every node has an incoming edge, just use all nodes.
	if len(roots) == 0 {
		for id := range allIDs {
			roots = append(roots, id)
		}
		sort.Strings(roots)
	}

	// BFS.
	visited := make(map[string]bool)
	queue := append([]string{}, roots...)
	var ordered []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		visited[cur] = true
		ordered = append(ordered, cur)
		for _, e := range edgesFrom[cur] {
			if !visited[e.Target] {
				queue = append(queue, e.Target)
			}
		}
	}
	// Append any unreachable nodes (disconnected graph).
	var missing []string
	for id := range allIDs {
		if !visited[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		ordered = append(ordered, missing...)
	}
	return ordered
}

// -----------------------------------------------------------------------
// Layout
// -----------------------------------------------------------------------

type layoutFile struct {
	APIVersion string               `yaml:"apiVersion"`
	Kind       string               `yaml:"kind"`
	Positions  map[string]layoutPos `yaml:"positions,omitempty"`
}

type layoutPos struct {
	X float64 `yaml:"x"`
	Y float64 `yaml:"y"`
}

func buildLayout(positions map[string]platform.Position, stepIDFor map[string]string) layoutFile {
	out := layoutFile{
		APIVersion: "cnips.io/v1",
		Kind:       "PipelineLayout",
		Positions:  make(map[string]layoutPos),
	}
	for nodeID, pos := range positions {
		if sid, ok := stepIDFor[nodeID]; ok {
			out.Positions[sid] = layoutPos{X: pos.X, Y: pos.Y}
		}
	}
	return out
}
