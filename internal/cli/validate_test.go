package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/cnips/cli/internal/artifact"
)

func TestValidatePipelineChecksDefaultCaseTarget(t *testing.T) {
	p := &artifact.Pipeline{
		Metadata: artifact.ObjectMeta{Name: "orders"},
		Spec: artifact.PipelineSpec{Steps: []artifact.Step{
			{ID: "route", Uses: "native/switch@1", Cases: map[string]string{"default": "missing"}},
		}},
	}
	errs := validatePipeline(p)
	if !containsMessage(errs, "cases.default") {
		t.Fatalf("default case reference error missing: %v", errs)
	}
}

func TestValidateLocalReferencesReportsMissingTypedDependencies(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pipelines/orders/pipeline.yaml", "apiVersion: cnips.io/v1\nkind: Pipeline\nmetadata:\n  name: orders\nspec:\n  trigger:\n    type: webhook\n    sourceRef: missing-source\n  steps:\n    - id: enrich\n      uses: function/enrich@latest\n")

	report := &validationReport{}
	validateLocalReferences(root, false, report)
	errs, _ := report.snapshot()
	if !containsMessage(errs, "sourceRef") {
		t.Fatalf("sourceRef error missing: %v", errs)
	}
	if !containsMessage(errs, "does not resolve") {
		t.Fatalf("missing dependency error missing: %v", errs)
	}
}

func TestValidateSecretLeaksRejectsLiteralCredentialFields(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "environments/prod.yaml", "apiVersion: cnips.io/v1\nkind: Environment\nmetadata:\n  name: prod\nspec:\n  secrets:\n    api_key:\n      value: plain-text-secret\n")

	report := &validationReport{}
	validateSecretLeaks(root, report)
	errs, _ := report.snapshot()
	if !containsMessage(errs, "literal credential") {
		t.Fatalf("secret leak error missing: %v", errs)
	}
}

func TestValidateSecretLeaksAllowsPulledMetadataIDs(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "transformations/test/component.yaml", "apiVersion: cnips.io/v1\nkind: Component\nmetadata:\n  id: 80f39533-1c97-45c4-81ff-b318f32698ba\n  name: test\nspec:\n  type: transformation\n  language: golang\n")

	report := &validationReport{}
	validateSecretLeaks(root, report)
	errs, _ := report.snapshot()
	if len(errs) != 0 {
		t.Fatalf("metadata UUID should not be reported as a secret: %v", errs)
	}
}

func containsMessage(messages []string, needle string) bool {
	for _, message := range messages {
		if strings.Contains(message, needle) {
			return true
		}
	}
	return false
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
