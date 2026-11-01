package agentstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/config"
)

func artifactFixture(key Key, name, body string) ArtifactWrite {
	return ArtifactWrite{Key: ArtifactKey{Session: key, Name: name}, Kind: "text", MIME: "text/plain", Data: []byte(body), Metadata: map[string]string{"label": "fixture"}}
}

func TestArtifactsVersionsIsolationAndExportCopies(t *testing.T) {
	s, key := testStore(t)
	request := artifactFixture(key, "evidence.txt", "original")
	first, err := s.SaveArtifact(t.Context(), request)
	if err != nil || first.Version != 1 || first.Hash != artifactHash(request.Data) || first.Size != 8 {
		t.Fatalf("first=%+v error=%v", first, err)
	}
	request.Data[0] = 'X'
	first.Data[0] = 'Y'
	first.Metadata["label"] = "changed"
	read, err := s.ExportArtifact(t.Context(), request.Key, 1)
	if err != nil || string(read.Data) != "original" || read.Metadata["label"] != "fixture" {
		t.Fatalf("caller changed stored bytes: %+v %v", read, err)
	}
	second, err := s.SaveArtifact(t.Context(), artifactFixture(key, "evidence.txt", "second"))
	if err != nil || second.Version != 2 || s.ArtifactURI(*first) == s.ArtifactURI(*second) {
		t.Fatalf("version identity=%+v %v", second, err)
	}
	other := request.Key
	other.Session.ID = "another"
	if _, err := s.ExportArtifact(t.Context(), other, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session isolation failed: %v", err)
	}
	shared := artifactFixture(key, "user:shared.txt", "shared")
	if _, err := s.SaveArtifact(t.Context(), shared); err != nil {
		t.Fatal(err)
	}
	other.Name = shared.Key.Name
	if read, err := s.ExportArtifact(t.Context(), other, 0); err != nil || string(read.Data) != "shared" {
		t.Fatalf("user namespace not shared: %v", err)
	}
	other.Session.User = "another-user"
	if _, err := s.ExportArtifact(t.Context(), other, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user namespace leaked: %v", err)
	}
	listed, err := s.ArtifactVersions(t.Context(), ArtifactKey{Session: key})
	if err != nil || len(listed) != 3 || listed[0].Data != nil {
		t.Fatalf("listing=%+v %v", listed, err)
	}
	request = artifactFixture(key, "evidence.txt", "cannot replace")
	request.Version = 1
	if _, err := s.SaveArtifact(t.Context(), request); err == nil {
		t.Fatal("immutable version was replaced")
	}
	if err := s.DeleteArtifacts(t.Context(), request.Key, ArtifactRetention{Version: 2}); err != nil {
		t.Fatal(err)
	}
	third, err := s.SaveArtifact(t.Context(), artifactFixture(key, "evidence.txt", "third"))
	if err != nil || third.Version != 3 {
		t.Fatalf("deleted version identity reused: %+v %v", third, err)
	}
	if err := s.DeleteArtifacts(t.Context(), request.Key, ArtifactRetention{}); err != nil {
		t.Fatal(err)
	}
	request.Version = 0
	fourth, err := s.SaveArtifact(t.Context(), request)
	if err != nil || fourth.Version != 4 {
		t.Fatalf("counter was lost on deletion: %+v %v", fourth, err)
	}
}

