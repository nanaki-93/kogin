package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project/securityrules"
	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

const (
	SecurityPromptVersion               = "security-file-v4"
	SecurityMaxSourceBytes              = 64 * 1024
	maxSecurityOutputBytes              = 64 * 1024
	maxSecurityFindings                 = securityrules.MaxSecurityFindings
	maxSecurityTextBytes                = 2048
	maxSecurityReasonBytes              = 1024
	minSecurityEchoWordCharacters       = 4
	minSecurityEchoSingleWordCharacters = 8

	SecurityStatusNotRun         = "not_run"
	SecurityStatusRunning        = "running"
	SecurityStatusCompleted      = "completed"
	SecurityStatusCompletedEmpty = "completed_empty"
	SecurityStatusPartial        = "partial"
	SecurityStatusStale          = "stale"
	SecurityStatusFailed         = "failed"
	SecurityStatusCanceled       = "canceled"

	SecurityTriageOpen      = securityrules.SecurityTriageOpen
	SecurityTriageDismissed = "dismissed"
	SecurityTriageAccepted  = "accepted"

	SecurityVerificationUnverified      = securityrules.SecurityVerificationUnverified
	SecurityVerificationVerified        = "verified"
	SecurityVerificationNotReproducible = "not_reproducible"

	SecuritySourceDeterministic = "deterministic"
	SecuritySourceAI            = FindingSourceAI
)

const securityReportSchemaVersion = "1"

var (
	securityKeyValueSecret = regexp.MustCompile(`(?i)(^|[^a-z0-9_])["']?(?:[a-z0-9]+[._-])*(?:secret[_-]?access[_-]?key|access[_-]?key(?:[_-]?id)?|api[_-]?(?:key|token)|access[_-]?token|auth[_-]?token|client[_-]?secret|private[_-]?key|credentials?|password|passwd|secret|token|pwd)["']?\s*[:=]\s*(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,;}"']+)`)
	securityAuthorization  = regexp.MustCompile(`(?im)(^|[^a-z0-9_])["']?(?:proxy[-_])?authorization["']?\s*[:=]\s*(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\r\n;}"']*)`)
	securityCWE            = regexp.MustCompile(`^CWE-[1-9][0-9]*$`)
)

type SecuritySourceAnchor = securityrules.SecuritySourceAnchor
type SecurityFinding = securityrules.SecurityFinding

// SecurityFileReport is owned by either deterministic rules or an AI review.
// The source-specific provenance makes those evidence types unambiguous.
type SecurityFileReport struct {
	SchemaVersion           string            `json:"schema_version"`
	ProjectID               string            `json:"project_id"`
	ProjectRevision         string            `json:"project_revision"`
	Path                    string            `json:"path"`
	ContentHash             string            `json:"content_hash"`
	Status                  string            `json:"status"`
	Source                  string            `json:"source"`
	RuleSetVersion          string            `json:"rule_set_version,omitempty"`
	Findings                []SecurityFinding `json:"findings"`
	Reason                  string            `json:"reason,omitempty"`
	Model                   string            `json:"model,omitempty"`
	ConfiguredModel         string            `json:"configured_model,omitempty"`
	Profile                 string            `json:"profile,omitempty"`
	Scope                   string            `json:"scope,omitempty"`
	ProviderOrigin          string            `json:"provider_origin,omitempty"`
	ReasoningEffort         string            `json:"reasoning_effort,omitempty"`
	PromptVersion           string            `json:"prompt_version,omitempty"`
	ContextPolicyVersion    string            `json:"context_policy_version"`
	InstructionsFingerprint string            `json:"instructions_fingerprint,omitempty"`
	GeneratedAt             time.Time         `json:"generated_at"`
}

type SecurityReportInput struct {
	ProjectID, ProjectRevision, Path, ContentHash, Source, RuleSetVersion string
	Model, ConfiguredModel, Profile, Scope, ProviderOrigin                string
	ReasoningEffort, PromptVersion, ContextPolicyVersion                  string
}

// SecurityFindingID excludes model wording, insights, CWE, and references.
func SecurityFindingID(f SecurityFinding) string {
	return securityrules.SecurityFindingID(f)
}

type SecurityReportCache struct {
	root string
}

var securityCacheMu sync.Mutex

func NewSecurityReportCache(root string) (*SecurityReportCache, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	return &SecurityReportCache{root: canonical}, nil
}

// Store requires current indexed facts. It only redacts prose/provenance, and
// otherwise rejects malformed states, IDs, fields, anchors, and identities.
func (c *SecurityReportCache) Store(report SecurityFileReport, indexed IndexFile) error {
	return c.store(report, indexed, nil)
}

// StoreAuthorized validates and serializes a report under the shared cache
// lock. Authorization runs after the temporary file is durable and immediately
// before its atomic rename, so rejected results never become visible.
func (c *SecurityReportCache) StoreAuthorized(report SecurityFileReport, indexed IndexFile, authorize func() error) error {
	if authorize == nil {
		return c.Store(report, indexed)
	}
	return c.store(report, indexed, authorize)
}

