package cli

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cnips/cli/internal/artifact"
	"github.com/cnips/cli/internal/platform"
	"github.com/cnips/cli/internal/project"
)

type pullManifest struct {
	BaseHead   string                  `json:"baseHead,omitempty" yaml:"baseHead,omitempty"`
	LocalHead  string                  `json:"localHead,omitempty" yaml:"localHead,omitempty"`
	RemoteHead string                  `json:"remoteHead,omitempty" yaml:"remoteHead,omitempty"`
	UpdatedAt  string                  `json:"updatedAt,omitempty" yaml:"updatedAt,omitempty"`
	Files      map[string]manifestFile `json:"files" yaml:"files"`
}

type manifestFile struct {
	Digest   string `json:"digest" yaml:"digest"`
	CodeHash string `json:"codeHash,omitempty" yaml:"codeHash,omitempty"`
	Content  string `json:"content,omitempty" yaml:"content,omitempty"`
}

var manifestComponentTypes = []string{
	"pipelines",
	"transformations",
	"approvals",
	"switches",
	"decisions",
	"sources",
	"destinations",
	"functions",
	"globalvariables",
	"configurations",
	"lock",
}

func writePullManifests(client *platform.Client, root, workspace string) error {
	files, err := collectComparableFiles(root)
	if err != nil {
		return err
	}
	manifestFiles, err := manifestFilesForRoot(root, files)
	if err != nil {
		return err
	}
	head := manifestFingerprint(manifestFiles)
	if err := writeLocalPullManifest(root, workspace, &pullManifest{
		BaseHead:   head,
		LocalHead:  head,
		RemoteHead: head,
		Files:      manifestFiles,
	}); err != nil {
		return err
	}
	tasks := make([]func() error, 0, len(manifestComponentTypes))
	for _, componentType := range manifestComponentTypes {
		componentType := componentType
		manifest := platform.Manifest{
			WorkspaceID:   workspace,
			ComponentType: componentType,
			Files:         platformManifestFiles(filterManifestEntries(manifestFiles, componentType)),
		}
		tasks = append(tasks, func() error {
			if _, err := client.UpsertManifest(workspace, componentType, manifest); err != nil {
				return fmt.Errorf("upsert %s manifest: %w", componentType, err)
			}
			return nil
		})
	}
	return runConcurrent(tasks...)
}

func writeChangedManifests(client *platform.Client, root, workspace string, changes []fileChange) error {
	files, err := collectComparableFiles(root)
	if err != nil {
		return err
	}
	manifestFiles, err := manifestFilesForRoot(root, files)
	if err != nil {
		return err
	}
	base, _ := readLocalPullManifest(root, workspace)
	if base == nil || base.Files == nil {
		base = &pullManifest{Files: map[string]manifestFile{}}
	}
	for _, change := range changes {
		if file, ok := manifestFiles[change.Path]; ok {
			base.Files[change.Path] = file
			continue
		}
		delete(base.Files, change.Path)
	}
	head := manifestFingerprint(base.Files)
	base.BaseHead = head
	base.LocalHead = head
	base.RemoteHead = head
	if err := writeLocalPullManifest(root, workspace, base); err != nil {
		return err
	}
	types := map[string]bool{}
	for _, change := range changes {
		if componentType := manifestTypeForPath(change.Path); componentType != "" {
			types[componentType] = true
		}
	}
	tasks := make([]func() error, 0, len(types))
	for componentType := range types {
		componentType := componentType
		manifest := platform.Manifest{
			WorkspaceID:   workspace,
			ComponentType: componentType,
			Files:         platformManifestFiles(filterManifestEntries(manifestFiles, componentType)),
		}
		tasks = append(tasks, func() error {
			if _, err := client.UpsertManifest(workspace, componentType, manifest); err != nil {
				return fmt.Errorf("upsert %s manifest: %w", componentType, err)
			}
			return nil
		})
	}
	return runConcurrent(tasks...)
}

