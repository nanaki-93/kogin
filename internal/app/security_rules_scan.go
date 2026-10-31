package app

import (
	"context"
	"fmt"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

type securityRulesSnapshot struct {
	root          string
	analysis      project.Analysis
	file          project.IndexFile
	source        string
	policyVersion string
}

// noRulePackReason explains a file the security rule packs do not read;
// runs saved before the packs covered every language say it the old way.
const (
	noRulePackReason       = "No security rule pack covers this file type."
	legacyNoRulePackReason = "Passive security rules require a Go source file."
)

func securityRulesInput(snapshot securityRulesSnapshot) project.SecurityReportInput {
	return project.SecurityReportInput{ProjectID: snapshot.analysis.ProjectID, ProjectRevision: snapshot.analysis.ProjectRevision,
		Path: snapshot.file.Path, ContentHash: snapshot.file.ContentHash, Source: project.SecuritySourceDeterministic,
		RuleSetVersion: project.SecurityRuleSetVersion, ContextPolicyVersion: snapshot.policyVersion}
}

// executeSecurityRulesScan applies the deterministic source-only rule pack of
// the file's language to one current, policy-eligible file. It never calls a
// provider or runs a process.
func (s *Service) executeSecurityRulesScan(ctx context.Context, snapshot securityRulesSnapshot, authorizePublication func(func() error) error) (*project.SecurityFileReport, error) {
	input := securityRulesInput(snapshot)
	scan, err := project.ScanSecurityRules(snapshot.file, snapshot.source)
	if err != nil {
		return nil, err
	}
	report := project.SecurityFileReport{
		SchemaVersion: "1", ProjectID: input.ProjectID, ProjectRevision: input.ProjectRevision,
		Path: input.Path, ContentHash: input.ContentHash, Source: input.Source, RuleSetVersion: input.RuleSetVersion,
		Findings: scan.Findings, ContextPolicyVersion: input.ContextPolicyVersion, GeneratedAt: time.Now().UTC(),
	}
	if scan.Truncated {
		report.Status = project.SecurityStatusPartial
		report.Reason = "Only the five most severe rule matches are shown; review the file for additional matches."
	} else if len(scan.Findings) == 0 {
		report.Status = project.SecurityStatusCompletedEmpty
	} else {
		report.Status = project.SecurityStatusCompleted
	}
	if err := s.validateSecurityRulesSnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	cache, err := project.NewSecurityReportCache(snapshot.root)
	if err != nil {
		return nil, err
	}
	store := func() error {
		return cache.StoreAuthorized(report, snapshot.file, func() error { return s.validateSecurityRulesSnapshot(ctx, snapshot) })
	}
	if err := publishAnalysisReport(store, authorizePublication); err != nil {
		return nil, err
	}
	return &report, nil
}

func (s *Service) prepareSecurityRulesScan(path, revision string) (securityRulesSnapshot, error) {
	analysis, err := s.manager.Analysis()
	if err != nil {
		return securityRulesSnapshot{}, err
	}
	if revision == "" || revision != analysis.ProjectRevision {
		return securityRulesSnapshot{}, project.ErrRevisionConflict
	}
	file, err := s.manager.IndexedFile(path)
	if err != nil {
		return securityRulesSnapshot{}, err
	}
	if file.Binary || !project.SecurityRulesCover(file.Path) {
		return securityRulesSnapshot{}, project.ErrSecurityRulesUnavailable
	}
	root := s.manager.Root()
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return securityRulesSnapshot{}, err
	}
	if decision := policy.Decide(file.Path); !decision.Include {
		return securityRulesSnapshot{}, fmt.Errorf("%w: %s", project.ErrExcludedFile, decision.Reason)
	}
	info, err := project.GetFileInfo(root, file.Path)
	if err != nil {
		return securityRulesSnapshot{}, err
	}
	if info.Binary || info.ContentHash != file.ContentHash {
		return securityRulesSnapshot{}, project.ErrRevisionConflict
	}
	return securityRulesSnapshot{root: root, analysis: *analysis, file: *file, source: info.Content, policyVersion: policy.Version()}, nil
}

func (s *Service) validateSecurityRulesSnapshot(ctx context.Context, snapshot securityRulesSnapshot) error {
	return s.validateSourceFileSnapshot(ctx, snapshot.root, snapshot.analysis, snapshot.file, snapshot.policyVersion, true)
}

// validateSourceFileSnapshot rechecks the identity and policy facts captured
// before source-only or provider-backed file work is published.
func (s *Service) validateSourceFileSnapshot(ctx context.Context, root string, analysis project.Analysis, file project.IndexFile, policyVersion string, requireText bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.manager.Root() != root {
		return project.ErrRevisionConflict
	}
	currentAnalysis, err := s.manager.Analysis()
	if err != nil {
		return err
	}
	if currentAnalysis.ProjectID != analysis.ProjectID || currentAnalysis.ProjectRevision != analysis.ProjectRevision {
		return project.ErrRevisionConflict
	}
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return err
	}
	if policy.Version() != policyVersion || !policy.Decide(file.Path).Include {
		return project.ErrRevisionConflict
	}
	info, err := project.GetFileInfo(root, file.Path)
	if err != nil {
		return err
	}
	if info.ContentHash != file.ContentHash || requireText && info.Binary {
		return project.ErrRevisionConflict
	}
	return nil
}