func TestArtifactRetentionProtectsRecoveryAndActiveRuns(t *testing.T) {
	s, key := testStore(t)
	request := artifactFixture(key, "report.txt", "report")
	for i := range 3 {
		request.Data = []byte(fmt.Sprint(i))
		if _, err := s.SaveArtifact(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteArtifacts(t.Context(), request.Key, ArtifactRetention{KeepLatest: 1, Before: time.Now().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	versions, err := s.ArtifactVersions(t.Context(), request.Key)
	if err != nil || len(versions) != 1 || versions[0].Version != 3 {
		t.Fatalf("retention=%+v %v", versions, err)
	}
	limits, err := config.ResolveAgentRuntime(config.AgentRuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Checkpoint(t.Context(), BudgetRecord{ID: "budget", Binding: Binding{"project", "revision", "policy", "configuration"}, Limits: limits.Limits, Sequence: 1}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := s.BeginRun(t.Context(), Run{ID: "run", Session: key, BudgetID: "budget", RequestHash: strings.Repeat("a", 64), SchemaHash: strings.Repeat("b", 64), SchemaVersion: "completion-v1", Status: "running", Started: now, Updated: now}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteArtifacts(t.Context(), request.Key, ArtifactRetention{}); !errors.Is(err, ErrArtifactInUse) {
		t.Fatalf("active evidence deleted: %v", err)
	}
	active, err := s.ArtifactInventory(t.Context())
	if err != nil || len(active) != 1 || !active[0].InUse || active[0].Data != nil {
		t.Fatalf("active metadata=%+v %v", active, err)
	}
	if err := s.DeleteArtifactVersion(t.Context(), request.Key, active[0].Version, active[0].Hash, nil); !errors.Is(err, ErrArtifactInUse) {
		t.Fatalf("exact deletion bypassed active protection: %v", err)
	}
	for _, name := range []string{"user:shared", "completion.json"} {
		reference := artifactFixture(key, name, "referenced evidence")
		reference.Key.Session.ID = "another-session"
		record, err := s.SaveArtifact(t.Context(), reference)
		if err != nil {
			t.Fatal(err)
		}
		if name == "completion.json" {
			err = s.mutate(t.Context(), func(db *database) error {
				return appendProgress(db, db.Runs["run"], "node", &NodeProgress{NodeID: "evidence", State: "recorded", Evidence: []ArtifactReference{{Session: reference.Key.Session, Name: name, Version: record.Version}}})
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := s.DeleteArtifactVersion(t.Context(), record.Key, record.Version, record.Hash, nil); !errors.Is(err, ErrArtifactInUse) {
			t.Fatalf("shared or referenced evidence deleted: %s %v", name, err)
		}
		inventory, err := s.ArtifactInventory(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range inventory {
			if item.Key == record.Key && !item.InUse {
				t.Fatalf("active reference omitted from inventory: %s", name)
			}
		}
	}
	if err := s.FinishRun(t.Context(), "run", "completed", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteArtifacts(t.Context(), request.Key, ArtifactRetention{}); err != nil {
		t.Fatal(err)
	}
	request.Protected, request.Deduplicate = true, true
	first, err := s.SaveArtifact(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SaveArtifact(t.Context(), request)
	if err != nil || first.Version != second.Version {
		t.Fatalf("identical evidence created another version: %v", err)
	}
	if err := s.DeleteArtifacts(t.Context(), request.Key, ArtifactRetention{}); !errors.Is(err, ErrArtifactInUse) {
		t.Fatalf("recovery evidence deleted: %v", err)
	}
	if err := s.DeleteArtifactVersion(t.Context(), request.Key, first.Version, first.Hash, nil); !errors.Is(err, ErrArtifactInUse) {
		t.Fatalf("exact deletion bypassed recovery protection: %v", err)
	}
}

func TestArtifactExactDeletionAndPrivateExports(t *testing.T) {
	s, key := testStore(t)
	record, err := s.SaveArtifact(t.Context(), artifactFixture(key, "report.txt", "private payload"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteArtifactVersion(t.Context(), record.Key, record.Version, strings.Repeat("0", 64), nil); !errors.Is(err, ErrArtifactConflict) {
		t.Fatalf("wrong hash deleted artifact: %v", err)
	}
	paths := map[string]bool{}
	for range 2 {
		path, err := s.ExportArtifactFile(t.Context(), record.Key, record.Version, record.Hash, nil)
		if err != nil || paths[path] || filepath.Dir(path) != filepath.Join(s.root, ".kasuri", "exports", "artifacts") {
			t.Fatalf("export path=%q error=%v", path, err)
		}
		paths[path] = true
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "private payload" {
			t.Fatalf("export content=%q %v", data, err)
		}
		info, err := os.Stat(path)
		if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("export permissions=%v %v", info, err)
		}
	}
	if err := s.DeleteArtifactVersion(t.Context(), record.Key, record.Version, record.Hash, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExportArtifactFile(t.Context(), record.Key, record.Version, record.Hash, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted artifact exported: %v", err)
	}
	for path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("deletion removed explicit exported copy: %v", err)
		}
	}
}

func TestArtifactMutationsRecheckAuthorizationAtPersistence(t *testing.T) {
	for _, action := range []string{"export", "delete"} {
		t.Run(action, func(t *testing.T) {
			s, key := testStore(t)
			record, err := s.SaveArtifact(t.Context(), artifactFixture(key, "report", "retained"))
			if err != nil {
				t.Fatal(err)
			}
			denied := errors.New("authority changed")
			current, calls := true, 0
			guard := func() error {
				calls++
				if !current {
					return denied
				}
				return nil
			}
			write := s.write
			s.write = func(path string, data []byte, mode os.FileMode, authorize func() error) error {
				current = false
				return write(path, data, mode, authorize)
			}
			if action == "export" {
				_, err = s.ExportArtifactFile(t.Context(), record.Key, record.Version, record.Hash, guard)
			} else {
				err = s.DeleteArtifactVersion(t.Context(), record.Key, record.Version, record.Hash, guard)
			}
			if !errors.Is(err, denied) || calls != 2 {
				t.Fatalf("final guard=%d %v", calls, err)
			}
			if _, err := s.ExportArtifact(t.Context(), record.Key, record.Version); err != nil {
				t.Fatal("rejected action deleted original")
			}
			files, err := filepath.Glob(filepath.Join(s.root, ".kasuri", "exports", "artifacts", "*"))
			if err != nil || len(files) != 0 {
				t.Fatalf("rejected export left files=%v %v", files, err)
			}
		})
	}
}

func TestArtifactExportRejectsSymlinksCancellationAndDiskFailure(t *testing.T) {
	s, key := testStore(t)
	record, err := s.SaveArtifact(t.Context(), artifactFixture(key, "report", "retained"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ExportArtifactFile(ctx, record.Key, record.Version, record.Hash, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled export: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(s.root, ".kasuri", "exports")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExportArtifactFile(t.Context(), record.Key, record.Version, record.Hash, nil); err == nil {
		t.Fatal("export followed symlink")
	}
	if err := os.Remove(filepath.Join(s.root, ".kasuri", "exports")); err != nil {
		t.Fatal(err)
	}
	s.write = func(string, []byte, os.FileMode, func() error) error { return errors.New("fixture disk failure") }
	if _, err := s.ExportArtifactFile(t.Context(), record.Key, record.Version, record.Hash, nil); !errors.Is(err, ErrPersistence) {
		t.Fatalf("export hid disk failure: %v", err)
	}
}

func TestArtifactsRejectCorruptionWithoutReplacingCatalog(t *testing.T) {
	for _, kind := range []string{"version", "duplicate", "unknown", "foreign", "hash", "size", "trailing", "permission"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "permission" && runtime.GOOS == "windows" {
				t.Skip("Windows access is governed by ACLs")
			}
			s, key := testStore(t)
			request := artifactFixture(key, "report.txt", "content")
			record, err := s.SaveArtifact(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			path, _ := s.path("artifacts.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "version":
				data = bytes.Replace(data, []byte(`"version":1`), []byte(`"version":9`), 1)
			case "duplicate":
				data = append([]byte(`{"version":1,`), data[1:]...)
			case "unknown":
				data = append([]byte(`{"extra":true,`), data[1:]...)
			case "foreign":
				data = bytes.ReplaceAll(data, []byte(s.workspace), []byte(strings.Repeat("0", 64)))
			case "hash":
				data = bytes.ReplaceAll(data, []byte(record.Hash), []byte(strings.Repeat("0", 64)))
			case "size":
				data = bytes.Replace(data, []byte(`"size":7`), []byte(`"size":8`), 1)
			case "trailing":
				data = append(data, []byte(` {}`)...)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "permission" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.SaveArtifact(t.Context(), request); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("accepted corrupt %s: %v", kind, err)
			}
			if read, err := os.ReadFile(path); err != nil || !bytes.Equal(read, data) {
				t.Fatal("corrupt artifact catalog was replaced")
			}
		})
	}
}

func TestArtifactBoundsSymlinksCancellationAndWriteFailure(t *testing.T) {
	s, key := testStore(t)
	for _, name := range []string{"", "..", "../escape", "a/b", "a\\b", "user:", "name\x1b"} {
		if _, err := s.SaveArtifact(t.Context(), artifactFixture(key, name, "content")); err == nil {
			t.Fatalf("accepted unsafe name %q", name)
		}
	}
	oversize := artifactFixture(key, "big", "")
	oversize.Data = make([]byte, maxArtifactBytes+1)
	if _, err := s.SaveArtifact(t.Context(), oversize); err == nil {
		t.Fatal("oversized payload accepted")
	}
	request := artifactFixture(key, "report", "original")
	if _, err := s.SaveArtifact(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	s.write = func(string, []byte, os.FileMode, func() error) error { return errors.New("fixture disk failure") }
	request.Data = []byte("not committed")
	if _, err := s.SaveArtifact(t.Context(), request); !errors.Is(err, ErrPersistence) {
		t.Fatalf("write failure hidden: %v", err)
	}
	if read, err := s.ExportArtifact(t.Context(), request.Key, 0); err != nil || string(read.Data) != "original" {
		t.Fatal("failed write replaced original")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ExportArtifact(ctx, request.Key, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	root := t.TempDir()
	other, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".kasuri"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".kasuri", "agents")); err != nil {
		t.Fatal(err)
	}
	if _, err := other.SaveArtifact(t.Context(), request); err == nil {
		t.Fatal("artifact followed a metadata symlink")
	}
}

func TestConcurrentArtifactVersionsKeepEveryPayload(t *testing.T) {
	s, key := testStore(t)
	other, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			store := s
			if i%2 == 0 {
				store = other
			}
			if _, err := store.SaveArtifact(t.Context(), artifactFixture(key, "parallel", fmt.Sprint(i))); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	records, err := s.ArtifactVersions(t.Context(), ArtifactKey{Session: key, Name: "parallel"})
	if err != nil || len(records) != 16 {
		t.Fatalf("lost versions: %d %v", len(records), err)
	}
	seen := map[string]bool{}
	for _, record := range records {
		read, err := s.ExportArtifact(t.Context(), record.Key, record.Version)
		if err != nil {
			t.Fatal(err)
		}
		seen[string(read.Data)] = true
	}
	if len(seen) != 16 {
		t.Fatal("concurrent writers overwrote content")
	}
	path, _ := s.path("artifacts.json")
	data, err := os.ReadFile(path)
	if err != nil || !json.Valid(data) {
		t.Fatal("catalog is not valid JSON")
	}
}