func readPullManifests(client *platform.Client, workspace string) (*pullManifest, error) {
	out := &pullManifest{Files: map[string]manifestFile{}}
	var mu sync.Mutex
	tasks := make([]func() error, 0, len(manifestComponentTypes))
	for _, componentType := range manifestComponentTypes {
		componentType := componentType
		tasks = append(tasks, func() error {
			manifest, err := client.GetManifest(workspace, componentType)
			if err != nil {
				if strings.Contains(err.Error(), "returned 404") {
					return nil
				}
				return fmt.Errorf("get %s manifest: %w", componentType, err)
			}
			mu.Lock()
			defer mu.Unlock()
			for path, file := range manifest.Files {
				out.Files[path] = manifestFile{Digest: file.Digest, CodeHash: file.CodeHash}
			}
			return nil
		})
	}
	if err := runConcurrent(tasks...); err != nil {
		return nil, err
	}
	head := manifestFingerprint(out.Files)
	out.BaseHead = head
	out.LocalHead = head
	out.RemoteHead = head
	return out, nil
}

func readComparisonBase(root string, client *platform.Client, workspace string, allowServerFallback bool) (*pullManifest, bool, error) {
	if manifest, err := readLocalPullManifest(root, workspace); err == nil && manifest != nil {
		return manifest, true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return nil, false, err
	}
	if !allowServerFallback {
		return &pullManifest{Files: map[string]manifestFile{}}, false, nil
	}
	manifest, err := readPullManifests(client, workspace)
	if err != nil {
		return nil, false, err
	}
	return manifest, false, nil
}

func readLocalPullManifest(root, workspace string) (*pullManifest, error) {
	return readLocalPullManifestFromPath(localPullManifestPath(root, workspace))
}

func readLocalPullManifestFromPath(path string) (*pullManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest pullManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if manifest.Files == nil {
		manifest.Files = map[string]manifestFile{}
	}
	if dropIgnoredManifestFiles(manifest.Files) {
		head := manifestFingerprint(manifest.Files)
		manifest.BaseHead = head
		manifest.LocalHead = head
		manifest.RemoteHead = head
	}
	if manifest.BaseHead == "" {
		manifest.BaseHead = manifestFingerprint(manifest.Files)
	}
	if manifest.LocalHead == "" {
		manifest.LocalHead = manifest.BaseHead
	}
	if manifest.RemoteHead == "" {
		manifest.RemoteHead = manifest.BaseHead
	}
	return &manifest, nil
}

func writeLocalPullManifest(root, workspace string, manifest *pullManifest) error {
	return writeLocalPullManifestToPath(localPullManifestPath(root, workspace), manifest)
}

func writeLocalPullManifestToPath(path string, manifest *pullManifest) error {
	if manifest.Files == nil {
		manifest.Files = map[string]manifestFile{}
	}
	if dropIgnoredManifestFiles(manifest.Files) {
		head := manifestFingerprint(manifest.Files)
		manifest.BaseHead = head
		manifest.LocalHead = head
		manifest.RemoteHead = head
	}
	if manifest.BaseHead == "" {
		manifest.BaseHead = manifestFingerprint(manifest.Files)
	}
	if manifest.LocalHead == "" {
		manifest.LocalHead = manifest.BaseHead
	}
	if manifest.RemoteHead == "" {
		manifest.RemoteHead = manifest.BaseHead
	}
	manifest.UpdatedAt = timeNowRFC3339()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func manifestFingerprint(files map[string]manifestFile) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write([]byte(files[path].Fingerprint()))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

func shortHead(head string) string {
	head = strings.TrimSpace(head)
	if head == "" {
		return "unknown"
	}
	if idx := strings.LastIndex(head, ":"); idx >= 0 {
		head = head[idx+1:]
	}
	if len(head) > 12 {
		return head[:12]
	}
	return head
}

func timeNowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func localPullManifestPath(root, workspace string) string {
	return filepath.Join(project.ManifestDir(root), "workspaces", safeStateName(workspace), "base-manifest.json")
}

func safeStateName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "..", "_")
	return replacer.Replace(value)
}

func hashFiles(files map[string]string) map[string]manifestFile {
	return manifestFiles(files)
}

