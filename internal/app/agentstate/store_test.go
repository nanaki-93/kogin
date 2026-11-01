package agentstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
)

func testStore(t *testing.T) (*Store, Key) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := Key{"app", "user", "session"}
	if _, err := s.CreateSession(t.Context(), key, State{}); err != nil {
		t.Fatal(err)
	}
	return s, key
}

func TestJournalNamespacesIsolationAndWriteRollback(t *testing.T) {
	s, key := testStore(t)
	if _, err := s.AppendEvent(t.Context(), key, Event{ID: "first", Data: json.RawMessage(`{"id":"first"}`)}, State{"app:shared": json.RawMessage(`1`), "user:shared": json.RawMessage(`2`), "private": json.RawMessage(`3`), "temp:secret": json.RawMessage(`4`)}); err != nil {
		t.Fatal(err)
	}
	other := Key{"app", "another", "session"}
	created, err := s.CreateSession(t.Context(), other, State{})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.State) != 1 || string(created.State["app:shared"]) != "1" {
		t.Fatalf("namespace leaked: %+v", created.State)
	}
	s.write = func(string, []byte, os.FileMode, func() error) error { return errors.New("fixture failure") }
	if _, err := s.AppendEvent(t.Context(), key, Event{ID: "lost", Data: json.RawMessage(`{}`)}, nil); !errors.Is(err, ErrPersistence) {
		t.Fatal(err)
	}
	read, err := s.Session(t.Context(), key)
	if err != nil || len(read.Events) != 1 {
		t.Fatalf("failed write became visible: %+v %v", read, err)
	}
	if _, ok := read.State["temp:secret"]; ok {
		t.Fatal("temporary state persisted")
	}
}

func TestJournalRejectsCorruptAndForeignCheckoutRecords(t *testing.T) {
	for _, kind := range []string{"version", "trailing", "duplicate", "foreign", "permission", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "permission" && runtime.GOOS == "windows" {
				t.Skip("Unix permission bits do not describe Windows ACLs")
			}
			s, _ := testStore(t)
			path, err := s.path("journal.json")
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "version":
				data = []byte(strings.Replace(string(data), fmt.Sprintf(`"version":%d`, journalVersion), `"version":99`, 1))
			case "trailing":
				data = append(data, []byte(` {}`)...)
			case "duplicate":
				data = append([]byte(`{"version":1,`), data[1:]...)
			case "foreign":
				data = []byte(strings.Replace(string(data), s.workspace, strings.Repeat("0", 64), 1))
			case "oversize":
				data = make([]byte, maxJournalBytes+1)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "permission" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Open(s.root); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("accepted %s: %v", kind, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(data) {
				t.Fatal("corrupt record was overwritten")
			}
		})
	}
}

func TestJournalTraversalSymlinkAndPassiveOpen(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".kasuri")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("opening journal created files")
	}
	for _, id := range []string{"../escape", "/absolute", "..", "a\\b", "a\x00b"} {
		if _, err := s.CreateSession(t.Context(), Key{"app", "user", id}, nil); err == nil {
			t.Fatalf("accepted identity %q", id)
		}
	}
	if err := os.Mkdir(filepath.Join(root, ".kasuri"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".kasuri", "agents")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession(t.Context(), Key{"app", "user", "session"}, nil); err == nil {
		t.Fatal("symlink metadata accepted")
	}
}

func TestJournalConcurrentStoresAndCanceledLock(t *testing.T) {
	s, key := testStore(t)
	other, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			store := s
			if i%2 == 0 {
				store = other
			}
			_, err := store.AppendEvent(t.Context(), key, Event{ID: fmt.Sprintf("event-%d", i), Data: json.RawMessage(`{}`)}, nil)
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	record, err := s.Session(t.Context(), key)
	if err != nil || len(record.Events) != 24 {
		t.Fatalf("lost concurrent appends: %+v %v", record, err)
	}
	lock, err := s.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := other.CreateSession(ctx, Key{"app", "user", "blocked"}, nil); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrPersistence) {
		t.Fatalf("lock ignored deadline: %v", err)
	}
}

