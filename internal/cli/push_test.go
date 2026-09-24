package cli

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/platform"
)

func TestBuildPushPlanMarksCreateAndUpdate(t *testing.T) {
	bundle := &localBundle{
		Transformations: []localComponent{{Slug: "normalize"}},
		Functions:       []localFunction{{Slug: "enrich"}},
	}
	state := &workspaceState{
		Transformations: map[string]platform.Transformation{"normalize": {ID: "tx-1", Name: "Normalize"}},
		Sources:         map[string]platform.Source{},
		Destinations:    map[string]platform.Destination{},
		Functions:       map[string]platform.Function{},
		GlobalVariables: map[string]platform.GlobalVariable{},
		Configurations:  map[string]platform.Configuration{},
		Pipelines:       map[string]platform.Pipeline{},
	}

	plan := buildPushPlan(bundle, state)
	if len(plan.Items) != 2 {
		t.Fatalf("expected 2 plan items, got %d", len(plan.Items))
	}
	if plan.Items[0].Action != "update" || plan.Items[0].ID != "tx-1" {
		t.Fatalf("expected transformation update, got %#v", plan.Items[0])
	}
	if plan.Items[1].Action != "create" {
		t.Fatalf("expected function create, got %#v", plan.Items[1])
	}
}

func TestBuildPushPlanMatchesComponentByMetadataID(t *testing.T) {
	bundle := &localBundle{
		Transformations: []localComponent{{
			Slug: "order-mapper-v2",
			Artifact: artifact.Component{
				Metadata: artifact.ObjectMeta{
					ID:   "tx-special",
					Name: "Order Mapper @ v2!",
				},
			},
		}},
	}
	state := &workspaceState{
		Transformations: map[string]platform.Transformation{
			"tx-special": {ID: "tx-special", Name: "Order Mapper @ v2!"},
		},
		Sources:         map[string]platform.Source{},
		Destinations:    map[string]platform.Destination{},
		Functions:       map[string]platform.Function{},
		GlobalVariables: map[string]platform.GlobalVariable{},
		Configurations:  map[string]platform.Configuration{},
		Pipelines:       map[string]platform.Pipeline{},
	}

	plan := buildPushPlan(bundle, state)
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}
	if plan.Items[0].Action != "update" || plan.Items[0].ID != "tx-special" {
		t.Fatalf("expected update by metadata ID, got %#v", plan.Items[0])
	}
	if plan.Items[0].Name != "Order Mapper @ v2!" {
		t.Fatalf("plan name=%q, want original platform name", plan.Items[0].Name)
	}
}

func TestBuildPushPlanMatchesPipelineByMetadataID(t *testing.T) {
	bundle := &localBundle{
		Pipelines: []artifact.Pipeline{{
			Metadata: artifact.ObjectMeta{
				ID:   "pl-special",
				Name: "order-sync-renamed",
			},
		}},
	}
	state := &workspaceState{
		Transformations: map[string]platform.Transformation{},
		Sources:         map[string]platform.Source{},
		Destinations:    map[string]platform.Destination{},
		Functions:       map[string]platform.Function{},
		GlobalVariables: map[string]platform.GlobalVariable{},
		Configurations:  map[string]platform.Configuration{},
		Pipelines: map[string]platform.Pipeline{
			"pl-special": {ID: "pl-special", Name: "Order Sync"},
		},
	}

	plan := buildPushPlan(bundle, state)
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}
	if plan.Items[0].Action != "update" || plan.Items[0].ID != "pl-special" {
		t.Fatalf("expected update by metadata ID, got %#v", plan.Items[0])
	}
}

func TestBuildPushPlanSkipsAIConversationalAgentPipeline(t *testing.T) {
	bundle := &localBundle{
		Pipelines: []artifact.Pipeline{{
			Metadata: artifact.ObjectMeta{Name: "cnips-agent-gitlab"},
			Spec: artifact.PipelineSpec{Steps: []artifact.Step{
				{ID: "ai-conversational-agent", Uses: "ai-conversational-agent@latest"},
			}},
		}},
	}
	state := &workspaceState{
		Transformations: map[string]platform.Transformation{},
		Sources:         map[string]platform.Source{},
		Destinations:    map[string]platform.Destination{},
		Functions:       map[string]platform.Function{},
		GlobalVariables: map[string]platform.GlobalVariable{},
		Configurations:  map[string]platform.Configuration{},
		Pipelines: map[string]platform.Pipeline{
			"cnips-agent-gitlab": {ID: "pl-ai-agent", Name: "cnips-agent-gitlab"},
		},
	}

	plan := buildPushPlan(bundle, state)
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(plan.Items))
	}
	if plan.Items[0].Action != "skip" || plan.Items[0].ID != "pl-ai-agent" {
		t.Fatalf("expected AI conversational agent pipeline skip, got %#v", plan.Items[0])
	}
}

