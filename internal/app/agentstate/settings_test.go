package agentstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

func settingsPreferences(t *testing.T) *AgentPreferences {
	t.Helper()
	runtime, err := config.ResolveAgentRuntime(config.AgentRuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return &AgentPreferences{Roles: RoleSettings{"bug", "code", "analyze"}, Limits: runtime.Limits}
}

func TestSettingsPassiveReadCASResetAndRestart(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Settings(t.Context())
	if err != nil || before.Version != 0 || before.Values != nil {
		t.Fatal(before, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".kogin")); !os.IsNotExist(err) {
		t.Fatal("passive settings read created metadata", err)
	}
	values, hash := settingsPreferences(t), strings.Repeat("a", 64)
	other, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for _, writer := range []*Store{store, other} {
		wg.Go(func() { _, err := writer.SaveSettings(t.Context(), 0, hash, values); errorsSeen <- err })
	}
	wg.Wait()
	close(errorsSeen)
	wins, conflicts := 0, 0
	for err := range errorsSeen {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrSettingsConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal("lost update", wins, conflicts)
	}
	values.Roles.Creation = "code"
	restarted, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	read, err := restarted.Settings(t.Context())
	if err != nil || read.Version != 1 || read.Values.Roles.Creation != "bug" {
		t.Fatal("restart or caller mutation changed settings", read, err)
	}
	reset, err := restarted.SaveSettings(t.Context(), 1, hash, nil)
	if err != nil || reset.Version != 2 || reset.Values != nil {
		t.Fatal(reset, err)
	}
	if _, err := store.SaveSettings(t.Context(), 0, hash, values); !errors.Is(err, ErrSettingsConflict) {
		t.Fatal("reset allowed ABA replacement", err)
	}
}

func TestSettingsFailedOrCanceledWritesPreserveRecord(t *testing.T) {
	store, _ := testStore(t)
	values, hash := settingsPreferences(t), strings.Repeat("b", 64)
	if _, err := store.SaveSettings(t.Context(), 0, hash, values); err != nil {
		t.Fatal(err)
	}
	path, err := store.path("journal.json")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store.write = func(string, []byte, os.FileMode, func() error) error { return errors.New("fixture disk failure") }
	if _, err := store.SaveSettings(t.Context(), 1, hash, nil); !errors.Is(err, ErrPersistence) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.SaveSettings(ctx, 1, hash, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed mutation changed durable settings", err)
	}
}

func TestSettingsCorruptOverridesNeverBecomeDefaults(t *testing.T) {
	for _, alter := range []func(*SettingsRecord){
		func(r *SettingsRecord) { r.Version = 0 },
		func(r *SettingsRecord) { r.Values.Roles.Creation = "arbitrary-provider" },
		func(r *SettingsRecord) { r.Values.Limits.ModelCalls = 0 },
	} {
		store, _ := testStore(t)
		if _, err := store.SaveSettings(t.Context(), 0, strings.Repeat("c", 64), settingsPreferences(t)); err != nil {
			t.Fatal(err)
		}
		db, err := store.load()
		if err != nil {
			t.Fatal(err)
		}
		alter(db.Settings)
		data, err := json.Marshal(db)
		if err != nil {
			t.Fatal(err)
		}
		path, err := store.path("journal.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Settings(t.Context()); !errors.Is(err, ErrCorrupt) {
			t.Fatal("corrupt override became a default", err)
		}
		if _, err := store.SaveSettings(t.Context(), 1, strings.Repeat("c", 64), nil); !errors.Is(err, ErrCorrupt) {
			t.Fatal("reset overwrote corrupt data", err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, data) {
			t.Fatal("corrupt settings were not preserved", err)
		}
	}
}

func TestSettingsSavedWithSkillSwitchesStillRead(t *testing.T) {
	values := settingsPreferences(t)
	data, err := json.Marshal(values)
	if err != nil || strings.Contains(string(data), "disabled_skills") {
		t.Fatalf("new settings still write skills: %s %v", data, err)
	}
	old := strings.Replace(string(data), `{"roles"`, `{"disabled_skills":["project-skills/review-code"],"roles"`, 1)
	var read AgentPreferences
	if err := json.Unmarshal([]byte(old), &read); err != nil || read.RetiredDisabledSkills != nil || read.Roles != values.Roles {
		t.Fatalf("settings saved with skill switches = %+v, %v", read, err)
	}
}