func TestJournalCanceledCommitIsNotAnIOFailure(t *testing.T) {
	for _, artifact := range []bool{false, true} {
		s, key := testStore(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s.write = func(_ string, _ []byte, _ os.FileMode, authorize func() error) error {
			cancel()
			return authorize()
		}
		var err error
		if artifact {
			_, err = s.SaveArtifact(ctx, ArtifactWrite{Key: ArtifactKey{Session: key, Name: "result"}, Kind: "text", MIME: "text/plain", Data: []byte("result")})
		} else {
			_, err = s.AppendEvent(ctx, key, Event{ID: "canceled", Data: json.RawMessage(`{}`)}, nil)
		}
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrPersistence) {
			t.Fatalf("canceled commit became a storage failure: %v", err)
		}
		if record, err := s.Session(t.Context(), key); err != nil || len(record.Events) != 0 {
			t.Fatalf("canceled journal append became visible: %+v %v", record, err)
		}
	}
	if err := persistenceOperationError(t.Context(), "lock", context.DeadlineExceeded); !errors.Is(err, ErrPersistence) {
		t.Fatal("internal lock timeout lost storage-failure classification")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	failure := errors.New("disk failure")
	if err := persistenceOperationError(ctx, "write", failure); !errors.Is(err, failure) || !errors.Is(err, ErrPersistence) {
		t.Fatal("cancellation hid an actual disk failure")
	}
}

func TestJournalSeparateProcessesKeepAllAppends(t *testing.T) {
	s, key := testStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	commands := make([]*exec.Cmd, 2)
	for i := range commands {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestJournalChildWriter$")
		command.Env = append(os.Environ(), "KASURI_TEST_JOURNAL="+s.root, fmt.Sprintf("KASURI_TEST_WRITER=%d", i))
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if command.ProcessState == nil {
				_ = command.Process.Kill()
				_ = command.Wait()
			}
		})
		commands[i] = command
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	record, err := s.Session(t.Context(), key)
	if err != nil || len(record.Events) != 20 {
		t.Fatalf("process writers lost events: %+v %v", record, err)
	}
}

func TestBudgetCheckpointRejectsStaleOrChangedAccounting(t *testing.T) {
	s, _ := testStore(t)
	limits, err := config.ResolveAgentRuntime(config.AgentRuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	record := BudgetRecord{ID: "budget", Binding: Binding{Project: "project", Revision: "revision", Policy: "policy", Configuration: "config"}, Limits: limits.Limits, Sequence: 1, Usage: BudgetUsage{ModelCalls: 1, InputTokens: 10, OutputTokens: 20}}
	if _, err := s.Checkpoint(t.Context(), record, nil); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"sequence", "binding", "accounting", "limits"} {
		changed := record
		changed.Sequence = 2
		switch kind {
		case "sequence":
			changed.Sequence = 1
		case "binding":
			changed.Binding.Revision = "replacement"
		case "accounting":
			changed.Usage.InputTokens = 9
		case "limits":
			changed.Limits.ModelCalls++
		}
		if _, err := s.Checkpoint(t.Context(), changed, nil); err == nil {
			t.Fatalf("accepted changed %s", kind)
		}
	}
	reopened, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := reopened.load()
	if err != nil || db.Budgets[record.ID] != record {
		t.Fatalf("failed checkpoint mutated budget: %+v %v", db, err)
	}
}

func TestJournalVersionsCompletionInvestigationAndGraphRunsExplicitly(t *testing.T) {
	s, key := testStore(t)
	limits, err := config.ResolveAgentRuntime(config.AgentRuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	budget := BudgetRecord{ID: "budget", Binding: Binding{Project: "project", Revision: "revision", Policy: "policy", Configuration: "config"}, Limits: limits.Limits, Sequence: 1}
	if _, err := s.Checkpoint(t.Context(), budget, nil); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"completion-v1", "investigation-v1", "change-graph-v1", "review-graph-v1", "repair-graph-v1", "confirmation-v1", "analysis-graph-v1", "knowledge-graph-v1", "grounding-v1", "delegation-v1", "mission-graph-v1", "multimodal-v1", "live-v1", "unknown-v1"} {
		now := time.Now().UTC()
		run := Run{ID: version, Session: key, BudgetID: budget.ID, RequestHash: strings.Repeat("a", 64), SchemaHash: strings.Repeat("b", 64), SchemaVersion: version, Status: "running", Started: now, Updated: now}
		_, err := s.BeginRun(t.Context(), run, nil)
		if version == "unknown-v1" {
			if err == nil {
				t.Fatal("unknown graph schema was accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(t.Context(), run.ID, "completed", nil); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := reopened.load()
	if err != nil || len(db.Runs) != 13 {
		t.Fatalf("history schema compatibility lost: %v", err)
	}
	for _, run := range db.Runs {
		if run.Status != "completed" {
			t.Fatalf("read changed terminal run: %+v", run)
		}
	}
}

func TestJournalChildWriter(t *testing.T) {
	root := os.Getenv("KASURI_TEST_JOURNAL")
	if root == "" {
		return
	}
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		_, err := s.AppendEvent(t.Context(), Key{"app", "user", "session"}, Event{ID: fmt.Sprintf("%s-%d", os.Getenv("KASURI_TEST_WRITER"), i), Data: json.RawMessage(`{}`)}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
}
