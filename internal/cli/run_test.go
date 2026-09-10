package cli

import (
	"testing"

	"github.com/cnips/cli/internal/runtime"
)

func TestSummarizeTraceStepsAggregatesRepeatedLoopChildren(t *testing.T) {
	steps := []runtime.StepTrace{
		{
			ID:     "loop",
			Status: "ok",
			Children: []runtime.StepTrace{
				{
					ID:         "loop-trans1",
					ParentID:   "loop",
					Status:     "ok",
					DurationMs: 2,
				},
				{
					ID:         "loop-2",
					ParentID:   "loop",
					Status:     "ok",
					DurationMs: 10,
					Children: []runtime.StepTrace{
						{ID: "loop-trans2", ParentID: "loop-2", Status: "ok", DurationMs: 1},
						{ID: "loop-trans2", ParentID: "loop-2", Status: "ok", DurationMs: 1},
					},
				},
				{
					ID:         "loop-trans1",
					ParentID:   "loop",
					Status:     "ok",
					DurationMs: 3,
				},
				{
					ID:         "loop-2",
					ParentID:   "loop",
					Status:     "ok",
					DurationMs: 12,
					Children: []runtime.StepTrace{
						{ID: "loop-trans2", ParentID: "loop-2", Status: "ok", DurationMs: 2},
						{ID: "loop-trans2", ParentID: "loop-2", Status: "ok", DurationMs: 2},
					},
				},
			},
		},
	}

	summary := summarizeTraceSteps(steps)
	if len(summary) != 1 {
		t.Fatalf("len(summary) = %d, want 1", len(summary))
	}

	children := summarizeTraceSteps(summary[0].Children)
	if len(children) != 2 {
		t.Fatalf("len(children) = %d, want 2", len(children))
	}
	if traceStepID(children[0]) != "loop-trans1" || children[0].Count != 2 || children[0].DurationMs != 5 {
		t.Fatalf("first child = %q x%d/%dms, want loop-trans1 x2/5ms", traceStepID(children[0]), children[0].Count, children[0].DurationMs)
	}
	if traceStepID(children[1]) != "loop-2" || children[1].Count != 2 || children[1].DurationMs != 22 {
		t.Fatalf("second child = %q x%d/%dms, want loop-2 x2/22ms", traceStepID(children[1]), children[1].Count, children[1].DurationMs)
	}

	nestedChildren := summarizeTraceSteps(children[1].Children)
	if len(nestedChildren) != 1 {
		t.Fatalf("len(nestedChildren) = %d, want 1", len(nestedChildren))
	}
	if traceStepID(nestedChildren[0]) != "loop-trans2" || nestedChildren[0].Count != 4 || nestedChildren[0].DurationMs != 6 {
		t.Fatalf("nested child = %q x%d/%dms, want loop-trans2 x4/6ms", traceStepID(nestedChildren[0]), nestedChildren[0].Count, nestedChildren[0].DurationMs)
	}
}

func TestSummarizeTraceStepsMarksParentWhenNestedChildErrors(t *testing.T) {
	steps := []runtime.StepTrace{
		{
			ID:     "loop",
			Status: "ok",
			Children: []runtime.StepTrace{
				{
					ID:       "loop-2",
					ParentID: "loop",
					Status:   "error",
					Error:    `loop arrayField "childItems": field "childItems" not found`,
				},
			},
		},
	}

	summary := summarizeTraceSteps(steps)
	if len(summary) != 1 {
		t.Fatalf("len(summary) = %d, want 1", len(summary))
	}
	if summary[0].Status != "error" {
		t.Fatalf("parent status = %q, want error", summary[0].Status)
	}
	want := `nested step loop-2 failed: loop arrayField "childItems": field "childItems" not found`
	if summary[0].Error != want {
		t.Fatalf("parent error = %q, want %q", summary[0].Error, want)
	}
}
