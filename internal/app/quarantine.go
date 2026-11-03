package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/logging"
	"github.com/nanaki-93/kogin/v2/internal/storage"
)

// Unreadable records must not block a checkout. An unreadable agent journal,
// task file or saved analysis progress is set aside: its bytes move next to it under a quarantined
// name, the move is recorded, and work continues without it. Records written
// by a newer Kogin are never moved; they are skipped and reported with
// the remedy. The change recovery journal is never set aside, because it may
// describe a half-finished Apply; its errors name the file and the remedy.

const (
	maxSetAsideRecords = 32
	setAsideLogPath    = ".kogin/quarantine.json"
)

// setAsideLogMu orders changes to a checkout's record of set-aside files.
var setAsideLogMu sync.Mutex

// QuarantinedFile is a record that was set aside or skipped.
type QuarantinedFile struct {
	Kind          string    `json:"kind"` // agent_journal, task or analysis_run
	File          string    `json:"file"`
	QuarantinedAs string    `json:"quarantined_as,omitempty"`
	Reason        string    `json:"reason"`
	Remedy        string    `json:"remedy"`
	At            time.Time `json:"at,omitzero"`
}

// QuarantineReport lists what was set aside and what newer files were skipped.
type QuarantineReport struct {
	Files   []QuarantinedFile `json:"files"`
	Skipped []QuarantinedFile `json:"skipped"`
}

const (
	remedySetAside = "Kogin no longer reads the set-aside file. Inspect it if you need its history, then delete it."
	remedyUpgrade  = "Upgrade Kogin to use it, or move it away."
)

// changeFileError names an unreadable or newer task file.
type changeFileError struct {
	path  string
	newer bool
	cause error
}

func (e *changeFileError) Error() string {
	if e.newer {
		return fmt.Sprintf("task file %s was written by a newer Kogin; %s", e.path, strings.ToLower(remedyUpgrade[:1])+remedyUpgrade[1:])
	}
	return fmt.Sprintf("task file %s is unreadable: %v", e.path, e.cause)
}

func (e *changeFileError) Unwrap() error { return e.cause }

func relativeMetadata(root, path string) string {
	if relative, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(relative)
	}
	return path
}

// openAgentJournal opens the checkout's agent journal, setting an unreadable
// one aside first. A journal from a newer Kogin stays an error.
func openAgentJournal(root string) (*agentstate.Store, error) {
	store, err := agentstate.Open(root)
	if err == nil || !errors.Is(err, agentstate.ErrCorrupt) || errors.Is(err, agentstate.ErrNewerJournal) {
		return store, err
	}
	record, quarantineErr := agentstate.Quarantine(context.Background(), root)
	if quarantineErr != nil {
		return nil, errors.Join(err, quarantineErr)
	}
	if record != nil {
		logging.Error("Set aside an unreadable agent journal", "file", record.File, "quarantined_as", record.QuarantinedAs, "reason", record.Reason)
	}
	return agentstate.Open(root)
}

// quarantineTask sets an unreadable task file aside and records the move.
func quarantineTask(root, id string, problem *changeFileError) error {
	from, err := changeMetadataPath(root, id+".json")
	if err != nil {
		return err
	}
	return setAside(root, "task", problem.path, from, func(stamp string) (string, error) {
		return changeMetadataPath(root, id+".quarantined-"+stamp+".json")
	}, problem.cause.Error())
}

// quarantineAnalysisRun sets unreadable saved analysis progress aside, so the
// overview and a new analysis no longer depend on it.
func quarantineAnalysisRun(root string) error {
	from, err := storage.MetadataPath(root, analysisRunRelativePath)
	if err != nil {
		return err
	}
	return setAside(root, "analysis_run", analysisRunRelativePath, from, func(stamp string) (string, error) {
		return storage.MetadataPath(root, ".kogin", "analysis", "run.quarantined-"+stamp+".json")
	}, errAnalysisRunCorrupt.Error())
}