func manifestFiles(files map[string]string) map[string]manifestFile {
	out := make(map[string]manifestFile, len(files))
	for path, body := range files {
		sum := sha256.Sum256([]byte(body))
		out[path] = manifestFile{Digest: fmt.Sprintf("%x", sum[:]), Content: body}
	}
	return out
}

func platformManifestFiles(files map[string]manifestFile) map[string]platform.ManifestFile {
	out := make(map[string]platform.ManifestFile, len(files))
	for path, file := range files {
		out[path] = platform.ManifestFile{Digest: file.Digest, CodeHash: file.CodeHash}
	}
	return out
}

func filterManifestEntries(files map[string]manifestFile, componentType string) map[string]manifestFile {
	out := map[string]manifestFile{}
	for path, file := range files {
		if manifestTypeForPath(path) == componentType {
			out[path] = file
		}
	}
	return out
}

func manifestTypeForPath(path string) string {
	if path == "cnips.lock" {
		return "lock"
	}
	for _, componentType := range manifestComponentTypes {
		if componentType == "lock" {
			continue
		}
		if strings.HasPrefix(path, componentType+"/") {
			return componentType
		}
	}
	return ""
}

func dropIgnoredManifestFiles(files map[string]manifestFile) bool {
	dropped := false
	for path := range files {
		if ignoredManifestPath(path) {
			delete(files, path)
			dropped = true
		}
	}
	return dropped
}

func ignoredManifestPath(path string) bool {
	path = filepath.ToSlash(path)
	return strings.HasPrefix(path, "apps/") || isGeneratedExecutionDir(path)
}

func isGeneratedExecutionDir(path string) bool {
	parts := strings.Split(strings.Trim(filepath.ToSlash(path), "/"), "/")
	if len(parts) < 3 {
		return false
	}
	for _, part := range parts[2:] {
		if strings.HasPrefix(part, "execution-") {
			return true
		}
	}
	return false
}

func changesFromManifest(root string, manifest *pullManifest) ([]fileChange, error) {
	local, err := collectComparableFiles(root)
	if err != nil {
		return nil, err
	}
	localHashes, err := manifestFilesForRoot(root, local)
	if err != nil {
		return nil, err
	}
	baseHashes := map[string]manifestFile{}
	if manifest != nil && manifest.Files != nil {
		baseHashes = manifest.Files
	}

	paths := map[string]bool{}
	for path := range localHashes {
		paths[path] = true
	}
	for path := range baseHashes {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)

	var changes []fileChange
	for _, path := range ordered {
		localFile, lok := localHashes[path]
		baseFile, bok := baseHashes[path]
		switch {
		case !bok:
			changes = append(changes, fileChange{Path: path, Status: "add-local", LocalExists: true, Local: splitLines(local[path])})
		case !lok:
			changes = append(changes, fileChange{Path: path, Status: "delete-local", RemoteExists: true, Remote: manifestRemoteLines(baseFile)})
		case !sameManifestFile(localFile, true, baseFile, true):
			changes = append(changes, fileChange{Path: path, Status: "modify", LocalExists: true, RemoteExists: true, Local: splitLines(local[path]), Remote: manifestRemoteLines(baseFile)})
		}
	}
	return changes, nil
}

func manifestFilesForRoot(root string, files map[string]string) (map[string]manifestFile, error) {
	out := manifestFiles(files)
	if err := annotateCodeHashes(root, out); err != nil {
		return nil, err
	}
	return out, nil
}

func annotateCodeHashes(root string, files map[string]manifestFile) error {
	for _, base := range localComponentBases {
		items, err := readLocalComponents(root, base.dir)
		if err != nil {
			return err
		}
		for _, item := range items {
			annotateSourceCodeFiles(root, files, item.Dir, item.Artifact.Spec.Language, item.Source)
		}
	}
	functions, err := readLocalFunctions(root)
	if err != nil {
		return err
	}
	for _, fn := range functions {
		annotateSourceCodeFiles(root, files, fn.Dir, fn.Artifact.Spec.Runtime, fn.Source)
	}
	return nil
}

