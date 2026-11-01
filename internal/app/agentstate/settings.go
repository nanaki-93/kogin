package agentstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
)

var ErrSettingsConflict = errors.New("agent settings changed; reload before editing")

type RoleSettings struct {
	Creation string `json:"creation"`
	Testing  string `json:"testing"`
	Review   string `json:"review"`
}

type AgentPreferences struct {
	Roles  RoleSettings       `json:"roles"`
	Limits config.AgentLimits `json:"limits"`
	// RetiredDisabledSkills is read from records and requests made before
	// project skills were removed, and ignored: it is dropped when read.
	RetiredDisabledSkills []string `json:"disabled_skills,omitempty"`
}

// Replacements are complete values. In particular, an omitted repairs field
// must not silently disable repairs. The same contract applies to durable
// records and HTTP.
func (p *AgentPreferences) UnmarshalJSON(data []byte) error {
	type plain AgentPreferences
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if !validJSON(data) || decoder.Decode(&value) != nil {
		return fmt.Errorf("invalid agent preferences object")
	}
	var fields struct {
		Limits map[string]json.RawMessage `json:"limits"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"model_calls", "tool_calls", "input_tokens", "output_tokens", "elapsed_seconds", "concurrent_calls", "repairs"} {
		raw, exists := fields.Limits[name]
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("agent preferences require every budget value")
		}
	}
	value.RetiredDisabledSkills = nil
	if err := AgentPreferences(value).Validate(); err != nil {
		return err
	}
	*p = AgentPreferences(value)
	return nil
}

// A reset keeps its version to prevent an old editor from winning an ABA race.
// Nil Values explicitly follows the operator's current configuration.
type SettingsRecord struct {
	Version       int64             `json:"version"`
	Configuration string            `json:"configuration"`
	Values        *AgentPreferences `json:"values"`
}

func (p AgentPreferences) Validate() error {
	for _, role := range []string{p.Roles.Creation, p.Roles.Testing, p.Roles.Review} {
		if !validRoleModel(role) {
			return fmt.Errorf("roles must reference analyze, bug or code, or a catalog model ID")
		}
	}
	return p.Limits.Validate()
}

// validRoleModel accepts a configured scope or the shape of a catalog model
// ID (configured:, agy: or pi:); the service checks the ID is available.
func validRoleModel(role string) bool {
	if slices.Contains([]string{"analyze", "bug", "code"}, role) {
		return true
	}
	if len(role) > 256 || strings.ContainsFunc(role, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return false
	}
	for _, prefix := range []string{"configured:", "agy:", "pi:"} {
		if id, ok := strings.CutPrefix(role, prefix); ok && id != "" {
			return true
		}
	}
	return false
}

func validateSettings(record *SettingsRecord) error {
	if record == nil {
		return nil // Journals written before settings existed remain readable.
	}
	if record.Version < 1 || record.Version > 1<<53 || !validHash(record.Configuration) {
		return ErrCorrupt
	}
	if record.Values != nil && record.Values.Validate() != nil {
		return ErrCorrupt
	}
	return nil
}

func cloneSettings(record SettingsRecord) SettingsRecord {
	if record.Values != nil {
		copy := *record.Values
		record.Values = &copy
	}
	return record
}

// Settings is passive: a missing record is version zero and creates no files.
func (s *Store) Settings(ctx context.Context) (SettingsRecord, error) {
	if err := context.Cause(ctx); err != nil {
		return SettingsRecord{}, err
	}
	db, err := s.load()
	if err != nil {
		return SettingsRecord{}, err
	}
	if db.Settings == nil {
		return SettingsRecord{}, nil
	}
	return cloneSettings(*db.Settings), context.Cause(ctx)
}

func (s *Store) SaveSettings(ctx context.Context, expected int64, configuration string, values *AgentPreferences) (SettingsRecord, error) {
	next := cloneSettings(SettingsRecord{Version: expected + 1, Configuration: configuration, Values: values})
	if expected < 0 || expected >= 1<<53 || validateSettings(&next) != nil {
		return SettingsRecord{}, fmt.Errorf("invalid agent settings")
	}
	err := s.mutate(ctx, func(db *database) error {
		version := int64(0)
		if db.Settings != nil {
			version = db.Settings.Version
		}
		if version != expected {
			return ErrSettingsConflict
		}
		db.Settings = &next
		return nil
	})
	if err != nil {
		return SettingsRecord{}, err
	}
	return cloneSettings(next), nil
}
