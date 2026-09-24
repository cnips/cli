package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// runWithLoader keeps noisy, long-running command internals out of the user's
// terminal while preserving their diagnostics if the operation fails.
func runWithLoader(label string, showSuccessOutput bool, fn func() error) error {
	stdout, stderr := os.Stdout, os.Stderr
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		return fn()
	}
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		_ = outRead.Close()
		_ = outWrite.Close()
		return fn()
	}
	os.Stdout, os.Stderr = outWrite, errWrite

	var outBuf, errBuf bytes.Buffer
	var drains sync.WaitGroup
	drains.Add(2)
	go func() { defer drains.Done(); _, _ = io.Copy(&outBuf, outRead) }()
	go func() { defer drains.Done(); _, _ = io.Copy(&errBuf, errRead) }()

	done := make(chan struct{})
	rendered := make(chan struct{})
	go func() {
		renderIndeterminateBar(stdout, label, done)
		close(rendered)
	}()
	runErr := fn()
	close(done)
	<-rendered
	os.Stdout, os.Stderr = stdout, stderr
	_ = outWrite.Close()
	_ = errWrite.Close()
	drains.Wait()
	_ = outRead.Close()
	_ = errRead.Close()

	if runErr != nil {
		if outBuf.Len() > 0 {
			_, _ = stdout.Write(outBuf.Bytes())
		}
		if errBuf.Len() > 0 {
			_, _ = stderr.Write(errBuf.Bytes())
		}
		return runErr
	}
	if showSuccessOutput && outBuf.Len() > 0 {
		_, _ = stdout.Write(outBuf.Bytes())
	}
	return nil
}

func renderIndeterminateBar(w *os.File, label string, done <-chan struct{}) {
	ticker := time.NewTicker(90 * time.Millisecond)
	defer ticker.Stop()
	const width = 24
	position := 0
	render := func(finished bool) {
		if finished {
			fmt.Fprintf(w, "\r%s [%s] 100%%\n", label, "========================")
			return
		}
		bar := make([]byte, width)
		for i := range bar {
			bar[i] = ' '
		}
		for i := 0; i < 5; i++ {
			bar[(position+i)%width] = '='
		}
		fmt.Fprintf(w, "\r%s [%s]", label, string(bar))
		position = (position + 1) % width
	}
	render(false)
	for {
		select {
		case <-done:
			render(true)
			return
		case <-ticker.C:
			render(false)
		}
	}
}
