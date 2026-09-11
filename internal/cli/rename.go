package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/project"
	"github.com/cnips/cli/internal/serializer"
)

var renameCmd = &cobra.Command{
	Use:   "rename <type> <old-name> <new-name>",
	Short: "Rename a local component, function, or pipeline",
	Long: `Renames a canonical local cnips artifact while preserving platform identity.

The command moves the artifact directory, updates metadata.name, rewrites local
pipeline references, refreshes cnips.lock, and remaps local sync manifests so a
push updates the same cnips resource instead of creating a replacement.

Types:
  pipeline, function, source, destination, transformation, approval, switch, decision, component`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := project.MustFindRoot()
		result, err := renameArtifact(root, args[0], args[1], args[2])
		if err != nil {
			return err
		}
		fmt.Printf("Renamed %s %q to %q\n", result.Kind, result.OldSlug, result.NewSlug)
		fmt.Printf("  moved: %s -> %s\n", result.OldPath, result.NewPath)
		if result.UpdatedPipelines > 0 {
			fmt.Printf("  updated pipeline references: %d\n", result.UpdatedPipelines)
		}
		if result.RemappedManifests > 0 {
			fmt.Printf("  remapped workspace manifests: %d\n", result.RemappedManifests)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(renameCmd)
}

type renameResult struct {
	Kind              string
	OldSlug           string
	NewSlug           string
	OldPath           string
	NewPath           string
	UpdatedPipelines  int
	RemappedManifests int
}

type renameTarget struct {
	Kind       string
	Base       string
	OldSlug    string
	NewSlug    string
	OldDir     string
	NewDir     string
	UsesPrefix string
}

func renameArtifact(root, kind, oldName, newName string) (renameResult, error) {
	lock, err := acquireProjectWriteLock(root, "rename")
	if err != nil {
		return renameResult{}, err
	}
	defer func() {
		_ = lock.Release("rename")
	}()
	return renameArtifactLocked(root, kind, oldName, newName)
}

func renameArtifactLocked(root, kind, oldName, newName string) (renameResult, error) {
	target, err := resolveRenameTarget(root, kind, oldName, newName)
	if err != nil {
		return renameResult{}, err
	}
	if err := os.Rename(target.OldDir, target.NewDir); err != nil {
		return renameResult{}, fmt.Errorf("move %s to %s: %w", relPath(root, target.OldDir), relPath(root, target.NewDir), err)
	}

	updatedPipelines := 0
	if target.Kind == "pipeline" {
		if err := renamePipelineManifest(target.NewDir, target.NewSlug); err != nil {
			return renameResult{}, err
		}
	} else {
		if err := renameComponentManifests(target.NewDir, target.NewSlug); err != nil {
			return renameResult{}, err
		}
		n, err := rewritePipelineReferences(root, target)
		if err != nil {
			return renameResult{}, err
		}
		updatedPipelines = n
	}

	if err := renameLockEntry(root, target); err != nil {
		return renameResult{}, err
	}
	if err := refreshLockFromLocal(root); err != nil {
		return renameResult{}, err
	}
	if err := recordRenameAlias(root, target); err != nil {
		return renameResult{}, err
	}
	remapped, err := remapWorkspaceManifestPaths(root, target.Base+"/"+target.OldSlug, target.Base+"/"+target.NewSlug)
	if err != nil {
		return renameResult{}, err
	}

	return renameResult{
		Kind:              target.Kind,
		OldSlug:           target.OldSlug,
		NewSlug:           target.NewSlug,
		OldPath:           relPath(root, target.OldDir),
		NewPath:           relPath(root, target.NewDir),
		UpdatedPipelines:  updatedPipelines,
		RemappedManifests: remapped,
	}, nil
}

func resolveRenameTarget(root, kind, oldName, newName string) (renameTarget, error) {
	normalizedKind, err := normalizeRenameKind(kind)
	if err != nil {
		return renameTarget{}, err
	}
	oldSlug := serializer.Slug(oldName)
	newSlug := serializer.Slug(newName)
	if oldSlug == "" || newSlug == "" {
		return renameTarget{}, fmt.Errorf("old-name and new-name are required")
	}
	if oldSlug == newSlug {
		return renameTarget{}, fmt.Errorf("%s is already named %q", normalizedKind, newSlug)
	}

	var candidates []renameTarget
	for _, base := range renameBasesForKind(normalizedKind) {
		oldDir := filepath.Join(root, base, oldSlug)
		if info, err := os.Stat(oldDir); err == nil && info.IsDir() {
			candidates = append(candidates, renameTarget{
				Kind:       renameKindForBase(normalizedKind, base),
				Base:       base,
				OldSlug:    oldSlug,
				NewSlug:    newSlug,
				OldDir:     oldDir,
				NewDir:     filepath.Join(root, base, newSlug),
				UsesPrefix: usesPrefixForBase(base),
			})
		}
	}
	if len(candidates) == 0 {
		return renameTarget{}, fmt.Errorf("%s %q not found", normalizedKind, oldSlug)
	}
	if len(candidates) > 1 {
		var paths []string
		for _, candidate := range candidates {
			paths = append(paths, relPath(root, candidate.OldDir))
		}
		sort.Strings(paths)
		return renameTarget{}, fmt.Errorf("%q matches multiple component types (%s); use source, destination, transformation, approval, switch, decision, or function", oldSlug, strings.Join(paths, ", "))
	}
	target := candidates[0]
	if info, err := os.Stat(target.NewDir); err == nil && info.IsDir() {
		return renameTarget{}, fmt.Errorf("%s already exists", relPath(root, target.NewDir))
	} else if err != nil && !os.IsNotExist(err) {
		return renameTarget{}, err
	}
	return target, nil
}

func normalizeRenameKind(kind string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "pipeline", "pipelines", "pipe":
		return "pipeline", nil
	case "function", "functions", "fn":
		return "function", nil
	case "source", "sources", "src", "extractor":
		return "source", nil
	case "destination", "destinations", "dest":
		return "destination", nil
	case "transformation", "transformations", "transform", "tx":
		return "transformation", nil
	case "approval", "approvals":
		return "approval", nil
	case "switch", "switches":
		return "switch", nil
	case "decision", "decisions":
		return "decision", nil
	case "component", "components":
		return "component", nil
	default:
		return "", fmt.Errorf("unsupported rename type %q (use pipeline, function, source, destination, transformation, approval, switch, decision, or component)", kind)
	}
}

