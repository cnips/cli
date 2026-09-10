package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/builder"
	"github.com/cnips/cli/internal/project"
)

var buildAll bool

var buildCmd = &cobra.Command{
	Use:   "build [name]",
	Short: "Build local components and functions",
	Long: `Compiles local components, functions, sources, and destinations.

Without arguments, builds everything that has changed since the last build.
Pass a component or function name to build a single target.

Supported runtimes:
  JavaScript / TypeScript   bun build (preferred) or fallback copy
  Go                        go build
  Python                    venv + pip install`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := project.MustFindRoot()
		if err := project.EnsureDirs(root); err != nil {
			return err
		}

		targets, err := collectBuildTargets(root, args)
		if err != nil {
			return err
		}

		if len(targets) == 0 {
			fmt.Println("Nothing to build.")
			return nil
		}

		built := 0
		skipped := 0
		for _, t := range targets {
			if !buildAll && !builder.NeedsBuild(root, t.name, t.dir) {
				if flagVerbose(cmd) {
					fmt.Printf("  skip %-30s (up to date)\n", t.name)
				}
				skipped++
				continue
			}
			fmt.Printf("  build %-28s [%s]...\n", t.name, t.kind)
			result, err := builder.Build(root, t.name, t.dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ERROR building %s: %v\n", t.name, err)
				continue
			}
			_ = builder.SaveDigest(root, t.name, t.dir)
			fmt.Printf("        %-28s → %s\n", "", result.Executable)
			built++
		}

		fmt.Printf("\nDone. built=%d  skipped=%d\n", built, skipped)
		return nil
	},
}

func init() {
	buildCmd.Flags().BoolVarP(&buildAll, "all", "a", false, "Force rebuild even if source has not changed")
	rootCmd.AddCommand(buildCmd)
}

// -----------------------------------------------------------------------
// Target discovery
// -----------------------------------------------------------------------

type buildTarget struct {
	name string
	dir  string
	kind string
}

func collectBuildTargets(root string, args []string) ([]buildTarget, error) {
	if len(args) == 1 {
		ns, name, version := artifact.UsesKind(args[0])
		// find in any known directory
		candidates := buildSearchCandidates(ns)
		for _, c := range candidates {
			d := filepath.Join(root, c.base, name)
			if info, err := os.Stat(d); err == nil && info.IsDir() {
				if c.kind == "function" {
					return []buildTarget{{name: name, dir: d, kind: c.kind}}, nil
				}
				if version == "" {
					return buildTargetsForDir(name, d, c.kind), nil
				}
				if versionDir, ok := artifact.LocalComponentDirKindVersion(root, name, c.kind, version); ok {
					return []buildTarget{{name: buildTargetName(name, version), dir: versionDir, kind: c.kind}}, nil
				}
				return nil, fmt.Errorf("component %q version %q not found", name, version)
			}
		}
		return nil, fmt.Errorf("no component or function named %q found", name)
	}

	// collect all
	var targets []buildTarget
	scanDirs := []struct{ kind, base string }{
		{"function", "functions"},
		{"component", "components"},
		{"source", "sources"},
		{"destination", "destinations"},
		{"transformation", "transformations"},
	}
	for _, sd := range scanDirs {
		base := filepath.Join(root, sd.base)
		entries, err := os.ReadDir(base)
		if err != nil {
			continue // directory may not exist
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(base, e.Name())
			targets = append(targets, buildTargetsForDir(e.Name(), dir, sd.kind)...)
		}
	}

	// also check pipelines for inline transformation manifests
	pipelines, _ := artifact.ListPipelines(root)
	for _, p := range pipelines {
		for _, s := range p.Spec.Steps {
			ns, name, _ := artifact.UsesKind(s.Uses)
			if ns == "function" {
				dir := filepath.Join(root, "functions", name)
				if info, err := os.Stat(dir); err == nil && info.IsDir() {
					targets = dedupeAdd(targets, buildTarget{name: name, dir: dir, kind: "function"})
				}
			}
		}
	}

	return targets, nil
}

func buildSearchCandidates(namespace string) []struct{ kind, base string } {
	switch namespace {
	case "function":
		return []struct{ kind, base string }{{"function", "functions"}}
	case "component":
		return []struct{ kind, base string }{{"component", "components"}}
	case "source":
		return []struct{ kind, base string }{{"source", "sources"}}
	case "destination":
		return []struct{ kind, base string }{{"destination", "destinations"}}
	case "transformation":
		return []struct{ kind, base string }{{"transformation", "transformations"}}
	case "approval":
		return []struct{ kind, base string }{{"approval", "approvals"}}
	case "switch":
		return []struct{ kind, base string }{{"switch", "switches"}}
	case "decision":
		return []struct{ kind, base string }{{"decision", "decisions"}}
	default:
		return []struct{ kind, base string }{
			{"function", "functions"},
			{"component", "components"},
			{"source", "sources"},
			{"destination", "destinations"},
			{"transformation", "transformations"},
			{"approval", "approvals"},
			{"switch", "switches"},
			{"decision", "decisions"},
		}
	}
}

func buildTargetsForDir(name, dir, kind string) []buildTarget {
	if kind == "function" {
		if hasCode(dir) {
			return []buildTarget{{name: name, dir: dir, kind: kind}}
		}
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err == nil {
		var targets []buildTarget
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			versionDir := filepath.Join(dir, entry.Name())
			if hasCode(versionDir) {
				targets = append(targets, buildTarget{
					name: buildTargetName(name, entry.Name()),
					dir:  versionDir,
					kind: kind,
				})
			}
		}
		if len(targets) > 0 {
			return targets
		}
	}

	if hasCode(dir) {
		return []buildTarget{{name: buildTargetName(name, "latest"), dir: dir, kind: kind}}
	}
	return nil
}

func buildTargetName(name, version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return name
	}
	if !strings.EqualFold(version, "latest") && !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return name + "@" + version
}

func hasCode(dir string) bool {
	for _, f := range []string{
		"package.json", "go.mod", "requirements.txt",
		"handler.ts", "handler.js", "handler.py",
		"index.ts", "index.js", "main.go", "main.py",
		"cnips.fn.yaml", "component.yaml", "source.yaml", "destination.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return true
		}
	}
	return false
}

func dedupeAdd(targets []buildTarget, t buildTarget) []buildTarget {
	for _, existing := range targets {
		if existing.name == t.name {
			return targets
		}
	}
	return append(targets, t)
}

func flagVerbose(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("verbose")
	return v
}
