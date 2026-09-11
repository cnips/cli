package builder

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectLanguageSupportsJSFunctionRuntime(t *testing.T) {
	dir := t.TempDir()
	manifest := `apiVersion: cnips.io/v1
kind: Function
metadata:
  name: ash-test1
spec:
  runtime: js
`
	if err := os.WriteFile(filepath.Join(dir, "cnips.fn.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	lang, err := detectLanguage(dir)
	if err != nil {
		t.Fatalf("detectLanguage: %v", err)
	}
	if lang != "javascript" {
		t.Fatalf("lang = %q, want javascript", lang)
	}
}

func TestDetectLanguageSupportsPlainJSHandler(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.js"), []byte("async function handleRequest(req, res) {}\n"), 0o644); err != nil {
		t.Fatalf("write handler.js: %v", err)
	}

	lang, err := detectLanguage(dir)
	if err != nil {
		t.Fatalf("detectLanguage: %v", err)
	}
	if lang != "javascript" {
		t.Fatalf("lang = %q, want javascript", lang)
	}
}

func TestEnsureJSWrappersUseComponentExecuteContract(t *testing.T) {
	outDir := t.TempDir()

	serverScript, runnerScript, err := EnsureJSWrappers(outDir)
	if err != nil {
		t.Fatalf("EnsureJSWrappers: %v", err)
	}

	server, err := os.ReadFile(serverScript)
	if err != nil {
		t.Fatalf("read server wrapper: %v", err)
	}
	serverText := string(server)
	for _, want := range []string{
		"component.execute || component.default?.execute || component.default",
		"execute(data, config, vars, LOG)",
		"import { format } from 'node:util'",
		"format(...a)",
	} {
		if !strings.Contains(serverText, want) {
			t.Fatalf("server wrapper missing %q", want)
		}
	}

	runner, err := os.ReadFile(runnerScript)
	if err != nil {
		t.Fatalf("read runner wrapper: %v", err)
	}
	if !strings.Contains(string(runner), "component.execute || component.default?.execute || component.default") {
		t.Fatal("runner wrapper does not support default exports")
	}
	if !strings.Contains(string(runner), "format(...a)") {
		t.Fatal("runner wrapper does not format log arguments")
	}

	if _, err := os.Stat(filepath.Clean(runnerScript)); err != nil {
		t.Fatalf("runner wrapper was not created: %v", err)
	}
}

func TestPythonServerWrapperSupportsConfiguredFunctionMethods(t *testing.T) {
	outDir := t.TempDir()

	serverScript, _, err := EnsurePythonWrappers(outDir)
	if err != nil {
		t.Fatalf("EnsurePythonWrappers: %v", err)
	}
	data, err := os.ReadFile(serverScript)
	if err != nil {
		t.Fatalf("read server wrapper: %v", err)
	}
	text := string(data)
	for _, want := range []string{"def do_GET(self):", "def do_POST(self):", "def do_PUT(self):", "def do_DELETE(self):", "urlparse(self.path).path != \"/execute\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("python server wrapper missing %q", want)
		}
	}
}

func TestWriteExpressHandlerEntryWrapsHandleRequestFunction(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	handler := `import { logger, levels } from "@cnips/log";

const LOG = logger(process.env.APP || "default", levels.info);

async function handleRequest(req, res) {
  return res.json({ ok: true });
}
`
	if err := os.WriteFile(filepath.Join(srcDir, "handler.js"), []byte(handler), 0o644); err != nil {
		t.Fatalf("write handler: %v", err)
	}

	if !JSUsesExpressHandler(srcDir) {
		t.Fatal("JSUsesExpressHandler = false, want true")
	}

	wrappedPath, err := writeExpressHandlerEntry(srcDir, outDir, "handler.js")
	if err != nil {
		t.Fatalf("writeExpressHandlerEntry: %v", err)
	}
	if err := ensureJSDevShims(outDir); err != nil {
		t.Fatalf("ensureJSDevShims: %v", err)
	}
	wrapped, err := os.ReadFile(wrappedPath)
	if err != nil {
		t.Fatalf("read wrapped entry: %v", err)
	}
	text := string(wrapped)
	for _, want := range []string{
		"async function handleRequest(req, res)",
		"await handleRequest(req, res)",
		"const functionConfig = JSON.parse(process.env.CNIPS_FUNCTION_CONFIG || '{}')",
		"req.headers[headerKey] = String(value)",
		"path === '/ping'",
		"path !== '/execute'",
		"createServer",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("wrapped entry missing %q:\n%s", want, text)
		}
	}

	shim, err := os.ReadFile(filepath.Join(outDir, "node_modules", "@cnips", "log", "index.js"))
	if err != nil {
		t.Fatalf("read cnips log shim: %v", err)
	}
	if !strings.Contains(string(shim), "format(...args)") {
		t.Fatal("cnips log shim does not format log arguments")
	}
}