func TestIndexRenameAliasesMatchesRenamedPipelineByOldName(t *testing.T) {
	root := t.TempDir()
	stateFile := &renameState{Entries: []renameRecord{{
		Kind:      "pipeline",
		OldPrefix: "pipelines/orders",
		NewPrefix: "pipelines/order-sync",
	}}}
	if err := writeRenameState(root, stateFile); err != nil {
		t.Fatalf("writeRenameState: %v", err)
	}
	state := &workspaceState{
		Pipelines: map[string]platform.Pipeline{
			"orders": {ID: "pl-1", Name: "orders"},
		},
	}
	if err := indexRenameAliases(root, state); err != nil {
		t.Fatalf("indexRenameAliases: %v", err)
	}
	bundle := &localBundle{
		Pipelines: []artifact.Pipeline{{Metadata: artifact.ObjectMeta{Name: "order-sync"}}},
	}
	plan := buildPushPlan(bundle, state)
	if len(plan.Items) != 1 || plan.Items[0].Action != "update" || plan.Items[0].ID != "pl-1" {
		t.Fatalf("expected renamed pipeline update, got %#v", plan.Items)
	}
}

func TestComponentToTransformationUsesOriginalName(t *testing.T) {
	item := localComponent{
		Slug: "order-mapper-v2",
		Artifact: artifact.Component{
			Metadata: artifact.ObjectMeta{
				ID:   "tx-special",
				Name: "Order Mapper @ v2!",
			},
		},
	}

	out := componentToTransformation(item, "workspace-1")
	if out.Name != "Order Mapper @ v2!" {
		t.Fatalf("Name=%q, want original platform name", out.Name)
	}
}

