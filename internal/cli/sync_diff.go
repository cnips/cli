package cli

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cnips/cli/internal/platform"
)

type syncComparison struct {
	BaseHead          string
	LocalHead         string
	RemoteHead        string
	TrackedLocalHead  string
	TrackedRemoteHead string
	LocalOnly         []fileChange
	RemoteOnly        []fileChange
	AutoMerged        []fileChange
	Conflicts         []fileChange
	All               []fileChange
}

type syncSession struct {
	ID        string             `json:"id"`
	CreatedAt string             `json:"createdAt"`
	APIURL    string             `json:"apiUrl"`
	Workspace string             `json:"workspace"`
	Resolver  string             `json:"resolver,omitempty"`
	Heads     syncSessionHeads   `json:"heads"`
	Summary   syncSessionSummary `json:"summary"`
	Files     []syncSessionFile  `json:"files"`
}

type syncSessionHeads struct {
	Base          string `json:"base"`
	Local         string `json:"local"`
	Remote        string `json:"remote"`
	TrackedLocal  string `json:"trackedLocal,omitempty"`
	TrackedRemote string `json:"trackedRemote,omitempty"`
}

type syncSessionSummary struct {
	LocalOnly  int `json:"localOnly"`
	RemoteOnly int `json:"remoteOnly"`
	Conflicts  int `json:"conflicts"`
}

type syncSessionFile struct {
	Path           string           `json:"path"`
	Status         string           `json:"status"`
	LocalExists    bool             `json:"localExists"`
	RemoteExists   bool             `json:"remoteExists"`
	Local          []string         `json:"local,omitempty"`
	Remote         []string         `json:"remote,omitempty"`
	Conflict       []string         `json:"conflict,omitempty"`
	ConflictChunks []conflictChunks `json:"conflictChunks,omitempty"`
}

type syncResolver struct {
	root    string
	session syncSession
}

type applyResolutionRequest struct {
	Resolutions map[string]string `json:"resolutions"`
	ApplyRemote bool              `json:"applyRemote"`
}

type applyResolutionResponse struct {
	Applied []string `json:"applied"`
}

