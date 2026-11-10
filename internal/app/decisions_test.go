package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const decisionRecord = `# 2. Decisions live as Markdown in the repository

Status: Accepted
Date: 2026-11-10

## Context

The plan gate needs somewhere to keep **why**.

## Options

1. Markdown files in the repository.
2. Rows in .kogin/kogin.db.

## Decision

Markdown, under docs/decisions.

## Revisit when

Records need to be queried across projects.
`

func TestADecisionRecordReadsItsTitleStatusAndSections(t *testing.T) {
	decision := parseDecision("docs/decisions/0002-decisions-as-markdown.md", decisionRecord)
	if decision.Number != 2 || decision.Title != "Decisions live as Markdown in the repository" || decision.Status != "accepted" || decision.Date != "2026-11-10" {
		t.Fatalf("decision = %+v", decision)
	}
	if len(decision.Sections) != 4 || decision.Sections[1].Heading != "Options" || !strings.HasPrefix(decision.Sections[1].Text, "1. Markdown files") || decision.Sections[3].Text != "Records need to be queried across projects." {
		t.Fatalf("sections = %+v", decision.Sections)
	}
	// The other common shape: an "ADR-7:" title and status as its own section.
	other := parseDecision("adr/0007-queue.md", "# ADR-7: Use a queue\n\n## Status\n\nSuperseded by 0009\n\n## Decision\n\nA queue.\n")
	if other.Number != 7 || other.Title != "Use a queue" || other.Status != "superseded by 0009" || len(other.Sections) != 1 {
		t.Fatalf("other = %+v", other)
	}
	// A file without a heading is named after itself.
	if bare := parseDecision("docs/decisions/0003-untitled.md", "Just text.\n"); bare.Title != "0003-untitled" || bare.Status != "" {
		t.Fatalf("bare = %+v", bare)
	}
}

func TestTheDecisionLogIsReadFromTheProjectAndToldToTheInvestigation(t *testing.T) {
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	ctx := context.Background()
	// Without records, the log is empty and says where it would be.
	empty, err := service.Decisions(ctx)
	if err != nil || len(empty.Decisions) != 0 || empty.Dir != "docs/decisions" || service.decisionsBrief(ctx) != "" {
		t.Fatalf("empty = %+v, %v", empty, err)
	}
	dir := filepath.Join(root, "docs", "decisions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"0001-old-choice.md":            "# 1. Old choice\n\nStatus: superseded by 0002\n\n## Decision\n\nGone.\n",
		"0002-decisions-as-markdown.md": decisionRecord,
		"README.md":                     "# Decisions\n\nHow to write one.\n",
		"notes.md":                      "# Not a record\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.manager.Reindex(); err != nil {
		t.Fatal(err)
	}
	report, err := service.Decisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Only numbered records, newest first.
	if len(report.Decisions) != 2 || report.Decisions[0].Number != 2 || report.Decisions[1].Title != "Old choice" || report.Dir != "docs/decisions" {
		t.Fatalf("report = %+v", report)
	}
	brief := service.decisionsBrief(ctx)
	if !strings.Contains(brief, "- 0002 Decisions live as Markdown in the repository (accepted): docs/decisions/0002-decisions-as-markdown.md") || strings.Contains(brief, "Old choice") {
		t.Fatalf("brief = %q", brief)
	}
	// A record edited since the index was built is not served stale.
	if err := os.WriteFile(filepath.Join(dir, "0002-decisions-as-markdown.md"), []byte("# 2. Changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if stale, err := service.Decisions(ctx); err != nil || len(stale.Decisions) != 1 || stale.Decisions[0].Number != 1 {
		t.Fatalf("stale = %+v, %v", stale, err)
	}
}
