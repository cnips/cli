// Package builder compiles local components and functions to runnable artifacts.
// Outputs land in <project-root>/.cnips/cache/artifacts/<name>/.
package builder

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/project"
)

// Result holds information about a successful build.
type Result struct {
	Name         string
	Language     string
	OutDir       string
	Executable   string // path to the bundled/compiled artifact
	ServerScript string // wrapper script for HTTP /ping + /execute
	RunnerScript string // one-shot wrapper script for sources
	Config       map[string]string
	IsBun        bool
	Protocol     string // http | unix
	Digest       string // sha256 of the built artifact
}

type Options struct {
	LogWriter io.Writer
}

// Build auto-detects the language of the component at srcDir and builds it.
// srcDir can be a function dir or a component dir.
func Build(root, name, srcDir string) (*Result, error) {
	return BuildWithOptions(root, name, srcDir, Options{})
}

func BuildWithOptions(root, name, srcDir string, opts Options) (*Result, error) {
	lang, err := detectLanguage(srcDir)
	if err != nil {
		return nil, fmt.Errorf("detect language for %s: %w", name, err)
	}
	config := functionConfig(srcDir)

	outDir := filepath.Join(project.ArtifactCacheDir(root), name)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}

	var result *Result
	switch lang {
	case "javascript", "typescript":
		result, err = buildJS(name, srcDir, outDir, opts)
	case "go":
		result, err = buildGo(name, srcDir, outDir, opts)
	case "python":
		result, err = buildPython(name, srcDir, outDir, opts)
	default:
		return nil, fmt.Errorf("unsupported language %q for %s", lang, name)
	}
	if err != nil {
		return nil, err
	}
	result.Config = config
	return result, nil
}

// NeedsBuild returns true when the source directory is newer than the last build artifact.
func NeedsBuild(root, name, srcDir string) bool {
	outDir := filepath.Join(project.ArtifactCacheDir(root), name)
	digestFile := filepath.Join(outDir, ".digest")
	stored, err := os.ReadFile(digestFile)
	if err != nil {
		return true // no previous build
	}
	current, err := dirDigest(srcDir)
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(stored)) != current
}

// SaveDigest writes the current source digest after a successful build.
func SaveDigest(root, name, srcDir string) error {
	outDir := filepath.Join(project.ArtifactCacheDir(root), name)
	digest, err := dirDigest(srcDir)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, ".digest"), []byte(digest), 0o644)
}

// -----------------------------------------------------------------------
// JS / TypeScript
// -----------------------------------------------------------------------

func buildJS(name, srcDir, outDir string, opts Options) (*Result, error) {
	isBun := hasBun()
	pkgJSON := filepath.Join(srcDir, "package.json")
	directServer := JSUsesDirectServer(srcDir)
	expressHandler := JSUsesExpressHandler(srcDir)

	// install dependencies
	if _, err := os.Stat(pkgJSON); err == nil {
		installCmd := "npm"
		installArgs := []string{"install", "--silent"}
		if isBun {
			installCmd = "bun"
			installArgs = []string{"install", "--silent"}
		}
		if err := runCmd(srcDir, opts.LogWriter, installCmd, installArgs...); err != nil {
			return nil, fmt.Errorf("install deps for %s: %w", name, err)
		}
	}

	// find entry point
	entry := findEntry(srcDir, []string{"handler.ts", "index.ts", "src/index.ts", "handler.js", "index.js", "src/index.js"})
	if entry == "" {
		return nil, fmt.Errorf("no entry file found in %s", srcDir)
	}
	entryPath := filepath.Join(srcDir, entry)
	if expressHandler {
		if err := ensureNodeModulesLink(srcDir, outDir); err != nil {
			return nil, err
		}
		if err := ensureJSDevShims(outDir); err != nil {
			return nil, err
		}
		wrappedEntry, err := writeExpressHandlerEntry(srcDir, outDir, entry)
		if err != nil {
			return nil, err
		}
		entryPath = wrappedEntry
		directServer = true
	}

	outFile := filepath.Join(outDir, "main.mjs")
	if !isBun && directServer {
		outFile = filepath.Join(outDir, "main.js")
	}

	if isBun {
		// bun build --target=bun --outfile=main.mjs entry
		if err := runCmd(srcDir, opts.LogWriter, "bun", "build", "--target=bun", "--outfile="+outFile, entryPath); err != nil {
			return nil, fmt.Errorf("bun build %s: %w", name, err)
		}
	} else {
		// fall back: just copy the entry; node will run it directly
		data, err := os.ReadFile(entryPath)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(outFile, data, 0o644); err != nil {
			return nil, err
		}
	}

	dig, _ := fileDigest(outFile)
	result := &Result{Name: name, Language: "javascript", OutDir: outDir, Executable: outFile, IsBun: isBun, Digest: dig}
	if JSUsesUnixSocket(srcDir) {
		result.Protocol = "unix"
	} else {
		result.Protocol = "http"
	}

	// Write server and one-shot runner wrappers so the component can be
	// started as an HTTP service (server.mjs) or run once (runner.mjs).
	if directServer {
		return result, nil
	}
	serverScript, runnerScript, err := EnsureJSWrappers(outDir)
	if err != nil {
		return nil, err
	}
	result.ServerScript = serverScript
	result.RunnerScript = runnerScript
	return result, nil
}