// setAside moves an unreadable record from its path to the one target names
// for the move's time, then appends the move to the checkout's log.
func setAside(root, kind, file, from string, target func(stamp string) (string, error), reason string) error {
	now := time.Now().UTC()
	to, err := target(now.Format("20060102T150405.000000000Z"))
	if err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("set aside %s: %w", file, err)
	}
	logging.Error("Set aside an unreadable record", "kind", kind, "file", file, "quarantined_as", relativeMetadata(root, to), "reason", reason)
	setAsideLogMu.Lock()
	defer setAsideLogMu.Unlock()
	records, err := setAsideRecords(root)
	if err != nil {
		records = nil // An unreadable log of earlier moves does not block this one.
	}
	records = append(records, QuarantinedFile{Kind: kind, File: file, QuarantinedAs: relativeMetadata(root, to), Reason: reason, Remedy: remedySetAside, At: now})
	if len(records) > maxSetAsideRecords {
		records = slices.Clone(records[len(records)-maxSetAsideRecords:])
	}
	return writeSetAsideRecords(root, records)
}

func writeSetAsideRecords(root string, records []QuarantinedFile) error {
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	path, err := storage.MetadataPath(root, setAsideLogPath)
	if err != nil {
		return err
	}
	return storage.WriteFileAuthorized(path, data, 0600, nil)
}

// setAsideRecords lists the task files and analysis progress set aside in
// the checkout, oldest first. Agent journals keep their own log.
func setAsideRecords(root string) ([]QuarantinedFile, error) {
	path, err := storage.MetadataPath(root, setAsideLogPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []QuarantinedFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []QuarantinedFile
	if err := json.Unmarshal(data, &records); err != nil || len(records) > maxSetAsideRecords {
		return nil, fmt.Errorf("the record of set-aside files is unreadable")
	}
	return records, nil
}

// Quarantine reports the checkout's set-aside records and the newer ones it
// skips. Listing tasks sets unreadable task files aside first.
func (s *Service) Quarantine(ctx context.Context) (*QuarantineReport, error) {
	root := s.manager.Root()
	report := &QuarantineReport{Files: []QuarantinedFile{}, Skipped: []QuarantinedFile{}}
	if _, err := listChangeSessionsWith(root, sessionListing{lenient: true, skipped: func(problem *changeFileError) {
		report.Skipped = append(report.Skipped, QuarantinedFile{Kind: "task", File: problem.path, Reason: "written by a newer Kogin", Remedy: remedyUpgrade})
	}}); err != nil {
		return nil, err
	}
	journals, err := agentstate.Quarantines(root)
	if err != nil {
		return nil, err
	}
	for _, record := range journals {
		report.Files = appendSetAside(report.Files, root, QuarantinedFile{Kind: "agent_journal", File: record.File, QuarantinedAs: record.QuarantinedAs, Reason: record.Reason, At: record.At})
	}
	records, err := setAsideRecords(root)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		report.Files = appendSetAside(report.Files, root, record)
	}
	slices.SortFunc(report.Files, func(a, b QuarantinedFile) int { return b.At.Compare(a.At) })
	if _, err := loadAnalysisRun(root); errors.Is(err, ErrNewerAnalysisRun) {
		report.Skipped = append(report.Skipped, QuarantinedFile{Kind: "analysis_run", File: analysisRunRelativePath, Reason: "written by a newer Kogin", Remedy: remedyUpgrade})
	}
	if _, err := agentstate.Open(root); errors.Is(err, agentstate.ErrNewerJournal) {
		var problem *agentstate.JournalError
		errors.As(err, &problem)
		report.Skipped = append(report.Skipped, QuarantinedFile{Kind: "agent_journal", File: problem.Path, Reason: problem.Reason, Remedy: remedyUpgrade})
	}
	return report, ctx.Err()
}

// appendSetAside adds a set-aside record as it reads now: paths recorded
// under .kasuri or .mini-orca name their place in .kogin, the remedy is this
// version's, and a record whose file was deleted is left out.
func appendSetAside(files []QuarantinedFile, root string, record QuarantinedFile) []QuarantinedFile {
	record.File = storage.CurrentMetadataPath(record.File)
	record.QuarantinedAs = storage.CurrentMetadataPath(record.QuarantinedAs)
	record.Remedy = remedySetAside
	if path, err := storage.MetadataPath(root, record.QuarantinedAs); err == nil {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return files
		}
	}
	return append(files, record)
}
