package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cnips/cli/internal/artifact"
)

func TestRenameTransformationPreservesIDAndUpdatesPipelineUses(t *testing.T) {
	root := newRenameTestProject(t)
	writeTestFile(t, root, "transformations/normalize/component.yaml", `apiVersion: cnips.io/v1
kind: Component
metadata:
  id: tx-1
  name: normalize
spec:
  type: transformation
  language: javascript
`)
	writeTestFile(t, root, "transformations/normalize/handler.js", "export default async function handler() {}\n")
	writeTestFile(t, root, "pipelines/orders/pipeline.yaml", `apiVersion: cnips.io/v1
kind: Pipeline
metadata:
  id: pl-1
  name: orders
spec:
  steps:
    - id: normalize
      uses: transformation/normalize@latest
`)

	result, err := renameArtifact(root, "transformation", "normalize", "normalize-order")
	if err != nil {
		t.Fatalf("renameArtifact: %v", err)
	}
	if result.UpdatedPipelines != 1 {
		t.Fatalf("UpdatedPipelines=%d, want 1", result.UpdatedPipelines)
	}
	if _, err := os.Stat(filepath.Join(root, "transformations", "normalize")); !os.IsNotExist(err) {
		t.Fatalf("old directory still exists or stat failed: %v", err)
	}

	comp, err := artifact.ParseComponent(filepath.Join(root, "transformations", "normalize-order"))
	if err != nil {
		t.Fatalf("ParseComponent: %v", err)
	}
	if comp.Metadata.ID != "tx-1" || comp.Metadata.Name != "normalize-order" {
		t.Fatalf("unexpected component metadata: %#v", comp.Metadata)
	}
	pipeline, err := artifact.ParsePipeline(filepath.Join(root, "pipelines", "orders"))
	if err != nil {
		t.Fatalf("ParsePipeline: %v", err)
	}
	if got := pipeline.Spec.Steps[0].Uses; got != "transformation/normalize-order@latest" {
		t.Fatalf("uses=%q, want renamed reference", got)
	}
}