// -----------------------------------------------------------------------
// Go
// -----------------------------------------------------------------------

func buildGo(name, srcDir, outDir string, opts Options) (*Result, error) {
	if err := ensureGoModuleSums(srcDir, opts); err != nil {
		return nil, err
	}
	buildDir := srcDir
	if GoUsesExecuteHandler(srcDir) {
		wrappedDir, err := writeGoExecuteWrapper(srcDir, outDir)
		if err != nil {
			return nil, err
		}
		buildDir = wrappedDir
	} else if GoUsesHTTPHandler(srcDir) || GoUsesFiberHandler(srcDir) {
		wrappedDir, err := writeGoFunctionWrapper(srcDir, outDir)
		if err != nil {
			return nil, err
		}
		buildDir = wrappedDir
	}
	outFile := filepath.Join(outDir, "main")
	if err := runCmd(buildDir, opts.LogWriter, "go", "build", "-buildvcs=false", "-o", outFile, "."); err != nil {
		return nil, fmt.Errorf("go build %s: %w", name, err)
	}
	dig, _ := fileDigest(outFile)
	protocol := "http"
	if GoUsesUnixSocket(srcDir) {
		protocol = "unix"
	}
	result := &Result{Name: name, Language: "go", OutDir: outDir, Executable: outFile, Protocol: protocol, Digest: dig}
	if GoUsesExecuteHandler(srcDir) {
		result.RunnerScript = outFile
	}
	return result, nil
}

// -----------------------------------------------------------------------
// Python
// -----------------------------------------------------------------------

func buildPython(name, srcDir, outDir string, opts Options) (*Result, error) {
	venvDir := filepath.Join(outDir, ".venv")

	// create venv if it doesn't exist
	if _, err := os.Stat(venvDir); os.IsNotExist(err) {
		if err := runCmd(srcDir, opts.LogWriter, "python3", "-m", "venv", venvDir); err != nil {
			return nil, fmt.Errorf("create venv for %s: %w", name, err)
		}
	}

	// install requirements
	reqFile := filepath.Join(srcDir, "requirements.txt")
	if _, err := os.Stat(reqFile); err == nil {
		pip := filepath.Join(venvDir, "bin", "pip")
		if err := runCmd(srcDir, opts.LogWriter, pip, "install", "-q", "-r", reqFile); err != nil {
			return nil, fmt.Errorf("pip install for %s: %w", name, err)
		}
	}

	// find entry
	entry := findEntry(srcDir, []string{"handler.py", "main.py", "src/main.py"})
	if entry == "" {
		return nil, fmt.Errorf("no Python entry file found in %s", srcDir)
	}

	// copy entry to outDir
	outFile := filepath.Join(outDir, "main.py")
	data, err := os.ReadFile(filepath.Join(srcDir, entry))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(outFile, data, 0o644); err != nil {
		return nil, err
	}

	python := filepath.Join(venvDir, "bin", "python3")
	dig, _ := fileDigest(outFile)
	result := &Result{Name: name, Language: "python", OutDir: outDir, Executable: python + " " + outFile, Digest: dig}
	serverScript, runnerScript, err := EnsurePythonWrappers(outDir)
	if err != nil {
		return nil, err
	}
	result.ServerScript = serverScript
	result.RunnerScript = runnerScript
	return result, nil
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

func detectLanguage(dir string) (string, error) {
	checks := []struct {
		files []string
		lang  string
	}{
		{[]string{"go.mod", "main.go"}, "go"},
		{[]string{"package.json", "handler.js", "index.js", "src/index.js"}, "javascript"},
		{[]string{"handler.ts", "index.ts", "src/index.ts"}, "typescript"},
		{[]string{"handler.py", "main.py", "requirements.txt"}, "python"},
	}
	for _, c := range checks {
		for _, f := range c.files {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				if c.lang == "typescript" || c.lang == "javascript" {
					return "javascript", nil
				}
				return c.lang, nil
			}
		}
	}

	// Check component manifest for explicit language
	comp, err := artifact.ParseComponent(dir)
	if err == nil && comp.Spec.Language != "" {
		return strings.ToLower(comp.Spec.Language), nil
	}
	fn, err := artifact.ParseFunction(dir)
	if err == nil && fn.Spec.Runtime != "" {
		rt := strings.ToLower(fn.Spec.Runtime)
		switch {
		case rt == "js" || rt == "javascript" || strings.HasPrefix(rt, "node") || strings.HasPrefix(rt, "bun"):
			return "javascript", nil
		case strings.HasPrefix(rt, "go"):
			return "go", nil
		case strings.HasPrefix(rt, "python"):
			return "python", nil
		}
	}
	return "", fmt.Errorf("cannot detect language in %s", dir)
}

