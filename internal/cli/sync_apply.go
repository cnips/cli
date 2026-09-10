package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type conflictState struct {
	Files map[string]conflictRecord `json:"files"`
}

type conflictRecord struct {
	RemoteDigest      string `json:"remoteDigest"`
	RemoteFingerprint string `json:"remoteFingerprint,omitempty"`
}

func applyRemoteOnly(root string, changes []fileChange) error {
	resolver := &syncResolver{root: root}
	for _, change := range nonDerivedChanges(changes) {
		file := syncSessionFile{
			Path:         change.Path,
			Status:       change.Status,
			LocalExists:  change.LocalExists,
			RemoteExists: change.RemoteExists,
			Local:        change.Local,
			Remote:       change.Remote,
		}
		if err := resolver.applyFile(file, "remote"); err != nil {
			return err
		}
	}
	return nil
}

func applyAutoMerged(root string, changes []fileChange) error {
	resolver := &syncResolver{root: root}
	for _, change := range nonDerivedChanges(changes) {
		file := syncSessionFile{
			Path:         change.Path,
			Status:       change.Status,
			LocalExists:  true,
			RemoteExists: change.RemoteExists,
			Local:        change.Local,
			Remote:       change.Remote,
		}
		if err := resolver.applyFile(file, "local"); err != nil {
			return err
		}
	}
	return nil
}

func applyConflictMarkers(root string, changes []fileChange) error {
	resolver := &syncResolver{root: root}
	for _, change := range nonDerivedChanges(changes) {
		if len(change.Conflict) > 0 {
			file := syncSessionFile{
				Path:         change.Path,
				Status:       change.Status,
				LocalExists:  true,
				RemoteExists: change.RemoteExists,
				Local:        change.Conflict,
				Remote:       change.Remote,
			}
			if err := resolver.applyFile(file, "local"); err != nil {
				return err
			}
			continue
		}
		file := syncSessionFile{
			Path:         change.Path,
			Status:       change.Status,
			LocalExists:  change.LocalExists,
			RemoteExists: change.RemoteExists,
			Local:        change.Local,
			Remote:       change.Remote,
		}
		if err := resolver.applyFile(file, "both"); err != nil {
			return err
		}
	}
	return nil
}

func recordConflictState(root, workspace string, changes []fileChange) error {
	if len(changes) == 0 {
		return nil
	}
	state, err := readConflictState(root, workspace)
	if err != nil {
		return err
	}
	if state.Files == nil {
		state.Files = map[string]conflictRecord{}
	}
	for _, change := range nonDerivedChanges(changes) {
		if !change.RemoteExists {
			delete(state.Files, change.Path)
			continue
		}
		state.Files[change.Path] = conflictRecord{
			RemoteDigest:      firstNonEmpty(change.RemoteDigest, digestLines(change.Remote)),
			RemoteFingerprint: firstNonEmpty(change.RemoteFingerprint, change.RemoteDigest, digestLines(change.Remote)),
		}
	}
	return writeConflictState(root, workspace, state)
}

func clearConflictState(root, workspace string, changes []fileChange) error {
	if len(changes) == 0 {
		return nil
	}
	state, err := readConflictState(root, workspace)
	if err != nil {
		return err
	}
	for _, change := range changes {
		delete(state.Files, change.Path)
	}
	if len(state.Files) == 0 {
		err := os.Remove(conflictStatePath(root, workspace))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return writeConflictState(root, workspace, state)
}

func readConflictState(root, workspace string) (*conflictState, error) {
	data, err := os.ReadFile(conflictStatePath(root, workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return &conflictState{Files: map[string]conflictRecord{}}, nil
		}
		return nil, err
	}
	var state conflictState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Files == nil {
		state.Files = map[string]conflictRecord{}
	}
	return &state, nil
}

func writeConflictState(root, workspace string, state *conflictState) error {
	path := conflictStatePath(root, workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func conflictStatePath(root, workspace string) string {
	return filepath.Join(root, ".cnips", "state", "workspaces", safeStateName(workspace), "conflicts.json")
}

func updateLocalManifestForRemoteChanges(root, workspace string, changes []fileChange) error {
	if len(changes) == 0 {
		return nil
	}
	base, err := readLocalPullManifest(root, workspace)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		base = &pullManifest{Files: map[string]manifestFile{}}
	}
	if base.Files == nil {
		base.Files = map[string]manifestFile{}
	}
	for _, change := range nonDerivedChanges(changes) {
		if !change.RemoteExists {
			delete(base.Files, change.Path)
			continue
		}
		file := manifestFile{
			Digest:  firstNonEmpty(change.RemoteDigest, digestLines(change.Remote)),
			Content: joinLines(change.Remote),
		}
		if change.RemoteFingerprint != "" && change.RemoteFingerprint != file.Digest {
			file.CodeHash = change.RemoteFingerprint
		}
		base.Files[change.Path] = file
	}
	head := manifestFingerprint(base.Files)
	base.BaseHead = head
	base.RemoteHead = head
	base.LocalHead = ""
	return writeLocalPullManifest(root, workspace, base)
}

func remoteAdvancingChanges(comparison *syncComparison) []fileChange {
	if comparison == nil {
		return nil
	}
	out := make([]fileChange, 0, len(comparison.RemoteOnly)+len(comparison.AutoMerged))
	out = append(out, comparison.RemoteOnly...)
	out = append(out, comparison.AutoMerged...)
	return out
}

func printEditorConflictInstructions(workspace string, comparison *syncComparison) {
	if len(comparison.Conflicts) > 0 {
		fmt.Printf("\nConflict markers were written into %d file(s) for workspace=%s.\n", len(nonDerivedChanges(comparison.Conflicts)), workspace)
		fmt.Println("Open the files in your editor, resolve the <<<<<<< cnips / ======= / >>>>>>> local sections, then run:")
		fmt.Println("  cnips status")
		fmt.Println("  cnips push")
		return
	}
	if len(comparison.RemoteOnly) > 0 {
		fmt.Printf("\nApplied %d remote change(s) from cnips into the working tree.\n", len(nonDerivedChanges(comparison.RemoteOnly)))
	}
	if len(comparison.AutoMerged) > 0 {
		fmt.Printf("\nAuto-merged %d file(s) with non-overlapping local and cnips changes.\n", len(nonDerivedChanges(comparison.AutoMerged)))
	}
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n") + "\n"
}

func digestLines(lines []string) string {
	sum := sha256.Sum256([]byte(joinLines(lines)))
	return fmt.Sprintf("%x", sum[:])
}

func hasConflictMarkers(body string) bool {
	return strings.Contains(body, "<<<<<<< cnips") &&
		strings.Contains(body, "=======") &&
		strings.Contains(body, ">>>>>>> local")
}