func compareLocalBaseRemote(root string, client *platform.Client, workspace string, base *pullManifest) (*syncComparison, error) {
	remoteRoot, err := os.MkdirTemp("", "cnips-remote-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(remoteRoot)

	var localFiles, remoteFiles map[string]string
	var localManifest, remoteManifest map[string]manifestFile
	if err := runConcurrent(
		func() error {
			var err error
			localFiles, err = collectComparableFiles(root)
			if err != nil {
				return err
			}
			localManifest, err = manifestFilesForRoot(root, localFiles)
			return err
		},
		func() error {
			if err := writeRemoteSnapshot(client, workspace, remoteRoot); err != nil {
				return err
			}
			var err error
			remoteFiles, err = collectComparableFiles(remoteRoot)
			if err != nil {
				return err
			}
			remoteManifest, err = manifestFilesForRoot(remoteRoot, remoteFiles)
			return err
		},
	); err != nil {
		return nil, err
	}
	aliases, err := renameAliasesForCompare(root, remoteRoot)
	if err != nil {
		return nil, err
	}
	applyRenameAliasesToStringMap(remoteFiles, aliases)
	applyRenameAliasesToManifest(remoteManifest, aliases)
	conflictState, err := readConflictState(root, workspace)
	if err != nil {
		return nil, err
	}

	baseManifest := map[string]manifestFile{}
	baseHead, trackedLocalHead, trackedRemoteHead := "", "", ""
	if base != nil && base.Files != nil {
		baseManifest = base.Files
		baseHead = base.BaseHead
		trackedLocalHead = base.LocalHead
		trackedRemoteHead = base.RemoteHead
	}
	originalBaseHead := baseHead
	if normalizeBaseToAgreedLocalRemote(baseManifest, localManifest, remoteManifest) {
		baseHead = manifestFingerprint(baseManifest)
		if trackedLocalHead == "" || trackedLocalHead == originalBaseHead {
			trackedLocalHead = baseHead
		}
		if trackedRemoteHead == "" || trackedRemoteHead == originalBaseHead {
			trackedRemoteHead = baseHead
		}
	}

	paths := map[string]bool{}
	for path := range localManifest {
		paths[path] = true
	}
	for path := range remoteManifest {
		paths[path] = true
	}
	for path := range baseManifest {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)

	out := &syncComparison{
		BaseHead:          firstNonEmpty(baseHead, manifestFingerprint(baseManifest)),
		LocalHead:         manifestFingerprint(localManifest),
		RemoteHead:        manifestFingerprint(remoteManifest),
		TrackedLocalHead:  trackedLocalHead,
		TrackedRemoteHead: trackedRemoteHead,
	}
	for _, path := range ordered {
		localFile, hasLocal := localManifest[path]
		remoteFile, hasRemote := remoteManifest[path]
		baseFile, hasBase := baseManifest[path]
		localChanged := !sameManifestFile(localFile, hasLocal, baseFile, hasBase)
		remoteChanged := !sameManifestFile(remoteFile, hasRemote, baseFile, hasBase)
		if !localChanged && !remoteChanged {
			continue
		}

		status := classifySyncStatus(hasLocal, hasRemote, localChanged, remoteChanged)
		if localChanged && remoteChanged && isResolvedRecordedConflict(path, localFiles[path], remoteFile, hasRemote, conflictState) {
			if sameManifestFile(localFile, hasLocal, remoteFile, hasRemote) {
				continue
			}
			status = classifySyncStatus(hasLocal, hasRemote, true, false)
			remoteChanged = false
		} else if localChanged && remoteChanged {
			merged, ok := mergeFileChange(baseFile.Content, localFiles[path], remoteFiles[path], hasBase, hasLocal, hasRemote)
			if ok {
				change := fileChange{
					Path:              path,
					Status:            "merge",
					LocalExists:       hasLocal,
					RemoteExists:      hasRemote,
					Local:             splitLines(merged),
					Remote:            splitLines(remoteFiles[path]),
					RemoteDigest:      remoteFile.Digest,
					RemoteFingerprint: remoteFile.Fingerprint(),
				}
				out.AutoMerged = append(out.AutoMerged, change)
				out.All = append(out.All, change)
				continue
			}
		}
		conflictLines, conflictChunks := conflictMarkerDetails(baseFile.Content, localFiles[path], remoteFiles[path], hasBase, hasLocal, hasRemote)
		change := fileChange{
			Path:              path,
			Status:            status,
			LocalExists:       hasLocal,
			RemoteExists:      hasRemote,
			Local:             splitLines(localFiles[path]),
			Remote:            splitLines(remoteFiles[path]),
			Conflict:          conflictLines,
			ConflictChunks:    conflictChunks,
			RemoteDigest:      remoteFile.Digest,
			RemoteFingerprint: remoteFile.Fingerprint(),
		}
		switch {
		case localChanged && remoteChanged:
			out.Conflicts = append(out.Conflicts, change)
		case localChanged:
			out.LocalOnly = append(out.LocalOnly, change)
		default:
			out.RemoteOnly = append(out.RemoteOnly, change)
		}
		out.All = append(out.All, change)
	}
	return out, nil
}

func normalizeBaseToAgreedLocalRemote(base, local, remote map[string]manifestFile) bool {
	changed := false
	for path := range base {
		_, hasLocal := local[path]
		_, hasRemote := remote[path]
		if !hasLocal && !hasRemote {
			delete(base, path)
			changed = true
		}
	}
	for path, localFile := range local {
		remoteFile, hasRemote := remote[path]
		if !hasRemote || !sameManifestFile(localFile, true, remoteFile, true) {
			continue
		}
		baseFile, hasBase := base[path]
		if sameManifestFile(localFile, true, baseFile, hasBase) {
			continue
		}
		base[path] = localFile
		changed = true
	}
	return changed
}

func isResolvedRecordedConflict(path, localBody string, remoteFile manifestFile, hasRemote bool, state *conflictState) bool {
	if state == nil || state.Files == nil || hasConflictMarkers(localBody) {
		return false
	}
	record, ok := state.Files[path]
	if !ok || !hasRemote {
		return false
	}
	return (record.RemoteFingerprint != "" && record.RemoteFingerprint == remoteFile.Fingerprint()) ||
		(record.RemoteDigest != "" && record.RemoteDigest == remoteFile.Digest)
}

type lineEdit struct {
	start int
	end   int
	lines []string
}

type conflictChunks struct {
	BaseStart   int      `json:"baseStart"`
	BaseEnd     int      `json:"baseEnd"`
	RemoteStart int      `json:"remoteStart"`
	RemoteEnd   int      `json:"remoteEnd"`
	LocalStart  int      `json:"localStart"`
	LocalEnd    int      `json:"localEnd"`
	Base        []string `json:"base,omitempty"`
	Remote      []string `json:"remote,omitempty"`
	Local       []string `json:"local,omitempty"`
}

func mergeFileChange(base, local, remote string, hasBase, hasLocal, hasRemote bool) (string, bool) {
	if !hasBase || !hasLocal || !hasRemote {
		return "", false
	}
	if local == remote {
		return local, true
	}
	if local == base {
		return remote, true
	}
	if remote == base {
		return local, true
	}
	baseLines := splitLines(base)
	localEdits := lineEdits(baseLines, splitLines(local))
	remoteEdits := lineEdits(baseLines, splitLines(remote))
	if editsConflict(localEdits, remoteEdits) {
		return "", false
	}
	edits := append(append([]lineEdit{}, localEdits...), remoteEdits...)
	return joinLines(applyLineEdits(baseLines, edits)), true
}

func conflictMarkerLines(base, local, remote string, hasBase, hasLocal, hasRemote bool) []string {
	lines, _ := conflictMarkerDetails(base, local, remote, hasBase, hasLocal, hasRemote)
	return lines
}

func conflictMarkerDetails(base, local, remote string, hasBase, hasLocal, hasRemote bool) ([]string, []conflictChunks) {
	if !hasBase || !hasLocal || !hasRemote {
		return nil, nil
	}
	baseLines := splitLines(base)
	localEdits := lineEdits(baseLines, splitLines(local))
	remoteEdits := lineEdits(baseLines, splitLines(remote))
	if !editsConflict(localEdits, remoteEdits) {
		return nil, nil
	}

	regions := conflictRegions(localEdits, remoteEdits)
	if len(regions) == 0 {
		return nil, nil
	}

	var outside []lineEdit
	for _, edit := range localEdits {
		if !editOverlapsAnyRegion(edit, regions) {
			outside = append(outside, edit)
		}
	}
	for _, edit := range remoteEdits {
		if !editOverlapsAnyRegion(edit, regions) {
			outside = append(outside, edit)
		}
	}

	chunks := make([]conflictChunks, 0, len(regions))
	for _, region := range regions {
		start, end := region.start, region.end
		localInside := editsInsideRegion(localEdits, region)
		remoteInside := editsInsideRegion(remoteEdits, region)
		remoteStart := changedLineIndex(start, remoteEdits)
		localStart := changedLineIndex(start, localEdits)
		baseSegment := append([]string{}, baseLines[start:end]...)
		remoteSegment := applyLineEdits(baseSegment, shiftLineEdits(remoteInside, -start))
		localSegment := applyLineEdits(baseSegment, shiftLineEdits(localInside, -start))
		marker := append([]string{"<<<<<<< cnips"}, remoteSegment...)
		marker = append(marker, "=======")
		marker = append(marker, localSegment...)
		marker = append(marker, ">>>>>>> local")
		outside = append(outside, lineEdit{start: start, end: end, lines: marker})
		chunks = append(chunks, conflictChunks{
			BaseStart:   start + 1,
			BaseEnd:     end,
			RemoteStart: remoteStart + 1,
			RemoteEnd:   remoteStart + len(remoteSegment),
			LocalStart:  localStart + 1,
			LocalEnd:    localStart + len(localSegment),
			Base:        baseSegment,
			Remote:      remoteSegment,
			Local:       localSegment,
		})
	}
	return applyLineEdits(baseLines, outside), chunks
}

func changedLineIndex(baseIndex int, edits []lineEdit) int {
	delta := 0
	for _, edit := range edits {
		if edit.end > baseIndex || (edit.start == edit.end && edit.start >= baseIndex) {
			continue
		}
		delta += len(edit.lines) - (edit.end - edit.start)
	}
	return baseIndex + delta
}

type conflictRegion struct {
	start int
	end   int
}

func conflictRegions(localEdits, remoteEdits []lineEdit) []conflictRegion {
	var regions []conflictRegion
	for _, localEdit := range localEdits {
		for _, remoteEdit := range remoteEdits {
			if sameLineEdit(localEdit, remoteEdit) || !lineEditsOverlap(localEdit, remoteEdit) {
				continue
			}
			regions = append(regions, conflictRegion{
				start: minInt(localEdit.start, remoteEdit.start),
				end:   maxInt(localEdit.end, remoteEdit.end),
			})
		}
	}
	sort.Slice(regions, func(i, j int) bool {
		if regions[i].start == regions[j].start {
			return regions[i].end < regions[j].end
		}
		return regions[i].start < regions[j].start
	})
	merged := regions[:0]
	for _, region := range regions {
		if len(merged) == 0 || region.start > merged[len(merged)-1].end {
			merged = append(merged, region)
			continue
		}
		if region.end > merged[len(merged)-1].end {
			merged[len(merged)-1].end = region.end
		}
	}
	return merged
}

func editOverlapsAnyRegion(edit lineEdit, regions []conflictRegion) bool {
	for _, region := range regions {
		if lineEditOverlapsRegion(edit, region) {
			return true
		}
	}
	return false
}

func editsInsideRegion(edits []lineEdit, region conflictRegion) []lineEdit {
	var inside []lineEdit
	for _, edit := range edits {
		if lineEditOverlapsRegion(edit, region) {
			inside = append(inside, edit)
		}
	}
	return inside
}

func lineEditOverlapsRegion(edit lineEdit, region conflictRegion) bool {
	if edit.start == edit.end {
		return edit.start >= region.start && edit.start <= region.end
	}
	return edit.start < region.end && region.start < edit.end
}

func shiftLineEdits(edits []lineEdit, delta int) []lineEdit {
	out := make([]lineEdit, 0, len(edits))
	for _, edit := range edits {
		out = append(out, shiftLineEdit(edit, delta))
	}
	return out
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func shiftLineEdit(edit lineEdit, delta int) lineEdit {
	return lineEdit{start: edit.start + delta, end: edit.end + delta, lines: edit.lines}
}

func applyLineEdits(base []string, edits []lineEdit) []string {
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].start == edits[j].start {
			return edits[i].end > edits[j].end
		}
		return edits[i].start > edits[j].start
	})
	out := append([]string{}, base...)
	for _, edit := range edits {
		next := append([]string{}, out[:edit.start]...)
		next = append(next, edit.lines...)
		next = append(next, out[edit.end:]...)
		out = next
	}
	return out
}