func renameBasesForKind(kind string) []string {
	switch kind {
	case "pipeline":
		return []string{"pipelines"}
	case "function":
		return []string{"functions"}
	case "source":
		return []string{"sources"}
	case "destination":
		return []string{"destinations"}
	case "approval":
		return []string{"approvals"}
	case "switch":
		return []string{"switches"}
	case "decision":
		return []string{"decisions"}
	case "transformation":
		return []string{"transformations"}
	default:
		return []string{"transformations", "approvals", "switches", "decisions", "sources", "destinations", "components", "apps"}
	}
}

func renameKindForBase(kind, base string) string {
	if kind != "component" {
		return kind
	}
	switch base {
	case "sources":
		return "source"
	case "destinations":
		return "destination"
	case "approvals":
		return "approval"
	case "switches":
		return "switch"
	case "decisions":
		return "decision"
	case "transformations":
		return "transformation"
	case "apps":
		return "app"
	default:
		return "component"
	}
}

func usesPrefixForBase(base string) string {
	switch base {
	case "sources":
		return "source"
	case "destinations":
		return "destination"
	case "functions":
		return "function"
	case "apps":
		return "app"
	case "approvals":
		return "approval"
	case "switches":
		return "switch"
	case "decisions":
		return "decision"
	default:
		return "transformation"
	}
}

func renamePipelineManifest(dir, newSlug string) error {
	p, err := artifact.ParsePipeline(dir)
	if err != nil {
		return err
	}
	p.Metadata.Name = newSlug
	p.Metadata.Slug = ""
	return artifact.WriteYAML(filepath.Join(dir, "pipeline.yaml"), p)
}