func TestPlatformLanguageUsesDataModelEnumValues(t *testing.T) {
	cases := map[string]string{
		"js":       "javascript",
		"nodejs22": "javascript",
		"go":       "golang",
		"golang":   "golang",
		"python3":  "python",
	}
	for in, want := range cases {
		if got := platformLanguage(in); got != want {
			t.Fatalf("platformLanguage(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestSourceCodePayloadMarksServerBuildPending(t *testing.T) {
	payload := sourceCodePayload(platform.SourceCode{Script: "export default 1"}, componentBuildStatus("TRANSFORMATION", "v3", "javascript"))
	if payload.BuildStatus != "pending" {
		t.Fatalf("BuildStatus=%q, want pending", payload.BuildStatus)
	}
}

func TestComponentToTransformationOmitsPendingBuildForManifestOnlyChange(t *testing.T) {
	item := localComponent{
		Slug: "normalize",
		Artifact: artifact.Component{
			Spec: artifact.ComponentSpec{
				Type:             "TRANSFORMATION",
				Language:         "javascript",
				SignatureVersion: "v3",
			},
		},
		Source: platform.SourceCode{Script: "export default 1"},
	}

	payload := componentToTransformation(item, "workspace-1")
	if payload.SourceCode.BuildStatus != "" {
		t.Fatalf("BuildStatus=%q, want empty for manifest-only change", payload.SourceCode.BuildStatus)
	}

	item.HasCodeChanges = true
	payload = componentToTransformation(item, "workspace-1")
	if payload.SourceCode.BuildStatus != "pending" {
		t.Fatalf("BuildStatus=%q, want pending for code change", payload.SourceCode.BuildStatus)
	}
}

func TestFunctionToPlatformIncludesHTTPMethod(t *testing.T) {
	item := localFunction{
		Slug: "enrich",
		Artifact: artifact.Function{
			Spec: artifact.FunctionSpec{
				Runtime:          "nodejs22",
				Type:             "GET",
				SignatureVersion: "v3",
				TemplateVersion:  "V3",
			},
		},
		Source: platform.SourceCode{Script: "async function handleRequest(req, res) {}"},
	}

	payload := functionToPlatform(item, "workspace-1")
	if payload.HTTPMethod != "GET" {
		t.Fatalf("HTTPMethod=%q, want GET", payload.HTTPMethod)
	}
	if payload.TemplateID != "express-v3" || payload.SignatureVersion != "express-v3" || payload.TemplateVersion != "V3" {
		t.Fatalf("unexpected function versions: templateID=%q signatureVersion=%q templateVersion=%q", payload.TemplateID, payload.SignatureVersion, payload.TemplateVersion)
	}

	item.Artifact.Spec.Type = ""
	item.Artifact.Spec.TemplateID = " EXPRESS-V3 "
	item.Artifact.Spec.SignatureVersion = "EXPRESS-V3"
	payload = functionToPlatform(item, "workspace-1")
	if payload.HTTPMethod != "POST" {
		t.Fatalf("HTTPMethod=%q, want POST default", payload.HTTPMethod)
	}
	if payload.TemplateID != "express-v3" || payload.SignatureVersion != "express-v3" {
		t.Fatalf("unexpected normalized versions: templateID=%q signatureVersion=%q", payload.TemplateID, payload.SignatureVersion)
	}

	item.Artifact.Spec = artifact.FunctionSpec{
		Runtime:         "go",
		TemplateVersion: "V2",
	}
	payload = functionToPlatform(item, "workspace-1")
	if payload.TemplateID != goFiberFunctionTemplateID || payload.SignatureVersion != "fiber-v2" {
		t.Fatalf("unexpected go fiber versions: templateID=%q signatureVersion=%q", payload.TemplateID, payload.SignatureVersion)
	}

	item.Artifact.Spec = artifact.FunctionSpec{
		Runtime:         "go",
		TemplateVersion: "V1",
	}
	payload = functionToPlatform(item, "workspace-1")
	if payload.TemplateID != goHTTPFunctionTemplateID || payload.SignatureVersion != "http-v1" {
		t.Fatalf("unexpected go http versions: templateID=%q signatureVersion=%q", payload.TemplateID, payload.SignatureVersion)
	}
}

func TestComponentToTransformationIncludesSwitchLabels(t *testing.T) {
	item := localComponent{
		Slug:    "route-orders",
		BaseDir: "switches",
		Artifact: artifact.Component{
			Spec: artifact.ComponentSpec{
				Type:         "switch",
				SwitchLabels: []string{"express", "standard", "express"},
			},
		},
	}

	payload := componentToTransformation(item, "workspace-1")
	want := []string{"express", "standard"}
	if strings.Join(payload.SwitchLabels, ",") != strings.Join(want, ",") {
		t.Fatalf("SwitchLabels=%v, want %v", payload.SwitchLabels, want)
	}
}

func TestPopulateSwitchLabelsFromPipelines(t *testing.T) {
	bundle := &localBundle{
		Transformations: []localComponent{{
			Slug:    "route-orders",
			BaseDir: "switches",
			Artifact: artifact.Component{
				Spec: artifact.ComponentSpec{
					Type:         "switch",
					SwitchLabels: []string{"existing"},
				},
			},
		}},
		Pipelines: []artifact.Pipeline{{
			Spec: artifact.PipelineSpec{Steps: []artifact.Step{
				{ID: "route", Uses: "switch/route-orders@latest", Cases: map[string]string{"express": "a", "standard": "b"}},
			}},
		}},
	}

	populateSwitchLabelsFromPipelines(bundle)

	got := bundle.Transformations[0].Artifact.Spec.SwitchLabels
	want := []string{"existing", "express", "standard"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("SwitchLabels=%v, want %v", got, want)
	}
}

func TestComponentBuildStatusOmitsNoBuildTemplates(t *testing.T) {
	if got := componentBuildStatus("TRANSFORMATION", "v1", "javascript"); got != "" {
		t.Fatalf("BuildStatus=%q, want empty for no-build template", got)
	}
}

func TestIsBuildPendingError(t *testing.T) {
	err := errors.New(`mgmt-srv returned 400 for PUT /workspace/default/transformations/id: {"status":400,"error":{"message":"build is pending","code":"invalid_request"}}`)
	if !isBuildPendingError(err) {
		t.Fatal("expected build pending response to be retryable")
	}
	if isBuildPendingError(errors.New("mgmt-srv returned 400: validation failed")) {
		t.Fatal("expected unrelated validation error to remain fatal")
	}
}

func TestPushableLocalChangesSkipsLocalDeletes(t *testing.T) {
	changes := []fileChange{
		{Path: "functions/removed/cnips.fn.yaml", Status: "delete-remote", LocalExists: false, RemoteExists: true},
		{Path: "functions/changed/cnips.fn.yaml", Status: "local", LocalExists: true, RemoteExists: true},
	}

	got := pushableLocalChanges(changes)
	if len(got) != 1 || got[0].Path != "functions/changed/cnips.fn.yaml" {
		t.Fatalf("pushableLocalChanges() = %#v", got)
	}
}

func TestTenantKeyFromTokenUsesTenantGroup(t *testing.T) {
	token := testToken(map[string]any{
		"groups": []map[string]any{
			{"groupId": "workspace-group", "groupType": "workspace"},
			{"groupId": "cnips-dev", "groupType": "tenant"},
		},
	})

	if got := tenantKeyFromToken(token); got != "cnips-dev" {
		t.Fatalf("tenantKeyFromToken() = %q, want cnips-dev", got)
	}
}

func TestPipelineToPlatformResolvesComponentSlugs(t *testing.T) {
	pipeline := artifact.Pipeline{
		Metadata: artifact.ObjectMeta{Name: "order-sync"},
		Spec: artifact.PipelineSpec{Steps: []artifact.Step{
			{ID: "normalize", Uses: "transformation/normalize@latest", Next: "ship"},
			{ID: "ship", Uses: "destination/sap@v1"},
		}},
	}
	state := &workspaceState{
		Transformations: map[string]platform.Transformation{"normalize": {ID: "tx-1", Name: "Normalize", Type: "TRANSFORMATION"}},
		Sources:         map[string]platform.Source{},
		Destinations:    map[string]platform.Destination{"sap": {ID: "dst-1", Name: "SAP"}},
		Functions:       map[string]platform.Function{},
		GlobalVariables: map[string]platform.GlobalVariable{},
		Configurations:  map[string]platform.Configuration{},
		Pipelines:       map[string]platform.Pipeline{},
	}

	out, err := pipelineToPlatform(pipeline, "workspace-1", state)
	if err != nil {
		t.Fatalf("pipelineToPlatform returned error: %v", err)
	}
	if len(out.TransformationList) != 1 || out.TransformationList[0].NodeID != "tx-1" {
		t.Fatalf("transformation not resolved: %#v", out.TransformationList)
	}
	if len(out.DestinationList) != 1 || out.DestinationList[0].NodeID != "dst-1" {
		t.Fatalf("destination not resolved: %#v", out.DestinationList)
	}
	if len(out.TransformationMap.EdgeMap) != 1 {
		t.Fatalf("expected one edge, got %#v", out.TransformationMap.EdgeMap)
	}
}

func TestPipelineToPlatformUsesLocalLayoutPositions(t *testing.T) {
	pipeline := artifact.Pipeline{
		Metadata: artifact.ObjectMeta{Name: "order-sync"},
		Layout: &artifact.PipelineLayout{Positions: map[string]artifact.PipelinePoint{
			"normalize": {X: 120, Y: 240},
		}},
		Spec: artifact.PipelineSpec{Steps: []artifact.Step{
			{ID: "normalize", Uses: "transformation/normalize@latest"},
		}},
	}
	state := &workspaceState{
		Transformations: map[string]platform.Transformation{"normalize": {ID: "tx-1", Name: "Normalize", Type: "TRANSFORMATION"}},
		Sources:         map[string]platform.Source{},
		Destinations:    map[string]platform.Destination{},
		Functions:       map[string]platform.Function{},
		GlobalVariables: map[string]platform.GlobalVariable{},
		Configurations:  map[string]platform.Configuration{},
		Pipelines: map[string]platform.Pipeline{
			"order-sync": {ID: "pl-1", Name: "order-sync", TransformationMap: &platform.TransformationMap{NodePositions: map[string]platform.Position{
				"normalize": {X: 5, Y: 10},
			}}},
		},
	}

	out, err := pipelineToPlatform(pipeline, "workspace-1", state)
	if err != nil {
		t.Fatalf("pipelineToPlatform returned error: %v", err)
	}
	if got := out.TransformationMap.NodePositions["normalize"]; got.X != 120 || got.Y != 240 {
		t.Fatalf("NodePositions[normalize] = %#v, want local layout position", got)
	}
}

func TestPipelineToPlatformCarriesSwitchLabelsFromCases(t *testing.T) {
	pipeline := artifact.Pipeline{
		Metadata: artifact.ObjectMeta{Name: "order-sync"},
		Spec: artifact.PipelineSpec{Steps: []artifact.Step{
			{ID: "route", Uses: "switch/route-orders@latest", Cases: map[string]string{"express": "ship", "standard": "queue"}},
			{ID: "ship", Uses: "destination/sap@latest"},
			{ID: "queue", Uses: "destination/sqs@latest"},
		}},
	}
	state := &workspaceState{
		Transformations: map[string]platform.Transformation{
			"route-orders": {ID: "sw-1", Name: "Route Orders", Type: "SWITCH", SwitchLabels: []string{"existing"}},
		},
		Sources: map[string]platform.Source{},
		Destinations: map[string]platform.Destination{
			"sap": {ID: "dst-1", Name: "SAP"},
			"sqs": {ID: "dst-2", Name: "SQS"},
		},
		Functions:       map[string]platform.Function{},
		GlobalVariables: map[string]platform.GlobalVariable{},
		Configurations:  map[string]platform.Configuration{},
		Pipelines:       map[string]platform.Pipeline{},
	}

	out, err := pipelineToPlatform(pipeline, "workspace-1", state)
	if err != nil {
		t.Fatalf("pipelineToPlatform returned error: %v", err)
	}
	if len(out.TransformationList) != 1 {
		t.Fatalf("TransformationList=%#v, want one switch", out.TransformationList)
	}
	want := []string{"existing", "express", "standard"}
	if strings.Join(out.TransformationList[0].SwitchLabels, ",") != strings.Join(want, ",") {
		t.Fatalf("SwitchLabels=%v, want %v", out.TransformationList[0].SwitchLabels, want)
	}
	if len(out.TransformationMap.EdgeMap) != 2 {
		t.Fatalf("EdgeMap=%#v, want two switch edges", out.TransformationMap.EdgeMap)
	}
}

func TestPipelineToPlatformPreservesExistingNodePositions(t *testing.T) {
	pipeline := artifact.Pipeline{
		Metadata: artifact.ObjectMeta{Name: "order-sync"},
		Spec: artifact.PipelineSpec{Steps: []artifact.Step{
			{ID: "normalize", Uses: "transformation/normalize@latest"},
		}},
	}
	state := &workspaceState{
		Transformations: map[string]platform.Transformation{"normalize": {ID: "tx-1", Name: "Normalize", Type: "TRANSFORMATION"}},
		Sources:         map[string]platform.Source{},
		Destinations:    map[string]platform.Destination{},
		Functions:       map[string]platform.Function{},
		GlobalVariables: map[string]platform.GlobalVariable{},
		Configurations:  map[string]platform.Configuration{},
		Pipelines: map[string]platform.Pipeline{
			"order-sync": {ID: "pl-1", Name: "order-sync", TransformationMap: &platform.TransformationMap{NodePositions: map[string]platform.Position{
				"normalize": {X: 300, Y: 450},
			}}},
		},
	}

	out, err := pipelineToPlatform(pipeline, "workspace-1", state)
	if err != nil {
		t.Fatalf("pipelineToPlatform returned error: %v", err)
	}
	if got := out.TransformationMap.NodePositions["normalize"]; got.X != 300 || got.Y != 450 {
		t.Fatalf("NodePositions[normalize] = %#v, want preserved remote position", got)
	}
}

func TestPipelineToPlatformPreservesPositionWhenStepIDChanges(t *testing.T) {
	pipeline := artifact.Pipeline{
		Metadata: artifact.ObjectMeta{Name: "order-sync"},
		Spec: artifact.PipelineSpec{Steps: []artifact.Step{
			{ID: "normalize-v2", Uses: "transformation/normalize@latest"},
		}},
	}
	state := &workspaceState{
		Transformations: map[string]platform.Transformation{"normalize": {ID: "tx-1", Name: "Normalize", Type: "TRANSFORMATION"}},
		Sources:         map[string]platform.Source{},
		Destinations:    map[string]platform.Destination{},
		Functions:       map[string]platform.Function{},
		GlobalVariables: map[string]platform.GlobalVariable{},
		Configurations:  map[string]platform.Configuration{},
		Pipelines: map[string]platform.Pipeline{
			"order-sync": {
				ID: "pl-1", Name: "order-sync",
				TransformationList: []platform.PipelineComponent{{ID: "normalize", NodeID: "tx-1"}},
				TransformationMap: &platform.TransformationMap{NodePositions: map[string]platform.Position{
					"normalize": {X: 700, Y: 900},
				}},
			},
		},
	}

	out, err := pipelineToPlatform(pipeline, "workspace-1", state)
	if err != nil {
		t.Fatalf("pipelineToPlatform returned error: %v", err)
	}
	if got := out.TransformationMap.NodePositions["normalize-v2"]; got.X != 700 || got.Y != 900 {
		t.Fatalf("NodePositions[normalize-v2] = %#v, want position from matching component nodeId", got)
	}
}

func testToken(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payloadBytes, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + payload + ".sig"
}

func TestFilterSinglePushChanges(t *testing.T) {
	changes := []fileChange{{Path: "transformations/normalize/component.yaml"}, {Path: "sources/orders/component.yaml"}}
	got, err := filterSinglePushChanges(changes, "transformation", "normalize")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != changes[0].Path {
		t.Fatalf("filtered changes = %#v", got)
	}
}