func editsConflict(left, right []lineEdit) bool {
	for _, l := range left {
		for _, r := range right {
			if sameLineEdit(l, r) {
				continue
			}
			if lineEditsOverlap(l, r) {
				return true
			}
		}
	}
	return false
}

func sameLineEdit(left, right lineEdit) bool {
	if left.start != right.start || left.end != right.end || len(left.lines) != len(right.lines) {
		return false
	}
	for i := range left.lines {
		if left.lines[i] != right.lines[i] {
			return false
		}
	}
	return true
}

func lineEditsOverlap(left, right lineEdit) bool {
	leftInsert := left.start == left.end
	rightInsert := right.start == right.end
	switch {
	case leftInsert && rightInsert:
		return left.start == right.start
	case leftInsert:
		return left.start > right.start && left.start < right.end
	case rightInsert:
		return right.start > left.start && right.start < left.end
	default:
		return left.start < right.end && right.start < left.end
	}
}

func lineEdits(base, changed []string) []lineEdit {
	lcs := make([][]int, len(base)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(changed)+1)
	}
	for i := len(base) - 1; i >= 0; i-- {
		for j := len(changed) - 1; j >= 0; j-- {
			if base[i] == changed[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var edits []lineEdit
	active := false
	start := 0
	replacement := []string{}
	closeEdit := func(i int) {
		if !active {
			return
		}
		edits = append(edits, lineEdit{start: start, end: i, lines: append([]string{}, replacement...)})
		active = false
		replacement = nil
	}

	i, j := 0, 0
	for i < len(base) || j < len(changed) {
		if i < len(base) && j < len(changed) && base[i] == changed[j] {
			closeEdit(i)
			i++
			j++
			continue
		}
		if !active {
			active = true
			start = i
		}
		if j < len(changed) && (i == len(base) || lcs[i][j+1] >= lcs[i+1][j]) {
			replacement = append(replacement, changed[j])
			j++
			continue
		}
		i++
	}
	closeEdit(i)
	return edits
}

func isFreshHydrationProject(root string) bool {
	files, err := collectComparableFiles(root)
	if err != nil {
		return false
	}
	for path := range files {
		if isDerivedComparableFile(path) || isInitSampleFile(path) {
			continue
		}
		return false
	}
	return true
}

func isInitSampleFile(path string) bool {
	switch path {
	case "pipelines/hello-world/pipeline.yaml",
		"pipelines/hello-world/transforms/greet.jsonata",
		"functions/hello-fn/cnips.fn.yaml",
		"functions/hello-fn/handler.js",
		"functions/hello-fn/package.json":
		return true
	default:
		return false
	}
}

func isDerivedComparableFile(path string) bool {
	return path == "cnips.lock"
}

func nonDerivedChanges(changes []fileChange) []fileChange {
	out := make([]fileChange, 0, len(changes))
	for _, change := range changes {
		if isDerivedComparableFile(change.Path) {
			continue
		}
		out = append(out, change)
	}
	return out
}

func printSyncSummary(comparison *syncComparison) {
	if comparison == nil || len(comparison.All) == 0 {
		return
	}
	fmt.Println("\nChanged files:")
	for _, change := range comparison.All {
		fmt.Printf("  %-12s %s\n", change.Status, change.Path)
	}
}

func sameManifestFile(a manifestFile, aok bool, b manifestFile, bok bool) bool {
	if aok != bok {
		return false
	}
	if !aok {
		return true
	}
	return a.FileIdentity() == b.FileIdentity()
}

func classifySyncStatus(hasLocal, hasRemote, localChanged, remoteChanged bool) string {
	switch {
	case localChanged && remoteChanged:
		return "conflict"
	case localChanged && !hasLocal && hasRemote:
		return "delete-remote"
	case localChanged:
		return "local"
	case remoteChanged && !hasRemote && hasLocal:
		return "delete-local"
	default:
		return "remote"
	}
}

func writeSyncSession(apiURL, workspace, resolverURL string, comparison *syncComparison) (string, error) {
	publicDir, ok := syncSessionPublicDir()
	if !ok {
		return "", nil
	}
	session := syncSession{
		ID:        syncSessionID(apiURL, workspace),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		APIURL:    apiURL,
		Workspace: workspace,
		Resolver:  resolverURL,
		Heads: syncSessionHeads{
			Base:          comparison.BaseHead,
			Local:         comparison.LocalHead,
			Remote:        comparison.RemoteHead,
			TrackedLocal:  comparison.TrackedLocalHead,
			TrackedRemote: comparison.TrackedRemoteHead,
		},
		Summary: syncSessionSummary{
			LocalOnly:  len(comparison.LocalOnly),
			RemoteOnly: len(comparison.RemoteOnly),
			Conflicts:  len(comparison.Conflicts),
		},
		Files: make([]syncSessionFile, 0, len(comparison.All)),
	}
	for _, change := range comparison.All {
		session.Files = append(session.Files, syncSessionFile{
			Path:           change.Path,
			Status:         change.Status,
			LocalExists:    change.LocalExists,
			RemoteExists:   change.RemoteExists,
			Local:          change.Local,
			Remote:         change.Remote,
			Conflict:       change.Conflict,
			ConflictChunks: change.ConflictChunks,
		})
	}

	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(publicDir, session.ID+".json")
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}

	webURL := strings.TrimRight(firstNonEmpty(os.Getenv("CNIPS_WEBAPP_URL"), "http://localhost:3000"), "/")
	return fmt.Sprintf("%s/cnips-sync/diff?session=%s", webURL, url.QueryEscape("/cnips-diff-sessions/"+session.ID+".json")), nil
}

func buildSyncSession(apiURL, workspace, resolverURL string, comparison *syncComparison) syncSession {
	session := syncSession{
		ID:        syncSessionID(apiURL, workspace),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		APIURL:    apiURL,
		Workspace: workspace,
		Resolver:  resolverURL,
		Heads: syncSessionHeads{
			Base:          comparison.BaseHead,
			Local:         comparison.LocalHead,
			Remote:        comparison.RemoteHead,
			TrackedLocal:  comparison.TrackedLocalHead,
			TrackedRemote: comparison.TrackedRemoteHead,
		},
		Summary: syncSessionSummary{
			LocalOnly:  len(comparison.LocalOnly),
			RemoteOnly: len(comparison.RemoteOnly),
			Conflicts:  len(comparison.Conflicts),
		},
		Files: make([]syncSessionFile, 0, len(comparison.All)),
	}
	for _, change := range comparison.All {
		session.Files = append(session.Files, syncSessionFile{
			Path:           change.Path,
			Status:         change.Status,
			LocalExists:    change.LocalExists,
			RemoteExists:   change.RemoteExists,
			Local:          change.Local,
			Remote:         change.Remote,
			Conflict:       change.Conflict,
			ConflictChunks: change.ConflictChunks,
		})
	}
	return session
}

func syncSessionPublicDir() (string, bool) {
	if dir := strings.TrimSpace(os.Getenv("CNIPS_WEBAPP_PUBLIC_DIR")); dir != "" {
		return filepath.Join(dir, "cnips-diff-sessions"), true
	}
	for _, dir := range []string{
		"/home/widasishan/Documents/Cnips/cnips-webapp-main/public/cnips-diff-sessions",
		filepath.Join("..", "cnips-webapp-main", "public", "cnips-diff-sessions"),
	} {
		if info, err := os.Stat(filepath.Dir(dir)); err == nil && info.IsDir() {
			return dir, true
		}
	}
	return "", false
}

func syncSessionID(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "\n") + time.Now().UTC().Format(time.RFC3339Nano))) //nolint:gosec // local display session id.
	return hex.EncodeToString(sum[:])[:12]
}

