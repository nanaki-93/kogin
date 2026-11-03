package app

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strconv"
)

// Compatibility decisions follow strict JSON validation. A duplicate header
// must never hide corrupt data behind a legacy-schema exception.
func decodeAgentHistory(data []byte, value any) error {
	if !jsontext.Value(data).IsValid() {
		return fmt.Errorf("history must contain one unambiguous JSON value")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

var errNewerTask = errors.New("task file was written by a newer Kogin")

func decodeCompatibleChangeHistory(data []byte, id string) (*ChangeSession, error) {
	if !jsontext.Value(data).IsValid() {
		return nil, fmt.Errorf("decode change history: history must contain one unambiguous JSON value")
	}
	data, err := dropRetiredChangeFields(data)
	if err != nil {
		return nil, fmt.Errorf("decode change history: %w", err)
	}
	var session ChangeSession
	if err := decodeAgentHistory(data, &session); err != nil {
		return nil, fmt.Errorf("decode change history: %w", err)
	}
	if session.SchemaVersion > 1 {
		return nil, errNewerTask
	}
	if session.SchemaVersion != 1 || session.ID != id || len(session.Targets) == 0 || len(session.Targets) > maxStoredTaskPaths || len(session.Messages) > maxChangeMessages {
		return nil, fmt.Errorf("unsupported or corrupt change history")
	}
	if err := validateStoredChange(session); err != nil {
		return nil, err
	}
	return &session, nil
}

// Older task files may carry fields that are no longer written: scope
// suggestions, and the workflow's probe summary and harness lessons. They are
// dropped on read, so such a task still loads and its next save omits them.
var (
	retiredChangeKeys   = []string{"scope_suggestions"}
	retiredWorkflowKeys = []string{"probe_summary", "harness_lessons"}
)

func dropRetiredChangeFields(data []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	dropped := dropKeys(fields, retiredChangeKeys)
	if raw := fields["workflow"]; len(raw) > 0 && string(raw) != "null" {
		var workflow map[string]json.RawMessage
		if err := json.Unmarshal(raw, &workflow); err != nil {
			return nil, err
		}
		if dropKeys(workflow, retiredWorkflowKeys) {
			encoded, err := json.Marshal(workflow)
			if err != nil {
				return nil, err
			}
			fields["workflow"], dropped = encoded, true
		}
	}
	if !dropped {
		return data, nil
	}
	return json.Marshal(fields)
}

func dropKeys(fields map[string]json.RawMessage, keys []string) bool {
	dropped := false
	for _, key := range keys {
		if _, found := fields[key]; found {
			delete(fields, key)
			dropped = true
		}
	}
	return dropped
}

func decodeCompatibleAnalysisHistory(data []byte) (*AnalysisRun, error) {
	if !jsontext.Value(data).IsValid() {
		return nil, errAnalysisRunCorrupt
	}
	// Retired single-stage jobs are archived bytes, never restored authority.
	// Keep this deliberate behavior without fabricating a unified run or graph.
	var header struct {
		SchemaVersion string `json:"schema_version"`
		Plan          struct {
			RetiredStage string `json:"compatibility_stage"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, errAnalysisRunCorrupt
	}
	if newerSchema(header.SchemaVersion, AnalysisRunSchemaVersion) {
		return nil, ErrNewerAnalysisRun
	}
	if header.SchemaVersion == AnalysisRunSchemaVersion && (header.Plan.RetiredStage == "semantic" || header.Plan.RetiredStage == "performance") {
		return nil, nil
	}
	var run AnalysisRun
	if decodeAgentHistory(data, &run) != nil || validateStoredAnalysisRun(&run) != nil {
		return nil, errAnalysisRunCorrupt
	}
	// Original schema-1 totals included ineligible stages. Validate the stored
	// aggregate before projecting today's eligible coverage; retain provenance.
	normalizeAnalysisRunEligibility(&run)
	if run.Reason == legacyAnalysisRunStaleReason {
		run.Reason = analysisRunStaleReason
	}
	return &run, nil
}

// newerSchema reports whether a stored decimal schema version is above the
// current one; anything else is for the caller's own validation.
func newerSchema(stored, current string) bool {
	version, err := strconv.Atoi(stored)
	supported, currentErr := strconv.Atoi(current)
	return err == nil && currentErr == nil && version > supported
}
