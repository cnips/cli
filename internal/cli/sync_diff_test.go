package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cnips/cli/internal/platform"
	"github.com/cnips/cli/internal/serializer"
)

func TestCompareLocalBaseRemoteUsesLiveWorkspaceWhenServerManifestIsStale(t *testing.T) {
	root := t.TempDir()
	localFunction := platform.Function{
		ID:          "fn-1",
		Name:        "example",
		Language:    "JAVASCRIPT",
		Description: "old description",
		SourceCode:  platform.SourceCode{Script: "module.exports = 'old'\n"},
	}
	if _, err := serializer.WriteFunctionAs(root, &localFunction, "example"); err != nil {
		t.Fatalf("write local function: %v", err)
	}
	localFiles, err := collectComparableFiles(root)
	if err != nil {
		t.Fatalf("collect local files: %v", err)
	}
	baseFiles, err := manifestFilesForRoot(root, localFiles)
	if err != nil {
		t.Fatalf("build base manifest: %v", err)
	}
	base := &pullManifest{Files: baseFiles}

	remoteFunction := localFunction
	remoteFunction.Description = "new description"
	remoteFunction.Script = "module.exports = 'new'\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/cnips-manifests/") {
			// Manifests can lag behind edits made outside the CLI.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"status":  http.StatusOK,
				"data": map[string]any{
					"files": platformManifestFiles(baseFiles),
				},
			})
			return
		}

		var list any = []any{}
		if strings.HasSuffix(r.URL.Path, "/functions") {
			list = []platform.Function{remoteFunction}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"status":  http.StatusOK,
			"data": map[string]any{
				"list":  list,
				"count": 0,
			},
		})
	}))
	defer server.Close()

	comparison, err := compareLocalBaseRemote(root, platform.NewClient(server.URL, "", ""), "ws", base)
	if err != nil {
		t.Fatalf("compareLocalBaseRemote: %v", err)
	}
	if len(comparison.RemoteOnly) == 0 {
		t.Fatal("expected live workspace edits to be detected despite a stale server manifest")
	}
	foundHandler := false
	for _, change := range comparison.RemoteOnly {
		if change.Path == "functions/example/handler.js" {
			foundHandler = true
			break
		}
	}
	if !foundHandler {
		t.Fatalf("expected remote handler change, got %#v", comparison.RemoteOnly)
	}
}

func TestSameManifestFileUsesFileDigestBeforeCodeHash(t *testing.T) {
	left := manifestFile{Digest: "digest-a", CodeHash: "same-code"}
	right := manifestFile{Digest: "digest-b", CodeHash: "same-code"}
	if sameManifestFile(left, true, right, true) {
		t.Fatalf("expected different file digests to be treated as different")
	}
}

func TestSameManifestFileFallsBackToCodeHash(t *testing.T) {
	left := manifestFile{CodeHash: "same-code"}
	right := manifestFile{CodeHash: "same-code"}
	if !sameManifestFile(left, true, right, true) {
		t.Fatalf("expected matching code hashes to be used when file digest is unavailable")
	}
}