func TestBuildResultIncludesFunctionConfig(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "functions", "hello")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := `apiVersion: cnips.io/v1
kind: Function
metadata:
  name: hello
spec:
  runtime: js
  config:
    foo: bar
`
	if err := os.WriteFile(filepath.Join(dir, "cnips.fn.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "handler.js"), []byte("async function handleRequest(req, res) { res.json({ ok: req.headers.foo }); }\n"), 0o644); err != nil {
		t.Fatalf("write handler: %v", err)
	}

	result, err := BuildWithOptions(root, "hello", dir, Options{})
	if err != nil {
		t.Fatalf("BuildWithOptions: %v", err)
	}
	if result.Config["foo"] != "bar" {
		t.Fatalf("Config[foo] = %q, want bar", result.Config["foo"])
	}
}

func TestWriteGoFunctionWrapperWrapsHTTPHandlerPackage(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	main := `package handler

import "net/http"

func HandleRequest(w http.ResponseWriter, r *http.Request) {
  _, _ = w.Write([]byte("ok"))
}
`
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(main), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module function\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	if !GoUsesHTTPHandler(srcDir) {
		t.Fatal("GoUsesHTTPHandler = false, want true")
	}

	wrappedDir, err := writeGoFunctionWrapper(srcDir, outDir)
	if err != nil {
		t.Fatalf("writeGoFunctionWrapper: %v", err)
	}
	wrappedMain, err := os.ReadFile(filepath.Join(wrappedDir, "main.go"))
	if err != nil {
		t.Fatalf("read wrapped main: %v", err)
	}
	handler, err := os.ReadFile(filepath.Join(wrappedDir, "handler", "main.go"))
	if err != nil {
		t.Fatalf("read handler copy: %v", err)
	}
	for _, want := range []string{
		`"function/handler"`,
		`applyFunctionConfigHeaders(r)`,
		`handler.HandleRequest(w, r)`,
		`http.HandleFunc("/ping"`,
	} {
		if !strings.Contains(string(wrappedMain), want) {
			t.Fatalf("wrapped main missing %q:\n%s", want, wrappedMain)
		}
	}
	if !strings.Contains(string(handler), "package handler") {
		t.Fatalf("handler copy = %s, want package handler", handler)
	}
}

func TestBuildGoExecuteSourceSupportsOneShot(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(srcDir, "logshim"), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	goMod := `module extractor

go 1.24

require github.com/zinscky/log v1.1.0

replace github.com/zinscky/log => ` + filepath.ToSlash(filepath.Join(srcDir, "logshim")) + `
`
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	logShimMod := `module github.com/zinscky/log

go 1.24
`
	if err := os.WriteFile(filepath.Join(srcDir, "logshim", "go.mod"), []byte(logShimMod), 0o644); err != nil {
		t.Fatalf("write logshim go.mod: %v", err)
	}
	logShim := `package log

const Info = 1

type Logger struct{}

func New(level int, app string) *Logger { return &Logger{} }
func (l *Logger) Info(format string, args ...any) {}
func (l *Logger) String() string { return "" }
`
	if err := os.WriteFile(filepath.Join(srcDir, "logshim", "log.go"), []byte(logShim), 0o644); err != nil {
		t.Fatalf("write logshim: %v", err)
	}
	main := "package handler\n\n" +
		"import \"github.com/zinscky/log\"\n\n" +
		"func Execute(config map[string]string, vars map[string]string, log *log.Logger) (string, error) {\n" +
		"	return `{\"ok\":true}`, nil\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(main), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	result, err := Build(root, "check-src-go@latest", srcDir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result.RunnerScript == "" {
		t.Fatal("RunnerScript is empty")
	}
	cmd := exec.Command(result.RunnerScript, "--oneshot") //nolint:gosec
	cmd.Env = append(os.Environ(), "CNIPS_CONFIG={}", "CNIPS_VARS={}")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run one-shot: %v", err)
	}
	if strings.TrimSpace(string(out)) != `{"ok":true}` {
		t.Fatalf("one-shot output = %q", out)
	}
}
