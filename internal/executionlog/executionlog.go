package executionlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxPreservedExecutions = 5

type Execution struct {
	ID      string
	Dir     string
	LogPath string
	File    *os.File
}

func Start(baseDir string) (*Execution, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}
	id := "execution-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	dir := filepath.Join(baseDir, id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, err
	}
	if err := pruneExecutions(baseDir, dir, maxPreservedExecutions); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	logPath := filepath.Join(dir, id+".log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	fmt.Fprintf(file, "execution: %s\n\n", id)
	return &Execution{ID: id, Dir: dir, LogPath: logPath, File: file}, nil
}

func (e *Execution) Close() error {
	if e == nil || e.File == nil {
		return nil
	}
	return e.File.Close()
}

type executionDir struct {
	path    string
	name    string
	modTime time.Time
}

func pruneExecutions(baseDir, currentDir string, max int) error {
	if max <= 0 {
		return nil
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return err
	}

	executions := make([]executionDir, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "execution-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		executions = append(executions, executionDir{
			path:    filepath.Join(baseDir, entry.Name()),
			name:    entry.Name(),
			modTime: info.ModTime(),
		})
	}
	if len(executions) <= max {
		return nil
	}

	sort.Slice(executions, func(i, j int) bool {
		if executions[i].modTime.Equal(executions[j].modTime) {
			return executions[i].name < executions[j].name
		}
		return executions[i].modTime.Before(executions[j].modTime)
	})

	currentDir = filepath.Clean(currentDir)
	removeCount := len(executions) - max
	for _, execution := range executions {
		if removeCount == 0 {
			break
		}
		if filepath.Clean(execution.path) == currentDir {
			continue
		}
		if err := os.RemoveAll(execution.path); err != nil {
			return err
		}
		removeCount--
	}
	return nil
}
