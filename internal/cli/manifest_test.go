package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChangesFromManifestReportsOnlyLocalChanges(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "transformations/normalize/component.yaml", "kind: Component\n")
	writeTestFile(t, root, "transformations/normalize/handler.js", "export default 1\n")

	files, err := collectComparableFiles(root)
	if err != nil {
		t.Fatalf("collectComparableFiles: %v", err)
	}
	manifest := &pullManifest{Files: manifestFiles(files)}
	writeTestFile(t, root, "transformations/normalize/handler.js", "export default 2\n")
	writeTestFile(t, root, "sources/orders/component.yaml", "kind: Component\n")

	changes, err := changesFromManifest(root, manifest)
	if err != nil {
		t.Fatalf("changesFromManifest: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes, got %#v", changes)
	}
	if changes[0].Path != "sources/orders/component.yaml" || changes[0].Status != "add-local" {
		t.Fatalf("unexpected first change: %#v", changes[0])
	}
	if changes[1].Path != "transformations/normalize/handler.js" || changes[1].Status != "modify" {
		t.Fatalf("unexpected second change: %#v", changes[1])
	}
}

func TestFilterBundleByChangesKeepsOnlyChangedArtifacts(t *testing.T) {
	bundle := &localBundle{
		Transformations: []localComponent{{Slug: "normalize"}, {Slug: "unchanged"}},
		Sources:         []localComponent{{Slug: "orders"}},
		Apps:            []localComponent{{Slug: "send-mail"}},
		Functions:       []localFunction{{Slug: "enrich"}},
	}
	changes := []fileChange{
		{Path: "transformations/normalize/handler.js", Status: "modify"},
		{Path: "functions/enrich/cnips.fn.yaml", Status: "modify"},
	}

	filtered := filterBundleByChanges(bundle, changes)
	if len(filtered.Transformations) != 1 || filtered.Transformations[0].Slug != "normalize" {
		t.Fatalf("unexpected transformations: %#v", filtered.Transformations)
	}
	if !filtered.Transformations[0].HasCodeChanges {
		t.Fatal("expected handler.js change to mark transformation as code changed")
	}
	if len(filtered.Sources) != 0 {
		t.Fatalf("unexpected sources: %#v", filtered.Sources)
	}
	if len(filtered.Functions) != 1 || filtered.Functions[0].Slug != "enrich" {
		t.Fatalf("unexpected functions: %#v", filtered.Functions)
	}
	if filtered.Functions[0].HasCodeChanges {
		t.Fatal("expected cnips.fn.yaml-only change to leave function code unchanged")
	}
	if len(filtered.Apps) != 1 || filtered.Apps[0].Slug != "send-mail" {
		t.Fatalf("apps should remain available only for pipeline resolution: %#v", filtered.Apps)
	}
}

func TestAppsAreIgnoredByComparableFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "apps/send-mail/component.yaml", "kind: Component\n")
	writeTestFile(t, root, "apps/send-mail/handler.js", "export default 1\n")
	writeTestFile(t, root, "pipelines/orders/pipeline.yaml", "kind: Pipeline\n")

	files, err := collectComparableFiles(root)
	if err != nil {
		t.Fatalf("collectComparableFiles: %v", err)
	}
	if _, ok := files["apps/send-mail/component.yaml"]; ok {
		t.Fatalf("app component should not be tracked: %#v", files)
	}
	if _, ok := files["apps/send-mail/handler.js"]; ok {
		t.Fatalf("app source should not be tracked: %#v", files)
	}
	if _, ok := files["pipelines/orders/pipeline.yaml"]; !ok {
		t.Fatalf("pipeline should still be tracked: %#v", files)
	}
}

func TestManifestFingerprintIsStableAndContentSensitive(t *testing.T) {
	left := map[string]manifestFile{
		"b.yaml": {Digest: "2"},
		"a.yaml": {Digest: "1"},
	}
	right := map[string]manifestFile{
		"a.yaml": {Digest: "1"},
		"b.yaml": {Digest: "2"},
	}
	if manifestFingerprint(left) != manifestFingerprint(right) {
		t.Fatal("expected fingerprint to be independent of map iteration order")
	}
	right["b.yaml"] = manifestFile{Digest: "3"}
	if manifestFingerprint(left) == manifestFingerprint(right) {
		t.Fatal("expected fingerprint to change when file digest changes")
	}
}

func TestLocalPullManifestPersistsHeads(t *testing.T) {
	root := t.TempDir()
	manifest := &pullManifest{
		BaseHead:   "sha256:base",
		LocalHead:  "sha256:local",
		RemoteHead: "sha256:remote",
		Files: map[string]manifestFile{
			"pipelines/orders/pipeline.yaml": {Digest: "abc"},
		},
	}
	if err := writeLocalPullManifest(root, "workspace/a", manifest); err != nil {
		t.Fatalf("writeLocalPullManifest: %v", err)
	}
	got, err := readLocalPullManifest(root, "workspace/a")
	if err != nil {
		t.Fatalf("readLocalPullManifest: %v", err)
	}
	if got.BaseHead != manifest.BaseHead || got.LocalHead != manifest.LocalHead || got.RemoteHead != manifest.RemoteHead {
		t.Fatalf("heads not preserved: %#v", got)
	}
}

func TestLocalPullManifestDropsLegacyAppEntries(t *testing.T) {
	root := t.TempDir()
	manifest := &pullManifest{
		BaseHead:   "sha256:base",
		LocalHead:  "sha256:local",
		RemoteHead: "sha256:remote",
		Files: map[string]manifestFile{
			"apps/send-mail/component.yaml":      {Digest: "app"},
			"pipelines/orders/pipeline.yaml":     {Digest: "pipe"},
			"transformations/normalize/main.go":  {Digest: "tx"},
			"transformations/normalize/go.mod":   {Digest: "mod"},
			"transformations/normalize/go.sum":   {Digest: "sum"},
			"transformations/normalize/notes.md": {Digest: "ignored"},
		},
	}
	if err := writeLocalPullManifest(root, "workspace/a", manifest); err != nil {
		t.Fatalf("writeLocalPullManifest: %v", err)
	}
	got, err := readLocalPullManifest(root, "workspace/a")
	if err != nil {
		t.Fatalf("readLocalPullManifest: %v", err)
	}
	if _, ok := got.Files["apps/send-mail/component.yaml"]; ok {
		t.Fatalf("legacy app entry should be dropped: %#v", got.Files)
	}
	if got.BaseHead == "sha256:base" || got.LocalHead == "sha256:local" || got.RemoteHead == "sha256:remote" {
		t.Fatalf("heads should be recalculated after dropping app entries: %#v", got)
	}
}

func writeTestFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