func TestRenameDecisionUsesOwnFolderAndPipelineNamespace(t *testing.T) {
	root := newRenameTestProject(t)
	writeTestFile(t, root, "decisions/fraud/component.yaml", `apiVersion: cnips.io/v1
kind: Component
metadata:
  id: decision-1
  name: fraud
spec:
  type: decision
  language: javascript
`)
	writeTestFile(t, root, "decisions/fraud/handler.js", "export default async function handler() {}\n")
	writeTestFile(t, root, "pipelines/orders/pipeline.yaml", `apiVersion: cnips.io/v1
kind: Pipeline
metadata:
  id: pl-1
  name: orders
spec:
  steps:
    - id: fraud
      uses: decision/fraud@latest
    - id: legacy-fraud
      uses: transformation/fraud@latest
`)

	result, err := renameArtifact(root, "decision", "fraud", "fraud-check")
	if err != nil {
		t.Fatalf("renameArtifact: %v", err)
	}
	if result.UpdatedPipelines != 1 {
		t.Fatalf("UpdatedPipelines=%d, want 1", result.UpdatedPipelines)
	}
	if _, err := os.Stat(filepath.Join(root, "decisions", "fraud")); !os.IsNotExist(err) {
		t.Fatalf("old directory still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "decisions", "fraud-check")); err != nil {
		t.Fatalf("new directory missing: %v", err)
	}

	pipeline, err := artifact.ParsePipeline(filepath.Join(root, "pipelines", "orders"))
	if err != nil {
		t.Fatalf("ParsePipeline: %v", err)
	}
	if got := pipeline.Spec.Steps[0].Uses; got != "decision/fraud-check@latest" {
		t.Fatalf("uses=%q, want decision/fraud-check@latest", got)
	}
	if got := pipeline.Spec.Steps[1].Uses; got != "transformation/fraud-check@latest" {
		t.Fatalf("legacy uses=%q, want transformation/fraud-check@latest", got)
	}
}

func TestRenameSourceUpdatesTriggerSourceRef(t *testing.T) {
	root := newRenameTestProject(t)
	writeTestFile(t, root, "sources/orders/component.yaml", `apiVersion: cnips.io/v1
kind: Component
metadata:
  id: src-1
  name: orders
spec:
  type: source
`)
	writeTestFile(t, root, "pipelines/sync/pipeline.yaml", `apiVersion: cnips.io/v1
kind: Pipeline
metadata:
  name: sync
spec:
  trigger:
    type: http
    sourceRef: orders
  steps:
    - id: source
      uses: source/orders@latest
`)

	if _, err := renameArtifact(root, "source", "orders", "webhook-orders"); err != nil {
		t.Fatalf("renameArtifact: %v", err)
	}
	pipeline, err := artifact.ParsePipeline(filepath.Join(root, "pipelines", "sync"))
	if err != nil {
		t.Fatalf("ParsePipeline: %v", err)
	}
	if pipeline.Spec.Trigger == nil || pipeline.Spec.Trigger.SourceRef != "webhook-orders" {
		t.Fatalf("trigger not renamed: %#v", pipeline.Spec.Trigger)
	}
	if got := pipeline.Spec.Steps[0].Uses; got != "source/webhook-orders@latest" {
		t.Fatalf("uses=%q, want source/webhook-orders@latest", got)
	}
}

func TestRenamePipelineMovesDirectoryAndMetadataOnly(t *testing.T) {
	root := newRenameTestProject(t)
	writeTestFile(t, root, "pipelines/orders/pipeline.yaml", `apiVersion: cnips.io/v1
kind: Pipeline
metadata:
  id: pl-1
  name: orders
spec:
  steps:
    - id: noop
      uses: native/template@latest
`)

	result, err := renameArtifact(root, "pipeline", "orders", "order-sync")
	if err != nil {
		t.Fatalf("renameArtifact: %v", err)
	}
	if result.UpdatedPipelines != 0 {
		t.Fatalf("UpdatedPipelines=%d, want 0", result.UpdatedPipelines)
	}
	pipeline, err := artifact.ParsePipeline(filepath.Join(root, "pipelines", "order-sync"))
	if err != nil {
		t.Fatalf("ParsePipeline: %v", err)
	}
	if pipeline.Metadata.ID != "pl-1" || pipeline.Metadata.Name != "order-sync" {
		t.Fatalf("unexpected pipeline metadata: %#v", pipeline.Metadata)
	}
}

func TestRenameRemapsLocalBaseManifestPaths(t *testing.T) {
	root := newRenameTestProject(t)
	writeTestFile(t, root, "functions/enrich/cnips.fn.yaml", `apiVersion: cnips.io/v1
kind: Function
metadata:
  id: fn-1
  name: enrich
spec:
  runtime: javascript
`)
	writeTestFile(t, root, "functions/enrich/handler.js", "export default async function handler() {}\n")
	manifest := &pullManifest{Files: map[string]manifestFile{
		"functions/enrich/cnips.fn.yaml": {Digest: "manifest-digest"},
		"functions/enrich/handler.js":    {Digest: "handler-digest"},
	}}
	if err := writeLocalPullManifest(root, "workspace/a", manifest); err != nil {
		t.Fatalf("writeLocalPullManifest: %v", err)
	}

	result, err := renameArtifact(root, "function", "enrich", "enrich-customer")
	if err != nil {
		t.Fatalf("renameArtifact: %v", err)
	}
	if result.RemappedManifests != 1 {
		t.Fatalf("RemappedManifests=%d, want 1", result.RemappedManifests)
	}
	got, err := readLocalPullManifest(root, "workspace/a")
	if err != nil {
		t.Fatalf("readLocalPullManifest: %v", err)
	}
	if _, ok := got.Files["functions/enrich/cnips.fn.yaml"]; ok {
		t.Fatalf("old manifest path still tracked: %#v", got.Files)
	}
	if got.Files["functions/enrich-customer/cnips.fn.yaml"].Digest != "manifest-digest" {
		t.Fatalf("new manifest path not remapped: %#v", got.Files)
	}
	if got.Files["functions/enrich-customer/handler.js"].Digest != "handler-digest" {
		t.Fatalf("new handler path not remapped: %#v", got.Files)
	}
	state, err := readRenameState(root)
	if err != nil {
		t.Fatalf("readRenameState: %v", err)
	}
	if len(state.Entries) != 1 {
		t.Fatalf("rename state entries=%#v, want 1 entry", state.Entries)
	}
	if state.Entries[0].OldPrefix != "functions/enrich" || state.Entries[0].NewPrefix != "functions/enrich-customer" {
		t.Fatalf("unexpected rename state: %#v", state.Entries[0])
	}
}

func TestRenameAliasesMapRemoteOldPathToLocalNewPath(t *testing.T) {
	files := map[string]string{
		"transformations/check-transformation-v3/component.yaml": "old",
		"transformations/check-transformation-v3/handler.js":     "handler",
	}
	manifest := map[string]manifestFile{
		"transformations/check-transformation-v3/component.yaml": {Digest: "old"},
		"transformations/check-transformation-v3/handler.js":     {Digest: "handler"},
	}
	aliases := []renameRecord{{
		Kind:      "transformation",
		OldPrefix: "transformations/check-transformation-v3",
		NewPrefix: "transformations/check-v3-transformation",
	}}

	applyRenameAliasesToStringMap(files, aliases)
	applyRenameAliasesToManifest(manifest, aliases)

	if _, ok := files["transformations/check-transformation-v3/component.yaml"]; ok {
		t.Fatalf("old file path still exists: %#v", files)
	}
	if files["transformations/check-v3-transformation/handler.js"] != "handler" {
		t.Fatalf("handler was not moved through alias: %#v", files)
	}
	if manifest["transformations/check-v3-transformation/component.yaml"].Digest != "old" {
		t.Fatalf("manifest was not moved through alias: %#v", manifest)
	}
}

func TestInferRenameAliasesFromMatchingComponentID(t *testing.T) {
	localRoot := t.TempDir()
	remoteRoot := t.TempDir()
	writeTestFile(t, localRoot, "transformations/check-v3-transformation/component.yaml", `apiVersion: cnips.io/v1
kind: Component
metadata:
  id: tx-1
  name: check-v3-transformation
spec:
  type: transformation
`)
	writeTestFile(t, remoteRoot, "transformations/check-transformation-v3/component.yaml", `apiVersion: cnips.io/v1
kind: Component
metadata:
  id: tx-1
  name: check-transformation-v3
spec:
  type: transformation
`)

	aliases, err := inferRenameAliases(localRoot, remoteRoot)
	if err != nil {
		t.Fatalf("inferRenameAliases: %v", err)
	}
	if len(aliases) != 1 {
		t.Fatalf("aliases=%#v, want 1", aliases)
	}
	if aliases[0].OldPrefix != "transformations/check-transformation-v3" ||
		aliases[0].NewPrefix != "transformations/check-v3-transformation" {
		t.Fatalf("unexpected alias: %#v", aliases[0])
	}
}

func newRenameTestProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, root, "cnips.yaml", "apiVersion: cnips.io/v1\nkind: Project\nmetadata:\n  name: test\n")
	writeTestFile(t, root, "cnips.lock", "apiVersion: cnips.io/v1\nkind: Lock\nspec: {}\n")
	return root
}
