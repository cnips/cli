package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type projectWriteLock struct {
	file *os.File
}

func acquireProjectWriteLock(root, operation string) (*projectWriteLock, error) {
	lockDir := filepath.Join(root, ".cnips")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, fmt.Errorf("prepare project lock for %s: %w", operation, err)
	}
	file, err := os.OpenFile(filepath.Join(lockDir, "project.write.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open project lock for %s: %w", operation, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire project lock for %s: %w", operation, err)
	}
	return &projectWriteLock{file: file}, nil
}

func (l *projectWriteLock) Release(operation string) error {
	if l == nil || l.file == nil {
		return nil
	}
	var unlockErr error
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		unlockErr = fmt.Errorf("release project lock for %s: %w", operation, err)
	}
	if err := l.file.Close(); err != nil && unlockErr == nil {
		unlockErr = fmt.Errorf("close project lock for %s: %w", operation, err)
	}
	l.file = nil
	return unlockErr
}