func printSyncLink(apiURL, workspace string, comparison *syncComparison) {
	link, err := writeSyncSession(apiURL, workspace, "", comparison)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: writing visual diff session: %v\n", err)
		return
	}
	if link == "" {
		fmt.Fprintln(os.Stderr, "warn: visual diff link unavailable; set CNIPS_WEBAPP_PUBLIC_DIR to cnips-webapp-main/public")
		return
	}
	fmt.Printf("\nVisual diff: %s\n", link)
}

func printSyncLinkWithResolver(root, apiURL, workspace string, comparison *syncComparison) error {
	if err := applyRemoteOnly(root, comparison.RemoteOnly); err != nil {
		return err
	}
	if err := applyAutoMerged(root, comparison.AutoMerged); err != nil {
		return err
	}
	if err := recordConflictState(root, workspace, comparison.Conflicts); err != nil {
		return err
	}
	if err := applyConflictMarkers(root, comparison.Conflicts); err != nil {
		return err
	}
	printEditorConflictInstructions(workspace, comparison)
	return nil
}

func (r *syncResolver) applyFile(file syncSessionFile, choice string) error {
	path, err := safeLocalPath(r.root, file.Path)
	if err != nil {
		return err
	}
	var lines []string
	exists := true
	switch choice {
	case "local":
		lines = file.Local
		exists = file.LocalExists
	case "remote":
		lines = file.Remote
		exists = file.RemoteExists
	case "both":
		lines = append([]string{"<<<<<<< cnips"}, file.Remote...)
		lines = append(lines, "=======")
		lines = append(lines, file.Local...)
		lines = append(lines, ">>>>>>> local")
	default:
		return fmt.Errorf("unsupported resolution %q for %s", choice, file.Path)
	}
	if !exists {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		pruneEmptyArtifactParents(r.root, filepath.Dir(path))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func safeLocalPath(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("unsafe path %q", rel)
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if cleanPath != cleanRoot && !strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes project root: %s", rel)
	}
	return cleanPath, nil
}

func artifactPathPrefix(path string) string {
	path = strings.Trim(filepath.ToSlash(path), "/")
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return ""
	}
	switch parts[0] {
	case "pipelines", "transformations", "approvals", "switches", "decisions", "sources", "destinations", "functions":
		return parts[0] + "/" + parts[1]
	default:
		return ""
	}
}