func findEntry(dir string, candidates []string) string {
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(dir, c)); err == nil {
			return c
		}
	}
	return ""
}

func hasBun() bool {
	_, err := exec.LookPath("bun")
	return err == nil
}

func runCmd(dir string, logWriter io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...) //nolint:gosec // developer build tool
	cmd.Dir = dir
	if logWriter != nil {
		cmd.Stdout = logWriter
		cmd.Stderr = logWriter
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

func GoUsesUnixSocket(srcDir string) bool {
	mainPath := filepath.Join(srcDir, "main.go")
	data, err := os.ReadFile(mainPath)
	if err != nil {
		return false
	}
	src := string(data)
	return strings.Contains(src, `net.Listen("unix"`) || strings.Contains(src, "net.Listen(\"unix\"")
}

func GoUsesHTTPHandler(srcDir string) bool {
	data, err := readGoEntry(srcDir)
	if err != nil {
		return false
	}
	src := string(data)
	return strings.Contains(src, "package handler") &&
		strings.Contains(src, "func HandleRequest(") &&
		strings.Contains(src, "http.ResponseWriter")
}

func GoUsesFiberHandler(srcDir string) bool {
	data, err := readGoEntry(srcDir)
	if err != nil {
		return false
	}
	src := string(data)
	return strings.Contains(src, "package handler") &&
		strings.Contains(src, "func HandleRequest(") &&
		strings.Contains(src, "*fiber.Ctx")
}

func GoUsesExecuteHandler(srcDir string) bool {
	data, err := readGoEntry(srcDir)
	if err != nil {
		return false
	}
	src := string(data)
	return strings.Contains(src, "package handler") &&
		strings.Contains(src, "func Execute(")
}

func GoExecuteParamCount(srcDir string) int {
	data, err := readGoEntry(srcDir)
	if err != nil {
		return 0
	}
	return countGoExecuteParams(string(data))
}

func countGoExecuteParams(src string) int {
	start := strings.Index(src, "func Execute(")
	if start < 0 {
		return 0
	}
	start += len("func Execute(")
	depth := 1
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				params := strings.TrimSpace(src[start:i])
				if params == "" {
					return 0
				}
				return strings.Count(params, ",") + 1
			}
		}
	}
	return 0
}

func readGoEntry(srcDir string) ([]byte, error) {
	entry := filepath.Join(srcDir, "main.go")
	return os.ReadFile(entry)
}

func functionConfig(srcDir string) map[string]string {
	fn, err := artifact.ParseFunction(srcDir)
	if err != nil || len(fn.Spec.Config) == 0 {
		return nil
	}
	config := make(map[string]string, len(fn.Spec.Config))
	for key, value := range fn.Spec.Config {
		config[key] = value
	}
	return config
}

func JSUsesUnixSocket(srcDir string) bool {
	data, err := readJSEntry(srcDir)
	if err != nil {
		return false
	}
	src := string(data)
	return strings.Contains(src, "SOCKET_PATH") || strings.Contains(src, "createServer((socket")
}

func JSUsesDirectServer(srcDir string) bool {
	data, err := readJSEntry(srcDir)
	if err != nil {
		return false
	}
	src := string(data)
	return strings.Contains(src, "app.all(\"/execute\"") ||
		strings.Contains(src, "app.all('/execute'") ||
		JSUsesUnixSocket(srcDir)
}

func JSUsesExpressHandler(srcDir string) bool {
	data, err := readJSEntry(srcDir)
	if err != nil {
		return false
	}
	src := string(data)
	return (strings.Contains(src, "function handleRequest(") || strings.Contains(src, "function handleRequest (")) &&
		!strings.Contains(src, "app.all(\"/execute\"") &&
		!strings.Contains(src, "app.all('/execute'")
}

