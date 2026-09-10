// Package fnrunner manages local function and component processes.
// Each process exposes /ping and /execute over HTTP, just as in cloud execution.
package fnrunner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cnips/cli/internal/builder"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Manager owns all running local processes.
type Manager struct {
	mu       sync.Mutex
	services map[string]*ServiceInfo
	logs     io.Writer
}

func NewManager() *Manager {
	return NewManagerWithLogs(nil)
}

func NewManagerWithLogs(logs io.Writer) *Manager {
	return &Manager{services: make(map[string]*ServiceInfo), logs: logs}
}

// ServiceInfo holds a running component/function process.
type ServiceInfo struct {
	Name        string
	Port        int
	SocketPath  string
	Cmd         *exec.Cmd
	cleanupOnce sync.Once
}

// Execute POSTs the payload to the /execute endpoint of the named service.
// If the service is not running, it is launched first.
func (m *Manager) Execute(name string, build *builder.Result, payload string) (string, error) {
	si, err := m.ensureRunning(name, build)
	if err != nil {
		return "", err
	}
	if build.Protocol == "unix" {
		return executeUnixSocket(si.SocketPath, payload)
	}
	return execute(si.Port, payload)
}

func (m *Manager) ensureRunning(name string, build *builder.Result) (*ServiceInfo, error) {
	m.mu.Lock()
	if si, ok := m.services[name]; ok {
		m.mu.Unlock()
		return si, nil
	}
	m.mu.Unlock()

	si, err := m.launch(name, build)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.services[name] = si
	m.mu.Unlock()
	return si, nil
}

func (m *Manager) launch(name string, build *builder.Result) (*ServiceInfo, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}

	portStr := fmt.Sprintf("%d", port)
	var cmd *exec.Cmd
	socketPath := ""

	switch build.Language {
	case "go":
		if build.Protocol == "unix" {
			socketPath = filepath.Join(build.OutDir, name+".sock")
			_ = os.Remove(socketPath)
			cmd = exec.Command(build.Executable, socketPath) //nolint:gosec
		} else {
			cmd = exec.Command(build.Executable, portStr) //nolint:gosec
		}
	case "javascript":
		if build.Protocol == "unix" {
			socketPath = filepath.Join(build.OutDir, name+".sock")
			_ = os.Remove(socketPath)
		}
		// Use server.mjs wrapper when available; fall back to main.mjs.
		script := build.ServerScript
		if script == "" {
			script = build.Executable
		}
		if build.IsBun {
			if build.Protocol == "unix" {
				cmd = exec.Command("bun", "run", script, socketPath) //nolint:gosec
			} else {
				cmd = exec.Command("bun", "run", script, portStr) //nolint:gosec
			}
		} else {
			if build.Protocol == "unix" {
				cmd = exec.Command("node", script, socketPath) //nolint:gosec
			} else {
				cmd = exec.Command("node", script, portStr) //nolint:gosec
			}
		}
	case "python":
		script := build.ServerScript
		if script == "" {
			script = pythonScriptFromExecutable(build.Executable)
		}
		python := pythonBinaryFromExecutable(build.Executable)
		if script != "" {
			cmd = exec.Command(python, script, portStr) //nolint:gosec
		} else {
			cmd = exec.Command(python, portStr) //nolint:gosec
		}
	default:
		return nil, fmt.Errorf("unsupported language %q", build.Language)
	}

	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port), fmt.Sprintf("APP=%s", name))
	if socketPath != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("SOCKET_PATH=%s", socketPath))
	}
	attachCommandLogs(cmd, m.logs)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}

	si := &ServiceInfo{Name: name, Port: port, SocketPath: socketPath, Cmd: cmd}

	if err := waitServiceReady(si, build.Protocol, 15*time.Second); err != nil {
		si.Stop()
		return nil, fmt.Errorf("service %s did not become ready: %w", name, err)
	}

	if m.logs != nil {
		fmt.Fprintf(m.logs, "[runner] %s started on port %d (pid %d)\n", name, port, cmd.Process.Pid)
	} else {
		fmt.Printf("  [runner] %s started on port %d (pid %d)\n", name, port, cmd.Process.Pid)
	}
	return si, nil
}