func pruneEmptyArtifactParents(root, dir string) {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return
	}
	current, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	for current != cleanRoot && strings.HasPrefix(current, cleanRoot+string(os.PathSeparator)) {
		rel, err := filepath.Rel(cleanRoot, current)
		if err != nil {
			return
		}
		if rel == "." || artifactPathPrefix(filepath.ToSlash(rel)) == "" {
			return
		}
		err = os.Remove(current)
		if err != nil {
			return
		}
		current = filepath.Dir(current)
	}
}

func sortPathsByDepthDesc(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		leftDepth := strings.Count(paths[i], "/")
		rightDepth := strings.Count(paths[j], "/")
		if leftDepth == rightDepth {
			return paths[i] > paths[j]
		}
		return leftDepth > rightDepth
	})
}

type terminalProgress struct {
	label string
	total int
	done  int
}

func newTerminalProgress(label string, total int) *terminalProgress {
	p := &terminalProgress{label: label, total: total}
	p.Render()
	return p
}

func (p *terminalProgress) Advance() {
	if p == nil {
		return
	}
	p.done++
	if p.done > p.total {
		p.done = p.total
	}
	p.Render()
}

func (p *terminalProgress) Finish() {
	if p == nil {
		return
	}
	p.done = p.total
	p.Render()
	fmt.Println()
}

func (p *terminalProgress) Render() {
	total := p.total
	if total <= 0 {
		total = 1
	}
	percent := p.done * 100 / total
	width := 30
	filled := percent * width / 100
	fmt.Printf("\r%s [%s%s] %3d%%", p.label, strings.Repeat("=", filled), strings.Repeat(" ", width-filled), percent)
}
