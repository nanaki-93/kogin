package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/storage"
)

// ErrQuarantineNotListed is returned for a set-aside file the checkout does
// not list (any more).
var ErrQuarantineNotListed = errors.New("this set-aside file is not listed")

// QuarantineDeleteRequest names a set-aside file of the selected checkout, as
// the report lists it, to delete.
type QuarantineDeleteRequest struct {
	ProjectID       string `json:"project_id"`
	ProjectRevision string `json:"project_revision"`
	QuarantinedAs   string `json:"quarantined_as"`
}

// setAsideDirs is where each kind of record in .kasuri/quarantine.json is set
// aside; agent journals keep their own record.
var setAsideDirs = map[string]string{"task": ".kasuri/changes", "analysis_run": ".kasuri/analysis"}

// DeleteQuarantined deletes a file Kasuri set aside, and its record, and
// returns the report without it. It is the remedy for a record Kasuri no
// longer reads; skipped records of a newer Kasuri are never deleted.
func (s *Service) DeleteQuarantined(ctx context.Context, request QuarantineDeleteRequest) (*QuarantineReport, error) {
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	if request.ProjectID != index.ProjectID || request.ProjectRevision != index.ProjectRevision {
		return nil, project.ErrRevisionConflict
	}
	root, target := s.manager.Root(), strings.TrimSpace(request.QuarantinedAs)
	if strings.HasPrefix(target, ".kasuri/agents/") {
		err = agentstate.DeleteQuarantined(ctx, root, target)
		if errors.Is(err, agentstate.ErrNotSetAside) {
			err = fmt.Errorf("%w: %s", ErrQuarantineNotListed, target)
		}
	} else {
		err = deleteSetAside(root, target)
	}
	if err != nil {
		return nil, err
	}
	return s.Quarantine(ctx)
}

// deleteSetAside deletes a task file or analysis progress listed in the
// checkout's record of set-aside files, then drops its record. A file that
// is already gone only loses its record.
func deleteSetAside(root, target string) error {
	setAsideLogMu.Lock()
	defer setAsideLogMu.Unlock()
	records, err := setAsideRecords(root)
	if err != nil {
		return err
	}
	index := slices.IndexFunc(records, func(record QuarantinedFile) bool {
		return storage.CurrentMetadataPath(record.QuarantinedAs) == target
	})
	if index < 0 || path.Dir(target) != setAsideDirs[records[index].Kind] || !setAsideName(path.Base(target)) {
		return fmt.Errorf("%w: %s", ErrQuarantineNotListed, target)
	}
	file, err := storage.MetadataPath(root, target)
	if err != nil {
		return err
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete %s: %w", target, err)
	}
	if err := writeSetAsideRecords(root, slices.Delete(records, index, index+1)); err != nil {
		return fmt.Errorf("deleted %s, but its record could not be updated: %w", target, err)
	}
	return nil
}

// setAsideName tells a set-aside file's name: <name>.quarantined-<UTC time>.json.
func setAsideName(name string) bool {
	_, stamp, ok := strings.Cut(name, ".quarantined-")
	return ok && stamp != ".json" && strings.HasSuffix(stamp, ".json")
}