func readJSEntry(srcDir string) ([]byte, error) {
	entry := findEntry(srcDir, []string{"handler.ts", "index.ts", "src/index.ts", "handler.js", "index.js", "src/index.js"})
	if entry == "" {
		return nil, fmt.Errorf("no JS entry file found in %s", srcDir)
	}
	return os.ReadFile(filepath.Join(srcDir, entry))
}

func writeExpressHandlerEntry(srcDir, outDir, entry string) (string, error) {
	entryPath := filepath.Join(srcDir, entry)
	data, err := os.ReadFile(entryPath)
	if err != nil {
		return "", err
	}
	wrappedPath := filepath.Join(outDir, "express-entry.mjs")
	if err := os.WriteFile(wrappedPath, []byte(string(data)+"\n\n"+expressServerTemplate), 0o644); err != nil {
		return "", err
	}
	return wrappedPath, nil
}

func ensureJSDevShims(outDir string) error {
	nodeModules := filepath.Join(outDir, "node_modules")
	if info, err := os.Lstat(nodeModules); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(nodeModules, "@cnips", "log", "package.json")); err == nil {
		return nil
	}
	logDir := filepath.Join(nodeModules, "@cnips", "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	packageJSON := `{"name":"@cnips/log","version":"0.0.0-local","main":"index.js","type":"commonjs"}`
	if err := os.WriteFile(filepath.Join(logDir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(logDir, "index.js"), []byte(cnipsLogShim), 0o644)
}

func ensureNodeModulesLink(srcDir, outDir string) error {
	srcNodeModules := filepath.Join(srcDir, "node_modules")
	if _, err := os.Stat(srcNodeModules); err != nil {
		return nil
	}
	dstNodeModules := filepath.Join(outDir, "node_modules")
	if _, err := os.Lstat(dstNodeModules); err == nil {
		return nil
	}
	if err := os.Symlink(srcNodeModules, dstNodeModules); err != nil {
		return fmt.Errorf("link node_modules into artifact cache: %w", err)
	}
	return nil
}

func ensureGoModuleSums(srcDir string, opts Options) error {
	if _, err := os.Stat(filepath.Join(srcDir, "go.mod")); err != nil {
		return nil
	}
	if err := runCmd(srcDir, opts.LogWriter, "go", "mod", "tidy"); err != nil {
		return fmt.Errorf("go mod tidy: %w", err)
	}
	return nil
}

func writeGoFunctionWrapper(srcDir, outDir string) (string, error) {
	wrappedDir := filepath.Join(outDir, "go-src")
	handlerDir := filepath.Join(wrappedDir, "handler")
	if err := os.RemoveAll(wrappedDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		return "", err
	}

	for _, file := range []string{"go.mod", "go.sum"} {
		src := filepath.Join(srcDir, file)
		data, err := os.ReadFile(src)
		if err != nil {
			if file == "go.sum" && os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if err := os.WriteFile(filepath.Join(wrappedDir, file), data, 0o644); err != nil {
			return "", err
		}
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, entry.Name()))
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(handlerDir, entry.Name()), data, 0o644); err != nil {
			return "", err
		}
	}

	main := goHTTPMainTemplate
	if GoUsesFiberHandler(srcDir) {
		main = goFiberMainTemplate
	}
	if err := os.WriteFile(filepath.Join(wrappedDir, "main.go"), []byte(main), 0o644); err != nil {
		return "", err
	}
	return wrappedDir, nil
}

func writeGoExecuteWrapper(srcDir, outDir string) (string, error) {
	wrappedDir := filepath.Join(outDir, "go-src")
	handlerDir := filepath.Join(wrappedDir, "handler")
	if err := os.RemoveAll(wrappedDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		return "", err
	}

	for _, file := range []string{"go.mod", "go.sum"} {
		src := filepath.Join(srcDir, file)
		data, err := os.ReadFile(src)
		if err != nil {
			if file == "go.sum" && os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if err := os.WriteFile(filepath.Join(wrappedDir, file), data, 0o644); err != nil {
			return "", err
		}
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, entry.Name()))
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(handlerDir, entry.Name()), data, 0o644); err != nil {
			return "", err
		}
	}

	modulePath, err := goModulePath(srcDir)
	if err != nil {
		return "", err
	}
	paramCount := GoExecuteParamCount(srcDir)
	if paramCount != 3 && paramCount != 4 {
		return "", fmt.Errorf("unsupported Go Execute signature in %s: expected 3 or 4 parameters, got %d", srcDir, paramCount)
	}
	main := goExecuteMainTemplate(modulePath, paramCount)
	if err := os.WriteFile(filepath.Join(wrappedDir, "main.go"), []byte(main), 0o644); err != nil {
		return "", err
	}
	return wrappedDir, nil
}