func TestClassifySyncStatus(t *testing.T) {
	cases := []struct {
		name          string
		hasLocal      bool
		hasRemote     bool
		localChanged  bool
		remoteChanged bool
		want          string
	}{
		{name: "same-file conflict", hasLocal: true, hasRemote: true, localChanged: true, remoteChanged: true, want: "conflict"},
		{name: "local only", hasLocal: true, hasRemote: true, localChanged: true, want: "local"},
		{name: "remote only", hasLocal: true, hasRemote: true, remoteChanged: true, want: "remote"},
		{name: "local deleted", hasLocal: false, hasRemote: true, localChanged: true, want: "delete-remote"},
		{name: "remote deleted", hasLocal: true, hasRemote: false, remoteChanged: true, want: "delete-local"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifySyncStatus(tc.hasLocal, tc.hasRemote, tc.localChanged, tc.remoteChanged)
			if got != tc.want {
				t.Fatalf("classifySyncStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlatformTransformationTypePreservesFamilies(t *testing.T) {
	cases := map[string]string{
		"APPROVAL":       "APPROVAL",
		"approval":       "APPROVAL",
		"SWITCH":         "SWITCH",
		"decision":       "DECISION",
		"TRANSFORMATION": "TRANSFORMATION",
		"LOOP":           "TRANSFORMATION",
		"":               "TRANSFORMATION",
	}
	for input, want := range cases {
		got := platformTransformationType(platform.Transformation{Type: input})
		if got != want {
			t.Fatalf("platformTransformationType(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestReadLocalFunctionsSkipsDirectoryWithoutManifest(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "functions/hello-fn/main.go", "package main\n")

	functions, err := readLocalFunctions(root)
	if err != nil {
		t.Fatalf("readLocalFunctions: %v", err)
	}
	if len(functions) != 0 {
		t.Fatalf("expected incomplete function directory to be skipped, got %#v", functions)
	}
}

func TestFreshHydrationProjectAllowsOnlyDerivedAndInitSampleFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "cnips.lock", "kind: Lock\n")
	writeTestFile(t, root, "pipelines/hello-world/pipeline.yaml", "kind: Pipeline\n")
	writeTestFile(t, root, "functions/hello-fn/handler.js", "module.exports = {}\n")

	if !isFreshHydrationProject(root) {
		t.Fatal("expected init scaffold files to be treated as fresh hydration")
	}

	writeTestFile(t, root, "transformations/normalize/component.yaml", "kind: Component\n")
	if isFreshHydrationProject(root) {
		t.Fatal("expected authored component to make project non-fresh")
	}
}

func TestNonDerivedChangesIgnoresLockfile(t *testing.T) {
	changes := []fileChange{
		{Path: "cnips.lock"},
		{Path: "functions/enrich/cnips.fn.yaml"},
	}
	got := nonDerivedChanges(changes)
	if len(got) != 1 || got[0].Path != "functions/enrich/cnips.fn.yaml" {
		t.Fatalf("unexpected non-derived changes: %#v", got)
	}
}

func TestNormalizeBaseToAgreedLocalRemoteSuppressesPollutedBase(t *testing.T) {
	base := map[string]manifestFile{
		"pipelines/orders/pipeline.yaml":                     {Digest: "bad-generated-id-digest"},
		"transformations/check-transformation-v3/handler.js": {Digest: "stale-old-path"},
	}
	local := map[string]manifestFile{
		"pipelines/orders/pipeline.yaml": {Digest: "canonical-digest"},
	}
	remote := map[string]manifestFile{
		"pipelines/orders/pipeline.yaml": {Digest: "canonical-digest"},
	}

	if !normalizeBaseToAgreedLocalRemote(base, local, remote) {
		t.Fatal("expected base to be normalized")
	}
	if base["pipelines/orders/pipeline.yaml"].Digest != "canonical-digest" {
		t.Fatalf("base was not normalized: %#v", base)
	}
	if _, ok := base["transformations/check-transformation-v3/handler.js"]; ok {
		t.Fatalf("stale base path was not removed: %#v", base)
	}
}

func TestApplyConflictMarkersWritesEditorMarkers(t *testing.T) {
	root := t.TempDir()
	change := fileChange{
		Path:         "functions/enrich/handler.js",
		LocalExists:  true,
		RemoteExists: true,
		Local:        []string{"export default 'local'"},
		Remote:       []string{"export default 'cnips'"},
	}

	if err := applyConflictMarkers(root, []fileChange{change}); err != nil {
		t.Fatalf("applyConflictMarkers: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(change.Path)))
	if err != nil {
		t.Fatalf("read conflict file: %v", err)
	}
	got := string(data)
	want := "<<<<<<< cnips\nexport default 'cnips'\n=======\nexport default 'local'\n>>>>>>> local\n"
	if got != want {
		t.Fatalf("conflict file mismatch:\n%s", got)
	}
}

func TestApplyRemoteOnlyPrunesEmptyArtifactDirectory(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "destinations/removed/component.yaml", "kind: Component\n")
	writeTestFile(t, root, "destinations/removed/handler.js", "module.exports = {}\n")

	changes := []fileChange{
		{Path: "destinations/removed/component.yaml", Status: "delete-local", LocalExists: true, RemoteExists: false},
		{Path: "destinations/removed/handler.js", Status: "delete-local", LocalExists: true, RemoteExists: false},
	}
	if err := applyRemoteOnly(root, changes); err != nil {
		t.Fatalf("applyRemoteOnly: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "destinations", "removed")); !os.IsNotExist(err) {
		t.Fatalf("expected empty artifact directory to be pruned, stat err=%v", err)
	}
}

func TestApplyRemoteOnlyKeepsArtifactDirectoryWithUnrelatedFile(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "destinations/removed/component.yaml", "kind: Component\n")
	writeTestFile(t, root, "destinations/removed/notes.md", "keep me\n")

	changes := []fileChange{
		{Path: "destinations/removed/component.yaml", Status: "delete-local", LocalExists: true, RemoteExists: false},
	}
	if err := applyRemoteOnly(root, changes); err != nil {
		t.Fatalf("applyRemoteOnly: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "destinations", "removed")); err != nil {
		t.Fatalf("expected non-empty artifact directory to remain: %v", err)
	}
}

func TestPruneStalePulledPathsRemovesRenamedTrackedArtifactDirectory(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "destinations/test-demo1/component.yaml", "kind: Component\n")
	writeTestFile(t, root, "destinations/test-demo1/handler.js", "module.exports = 'old local edit'\n")
	writeTestFile(t, root, "destinations/test-demo1-updated/component.yaml", "kind: Component\n")
	writeTestFile(t, root, "destinations/test-demo1-updated/handler.js", "module.exports = 'remote renamed'\n")

	previous := &pullManifest{Files: map[string]manifestFile{
		"destinations/test-demo1/component.yaml": {Digest: "old-component"},
		"destinations/test-demo1/handler.js":     {Digest: "old-handler"},
	}}
	active := map[string]bool{
		"destinations/test-demo1-updated": true,
	}
	if err := pruneStalePulledPaths(root, previous, active); err != nil {
		t.Fatalf("pruneStalePulledPaths: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "destinations", "test-demo1")); !os.IsNotExist(err) {
		t.Fatalf("expected stale pre-rename artifact directory to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "destinations", "test-demo1-updated", "handler.js")); err != nil {
		t.Fatalf("expected renamed artifact to remain: %v", err)
	}
}

func TestPruneStalePulledPathsRemovesRemotelyDeletedTrackedArtifactDirectory(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "destinations/httpbin-post-destination/component.yaml", "kind: Component\n")
	writeTestFile(t, root, "destinations/httpbin-post-destination/main.go", "package main\n")

	previous := &pullManifest{Files: map[string]manifestFile{
		"destinations/httpbin-post-destination/component.yaml": {Digest: "old-component"},
		"destinations/httpbin-post-destination/main.go":        {Digest: "old-main"},
	}}
	if err := pruneStalePulledPaths(root, previous, map[string]bool{}); err != nil {
		t.Fatalf("pruneStalePulledPaths: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "destinations", "httpbin-post-destination")); !os.IsNotExist(err) {
		t.Fatalf("expected remotely deleted artifact directory to be removed, stat err=%v", err)
	}
}

func TestResolvedRecordedConflictIsNotReclassifiedAsConflict(t *testing.T) {
	state := &conflictState{Files: map[string]conflictRecord{
		"functions/enrich/handler.js": {RemoteDigest: digestLines([]string{"export default 'cnips'"})},
	}}
	remote := manifestFile{Digest: digestLines([]string{"export default 'cnips'"})}

	if !isResolvedRecordedConflict("functions/enrich/handler.js", "export default 'resolved'\n", remote, true, state) {
		t.Fatal("expected saved file without conflict markers to be treated as resolved")
	}
	withMarkers := "<<<<<<< cnips\nexport default 'cnips'\n=======\nexport default 'local'\n>>>>>>> local\n"
	if isResolvedRecordedConflict("functions/enrich/handler.js", withMarkers, remote, true, state) {
		t.Fatal("expected file with conflict markers to remain conflicted")
	}
}

func TestResolvedRecordedConflictUsesRemoteFingerprint(t *testing.T) {
	state := &conflictState{Files: map[string]conflictRecord{
		"functions/enrich/handler.js": {RemoteDigest: "old-digest", RemoteFingerprint: "same-code"},
	}}
	remote := manifestFile{Digest: "new-digest", CodeHash: "same-code"}

	if !isResolvedRecordedConflict("functions/enrich/handler.js", "export default 'resolved'\n", remote, true, state) {
		t.Fatal("expected matching remote fingerprint to mark conflict as resolved")
	}
}

func TestRecordConflictStateKeepsExactRemoteDigest(t *testing.T) {
	root := t.TempDir()
	change := fileChange{
		Path:              "functions/enrich/handler.js",
		RemoteExists:      true,
		Remote:            []string{"export default 'cnips'"},
		RemoteDigest:      "exact-digest-without-rendered-newline",
		RemoteFingerprint: "exact-code-hash",
	}

	if err := recordConflictState(root, "default", []fileChange{change}); err != nil {
		t.Fatalf("recordConflictState: %v", err)
	}
	state, err := readConflictState(root, "default")
	if err != nil {
		t.Fatalf("readConflictState: %v", err)
	}
	record := state.Files[change.Path]
	if record.RemoteDigest != change.RemoteDigest {
		t.Fatalf("RemoteDigest=%q, want %q", record.RemoteDigest, change.RemoteDigest)
	}
	if record.RemoteFingerprint != change.RemoteFingerprint {
		t.Fatalf("RemoteFingerprint=%q, want %q", record.RemoteFingerprint, change.RemoteFingerprint)
	}
}

func TestLineMergeAllowsNonOverlappingChanges(t *testing.T) {
	base := "line 1\nline 2\nline 3\n"
	local := "local 1\nline 2\nline 3\n"
	remote := "line 1\nline 2\nremote 3\n"

	merged, ok := mergeFileChange(base, local, remote, true, true, true)
	if !ok {
		t.Fatal("expected non-overlapping line changes to merge")
	}
	want := "local 1\nline 2\nremote 3\n"
	if merged != want {
		t.Fatalf("merged content mismatch:\n%s", merged)
	}
}

func TestLineMergeRejectsOverlappingChanges(t *testing.T) {
	base := "line 1\nline 2\n"
	local := "local 1\nline 2\n"
	remote := "remote 1\nline 2\n"

	if _, ok := mergeFileChange(base, local, remote, true, true, true); ok {
		t.Fatal("expected overlapping line changes to remain conflicted")
	}
}

func TestConflictMarkerLinesOnlyWrapOverlap(t *testing.T) {
	base := "keep top\nline 1\nkeep bottom\n"
	local := "keep top\nlocal 1\nkeep bottom\n"
	remote := "keep top\nremote 1\nkeep bottom\n"

	got := conflictMarkerLines(base, local, remote, true, true, true)
	want := []string{
		"keep top",
		"<<<<<<< cnips",
		"remote 1",
		"=======",
		"local 1",
		">>>>>>> local",
		"keep bottom",
	}
	if len(got) != len(want) {
		t.Fatalf("marker length=%d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("marker line %d=%q, want %q\n%#v", i, got[i], want[i], got)
		}
	}
}

func TestConflictMarkerDetailsReportsChunkLines(t *testing.T) {
	base := "keep top\nline 1\nkeep bottom\n"
	local := "keep top\nlocal 1\nkeep bottom\n"
	remote := "keep top\nremote 1\nkeep bottom\n"

	_, chunks := conflictMarkerDetails(base, local, remote, true, true, true)
	if len(chunks) != 1 {
		t.Fatalf("chunks=%d, want 1: %#v", len(chunks), chunks)
	}
	chunk := chunks[0]
	if chunk.BaseStart != 2 || chunk.BaseEnd != 2 {
		t.Fatalf("base range=%d:%d, want 2:2", chunk.BaseStart, chunk.BaseEnd)
	}
	if chunk.RemoteStart != 2 || chunk.RemoteEnd != 2 {
		t.Fatalf("remote range=%d:%d, want 2:2", chunk.RemoteStart, chunk.RemoteEnd)
	}
	if chunk.LocalStart != 2 || chunk.LocalEnd != 2 {
		t.Fatalf("local range=%d:%d, want 2:2", chunk.LocalStart, chunk.LocalEnd)
	}
	if len(chunk.Remote) != 1 || chunk.Remote[0] != "remote 1" {
		t.Fatalf("remote chunk mismatch: %#v", chunk.Remote)
	}
	if len(chunk.Local) != 1 || chunk.Local[0] != "local 1" {
		t.Fatalf("local chunk mismatch: %#v", chunk.Local)
	}
}

func TestConflictMarkerDetailsKeepsSeparateChunks(t *testing.T) {
	base := "line 1\nkeep 2\nline 3\nkeep 4\n"
	local := "local 1\nkeep 2\nlocal 3\nkeep 4\n"
	remote := "remote 1\nkeep 2\nremote 3\nkeep 4\n"

	got, chunks := conflictMarkerDetails(base, local, remote, true, true, true)
	if len(chunks) != 2 {
		t.Fatalf("chunks=%d, want 2: %#v", len(chunks), chunks)
	}
	want := []string{
		"<<<<<<< cnips",
		"remote 1",
		"=======",
		"local 1",
		">>>>>>> local",
		"keep 2",
		"<<<<<<< cnips",
		"remote 3",
		"=======",
		"local 3",
		">>>>>>> local",
		"keep 4",
	}
	if len(got) != len(want) {
		t.Fatalf("marker length=%d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("marker line %d=%q, want %q\n%#v", i, got[i], want[i], got)
		}
	}
	if chunks[0].BaseStart != 1 || chunks[1].BaseStart != 3 {
		t.Fatalf("unexpected chunk base starts: %#v", chunks)
	}
}

func TestConflictMarkerDetailsReportsShiftedSideCoordinates(t *testing.T) {
	base := "line 1\nline 2\nline 3\nline 4\n"
	local := "line 1\nline 2\nlocal 3\nline 4\n"
	remote := "remote inserted 1\nremote inserted 2\nremote inserted 3\nline 1\nline 2\nremote 3\nline 4\n"

	_, chunks := conflictMarkerDetails(base, local, remote, true, true, true)
	if len(chunks) != 1 {
		t.Fatalf("chunks=%d, want 1: %#v", len(chunks), chunks)
	}
	chunk := chunks[0]
	if chunk.BaseStart != 3 || chunk.BaseEnd != 3 {
		t.Fatalf("base range=%d:%d, want 3:3", chunk.BaseStart, chunk.BaseEnd)
	}
	if chunk.RemoteStart != 6 || chunk.RemoteEnd != 6 {
		t.Fatalf("remote range=%d:%d, want 6:6", chunk.RemoteStart, chunk.RemoteEnd)
	}
	if chunk.LocalStart != 3 || chunk.LocalEnd != 3 {
		t.Fatalf("local range=%d:%d, want 3:3", chunk.LocalStart, chunk.LocalEnd)
	}
}

func TestBuildSyncSessionIncludesConflictDetails(t *testing.T) {
	comparison := &syncComparison{
		All: []fileChange{{
			Path:           "functions/enrich/handler.js",
			Status:         "conflict",
			LocalExists:    true,
			RemoteExists:   true,
			Local:          []string{"local"},
			Remote:         []string{"remote"},
			Conflict:       []string{"<<<<<<< cnips", "remote", "=======", "local", ">>>>>>> local"},
			ConflictChunks: []conflictChunks{{BaseStart: 1, BaseEnd: 1, Remote: []string{"remote"}, Local: []string{"local"}}},
		}},
		Conflicts: []fileChange{{Path: "functions/enrich/handler.js"}},
	}

	session := buildSyncSession("http://localhost:8090", "default", "", comparison)
	if len(session.Files) != 1 {
		t.Fatalf("files=%d, want 1", len(session.Files))
	}
	file := session.Files[0]
	if len(file.Conflict) == 0 {
		t.Fatal("expected conflict marker lines in session")
	}
	if len(file.ConflictChunks) != 1 || file.ConflictChunks[0].BaseStart != 1 {
		t.Fatalf("expected conflict chunks in session: %#v", file.ConflictChunks)
	}
}

func TestSameComponentSiblingFileUsesOwnDigest(t *testing.T) {
	baseHandler := manifestFile{Digest: "handler-base", CodeHash: "code-base"}
	localHandler := manifestFile{Digest: "handler-local", CodeHash: "code-local"}
	remoteHandler := manifestFile{Digest: "handler-remote", CodeHash: "code-remote"}
	basePackage := manifestFile{Digest: "package-same", CodeHash: "code-base"}
	localPackage := manifestFile{Digest: "package-same", CodeHash: "code-local"}
	remotePackage := manifestFile{Digest: "package-same", CodeHash: "code-remote"}

	if !sameManifestFile(basePackage, true, localPackage, true) {
		t.Fatal("expected unchanged package file to ignore sibling code hash changes")
	}
	if !sameManifestFile(basePackage, true, remotePackage, true) {
		t.Fatal("expected remote unchanged package file to ignore sibling code hash changes")
	}
	if sameManifestFile(baseHandler, true, localHandler, true) {
		t.Fatal("expected local handler change to be detected")
	}
	if sameManifestFile(baseHandler, true, remoteHandler, true) {
		t.Fatal("expected remote handler change to be detected")
	}
}

func TestIsComponentVersionDir(t *testing.T) {
	cases := map[string]bool{
		"latest":       true,
		"v1":           true,
		"v12":          true,
		"v123":         true,
		"v0":           true,
		"component":    false,
		"handler.js":   false,
		"v":            false,
		"v1a":          false,
		"V1":           false,
		"node_modules": false,
		".git":         false,
		"":             false,
	}
	for name, want := range cases {
		if got := isComponentVersionDir(name); got != want {
			t.Errorf("isComponentVersionDir(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestHasLocalVersionDirsDetectsMultiVersionLayout(t *testing.T) {
	root := t.TempDir()

	// No component directories at all → false.
	if hasLocalVersionDirs(root) {
		t.Fatal("expected false for empty root")
	}

	// Single-version component (no version subdirs) → false.
	singleDir := filepath.Join(root, "transformations", "my-tx")
	if err := os.MkdirAll(singleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(singleDir, "component.yaml"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if hasLocalVersionDirs(root) {
		t.Fatal("expected false for single-version layout")
	}

	// Add a version subdirectory → true.
	versionDir := filepath.Join(singleDir, "v1")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !hasLocalVersionDirs(root) {
		t.Fatal("expected true after adding v1/ subdirectory")
	}
}
