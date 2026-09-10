package cli

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/builder"
	"github.com/cnips/cli/internal/executionlog"
	"github.com/cnips/cli/internal/fnrunner"
	"github.com/cnips/cli/internal/project"
)

var fnCmd = &cobra.Command{
	Use:   "fn",
	Short: "Function development commands",
}

var fnDevCmd = &cobra.Command{
	Use:   "dev <function-name>",
	Short: "Run a function locally with auto-rebuild on file changes",
	Long: `Starts a local HTTP server for the named function and watches source files.
The function is rebuilt and restarted whenever a source file changes.

The server exposes:
  GET  /ping      health check (returns 200 ok)
  POST /execute   execute the function with a JSON body

Uses bun (preferred) or node for JavaScript/TypeScript, go for Go, python3 for Python.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFunctionDev(args[0])
	},
}

var funCmd = &cobra.Command{
	Use:   "fun <function-name>",
	Short: "Run a function locally with auto-rebuild on file changes",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFunctionDev(args[0])
	},
}

func init() {
	fnCmd.AddCommand(fnDevCmd)
	rootCmd.AddCommand(fnCmd)
	rootCmd.AddCommand(funCmd)
}

func runFunctionDev(fnName string) error {
	root := project.MustFindRoot()
	if err := project.EnsureDirs(root); err != nil {
		return err
	}

	fnDir := filepath.Join(root, "functions", fnName)
	if _, err := os.Stat(fnDir); os.IsNotExist(err) {
		return fmt.Errorf("function %q not found at %s\nCreate it with: mkdir -p functions/%s", fnName, fnDir, fnName)
	}

	lang, err := detectFnLanguage(fnDir, fnName, root)
	if err != nil {
		return err
	}

	fmt.Printf("Starting function %q  [lang=%s]\n", fnName, lang)
	fmt.Printf("Source: %s\n\n", fnDir)

	execLog, err := executionlog.Start(fnDir)
	if err != nil {
		return fmt.Errorf("create execution log: %w", err)
	}
	defer execLog.Close()
	fmt.Printf("Logs: %s\n\n", execLog.LogPath)

	si, err := startFunctionDevServer(root, fnName, fnDir, lang, execLog.File)
	if err != nil {
		return fmt.Errorf("start function: %w", err)
	}

	fmt.Printf("  Function %q is ready on http://localhost:%d\n", fnName, si.Port)
	fmt.Printf("  POST http://localhost:%d/execute  with JSON body\n", si.Port)
	fmt.Printf("  GET  http://localhost:%d/ping\n\n", si.Port)

	watch, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watch.Close()

	if err := watchDir(watch, fnDir); err != nil {
		return err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	currentSI := si
	for {
		select {
		case sig := <-sigCh:
			fmt.Printf("\nReceived %v, shutting down...\n", sig)
			currentSI.Stop()
			return nil

		case event, ok := <-watch.Events:
			if !ok {
				return nil
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove) == 0 {
				continue
			}
			if isIgnored(event.Name) {
				continue
			}
			fmt.Printf("  [watch] %s changed — rebuilding...\n", event.Name)

			currentSI.Stop()

			newSI, err := startFunctionDevServer(root, fnName, fnDir, lang, execLog.File)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  restart error: %v\n", err)
				continue
			}
			currentSI = newSI
			fmt.Printf("  [watch] function %q restarted on port %d\n", fnName, newSI.Port)

		case err := <-watch.Errors:
			fmt.Fprintf(os.Stderr, "  watcher error: %v\n", err)
		}
	}
}

func startFunctionDevServer(root, fnName, fnDir, lang string, logs *os.File) (*fnrunner.ServiceInfo, error) {
	if lang == "javascript" || lang == "go" {
		build, err := builder.BuildWithOptions(root, fnName, fnDir, builder.Options{LogWriter: logs})
		if err != nil {
			return nil, err
		}
		_ = builder.SaveDigest(root, fnName, fnDir)
		mgr := fnrunner.NewManagerWithLogs(logs)
		return mgr.StartDev(fnName, func() (*builder.Result, error) {
			return build, nil
		})
	}
	return fnrunner.RunDevServerWithLogs(fnName, fnDir, lang, logs)
}

func detectFnLanguage(fnDir, fnName, root string) (string, error) {
	// try manifest first
	fn, err := artifact.ParseFunction(fnDir)
	if err == nil && fn.Spec.Runtime != "" {
		rt := fn.Spec.Runtime
		switch {
		case rt == "go" || len(rt) > 2 && rt[:2] == "go":
			return "go", nil
		case rt == "python" || len(rt) > 6 && rt[:6] == "python":
			return "python", nil
		default:
			return "javascript", nil
		}
	}

	// fallback: file detection
	for _, f := range []struct{ file, lang string }{
		{"go.mod", "go"},
		{"main.go", "go"},
		{"requirements.txt", "python"},
		{"handler.py", "python"},
		{"main.py", "python"},
		{"package.json", "javascript"},
		{"handler.ts", "javascript"},
		{"handler.js", "javascript"},
		{"index.ts", "javascript"},
		{"index.js", "javascript"},
	} {
		if _, err := os.Stat(filepath.Join(fnDir, f.file)); err == nil {
			return f.lang, nil
		}
	}

	return "", fmt.Errorf("cannot detect language for function %q in %s", fnName, fnDir)
}

func watchDir(w *fsnotify.Watcher, dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if isIgnoredDir(d.Name()) {
				return filepath.SkipDir
			}
			return w.Add(path)
		}
		return nil
	})
}

func isIgnored(name string) bool {
	for _, suffix := range []string{".log", ".tmp", "~"} {
		if len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix {
			return true
		}
	}
	return false
}

func isIgnoredDir(name string) bool {
	for _, d := range []string{"node_modules", ".git", ".cnips", "__pycache__", ".venv", "dist"} {
		if name == d {
			return true
		}
	}
	return false
}