func goModulePath(srcDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(srcDir, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			module := strings.TrimSpace(strings.TrimPrefix(line, "module "))
			if module != "" {
				return module, nil
			}
		}
	}
	return "", fmt.Errorf("go.mod in %s does not declare a module", srcDir)
}

func fileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func dirDigest(dir string) (string, error) {
	h := sha256.New()
	h.Write([]byte(serverMJSTemplate))
	h.Write([]byte(runnerMJSTemplate))
	h.Write([]byte(expressServerTemplate))
	h.Write([]byte(cnipsLogShim))
	if GoUsesExecuteHandler(dir) {
		fmt.Fprintf(h, "go-execute-wrapper:v1:%d", GoExecuteParamCount(dir))
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// skip hidden dirs and node_modules
		if strings.Contains(path, "node_modules") || strings.Contains(path, ".git") {
			return filepath.SkipDir
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		h.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// -----------------------------------------------------------------------
// JS runtime wrappers
// -----------------------------------------------------------------------

// serverMJSTemplate is the HTTP server wrapper written alongside main.mjs.
// It wraps the user's execute(data, config, vars, LOG) export in /ping + /execute.
const serverMJSTemplate = `import * as component from './main.mjs';
import { createServer } from 'node:http';
import { format } from 'node:util';

const execute = component.execute || component.default?.execute || component.default;
if (typeof execute !== 'function') {
  throw new Error("Component must export an execute function or default function");
}

const port = parseInt(process.env.PORT || process.argv[2] || '0', 10);

const LOG = {
  info:  (...a) => process.stderr.write('[INFO]  ' + format(...a) + '\n'),
  debug: () => {},
  error: (...a) => process.stderr.write('[ERROR] ' + format(...a) + '\n'),
  warn:  (...a) => process.stderr.write('[WARN]  ' + format(...a) + '\n'),
};

function requestPath(req) {
  return new URL(req.url || '/', 'http://localhost').pathname.replace(/\/+$/, '') || '/';
}

const server = createServer(async (req, res) => {
  const path = requestPath(req);
  if (req.method === 'GET' && path === '/ping') {
    res.writeHead(200, { 'Content-Type': 'text/plain' });
    res.end('ok');
    return;
  }
  if (req.method === 'POST' && path === '/execute') {
    const chunks = [];
    req.on('data', d => chunks.push(d));
    req.on('end', async () => {
      try {
        const body = JSON.parse(Buffer.concat(chunks).toString() || '{}');
        // Support both raw payload and { data, config, vars } envelope
        const data   = body.data    !== undefined ? body.data    : body;
        const config = body.config  || {};
        const vars   = body.vars    || {};
        const result = await execute(data, config, vars, LOG);
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(result));
      } catch (e) {
        res.writeHead(500, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: e.message }));
      }
    });
    return;
  }
  res.writeHead(404); res.end(` + "`not found: ${path}`" + `);
});

server.listen(port, () => {});
`

// runnerMJSTemplate is the one-shot wrapper used by source/extractor steps.
// It calls execute(config, vars, LOG) and prints the result to stdout.
const runnerMJSTemplate = `import * as component from './main.mjs';
import { format } from 'node:util';

const execute = component.execute || component.default?.execute || component.default;
if (typeof execute !== 'function') {
  throw new Error("Component must export an execute function or default function");
}

const LOG = {
  info:  (...a) => process.stderr.write('[INFO]  ' + format(...a) + '\n'),
  debug: () => {},
  error: (...a) => process.stderr.write('[ERROR] ' + format(...a) + '\n'),
  warn:  (...a) => process.stderr.write('[WARN]  ' + format(...a) + '\n'),
};

const config = JSON.parse(process.env.CNIPS_CONFIG || '{}');
const vars   = JSON.parse(process.env.CNIPS_VARS   || '{}');

try {
  const result = await execute(config, vars, LOG);
  process.stdout.write(JSON.stringify(result));
} catch (e) {
  process.stderr.write('[ERROR] ' + e.message + '\n');
  process.exit(1);
}
`

const expressServerTemplate = `import { createServer } from 'node:http';
import fs from 'node:fs';

process.on('uncaughtException', (err) => {
  if (typeof LOG !== 'undefined' && LOG.error) LOG.error(` + "`Uncaught Exception: ${err.message}`" + `);
  fs.writeFileSync('error.log', ` + "`Uncaught Exception: ${err.message}\\n${err.stack}`" + `);
});
process.on('unhandledRejection', (reason, promise) => {
  if (typeof LOG !== 'undefined' && LOG.error) LOG.error(` + "`Unhandled Rejection at: ${promise}, reason: ${reason}`" + `);
  fs.writeFileSync('unhandledRejection.log', ` + "`Unhandled Rejection at: ${promise}, reason: ${reason}\\n`" + `);
});

const port = process.env.PORT || process.argv[2] || 3000;
const functionConfig = JSON.parse(process.env.CNIPS_FUNCTION_CONFIG || '{}');

function requestPath(req) {
  return new URL(req.url || '/', 'http://localhost').pathname.replace(/\/+$/, '') || '/';
}

function applyFunctionConfigHeaders(req) {
  for (const [key, value] of Object.entries(functionConfig)) {
    const headerKey = key.toLowerCase();
    if (req.headers[headerKey] === undefined) {
      req.headers[headerKey] = String(value);
    }
  }
}

function writeJSON(res, statusCode, value) {
  if (!res.headersSent) res.writeHead(statusCode, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(value));
}

function createResponse(res) {
  let statusCode = 200;
  return {
    get headersSent() { return res.headersSent; },
    status(code) { statusCode = code; return this; },
    json(value) { writeJSON(res, statusCode, value); return this; },
    send(value) {
      if (typeof value === 'object') return this.json(value);
      if (!res.headersSent) res.writeHead(statusCode, { 'Content-Type': 'text/plain' });
      res.end(String(value ?? ''));
      return this;
    },
  };
}

const server = createServer(async (req, rawRes) => {
  const path = requestPath(req);
  if (path === '/ping') {
    writeJSON(rawRes, 200, { status: 'ok' });
    return;
  }
  if (path !== '/execute') {
    rawRes.writeHead(404); rawRes.end(` + "`not found: ${path}`" + `);
    return;
  }

  const chunks = [];
  req.on('data', chunk => chunks.push(chunk));
  req.on('end', async () => {
    const reqId = req.headers['x-request-id'] || 'defaultRequestId';
    try {
      const rawBody = Buffer.concat(chunks).toString() || '{}';
      req.body = JSON.parse(rawBody);
      applyFunctionConfigHeaders(req);
      const res = createResponse(rawRes);
      await handleRequest(req, res);
      if (!rawRes.headersSent) writeJSON(rawRes, 204, null);
    } catch (err) {
      writeJSON(rawRes, 500, { error: err.message });
    } finally {
    if (typeof LOG === 'undefined' || !LOG.getLogs) return;
    fs.writeFileSync(reqId + '.log', LOG.getLogs().join('\n'));
    if (LOG.clearLogs) LOG.clearLogs();
    }
  });
});

server.listen(port, () => {
  if (typeof LOG !== 'undefined' && LOG.info) LOG.info('Server started on port %s', port);
});
`

const cnipsLogShim = `const { format } = require('node:util');

function logger() {
  const logs = [];
  const write = (level, args) => {
    const line = '[' + level + '] ' + format(...args);
    logs.push(line);
    process.stderr.write(line + '\n');
  };
  return {
    info: (...args) => write('INFO', args),
    debug: (...args) => write('DEBUG', args),
    warn: (...args) => write('WARN', args),
    error: (...args) => write('ERROR', args),
    getLogs: () => logs.slice(),
    clearLogs: () => { logs.length = 0; },
  };
}

module.exports = {
  logger,
  levels: { debug: 'debug', info: 'info', warn: 'warn', error: 'error' },
};
`

const goHTTPMainTemplate = `package main

import (
	"encoding/json"
	"fmt"
	"function/handler"
	"net/http"
	"os"
)

func main() {
	port := "3000"
	if len(os.Args) > 1 && os.Args[1] != "" {
		port = os.Args[1]
	}
	http.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("pong")) })
	http.HandleFunc("/execute", func(w http.ResponseWriter, r *http.Request) {
		applyFunctionConfigHeaders(r)
		handler.HandleRequest(w, r)
	})
	if err := http.ListenAndServe(fmt.Sprintf(":%%s", port), nil); err != nil {
		panic(err)
	}
}

func applyFunctionConfigHeaders(r *http.Request) {
	for key, value := range functionConfig() {
		if r.Header.Get(key) == "" {
			r.Header.Set(key, value)
		}
	}
}

func functionConfig() map[string]string {
	config := map[string]string{}
	_ = json.Unmarshal([]byte(os.Getenv("CNIPS_FUNCTION_CONFIG")), &config)
	return config
}
`

const goFiberMainTemplate = `package main

import (
	"encoding/json"
	"fmt"
	"function/handler"
	"os"

	"github.com/gofiber/fiber/v2"
)

func main() {
	port := "3000"
	if len(os.Args) > 1 && os.Args[1] != "" {
		port = os.Args[1]
	}
	app := fiber.New()
	app.Use("/ping", func(c *fiber.Ctx) error { return c.Send([]byte("pong")) })
	app.Use("/execute", func(c *fiber.Ctx) error {
		applyFunctionConfigHeaders(c)
		return handler.HandleRequest(c)
	})
	if err := app.Listen(fmt.Sprintf(":%s", port)); err != nil {
		panic(err)
	}
}

func applyFunctionConfigHeaders(c *fiber.Ctx) {
	for key, value := range functionConfig() {
		if c.Get(key) == "" {
			c.Request().Header.Set(key, value)
		}
	}
}

func functionConfig() map[string]string {
	config := map[string]string{}
	_ = json.Unmarshal([]byte(os.Getenv("CNIPS_FUNCTION_CONFIG")), &config)
	return config
}
`

func goExecuteMainTemplate(modulePath string, paramCount int) string {
	executeHTTP := `result, err := handler.Execute(data, config, vars, logger)`
	executeOneShot := `result, err := handler.Execute(config, vars, logger)`
	if paramCount == 4 {
		executeOneShot = `result, err := handler.Execute("", config, vars, logger)`
	} else {
		executeHTTP = `result, err := handler.Execute(config, vars, logger)`
	}
	return fmt.Sprintf(`package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"%s/handler"
	"github.com/zinscky/log"
)

type executeEnvelope struct {
	Data   json.RawMessage     `+"`json:\"data\"`"+`
	Config map[string]string   `+"`json:\"config\"`"+`
	Vars   map[string]string   `+"`json:\"vars\"`"+`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--oneshot" {
		if err := runOneShot(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	port := "3000"
	if len(os.Args) > 1 && os.Args[1] != "" {
		port = os.Args[1]
	}
	http.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("pong")) })
	http.HandleFunc("/execute", handleExecute)
	if err := http.ListenAndServe(fmt.Sprintf(":%%s", port), nil); err != nil {
		panic(err)
	}
}

func runOneShot() error {
	config := stringMapFromJSON(os.Getenv("CNIPS_CONFIG"))
	vars := stringMapFromJSON(os.Getenv("CNIPS_VARS"))
	logger := log.New(log.Info, "cnips")
	%s
	if err != nil {
		return err
	}
	flushLogs(logger)
	return writeResult(os.Stdout, result)
}

func handleExecute(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	var envelope executeEnvelope
	if len(body) > 0 {
		_ = json.Unmarshal(body, &envelope)
	}
	data := "{}"
	if len(envelope.Data) > 0 {
		data = string(envelope.Data)
	}
	_ = data
	logger := log.New(log.Info, "cnips")
	config := envelope.Config
	if config == nil {
		config = map[string]string{}
	}
	vars := envelope.Vars
	if vars == nil {
		vars = map[string]string{}
	}
	%s
	if err != nil {
		writeError(w, err)
		return
	}
	flushLogs(logger)
	w.Header().Set("Content-Type", "application/json")
	if err := writeResult(w, result); err != nil {
		writeError(w, err)
	}
}

func stringMapFromJSON(raw string) map[string]string {
	out := map[string]string{}
	if raw == "" {
		return out
	}
	var stringsOnly map[string]string
	if err := json.Unmarshal([]byte(raw), &stringsOnly); err == nil {
		return stringsOnly
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return out
	}
	for key, value := range values {
		out[key] = fmt.Sprint(value)
	}
	return out
}

func writeResult(w io.Writer, result any) error {
	switch value := result.(type) {
	case string:
		_, err := io.WriteString(w, value)
		return err
	default:
		return json.NewEncoder(w).Encode(value)
	}
}

func writeError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func flushLogs(logger *log.Logger) {
	if logger == nil {
		return
	}
	if logs := logger.String(); logs != "" {
		fmt.Fprintln(os.Stderr, logs)
	}
}
`, modulePath, executeOneShot, executeHTTP)
}

// EnsureJSWrappers writes server.mjs and runner.mjs into outDir and returns
// their paths. It is also used when loading older cached JS artifacts that
// predate the wrapper files.
func EnsureJSWrappers(outDir string) (string, string, error) {
	serverScript := filepath.Join(outDir, "server.mjs")
	runnerScript := filepath.Join(outDir, "runner.mjs")
	if err := os.WriteFile(serverScript, []byte(serverMJSTemplate), 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(runnerScript, []byte(runnerMJSTemplate), 0o644); err != nil {
		return "", "", err
	}
	return serverScript, runnerScript, nil
}

const pythonWrapperTemplate = `import asyncio
import importlib.util
import inspect
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse


class Logger:
    def _write(self, level, message, *args):
        text = str(message)
        for arg in args:
            text = text.replace("{}", str(arg), 1)
        if args and "{}" not in str(message):
            text = text + " " + " ".join(str(arg) for arg in args)
        print(f"[{level}] {text}", file=sys.stderr)

    def info(self, message, *args):
        self._write("INFO", message, *args)

    def debug(self, message, *args):
        return None

    def warn(self, message, *args):
        self._write("WARN", message, *args)

    def error(self, message, *args):
        self._write("ERROR", message, *args)


def _load_execute():
    path = os.path.join(os.path.dirname(__file__), "main.py")
    spec = importlib.util.spec_from_file_location("cnips_component_main", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.execute


async def _call_execute(execute, args):
    result = execute(*args)
    if inspect.isawaitable(result):
        result = await result
    return result


def _json_default(value):
    if hasattr(value, "isoformat"):
        return value.isoformat()
    return str(value)


def _run(coro):
    return asyncio.run(coro)


def _source_args(execute, config, vars, logger):
    params = list(inspect.signature(execute).parameters)
    if len(params) <= 2:
        return [config, logger]
    return [config, vars, logger]


def _component_args(execute, event, config, vars, logger):
    params = list(inspect.signature(execute).parameters)
    if len(params) <= 2:
        return [config, logger]
    if len(params) == 3:
        return [event, config, logger]
    return [event, config, vars, logger]
`

const pythonRunnerTemplate = pythonWrapperTemplate + `
def main():
    execute = _load_execute()
    config = json.loads(os.environ.get("CNIPS_CONFIG", "{}"))
    vars = json.loads(os.environ.get("CNIPS_VARS", "{}"))
    logger = Logger()
    result = _run(_call_execute(execute, _source_args(execute, config, vars, logger)))
    sys.stdout.write(json.dumps(result, default=_json_default))


if __name__ == "__main__":
    main()
`

const pythonServerTemplate = pythonWrapperTemplate + `
execute = _load_execute()
logger = Logger()
function_config = json.loads(os.environ.get("CNIPS_FUNCTION_CONFIG", "{}"))


class Handler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        return None

    def do_GET(self):
        if self.path == "/ping":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
            return
        self._handle_execute()

    def do_POST(self):
        self._handle_execute()

    def do_PUT(self):
        self._handle_execute()

    def do_DELETE(self):
        self._handle_execute()

    def _handle_execute(self):
        if urlparse(self.path).path != "/execute":
            self.send_response(404)
            self.end_headers()
            return

        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length).decode("utf-8")
        try:
            body = json.loads(raw) if raw else {}
            if isinstance(body, dict) and ("data" in body or "config" in body or "vars" in body):
                event = body.get("data", {})
                config = body.get("config", function_config)
                vars = body.get("vars", {})
            else:
                event = body
                config = function_config
                vars = {}
            result = _run(_call_execute(execute, _component_args(execute, event, config, vars, logger)))
            if result is None:
                result = event
            response = json.dumps(result, default=_json_default).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(response)
        except Exception as exc:
            response = json.dumps({"error": str(exc)}).encode("utf-8")
            self.send_response(500)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(response)


def main():
    port = int(sys.argv[1] if len(sys.argv) > 1 else os.environ.get("PORT", "8080"))
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
`

// EnsurePythonWrappers writes server.py and runner.py into outDir and returns
// their paths. The wrappers import main.py and adapt Python template signatures.
func EnsurePythonWrappers(outDir string) (string, string, error) {
	serverScript := filepath.Join(outDir, "server.py")
	runnerScript := filepath.Join(outDir, "runner.py")
	if err := os.WriteFile(serverScript, []byte(pythonServerTemplate), 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(runnerScript, []byte(pythonRunnerTemplate), 0o644); err != nil {
		return "", "", err
	}
	return serverScript, runnerScript, nil
}