func annotateSourceCodeFiles(root string, files map[string]manifestFile, dir, language string, source platform.SourceCode) {
	codeHash := sourceCodeHash(language, source)
	if codeHash == "" {
		return
	}
	for _, name := range sourceCodeFileNames(language) {
		rel, err := filepath.Rel(root, filepath.Join(dir, name))
		if err != nil {
			continue
		}
		path := filepath.ToSlash(rel)
		file, ok := files[path]
		if !ok {
			continue
		}
		file.CodeHash = codeHash
		files[path] = file
	}
}

func sourceCodeHash(language string, source platform.SourceCode) string {
	main := ""
	mod := ""
	switch sourceCodeLanguage(language) {
	case "GO":
		if source.Golang == nil {
			return ""
		}
		main = source.Golang.Main
		mod = source.Golang.Mod
	case "PYTHON":
		if source.Python == nil {
			return ""
		}
		main = source.Python.Main
		mod = source.Python.Requirements
	case "JS":
		main = source.Script
		if source.JavaScript != nil {
			mod = source.JavaScript.PackageJSON
		}
	default:
		main = source.Script
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%s\n%s", main, mod))) //nolint:gosec // Matches platform content fingerprinting.
	return fmt.Sprintf("%x", sum[:])
}

func sourceCodeFileNames(language string) []string {
	switch sourceCodeLanguage(language) {
	case "GO":
		return []string{"main.go", "go.mod"}
	case "PYTHON":
		return []string{"handler.py", "requirements.txt"}
	case "JS":
		return []string{"handler.js", "package.json"}
	default:
		return []string{"handler.script"}
	}
}

func (f manifestFile) Fingerprint() string {
	if f.CodeHash != "" {
		return f.CodeHash
	}
	return f.Digest
}

func (f manifestFile) FileIdentity() string {
	if f.Digest != "" {
		return f.Digest
	}
	return f.Fingerprint()
}

func manifestRemoteLines(file manifestFile) []string {
	if file.CodeHash != "" {
		return []string{fmt.Sprintf("<remote content not stored; codeHash=%s digest=%s>", file.CodeHash, file.Digest)}
	}
	return []string{fmt.Sprintf("<remote content not stored; digest=%s>", file.Digest)}
}

func refreshLockFromLocal(root string) error {
	lock, err := artifact.ParseLock(root)
	if err != nil {
		return err
	}
	byKey := map[string]int{}
	for i, c := range lock.Spec.Components {
		byKey[lockKey(c.Kind, c.Name)] = i
	}

	addOrUpdate := func(name, kind, lang, sigVer, tmplVer, dir string) error {
		integrity, err := dirIntegrity(dir)
		if err != nil {
			return err
		}
		component := artifact.LockComponent{
			Name:             name,
			Kind:             kind,
			Version:          "local",
			Source:           "local",
			Language:         lang,
			SignatureVersion: sigVer,
			TemplateVersion:  tmplVer,
			Integrity:        integrity,
		}
		key := lockKey(kind, name)
		if idx, ok := byKey[key]; ok {
			existing := lock.Spec.Components[idx]
			component.Version = firstNonEmpty(existing.Version, component.Version)
			component.Source = firstNonEmpty(existing.Source, component.Source)
			component.Path = existing.Path
			component.ArtifactID = existing.ArtifactID
			component.PluginFileName = existing.PluginFileName
			component.IsBunBuild = existing.IsBunBuild
			component.Publisher = existing.Publisher
			lock.Spec.Components[idx] = component
			return nil
		}
		lock.Spec.Components = append(lock.Spec.Components, component)
		byKey[key] = len(lock.Spec.Components) - 1
		return nil
	}

	for _, base := range localComponentBases {
		items, err := readLocalComponents(root, base.dir)
		if err != nil {
			return err
		}
		for _, item := range items {
			kind := componentLocalKind(item)
			if err := addOrUpdate(item.Slug, kind, item.Artifact.Spec.Language, item.Artifact.Spec.SignatureVersion, item.Artifact.Spec.TemplateVersion, item.Dir); err != nil {
				return fmt.Errorf("lock %s %s: %w", kind, item.Slug, err)
			}
		}
	}
	functions, err := readLocalFunctions(root)
	if err != nil {
		return err
	}
	for _, item := range functions {
		if err := addOrUpdate(item.Slug, "function", item.Artifact.Spec.Runtime, item.Artifact.Spec.SignatureVersion, item.Artifact.Spec.TemplateVersion, item.Dir); err != nil {
			return fmt.Errorf("lock function %s: %w", item.Slug, err)
		}
	}

	sort.SliceStable(lock.Spec.Components, func(i, j int) bool {
		a := lock.Spec.Components[i]
		b := lock.Spec.Components[j]
		if a.Kind == b.Kind {
			return a.Name < b.Name
		}
		return a.Kind < b.Kind
	})
	return artifact.WriteYAML(filepath.Join(root, "cnips.lock"), lock)
}

func dirIntegrity(dir string) (string, error) {
	files := map[string]string{}
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".cnips", "node_modules", ".venv":
				return filepath.SkipDir
			}
			if strings.HasPrefix(d.Name(), "execution-") {
				return filepath.SkipDir
			}
			return nil
		}
		if !isComparableFile(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = strings.ReplaceAll(string(data), "\r\n", "\n")
		return nil
	}); err != nil {
		return "", err
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write([]byte(files[path]))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

func changedPathSet(changes []fileChange) map[string]bool {
	out := make(map[string]bool, len(changes))
	for _, change := range changes {
		out[change.Path] = true
	}
	return out
}

func filterBundleByChanges(bundle *localBundle, changes []fileChange) *localBundle {
	changed := changedPathSet(changes)
	anyUnder := func(prefix string) bool {
		prefix = strings.TrimSuffix(prefix, "/") + "/"
		for path := range changed {
			if strings.HasPrefix(path, prefix) {
				return true
			}
		}
		return false
	}
	hasCodeUnder := func(prefix string) bool {
		prefix = strings.TrimSuffix(prefix, "/") + "/"
		for path := range changed {
			if strings.HasPrefix(path, prefix) && isComponentCodePath(strings.TrimPrefix(path, prefix)) {
				return true
			}
		}
		return false
	}
	out := &localBundle{}
	out.Apps = bundle.Apps
	for _, base := range transformationFamilyBases {
		for _, item := range bundle.Transformations {
			prefix := base.dir + "/" + item.Slug
			if (item.BaseDir != "" && item.BaseDir != base.dir) || !anyUnder(prefix) {
				continue
			}
			item.HasCodeChanges = hasCodeUnder(prefix)
			out.Transformations = append(out.Transformations, item)
		}
	}
	for _, item := range bundle.Sources {
		if anyUnder("sources/" + item.Slug) {
			item.HasCodeChanges = hasCodeUnder("sources/" + item.Slug)
			out.Sources = append(out.Sources, item)
		}
	}
	for _, item := range bundle.Destinations {
		if anyUnder("destinations/" + item.Slug) {
			item.HasCodeChanges = hasCodeUnder("destinations/" + item.Slug)
			out.Destinations = append(out.Destinations, item)
		}
	}
	for _, item := range bundle.Functions {
		if anyUnder("functions/" + item.Slug) {
			item.HasCodeChanges = hasCodeUnder("functions/" + item.Slug)
			out.Functions = append(out.Functions, item)
		}
	}
	for _, item := range bundle.GlobalVariables {
		if changed["globalvariables/"+item.Metadata.Name+".yaml"] {
			out.GlobalVariables = append(out.GlobalVariables, item)
		}
	}
	for _, item := range bundle.Configurations {
		if changed["configurations/"+item.Metadata.Name+".yaml"] {
			out.Configurations = append(out.Configurations, item)
		}
	}
	for _, item := range bundle.Pipelines {
		if anyUnder("pipelines/" + item.Metadata.Name) {
			out.Pipelines = append(out.Pipelines, item)
		}
	}
	return out
}

func isComponentCodePath(path string) bool {
	switch filepath.ToSlash(path) {
	case "handler.js", "package.json", "main.go", "go.mod", "go.sum", "handler.py", "requirements.txt", "handler.script":
		return true
	default:
		return false
	}
}

func lockKey(kind, name string) string {
	return kind + "/" + name
}
