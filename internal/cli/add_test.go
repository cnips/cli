package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cnips/cli/internal/artifact"
)

func TestAddComponentScaffoldsJavaScriptSourceV2(t *testing.T) {
	root := newAddTestProject(t)

	result, err := addComponent(root, "HTTP Orders", addOptions{
		ComponentType: "source",
		Language:      "javascript",
	})
	if err != nil {
		t.Fatalf("addComponent: %v", err)
	}
	if result.Name != "http-orders" || result.SignatureVersion != "v2" {
		t.Fatalf("unexpected result: %#v", result)
	}

	comp, err := artifact.ParseComponent(filepath.Join(root, "sources", "http-orders"))
	if err != nil {
		t.Fatalf("ParseComponent: %v", err)
	}
	if comp.Spec.Type != "source" || comp.Spec.Language != "javascript" || comp.Spec.SignatureVersion != "v2" {
		t.Fatalf("unexpected manifest: %#v", comp.Spec)
	}
	assertFileExists(t, filepath.Join(root, "sources", "http-orders", "handler.js"))
	assertFileExists(t, filepath.Join(root, "sources", "http-orders", "package.json"))
	assertFileContains(t, filepath.Join(root, "sources", "http-orders", "handler.js"), "Extractor Function Contract")
}

func TestAddComponentScaffoldsJavaScriptTransformationV3(t *testing.T) {
	root := newAddTestProject(t)

	result, err := addComponent(root, "Normalize Order", addOptions{
		ComponentType: "transformation",
		Language:      "js",
	})
	if err != nil {
		t.Fatalf("addComponent: %v", err)
	}
	if result.SignatureVersion != "v3" {
		t.Fatalf("signatureVersion = %q, want v3", result.SignatureVersion)
	}

	comp, err := artifact.ParseComponent(filepath.Join(root, "transformations", "normalize-order"))
	if err != nil {
		t.Fatalf("ParseComponent: %v", err)
	}
	if comp.Spec.TemplateVersion != "v3" {
		t.Fatalf("templateVersion = %q, want v3", comp.Spec.TemplateVersion)
	}
	assertFileContains(t, filepath.Join(root, "transformations", "normalize-order", "handler.js"), "Transformation Function Contract")
}

func TestAddComponentScaffoldsTransformationFamilyTypes(t *testing.T) {
	root := newAddTestProject(t)

	for _, tc := range []struct {
		name string
		typ  string
		lang string
		want string
		dir  string
	}{
		{name: "Approval Gate", typ: "approval", lang: "python", want: "v1", dir: "approvals"},
		{name: "Routing Switch", typ: "switch", lang: "go", want: "v2", dir: "switches"},
		{name: "Fraud Decision", typ: "decision", lang: "javascript", want: "v3", dir: "decisions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := addComponent(root, tc.name, addOptions{
				ComponentType: tc.typ,
				Language:      tc.lang,
			})
			if err != nil {
				t.Fatalf("addComponent: %v", err)
			}
			if result.SignatureVersion != tc.want {
				t.Fatalf("signatureVersion = %q, want %s", result.SignatureVersion, tc.want)
			}

			dir := filepath.Join(root, tc.dir, result.Name)
			comp, err := artifact.ParseComponent(dir)
			if err != nil {
				t.Fatalf("ParseComponent: %v", err)
			}
			if comp.Spec.Type != tc.typ {
				t.Fatalf("spec.type = %q", comp.Spec.Type)
			}
		})
	}
}

func TestAddGoFunctionScaffoldsFiberSignature(t *testing.T) {
	root := newAddTestProject(t)

	result, err := addComponent(root, "Webhook Fn", addOptions{
		ComponentType: "function",
		Language:      "go",
		GoFramework:   "fiber",
		Method:        "PUT",
	})
	if err != nil {
		t.Fatalf("addComponent: %v", err)
	}
	if result.SignatureVersion != "fiber-v2" || result.TemplateVersion != "V2" {
		t.Fatalf("unexpected versions: %#v", result)
	}

	fn, err := artifact.ParseFunction(filepath.Join(root, "functions", "webhook-fn"))
	if err != nil {
		t.Fatalf("ParseFunction: %v", err)
	}
	if fn.Spec.TemplateID != goFiberFunctionTemplateID || fn.Spec.SignatureVersion != "fiber-v2" || fn.Spec.TemplateVersion != "V2" || fn.Spec.Type != "PUT" {
		t.Fatalf("unexpected manifest: %#v", fn.Spec)
	}
	assertFileExists(t, filepath.Join(root, "functions", "webhook-fn", "main.go"))
	assertFileExists(t, filepath.Join(root, "functions", "webhook-fn", "go.mod"))
	assertFileContains(t, filepath.Join(root, "functions", "webhook-fn", "main.go"), "*fiber.Ctx")
}

