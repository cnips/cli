package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cnips/cli/internal/artifact"
)

func TestInferBuildResultEnsuresJSWrappersForCachedArtifact(t *testing.T) {
	outDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outDir, "main.mjs"), []byte("export async function execute() { return {}; }\n"), 0o644); err != nil {
		t.Fatalf("write cached main.mjs: %v", err)
	}

	result := inferBuildResult("general-src", t.TempDir(), outDir)

	if result.Language != "javascript" {
		t.Fatalf("Language = %q, want javascript", result.Language)
	}
	if result.RunnerScript == "" {
		t.Fatal("RunnerScript is empty")
	}
	if result.ServerScript == "" {
		t.Fatal("ServerScript is empty")
	}
	if _, err := os.Stat(result.RunnerScript); err != nil {
		t.Fatalf("runner script was not created: %v", err)
	}
	if _, err := os.Stat(result.ServerScript); err != nil {
		t.Fatalf("server script was not created: %v", err)
	}
}

func TestRunComponentRejectsMissingTarget(t *testing.T) {
	_, err := RunComponent(ComponentRunOptions{Root: t.TempDir(), Kind: "transformation", Name: "missing", Payload: `{}`})
	if err == nil || !strings.Contains(err.Error(), "not found locally") {
		t.Fatalf("error = %v", err)
	}
}

func TestProcessorNativeDataMapper(t *testing.T) {
	step := &artifact.Step{
		ID:   "mapper",
		Uses: "native/data-mapper@1",
		With: map[string]any{
			"mappings": []map[string]any{
				{"source": "$.order.id", "target": "orderId"},
				{"source": "$.missing", "target": "status", "default": "new"},
			},
		},
	}

	out, next, _, err := executeNative(step, "data-mapper", `{"order":{"id":"ord-123"}}`, nil, nil, "", RunOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("executeNative data-mapper: %v", err)
	}
	if next != "" {
		t.Fatalf("next = %q, want empty", next)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if got["orderId"] != "ord-123" || got["status"] != "new" {
		t.Fatalf("unexpected mapped output: %#v", got)
	}
}

func TestProcessorNativeJSONFilterRoutesBranches(t *testing.T) {
	step := &artifact.Step{
		ID:   "filter",
		Uses: "native/json-filter@1",
		With: map[string]any{
			"expression": "amount >= 100",
		},
		Branches: map[string]string{
			"true":  "paid",
			"false": "review",
		},
	}

	out, next, _, err := executeNative(step, "json-filter", `{"amount":125}`, nil, nil, "", RunOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("executeNative json-filter: %v", err)
	}
	if next != "" {
		t.Fatalf("json-filter next = %q, want default empty because JSON_FILTER does not set decision", next)
	}
	if out != "true" {
		t.Fatalf("json-filter output = %s, want true", out)
	}

	step.Uses = "native/regex-extractor@1"
	step.With = map[string]any{
		"source_field": "$.email",
		"pattern":      `@example\.com$`,
		"mode":         "match_test",
	}
	out, next, _, err = executeNative(step, "regex-extractor", `{"email":"ada@example.com"}`, nil, nil, "", RunOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("executeNative regex-extractor: %v", err)
	}
	if next != "paid" {
		t.Fatalf("regex-extractor next = %q, want paid", next)
	}
	if out == "" {
		t.Fatal("regex-extractor output is empty")
	}
}

func TestProcessorNativeJSONFilterSerializesFilteredObjects(t *testing.T) {
	step := &artifact.Step{
		ID:   "json-filter",
		Uses: "native/json-filter@1",
		With: map[string]any{
			"expression": "items.filter(item, item.quantity > 1)",
		},
	}
	payload := `{"items":[{"sku":"A-1","quantity":2},{"sku":"B-2","quantity":1},{"sku":"A-1","quantity":3}]}`

	out, _, _, err := executeNative(step, "json-filter", payload, nil, nil, "", RunOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("executeNative json-filter object array: %v", err)
	}
	var got map[string][]map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal output: %v\noutput: %s", err, out)
	}
	items := got["items"]
	if len(items) != 2 {
		t.Fatalf("len(output.items) = %d, want 2: %#v", len(items), got)
	}
	if items[0]["sku"] != "A-1" || items[1]["quantity"] != float64(3) {
		t.Fatalf("unexpected filtered output: %#v", got)
	}
}

