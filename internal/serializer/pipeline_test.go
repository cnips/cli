package serializer

import (
	"reflect"
	"testing"

	"github.com/cnips/cli/internal/platform"
)

func TestPipelineBFSOrderIsDeterministicForDisconnectedNodes(t *testing.T) {
	p := &platform.Pipeline{
		TransformationList: []platform.PipelineComponent{
			{ID: "tx-b", Name: "Duplicate"},
			{ID: "tx-a", Name: "Duplicate"},
			{ID: "tx-c", Name: "Duplicate"},
		},
		TransformationMap: &platform.TransformationMap{},
	}

	first := bfsOrder(p, map[string][]platform.EdgeMap{})
	for i := 0; i < 20; i++ {
		if got := bfsOrder(p, map[string][]platform.EdgeMap{}); !reflect.DeepEqual(got, first) {
			t.Fatalf("bfsOrder changed between runs: first=%v got=%v", first, got)
		}
	}
	want := []string{"tx-a", "tx-b", "tx-c"}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("bfsOrder = %v, want %v", first, want)
	}
}

func TestBuildStepUsesAppSlugForAppPipelineComponent(t *testing.T) {
	step, err := buildStep(
		&platform.PipelineComponent{
			ID:          "node-1",
			NodeID:      "app-1",
			Name:        "Send Mail",
			Type:        "DESTINATION",
			IsApp:       true,
			UsedVersion: "1.2.3",
			Config:      map[string]any{"subject": "hello"},
		},
		"node-1",
		map[string]string{"node-1": "send-mail"},
		nil,
		nil,
		nil,
		nil,
		map[string]string{"app-1": "cidaas-send-mail"},
		nil,
		nil,
		nil,
		map[string]map[string]any{"app-1": map[string]any{"base_url": "https://example.test"}},
	)
	if err != nil {
		t.Fatalf("buildStep: %v", err)
	}
	if step.Uses != "app/cidaas-send-mail@1.2.3" {
		t.Fatalf("uses=%q, want app/cidaas-send-mail@1.2.3", step.Uses)
	}
	if step.With["base_url"] != "https://example.test" {
		t.Fatalf("base_url config missing from app step: %#v", step.With)
	}
	if step.With["subject"] != "hello" {
		t.Fatalf("node config missing from app step: %#v", step.With)
	}
}

func TestBuildStepUsesTransformationFamilyNamespace(t *testing.T) {
	step, err := buildStep(
		&platform.PipelineComponent{
			ID:          "node-1",
			NodeID:      "switch-1",
			Name:        "Route Orders",
			Type:        "SWITCH",
			UsedVersion: "latest",
		},
		"node-1",
		map[string]string{"node-1": "route"},
		nil,
		map[string]string{"switch-1": "route-orders"},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("buildStep: %v", err)
	}
	if step.Uses != "switch/route-orders@latest" {
		t.Fatalf("uses=%q, want switch/route-orders@latest", step.Uses)
	}
}

func TestBuildStepUsesCasesForSingleSwitchEdge(t *testing.T) {
	step, err := buildStep(
		&platform.PipelineComponent{
			ID:     "switch-node",
			NodeID: "switch-1",
			Name:   "Route Orders",
			Type:   "SWITCH",
		},
		"switch-node",
		map[string]string{"switch-node": "route-orders", "dest-node": "express-destination"},
		map[string][]platform.EdgeMap{
			"switch-node": {
				{Source: "switch-node", Target: "dest-node", Label: "express"},
			},
		},
		map[string]string{"switch-1": "route-orders"},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("buildStep: %v", err)
	}
	if step.Next != "" {
		t.Fatalf("Next=%q, want empty for labelled switch edge", step.Next)
	}
	if step.Cases["express"] != "express-destination" {
		t.Fatalf("Cases=%v, want express case", step.Cases)
	}
}