func renameComponentManifests(dir, newSlug string) error {
	return filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "node_modules" || entry.Name() == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}
		switch entry.Name() {
		case "component.yaml", "source.yaml", "destination.yaml", "transformation.yaml":
			comp, err := artifact.ParseComponent(filepath.Dir(path))
			if err != nil {
				return err
			}
			comp.Metadata.Name = newSlug
			comp.Metadata.Slug = ""
			return artifact.WriteYAML(path, comp)
		case "cnips.fn.yaml":
			fn, err := artifact.ParseFunction(filepath.Dir(path))
			if err != nil {
				return err
			}
			fn.Metadata.Name = newSlug
			fn.Metadata.Slug = ""
			return artifact.WriteYAML(path, fn)
		default:
			return nil
		}
	})
}

func rewritePipelineReferences(root string, target renameTarget) (int, error) {
	pipelinesDir := filepath.Join(root, "pipelines")
	entries, err := os.ReadDir(pipelinesDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(pipelinesDir, entry.Name())
		p, err := artifact.ParsePipeline(dir)
		if err != nil {
			continue
		}
		changed := false
		if target.UsesPrefix == "source" && p.Spec.Trigger != nil && p.Spec.Trigger.SourceRef == target.OldSlug {
			p.Spec.Trigger.SourceRef = target.NewSlug
			changed = true
		}
		for i := range p.Spec.Steps {
			if rewriteUses(&p.Spec.Steps[i].Uses, target.UsesPrefix, target.OldSlug, target.NewSlug) {
				changed = true
			}
			if target.UsesPrefix != "transformation" && rewriteUses(&p.Spec.Steps[i].Uses, "transformation", target.OldSlug, target.NewSlug) {
				changed = true
			}
		}
		if !changed {
			continue
		}
		if err := artifact.WriteYAML(filepath.Join(dir, "pipeline.yaml"), p); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

func rewriteUses(uses *string, namespace, oldSlug, newSlug string) bool {
	ns, name, version := artifact.UsesKind(*uses)
	if ns != namespace || name != oldSlug {
		return false
	}
	next := ns + "/" + newSlug
	if version != "" {
		next += "@" + version
	}
	*uses = next
	return true
}

func renameLockEntry(root string, target renameTarget) error {
	if target.Kind == "pipeline" || target.Kind == "app" || target.Kind == "component" {
		return nil
	}
	lock, err := artifact.ParseLock(root)
	if err != nil {
		return err
	}
	changed := false
	for i := range lock.Spec.Components {
		c := &lock.Spec.Components[i]
		if (c.Kind == target.UsesPrefix || (target.UsesPrefix != "transformation" && c.Kind == "transformation")) && c.Name == target.OldSlug {
			c.Name = target.NewSlug
			if c.Kind == "transformation" && target.UsesPrefix != "transformation" {
				c.Kind = target.UsesPrefix
			}
			if c.Path != "" {
				c.Path = strings.Replace(c.Path, target.Base+"/"+target.OldSlug, target.Base+"/"+target.NewSlug, 1)
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return artifact.WriteYAML(filepath.Join(root, "cnips.lock"), lock)
}

func remapWorkspaceManifestPaths(root, oldPrefix, newPrefix string) (int, error) {
	stateDir := filepath.Join(root, ".cnips", "state", "workspaces")
	entries, err := os.ReadDir(stateDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	oldPrefix = strings.TrimSuffix(filepath.ToSlash(oldPrefix), "/") + "/"
	newPrefix = strings.TrimSuffix(filepath.ToSlash(newPrefix), "/") + "/"
	updated := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(stateDir, entry.Name(), "base-manifest.json")
		manifest, err := readLocalPullManifestFromPath(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return updated, err
		}
		changed := false
		for oldPath, file := range manifest.Files {
			if !strings.HasPrefix(oldPath, oldPrefix) {
				continue
			}
			newPath := newPrefix + strings.TrimPrefix(oldPath, oldPrefix)
			delete(manifest.Files, oldPath)
			manifest.Files[newPath] = file
			changed = true
		}
		if !changed {
			continue
		}
		head := manifestFingerprint(manifest.Files)
		manifest.BaseHead = head
		manifest.LocalHead = head
		manifest.RemoteHead = head
		if err := writeLocalPullManifestToPath(path, manifest); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}