func TestProcessorNativeStopErrorEvaluatesCELCondition(t *testing.T) {
	step := &artifact.Step{
		ID:   "stop",
		Uses: "native/stop-error@1",
		With: map[string]any{
			"condition": "order.amount <= 0",
			"message":   "Invalid order {{ order.id }}: status={{ order.status }}, amount={{ order.amount }}",
		},
		Next: "next-step",
	}
	payload := `[{
		"customer": {
			"id": "cust-1001",
			"name": "Ada Lovelace",
			"tier": "gold",
			"email": "ada@example.com"
		},
		"order": {
			"id": "ord-9001",
			"amount": 125.5,
			"status": "paid",
			"description": "Invoice INV-2026-00042 for customer Ada Lovelace",
			"markdown": "# Order ord-9001\n\nPaid order for **Ada Lovelace**."
		},
		"items": [
			{ "sku": "A-1", "category": "book", "quantity": 2, "price": 20, "updatedAt": "2026-06-16T08:00:00Z" },
			{ "sku": "B-2", "category": "tool", "quantity": 1, "price": 85.5, "updatedAt": "2026-06-16T08:05:00Z" },
			{ "sku": "A-1", "category": "book", "quantity": 3, "price": 20, "updatedAt": "2026-06-16T08:10:00Z" }
		]
	}]`

	out, next, _, err := executeNative(step, "stop-error", payload, nil, nil, "", RunOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("executeNative stop-error false condition: %v", err)
	}
	if next != "next-step" {
		t.Fatalf("next = %q, want next-step", next)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	order := got["order"].(map[string]any)
	if order["id"] != "ord-9001" {
		t.Fatalf("order.id = %#v, want ord-9001", order["id"])
	}

	failingPayload := `{"order":{"id":"ord-9002","amount":0,"status":"failed"}}`
	_, _, _, err = executeNative(step, "stop-error", failingPayload, nil, nil, "", RunOptions{}, nil, nil)
	if err == nil {
		t.Fatal("executeNative stop-error true condition succeeded, want error")
	}
	want := "Invalid order ord-9002: status=failed, amount=0"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestProcessorNativeStopErrorDeclaresDynamicInputFields(t *testing.T) {
	step := &artifact.Step{
		ID:   "stop",
		Uses: "native/stop-error@1",
		With: map[string]any{
			"condition": "order.amount <= 0",
			"message":   "bad order",
		},
		Next: "next-step",
	}

	out, next, _, err := executeNative(step, "stop-error", `{"order":{"amount":125.5}}`, nil, nil, "", RunOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("executeNative stop-error dynamic fields: %v", err)
	}
	if next != "next-step" {
		t.Fatalf("next = %q, want next-step", next)
	}
	if out == "" {
		t.Fatal("output is empty")
	}
}

func TestExecuteSourceMissingLocalComponentReturnsClearError(t *testing.T) {
	_, err := executeSource(
		&artifact.Step{ID: "missing-source", Uses: "source/missing-source@latest"},
		"missing-source",
		"source",
		"latest",
		"{}",
		map[string]string{},
		nil,
		RunOptions{Root: t.TempDir()},
		nil,
	)
	if err == nil {
		t.Fatal("executeSource succeeded, want missing source error")
	}
	if !strings.Contains(err.Error(), `source "missing-source" version "latest" not found locally`) {
		t.Fatalf("error = %q, want missing source message", err.Error())
	}
}

func TestNativeApprovalAutoApprove(t *testing.T) {
	step := &artifact.Step{ID: "review", Uses: "native/approval@1", Next: "ship"}
	payload := `{"approved":true}`
	var logs bytes.Buffer

	out, next, _, err := nativeApproval(step, nil, payload, RunOptions{
		AutoApprove: true,
		LogWriter:   &logs,
	})
	if err != nil {
		t.Fatalf("nativeApproval auto approve: %v", err)
	}
	if out != payload {
		t.Fatalf("output = %s, want original payload", out)
	}
	if next != "ship" {
		t.Fatalf("next = %q, want ship", next)
	}
	if !strings.Contains(logs.String(), "auto-approved") {
		t.Fatalf("logs = %q, want auto-approved entry", logs.String())
	}
}

func TestNativeApprovalAutoReject(t *testing.T) {
	step := &artifact.Step{ID: "review", Uses: "native/approval@1", Next: "ship"}
	var logs bytes.Buffer

	_, _, _, err := nativeApproval(step, nil, `{}`, RunOptions{
		AutoReject: true,
		LogWriter:  &logs,
	})
	if err == nil {
		t.Fatal("nativeApproval auto reject succeeded, want error")
	}
	if !strings.Contains(err.Error(), "--auto-reject") {
		t.Fatalf("error = %q, want --auto-reject", err.Error())
	}
	if !strings.Contains(logs.String(), "auto-rejected") {
		t.Fatalf("logs = %q, want auto-rejected entry", logs.String())
	}
}

func TestNativeApprovalAutoApproveRejectConflict(t *testing.T) {
	step := &artifact.Step{ID: "review", Uses: "native/approval@1"}

	_, _, _, err := nativeApproval(step, nil, `{}`, RunOptions{
		AutoApprove: true,
		AutoReject:  true,
	})
	if err == nil {
		t.Fatal("nativeApproval auto approve/reject conflict succeeded, want error")
	}
	if !strings.Contains(err.Error(), "cannot be both auto-approved and auto-rejected") {
		t.Fatalf("error = %q, want conflict message", err.Error())
	}
}

func TestProcessorNativeCryptoHash(t *testing.T) {
	step := &artifact.Step{
		ID:   "crypto",
		Uses: "native/crypto@1",
		With: map[string]any{
			"operation":    "hash",
			"algorithm":    "sha256",
			"source_field": "$.text",
			"output_field": "digest",
			"encoding":     "hex",
		},
	}

	out, _, _, err := executeNative(step, "crypto", `{"text":"hello"}`, nil, nil, "", RunOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("executeNative crypto: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if got["digest"] != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("unexpected digest: %#v", got["digest"])
	}
}

func TestResolveLocalSecretUsesDerivedPlatformReferenceAlias(t *testing.T) {
	secret, err := resolveLocalSecret("", "platform://pipeline-step-config/jwt/secret-access-id", map[string]string{
		"JWT_SECRET_ACCESS_ID": "local-jwt-secret",
	})
	if err != nil {
		t.Fatalf("resolveLocalSecret: %v", err)
	}
	if secret != "local-jwt-secret" {
		t.Fatalf("secret = %q, want local-jwt-secret", secret)
	}
}

func TestResolveLocalSecretUsesPlaceholderForPulledPlatformReference(t *testing.T) {
	secret, err := resolveLocalSecret("", "platform://pipeline-step-config/jwt/secret-access-id", nil)
	if err != nil {
		t.Fatalf("resolveLocalSecret: %v", err)
	}
	if secret != localPulledSecretPlaceholder {
		t.Fatalf("secret = %q, want %q", secret, localPulledSecretPlaceholder)
	}
}

func TestExtractLoopItemsInfersTopLevelArrayWithoutExpression(t *testing.T) {
	items, parent, err := extractLoopItems(`[{"id":"one"},{"id":"two"}]`, loopConfig{BatchSize: 1})
	if err != nil {
		t.Fatalf("extractLoopItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if _, ok := parent.([]any); !ok {
		t.Fatalf("parent = %T, want []any", parent)
	}

	payload, err := buildLoopItemPayload(parent, items[0], 0, 0, 0, len(items))
	if err != nil {
		t.Fatalf("buildLoopItemPayload: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("unmarshal loop item payload: %v", err)
	}
	item, ok := got["item"].(map[string]any)
	if !ok {
		t.Fatalf("item context missing from payload: %#v", got)
	}
	data, ok := item["data"].(map[string]any)
	if !ok || data["id"] != "one" {
		t.Fatalf("item.data = %#v, want first loop item", item["data"])
	}
}

func TestExtractLoopItemsUsesArrayFieldInsideSingleExtractorEvent(t *testing.T) {
	items, parent, err := extractLoopItems(
		`[{"grid":[[{"name":"Alice"}],[{"name":"Bob"}]]}]`,
		loopConfig{ArrayField: "grid", BatchSize: 1},
	)
	if err != nil {
		t.Fatalf("extractLoopItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	firstRow, ok := items[0].([]any)
	if !ok || len(firstRow) != 1 {
		t.Fatalf("items[0] = %#v, want first grid row", items[0])
	}
	firstPerson, ok := firstRow[0].(map[string]any)
	if !ok || firstPerson["name"] != "Alice" {
		t.Fatalf("first row item = %#v, want Alice", firstRow[0])
	}
	if _, ok := parent.([]any); !ok {
		t.Fatalf("parent = %T, want []any", parent)
	}
}

func TestExtractLoopItemsUsesArrayFieldAcrossExtractorEvents(t *testing.T) {
	items, parent, err := extractLoopItems(
		`[{"sub":"one","items":[{"id":"one-1"},{"id":"one-2"}]},{"sub":"two","items":[{"id":"two-1"},{"id":"two-2"}]}]`,
		loopConfig{ArrayField: "items", BatchSize: 1},
	)
	if err != nil {
		t.Fatalf("extractLoopItems: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("len(items) = %d, want 4", len(items))
	}
	first, ok := items[0].(map[string]any)
	if !ok || first["id"] != "one-1" {
		t.Fatalf("items[0] = %#v, want first item from first extractor event", items[0])
	}
	last, ok := items[3].(map[string]any)
	if !ok || last["id"] != "two-2" {
		t.Fatalf("items[3] = %#v, want last item from second extractor event", items[3])
	}
	if _, ok := parent.([]any); !ok {
		t.Fatalf("parent = %T, want []any", parent)
	}
}

func TestExtractLoopItemsDotUsesCurrentLoopObjectAsSingleItem(t *testing.T) {
	items, parent, err := extractLoopItems(
		`{"sku":"A-1","loopMetadata":{"current":{"sku":"A-1","quantity":2}}}`,
		loopConfig{ArrayField: ".", BatchSize: 1},
	)
	if err != nil {
		t.Fatalf("extractLoopItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	item, ok := items[0].(map[string]any)
	if !ok || item["sku"] != "A-1" {
		t.Fatalf("items[0] = %#v, want current loop object", items[0])
	}
	if _, ok := parent.(map[string]any); !ok {
		t.Fatalf("parent = %T, want map[string]any", parent)
	}
}

func TestNativeLoopRecordsBodyStepTraces(t *testing.T) {
	step := &artifact.Step{
		ID:   "loop",
		Uses: "native/loop@1",
		With: map[string]any{
			"items": "items",
			"body": []any{
				map[string]any{
					"id":   "double",
					"uses": "native/transformation@1",
					"with": map[string]any{
						"expression": "item.data.value * 2",
					},
				},
			},
		},
	}
	trace := &StepTrace{ID: step.ID, Uses: step.Uses}

	out, next, _, err := executeNative(step, "loop", `{"items":[{"value":2},{"value":4}]}`, map[string]string{}, nil, "", RunOptions{}, nil, trace)
	if err != nil {
		t.Fatalf("executeNative loop: %v", err)
	}
	if next != "" {
		t.Fatalf("next = %q, want empty", next)
	}
	if out != `[4,8]` {
		t.Fatalf("output = %s, want [4,8]", out)
	}
	if len(trace.Children) != 2 {
		t.Fatalf("len(trace.Children) = %d, want 2", len(trace.Children))
	}
	for i, child := range trace.Children {
		if child.ID != "double" {
			t.Fatalf("child[%d].ID = %q, want double", i, child.ID)
		}
		if child.ParentID != "loop" {
			t.Fatalf("child[%d].ParentID = %q, want loop", i, child.ParentID)
		}
		if child.LoopIndex == nil || *child.LoopIndex != i {
			t.Fatalf("child[%d].LoopIndex = %#v, want %d", i, child.LoopIndex, i)
		}
		if child.Status != "ok" {
			t.Fatalf("child[%d].Status = %q, want ok", i, child.Status)
		}
	}
}

func TestNativeLoopFlattensNestedLoopBodyResults(t *testing.T) {
	step := &artifact.Step{
		ID:   "outer-loop",
		Uses: "native/loop@1",
		With: map[string]any{
			"items": "items",
			"body": []any{
				map[string]any{
					"id":   "inner-loop",
					"uses": "native/loop@1",
					"with": map[string]any{
						"items": "childItems",
						"body": []any{
							map[string]any{
								"id":   "child-value",
								"uses": "native/transformation@1",
								"with": map[string]any{
									"expression": `{"parentId": parentId, "childId": childId}`,
								},
							},
						},
					},
				},
			},
		},
	}

	payload := `{"items":[{"id":"p1","childItems":[{"parentId":"p1","childId":"c1"},{"parentId":"p1","childId":"c2"}]},{"id":"p2","childItems":[{"parentId":"p2","childId":"c3"}]}]}`
	out, _, _, err := executeNative(step, "loop", payload, map[string]string{}, nil, "", RunOptions{}, nil, nil)
	if err != nil {
		t.Fatalf("executeNative nested loop: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal output: %v\noutput: %s", err, out)
	}
	if len(got) != 3 {
		t.Fatalf("len(output) = %d, want 3: %#v", len(got), got)
	}
	if got[0]["childId"] != "c1" || got[2]["parentId"] != "p2" {
		t.Fatalf("unexpected flattened output: %#v", got)
	}
}

func TestLoopBodyStepsStopsAfterNestedLoop(t *testing.T) {
	stepsByID := map[string]*artifact.Step{
		"loop-trans1": {
			ID:   "loop-trans1",
			Uses: "transformation/loop-trans1@latest",
			Next: "loop-2",
		},
		"loop-2": {
			ID:   "loop-2",
			Uses: "native/loop@1",
			Next: "loop-trans2",
		},
		"loop-trans2": {
			ID:   "loop-trans2",
			Uses: "transformation/loop-trans2@latest",
			Next: "loop-2",
		},
	}

	body := loopBodyStepsFromNext("loop", "loop-trans1", stepsByID)
	if len(body) != 2 {
		t.Fatalf("len(body) = %d, want 2", len(body))
	}
	if body[0].ID != "loop-trans1" || body[1].ID != "loop-2" {
		t.Fatalf("body IDs = [%s %s], want [loop-trans1 loop-2]", body[0].ID, body[1].ID)
	}
}

func TestLoopNextAfterBodyUsesDestinationOutsideLoopBody(t *testing.T) {
	pipeline := &artifact.Pipeline{
		Spec: artifact.PipelineSpec{
			Steps: []artifact.Step{
				{ID: "src-loop-test", Uses: "source/src-loop-test@latest", Next: "loop"},
				{ID: "loop", Uses: "native/loop@1", Next: "loop-trans1"},
				{ID: "loop-trans1", Uses: "transformation/loop-trans1@latest", Next: "loop-2"},
				{ID: "loop-dest", Uses: "destination/loop-dest@latest"},
				{ID: "loop-2", Uses: "native/loop@1", Next: "loop-trans2"},
				{ID: "loop-trans2", Uses: "transformation/loop-trans2@latest", Next: "loop-2"},
			},
		},
	}
	bodySteps := []artifact.Step{
		{ID: "loop-trans1", Uses: "transformation/loop-trans1@latest"},
		{ID: "loop-2", Uses: "native/loop@1"},
	}

	next := loopNextAfterBody(pipeline, "loop", bodySteps)
	if next != "loop-dest" {
		t.Fatalf("next = %q, want loop-dest", next)
	}
}

func TestLoopBodyStartAfterDestinationUsesFollowingNestedLoopBlock(t *testing.T) {
	pipeline := &artifact.Pipeline{
		Spec: artifact.PipelineSpec{
			Steps: []artifact.Step{
				{ID: "nested-array-loop-test-src", Uses: "source/nested-array-loop-test-src@latest", Next: "loop"},
				{ID: "loop", Uses: "native/loop@1", Next: "nested-array-loop-test-destination"},
				{ID: "nested-array-loop-test-destination", Uses: "destination/nested-array-loop-test-destination@latest"},
				{ID: "loop-2", Uses: "native/loop@1", Next: "nested-array-loop-test-transformation"},
				{ID: "nested-array-loop-test-transformation", Uses: "transformation/nested-array-loop-test-transformation@latest", Next: "loop-2"},
			},
		},
	}

	bodyStart := loopBodyStartAfterDestination(pipeline, "loop", "nested-array-loop-test-destination")
	if bodyStart != "loop-2" {
		t.Fatalf("bodyStart = %q, want loop-2", bodyStart)
	}
}

func TestResolveLoopBodyStepsPrefersDestinationLayoutOverBackEdge(t *testing.T) {
	pipelineDir := t.TempDir()
	pipelineYAML := []byte(`apiVersion: cnips.io/v1
kind: Pipeline
metadata:
  name: nested-loop-1
spec:
  steps:
    - id: src-loop-test
      uses: source/src-loop-test@latest
      next: loop
    - id: loop
      uses: native/loop@1
      next: loop-dest
    - id: loop-dest
      uses: destination/loop-dest@latest
    - id: loop-trans1
      uses: transformation/loop-trans1@latest
      next: loop-2
    - id: loop-2
      uses: native/loop@1
      next: loop
    - id: loop-trans2
      uses: transformation/loop-trans2@latest
      next: loop-2
`)
	if err := os.WriteFile(filepath.Join(pipelineDir, "pipeline.yaml"), pipelineYAML, 0o644); err != nil {
		t.Fatalf("write pipeline.yaml: %v", err)
	}

	step := &artifact.Step{ID: "loop", Uses: "native/loop@1", Next: "loop-dest"}
	body, next, err := resolveLoopBodySteps(step, nil, pipelineDir)
	if err != nil {
		t.Fatalf("resolveLoopBodySteps: %v", err)
	}
	if next != "loop-dest" {
		t.Fatalf("next = %q, want loop-dest", next)
	}
	if len(body) != 2 {
		t.Fatalf("len(body) = %d, want 2", len(body))
	}
	if body[0].ID != "loop-trans1" || body[1].ID != "loop-2" {
		t.Fatalf("body IDs = [%s %s], want [loop-trans1 loop-2]", body[0].ID, body[1].ID)
	}
}
