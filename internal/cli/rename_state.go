package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/cnips/cli/internal/artifact"
)

type renameState struct {
	Entries []renameRecord `json:"entries"`
}

type renameRecord struct {
	Kind      string `json:"kind"`
	OldPrefix string `json:"oldPrefix"`
	NewPrefix string `json:"newPrefix"`
}

func recordRenameAlias(root string, target renameTarget) error {
	if target.Kind == "app" || target.Kind == "component" {
		return nil
	}
	record := renameRecord{
		Kind:      target.Kind,
		OldPrefix: target.Base + "/" + target.OldSlug,
		NewPrefix: target.Base + "/" + target.NewSlug,
	}
	state, err := readRenameState(root)
	if err != nil {
		return err
	}
	out := state.Entries[:0]
	for _, entry := range state.Entries {
		if normalizePathPrefix(entry.NewPrefix) == normalizePathPrefix(record.OldPrefix) {
			record.OldPrefix = entry.OldPrefix
			continue
		}
		if normalizePathPrefix(entry.OldPrefix) == normalizePathPrefix(record.OldPrefix) ||
			normalizePathPrefix(entry.NewPrefix) == normalizePathPrefix(record.NewPrefix) {
			continue
		}
		out = append(out, entry)
	}
	state.Entries = append(out, record)
	return writeRenameState(root, state)
}

func readRenameState(root string) (*renameState, error) {
	data, err := os.ReadFile(renameStatePath(root))
	if os.IsNotExist(err) {
		return &renameState{}, nil
	}
	if err != nil {
		return nil, err
	}
	var state renameState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func writeRenameState(root string, state *renameState) error {
	path := renameStatePath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func renameStatePath(root string) string {
	return filepath.Join(root, ".cnips", "state", "renames.json")
}

func renameAliasesForCompare(localRoot, remoteRoot string) ([]renameRecord, error) {
	state, err := readRenameState(localRoot)
	if err != nil {
		return nil, err
	}
	out := append([]renameRecord{}, state.Entries...)
	inferred, err := inferRenameAliases(localRoot, remoteRoot)
	if err != nil {
		return nil, err
	}
	for _, record := range inferred {
		if !hasRenameAlias(out, record) {
			out = append(out, record)
		}
	}
	return out, nil
}

func hasRenameAlias(records []renameRecord, candidate renameRecord) bool {
	for _, record := range records {
		if normalizePathPrefix(record.OldPrefix) == normalizePathPrefix(candidate.OldPrefix) &&
			normalizePathPrefix(record.NewPrefix) == normalizePathPrefix(candidate.NewPrefix) {
			return true
		}
	}
	return false
}

func inferRenameAliases(localRoot, remoteRoot string) ([]renameRecord, error) {
	local, err := artifactIDsByPrefix(localRoot)
	if err != nil {
		return nil, err
	}
	remote, err := artifactIDsByPrefix(remoteRoot)
	if err != nil {
		return nil, err
	}
	var out []renameRecord
	for remoteID, remoteRecord := range remote {
		localRecord, ok := local[remoteID]
		if !ok || normalizePathPrefix(localRecord.OldPrefix) == normalizePathPrefix(remoteRecord.OldPrefix) {
			continue
		}
		out = append(out, renameRecord{
			Kind:      localRecord.Kind,
			OldPrefix: remoteRecord.OldPrefix,
			NewPrefix: localRecord.OldPrefix,
		})
	}
	return out, nil
}

func artifactIDsByPrefix(root string) (map[string]renameRecord, error) {
	out := map[string]renameRecord{}
	for _, base := range localComponentBases {
		entries, err := os.ReadDir(filepath.Join(root, base.dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root, base.dir, entry.Name())
			comp, err := artifact.ParseComponent(preferredComponentDir(dir))
			if err != nil || comp.Metadata.ID == "" {
				continue
			}
			out[base.kind+"\x00"+comp.Metadata.ID] = renameRecord{Kind: base.kind, OldPrefix: base.dir + "/" + entry.Name()}
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "functions"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		fn, err := artifact.ParseFunction(filepath.Join(root, "functions", entry.Name()))
		if err != nil || fn.Metadata.ID == "" {
			continue
		}
		out["function\x00"+fn.Metadata.ID] = renameRecord{Kind: "function", OldPrefix: "functions/" + entry.Name()}
	}
	return out, nil
}

func applyRenameAliasesToStringMap(files map[string]string, aliases []renameRecord) {
	for _, alias := range aliases {
		moveAliasedPath(files, alias, func(v string) string { return v })
	}
}

func applyRenameAliasesToManifest(files map[string]manifestFile, aliases []renameRecord) {
	for _, alias := range aliases {
		moveAliasedPath(files, alias, func(v manifestFile) manifestFile { return v })
	}
}

func moveAliasedPath[T any](files map[string]T, alias renameRecord, clone func(T) T) {
	oldPrefix := normalizePathPrefix(alias.OldPrefix)
	newPrefix := normalizePathPrefix(alias.NewPrefix)
	if oldPrefix == "" || newPrefix == "" || oldPrefix == newPrefix {
		return
	}
	for path, value := range files {
		normalized := filepath.ToSlash(path)
		if !strings.HasPrefix(normalized, oldPrefix+"/") {
			continue
		}
		newPath := newPrefix + "/" + strings.TrimPrefix(normalized, oldPrefix+"/")
		if _, exists := files[newPath]; exists {
			continue
		}
		files[newPath] = clone(value)
		delete(files, path)
	}
}

func normalizePathPrefix(prefix string) string {
	return strings.Trim(strings.TrimSpace(filepath.ToSlash(prefix)), "/")
}

func renamePrefixSlug(prefix string) string {
	prefix = normalizePathPrefix(prefix)
	if idx := strings.LastIndex(prefix, "/"); idx >= 0 {
		return prefix[idx+1:]
	}
	return prefix
}