// Stop terminates the service process.
func (s *ServiceInfo) Stop() {
	s.cleanupOnce.Do(func() {
		if s.Cmd.Process != nil {
			pgid, err := syscall.Getpgid(s.Cmd.Process.Pid)
			if err == nil {
				_ = syscall.Kill(-pgid, syscall.SIGTERM)
				time.Sleep(500 * time.Millisecond)
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
			_ = s.Cmd.Process.Kill()
		}
	})
}

// StopAll terminates all running services.
func (m *Manager) StopAll() {
	m.mu.Lock()
	svc := make([]*ServiceInfo, 0, len(m.services))
	for _, si := range m.services {
		svc = append(svc, si)
	}
	m.mu.Unlock()
	for _, si := range svc {
		si.Stop()
	}
}

// StartForDev starts a service and keeps it running until ctx is cancelled.
// It also starts a file watcher that rebuilds on source changes.
func (m *Manager) StartDev(name string, buildFn func() (*builder.Result, error)) (*ServiceInfo, error) {
	build, err := buildFn()
	if err != nil {
		return nil, err
	}
	return m.launch(name, build)
}

// -----------------------------------------------------------------------
// HTTP helpers
// -----------------------------------------------------------------------

func execute(port int, payload string) (string, error) {
	url := fmt.Sprintf("http://localhost:%d/execute", port)

	// Wrap in { "data": ..., "config": {}, "vars": {} } envelope if not already.
	var reqBody string
	if strings.HasPrefix(strings.TrimSpace(payload), "{") {
		reqBody = payload
	} else {
		b, _ := json.Marshal(payload)
		reqBody = string(b)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBufferString(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("execute returned %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

func executeUnixSocket(socketPath, payload string) (string, error) {
	conn, err := net.DialTimeout("unix", socketPath, 30*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	req, err := pluginRequest(payload)
	if err != nil {
		return "", err
	}
	if _, err := conn.Write([]byte(req + "\n")); err != nil {
		return "", err
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", err
	}
	return pluginResponseEvent(line)
}

// ExecuteOnPort is exported for direct use by the runtime engine.
func ExecuteOnPort(port int, payload string) (string, error) {
	return execute(port, payload)
}

func pluginRequest(payload string) (string, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		body = map[string]any{"data": payload}
	}

	data := body
	if v, ok := body["data"]; ok {
		data = map[string]any{}
		switch typed := v.(type) {
		case map[string]any:
			data = typed
		default:
			data["value"] = typed
		}
	}
	eventBytes, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	req := map[string]any{
		"event":  string(eventBytes),
		"config": stringifyMap(body["config"]),
		"vars":   stringifyMap(body["vars"]),
		"app":    body["app"],
	}
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func pluginResponseEvent(line string) (string, error) {
	var resp struct {
		Event string            `json:"event"`
		Vars  map[string]string `json:"vars"`
		Error string            `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", errors.New(resp.Error)
	}
	if resp.Event == "" {
		return "{}", nil
	}
	return resp.Event, nil
}

func stringifyMap(value any) map[string]string {
	out := map[string]string{}
	values, ok := value.(map[string]any)
	if !ok {
		return out
	}
	for k, v := range values {
		switch typed := v.(type) {
		case string:
			out[k] = typed
		default:
			b, _ := json.Marshal(typed)
			out[k] = string(b)
		}
	}
	return out
}

// ExecuteOneShot runs a source/extractor component once in one-shot mode
// (no HTTP server). It imports the bundled code through runner.mjs and
// captures stdout as the result. config and vars are serialised as JSON
// and passed via CNIPS_CONFIG / CNIPS_VARS environment variables.
func ExecuteOneShot(name string, build *builder.Result, config, vars map[string]any) (string, error) {
	return ExecuteOneShotWithLogs(name, build, config, vars, nil)
}

func ExecuteOneShotWithLogs(name string, build *builder.Result, config, vars map[string]any, logs io.Writer) (string, error) {
	script := build.RunnerScript
	if script == "" && build.Language == "python" {
		script = pythonScriptFromExecutable(build.Executable)
	}
	if script == "" {
		return "", fmt.Errorf("no runner script for %s (language=%s)", name, build.Language)
	}

	configJSON, _ := json.Marshal(config)
	varsJSON, _ := json.Marshal(vars)

	var cmd *exec.Cmd
	switch build.Language {
	case "javascript":
		if build.IsBun {
			cmd = exec.Command("bun", "run", script) //nolint:gosec
		} else {
			cmd = exec.Command("node", script) //nolint:gosec
		}
	case "python":
		cmd = exec.Command(pythonBinaryFromExecutable(build.Executable), script) //nolint:gosec
	case "go":
		cmd = exec.Command(script, "--oneshot") //nolint:gosec
	default:
		return "", fmt.Errorf("one-shot runner is not supported for language %s", build.Language)
	}
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("CNIPS_CONFIG=%s", configJSON),
		fmt.Sprintf("CNIPS_VARS=%s", varsJSON),
	)
	if logs != nil {
		cmd.Stderr = logs
	} else {
		cmd.Stderr = os.Stderr
	}

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("one-shot %s failed: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func pythonBinaryFromExecutable(executable string) string {
	parts := strings.SplitN(executable, " ", 2)
	if parts[0] != "" {
		return parts[0]
	}
	return "python3"
}

func pythonScriptFromExecutable(executable string) string {
	parts := strings.SplitN(executable, " ", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}

func waitReady(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://localhost:%d/ping", port)
	for time.Now().Before(deadline) {
		resp, err := httpClient.Get(url) //nolint:noctx
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for port %d", port)
}

func waitServiceReady(si *ServiceInfo, protocol string, timeout time.Duration) error {
	if protocol != "unix" {
		return waitReady(si.Port, timeout)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", si.SocketPath, 300*time.Millisecond)
		if err == nil {
			_, _ = conn.Write([]byte("PING:cnips\n"))
			_ = conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for socket %s", si.SocketPath)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// RunDevServer starts the local component source directly — for `cnips fn dev`.
// It launches the source file with the appropriate runtime without building first.
func RunDevServer(name, srcDir, lang string) (*ServiceInfo, error) {
	return RunDevServerWithLogs(name, srcDir, lang, nil)
}

func RunDevServerWithLogs(name, srcDir, lang string, logs io.Writer) (*ServiceInfo, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	portStr := fmt.Sprintf("%d", port)

	var cmd *exec.Cmd
	switch lang {
	case "javascript":
		entry := findEntry(srcDir, []string{"handler.ts", "index.ts", "src/index.ts", "handler.js", "index.js"})
		if entry == "" {
			return nil, fmt.Errorf("no JS entry found in %s", srcDir)
		}
		if hasBun() {
			cmd = exec.Command("bun", "run", filepath.Join(srcDir, entry), portStr) //nolint:gosec
		} else {
			cmd = exec.Command("node", filepath.Join(srcDir, entry), portStr) //nolint:gosec
		}
	case "go":
		// build in place then run
		outFile := filepath.Join(os.TempDir(), "cnips-dev-"+name)
		buildCmd := exec.Command("go", "build", "-o", outFile, ".")
		buildCmd.Dir = srcDir
		attachCommandLogs(buildCmd, logs)
		if err := buildCmd.Run(); err != nil {
			return nil, fmt.Errorf("go build: %w", err)
		}
		cmd = exec.Command(outFile, portStr) //nolint:gosec
	case "python":
		entry := findEntry(srcDir, []string{"handler.py", "main.py"})
		if entry == "" {
			return nil, fmt.Errorf("no Python entry found in %s", srcDir)
		}
		cmd = exec.Command("python3", filepath.Join(srcDir, entry), portStr) //nolint:gosec
	default:
		return nil, fmt.Errorf("unsupported language %s", lang)
	}

	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port), fmt.Sprintf("APP=%s", name))
	attachCommandLogs(cmd, logs)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := waitReady(port, 15*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	return &ServiceInfo{Name: name, Port: port, Cmd: cmd}, nil
}

func attachCommandLogs(cmd *exec.Cmd, logs io.Writer) {
	if logs != nil {
		cmd.Stdout = logs
		cmd.Stderr = logs
		return
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
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