func TestAddFunctionScaffoldsTemplateVersionValuesAndDefaultMethod(t *testing.T) {
	root := newAddTestProject(t)

	for _, tc := range []struct {
		name string
		lang string
		sig  string
		tmpl string
		id   string
	}{
		{name: "Node Fn", lang: "javascript", sig: "express-v3", tmpl: "V3", id: "express-v3"},
		{name: "Python Fn", lang: "python", sig: "python-v1", tmpl: "V1", id: "python-v1"},
		{name: "Go Fn", lang: "go", sig: "http-v1", tmpl: "V1", id: goHTTPFunctionTemplateID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := addComponent(root, tc.name, addOptions{
				ComponentType: "function",
				Language:      tc.lang,
			})
			if err != nil {
				t.Fatalf("addComponent: %v", err)
			}
			if result.SignatureVersion != tc.sig || result.TemplateVersion != tc.tmpl {
				t.Fatalf("unexpected versions: %#v", result)
			}
			fn, err := artifact.ParseFunction(filepath.Join(root, "functions", result.Name))
			if err != nil {
				t.Fatalf("ParseFunction: %v", err)
			}
			if fn.Spec.TemplateID != tc.id || fn.Spec.SignatureVersion != tc.sig || fn.Spec.TemplateVersion != tc.tmpl || fn.Spec.Type != "POST" {
				t.Fatalf("unexpected manifest: %#v", fn.Spec)
			}
		})
	}
}

func TestAddFunctionRejectsUnsupportedMethod(t *testing.T) {
	root := newAddTestProject(t)

	if _, err := addComponent(root, "Bad Method", addOptions{
		ComponentType: "function",
		Language:      "javascript",
		Method:        "PATCH",
	}); err == nil {
		t.Fatal("expected error for unsupported function method")
	}
}

func TestAddComponentScaffoldsPythonAndGoComponentVersions(t *testing.T) {
	root := newAddTestProject(t)

	pyResult, err := addComponent(root, "Archive Destination", addOptions{
		ComponentType: "destination",
		Language:      "python",
	})
	if err != nil {
		t.Fatalf("add python component: %v", err)
	}
	if pyResult.SignatureVersion != "v1" {
		t.Fatalf("python signatureVersion = %q, want v1", pyResult.SignatureVersion)
	}
	assertFileContains(t, filepath.Join(root, "destinations", "archive-destination", "handler.py"), "Implement your destination here")

	goResult, err := addComponent(root, "Order Mapper", addOptions{
		ComponentType: "transformation",
		Language:      "golang",
	})
	if err != nil {
		t.Fatalf("add go component: %v", err)
	}
	if goResult.SignatureVersion != "v2" {
		t.Fatalf("go signatureVersion = %q, want v2", goResult.SignatureVersion)
	}
	assertFileContains(t, filepath.Join(root, "transformations", "order-mapper", "main.go"), "inside transformation")
}

func TestAddRejectsGoFrameworkOutsideGoFunctions(t *testing.T) {
	root := newAddTestProject(t)

	if _, err := addComponent(root, "Bad", addOptions{
		ComponentType: "transformation",
		Language:      "go",
		GoFramework:   "fiber",
	}); err == nil {
		t.Fatal("expected error for go-framework on non-function")
	}

	if _, err := addComponent(root, "Bad Fn", addOptions{
		ComponentType: "function",
		Language:      "javascript",
		GoFramework:   "fiber",
	}); err == nil {
		t.Fatal("expected error for go-framework on non-Go function")
	}
}

func TestAddRejectsExistingComponent(t *testing.T) {
	root := newAddTestProject(t)
	if _, err := addComponent(root, "Normalize", addOptions{ComponentType: "transformation", Language: "python"}); err != nil {
		t.Fatalf("first addComponent: %v", err)
	}
	if _, err := addComponent(root, "Normalize", addOptions{ComponentType: "transformation", Language: "python"}); err == nil {
		t.Fatal("expected error for existing component")
	}
}

func newAddTestProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cnips.yaml"), []byte("apiVersion: cnips.io/v1\nkind: Project\nmetadata:\n  name: test\n"), 0o644); err != nil {
		t.Fatalf("write cnips.yaml: %v", err)
	}
	return root
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s does not contain %q", path, want)
	}
}