func (c *SecurityReportCache) store(report SecurityFileReport, indexed IndexFile, authorize func() error) error {
	securityCacheMu.Lock()
	defer securityCacheMu.Unlock()
	report = cloneSecurityFileReportValue(report)
	redactSecurityReport(&report)
	if !validStoredSecurityReport(report, &indexed) {
		return fmt.Errorf("security report is invalid")
	}
	path, err := c.resolveCachePath(report.Path, report.Source)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode security report: %w", err)
	}
	if authorize == nil {
		return storage.WriteFile(path, data, 0600)
	}
	return storage.WriteFileAuthorized(path, data, 0600, authorize)
}
func (c *SecurityReportCache) Load(input SecurityReportInput) (*SecurityFileReport, error) {
	securityCacheMu.Lock()
	defer securityCacheMu.Unlock()
	redactSecurityInput(&input)
	if err := validSecurityInput(input); err != nil {
		return nil, err
	}
	path, err := c.resolveCachePath(input.Path, input.Source)
	if err != nil {
		return nil, fmt.Errorf("read security cache: %w", err)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read security cache: %w", err)
	}
	var report SecurityFileReport
	if err := decodeStoredSecurityReport(data, &report); err != nil || !validStoredSecurityReport(report, nil) {
		if recoverErr := storage.RecoverCorrupt(path); recoverErr != nil {
			return nil, recoverErr
		}
		return nil, nil
	}
	current, err := c.reportCurrent(report, input)
	if err != nil {
		return nil, err
	}
	if !current {
		report.Status = SecurityStatusStale
	}
	return cloneSecurityFileReport(&report), nil
}

// LoadStored reads a file's stored report from one source as it was saved,
// whatever model or rules wrote it; nil when there is none or it is unreadable.
func (c *SecurityReportCache) LoadStored(path, source string) (*SecurityFileReport, error) {
	securityCacheMu.Lock()
	defer securityCacheMu.Unlock()
	file, err := c.resolveCachePath(path, source)
	if err != nil {
		return nil, fmt.Errorf("read security cache: %w", err)
	}
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read security cache: %w", err)
	}
	var report SecurityFileReport
	if decodeStoredSecurityReport(data, &report) != nil || !validStoredSecurityReport(report, nil) || report.Path != path || report.Source != source {
		return nil, nil
	}
	return cloneSecurityFileReport(&report), nil
}

func (c *SecurityReportCache) Clear() error {
	securityCacheMu.Lock()
	defer securityCacheMu.Unlock()
	directory, err := storage.MetadataPath(c.root, ".kasuri", "security", "files")
	if err != nil {
		return fmt.Errorf("clear security cache: %w", err)
	}
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("clear security cache: %w", err)
	}
	return nil
}

func (c *SecurityReportCache) resolveCachePath(path, source string) (string, error) {
	key := sha256.Sum256([]byte(source + "\x00" + path))
	return storage.MetadataPath(c.root, ".kasuri", "security", "files", hex.EncodeToString(key[:])+".json")
}

func (c *SecurityReportCache) reportCurrent(report SecurityFileReport, input SecurityReportInput) (bool, error) {
	if !securityReportMatchesInput(report, input) {
		return false, nil
	}
	policy, err := NewContextPolicy(c.root)
	if err != nil {
		return false, nil
	}
	if report.Source == SecuritySourceAI {
		if err := validateInstructionsWithPolicy(c.root, report.Path, report.InstructionsFingerprint, policy); err != nil {
			if !errors.Is(err, ErrRevisionConflict) {
				return false, err
			}
			return false, nil
		}
	}
	if policy.Version() != report.ContextPolicyVersion || !policy.Decide(report.Path).Include {
		return false, nil
	}
	current, err := GetFileInfo(c.root, report.Path)
	return err == nil && current.SizeBytes <= SecurityMaxSourceBytes && current.ContentHash == report.ContentHash, nil
}

func LoadSecurityFileReport(root string, input SecurityReportInput) (*SecurityFileReport, error) {
	cache, err := NewSecurityReportCache(root)
	if err != nil {
		return nil, err
	}
	return cache.Load(input)
}

func decodeStoredSecurityReport(data []byte, report *SecurityFileReport) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(report); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("security report must contain one object")
	}
	return nil
}

func cloneSecurityFileReportValue(source SecurityFileReport) SecurityFileReport {
	return *cloneSecurityFileReport(&source)
}
func cloneSecurityFileReport(source *SecurityFileReport) *SecurityFileReport {
	if source == nil {
		return nil
	}
	clone := *source
	if source.Findings != nil {
		clone.Findings = make([]SecurityFinding, len(source.Findings))
		copy(clone.Findings, source.Findings)
	}
	for index := range clone.Findings {
		clone.Findings[index].EngineeringInsight = CloneEngineeringInsight(source.Findings[index].EngineeringInsight)
	}
	return &clone
}
