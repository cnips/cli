package cli

import "strings"

type componentBase struct {
	dir  string
	kind string
}

var transformationFamilyBases = []componentBase{
	{dir: "transformations", kind: "transformation"},
	{dir: "approvals", kind: "approval"},
	{dir: "switches", kind: "switch"},
	{dir: "decisions", kind: "decision"},
}

var localComponentBases = []componentBase{
	{dir: "components", kind: "component"},
	{dir: "transformations", kind: "transformation"},
	{dir: "approvals", kind: "approval"},
	{dir: "switches", kind: "switch"},
	{dir: "decisions", kind: "decision"},
	{dir: "sources", kind: "source"},
	{dir: "destinations", kind: "destination"},
}

func transformationFamilyKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "transformation", "approval", "switch", "decision":
		return true
	default:
		return false
	}
}

func transformationFamilyBaseDir(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "approval":
		return "approvals"
	case "switch":
		return "switches"
	case "decision":
		return "decisions"
	default:
		return "transformations"
	}
}

func kindForComponentBase(base string) string {
	for _, candidate := range localComponentBases {
		if candidate.dir == base {
			return candidate.kind
		}
	}
	return strings.TrimSuffix(base, "s")
}
