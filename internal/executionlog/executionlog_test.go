package executionlog

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStartPrunesOldestExecutionFolders(t *testing.T) {
	baseDir := t.TempDir()
	startedAt := time.Now().Add(-time.Hour)
	for i := 1; i <= maxPreservedExecutions; i++ {
		name := "execution-old-" + strconv.Itoa(i)
		dir := filepath.Join(baseDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create old execution dir: %v", err)
		}
		modTime := startedAt.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(dir, modTime, modTime); err != nil {
			t.Fatalf("set old execution time: %v", err)
		}
	}

	keepDir := filepath.Join(baseDir, "notes")
	if err := os.MkdirAll(keepDir, 0o755); err != nil {
		t.Fatalf("create non-execution dir: %v", err)
	}

	execution, err := Start(baseDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := execution.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(filepath.Join(baseDir, "execution-old-1")); !os.IsNotExist(err) {
		t.Fatalf("oldest execution dir still exists or stat failed with unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "execution-old-2")); err != nil {
		t.Fatalf("second-oldest execution dir was removed unexpectedly: %v", err)
	}
	if _, err := os.Stat(execution.Dir); err != nil {
		t.Fatalf("new execution dir missing: %v", err)
	}
	if _, err := os.Stat(keepDir); err != nil {
		t.Fatalf("non-execution dir was removed: %v", err)
	}

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	executionCount := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "execution-") {
			executionCount++
		}
	}
	if executionCount != maxPreservedExecutions {
		t.Fatalf("execution dir count = %d, want %d", executionCount, maxPreservedExecutions)
	}
}
