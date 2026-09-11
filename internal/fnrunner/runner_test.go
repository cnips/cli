package fnrunner

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteUsesMethodAndCollectsRequestLog(t *testing.T) {
	logDir := t.TempDir()
	var logs strings.Builder
	errCh := make(chan error, 1)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			errCh <- fmt.Errorf("method = %q, want DELETE", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Client-ID") != "from-config" {
			errCh <- fmt.Errorf("Client-ID = %q, want from-config", r.Header.Get("Client-ID"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			errCh <- fmt.Errorf("missing X-Request-ID")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(filepath.Join(logDir, requestID+".log"), []byte("[INFO] collected\n"), 0o644); err != nil {
			errCh <- fmt.Errorf("write request log: %w", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		errCh <- nil
		fmt.Fprint(w, `{"ok":true}`)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	got, err := execute(port, "DELETE", `{"data":{"id":1},"config":{"Client-ID":"from-config"}}`, logDir, &logs, true)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got != `{"ok":true}` {
		t.Fatalf("response = %q", got)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "[INFO] collected") {
		t.Fatalf("logs = %q, want collected request log", logs.String())
	}
}

func TestExecuteUsesExplicitHeadersBeforeConfig(t *testing.T) {
	errCh := make(chan error, 1)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Client-ID"); got != "from-headers" {
			errCh <- fmt.Errorf("Client-ID = %q, want from-headers", got)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		errCh <- nil
		fmt.Fprint(w, `{"ok":true}`)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	_, err = execute(port, "POST", `{"headers":{"Client-ID":"from-headers"},"config":{"Client-ID":"from-config"}}`, "", nil, true)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestExecuteRequiresHeadersOrConfigForFunctionExecution(t *testing.T) {
	_, err := execute(1, "POST", `{"data":{"id":1}}`, "", nil, true)
	if !errors.Is(err, errMissingForwardHeaders) {
		t.Fatalf("error = %v, want errMissingForwardHeaders", err)
	}
}
