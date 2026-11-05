package app

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/gitops"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

// HealthTrend says whether fixing converges: per day, the issues first
// reported and the issues merged tasks fixed. Issues the profile leaves out
// for being in tests or on other platforms, and repeats of dismissed
// issues, are not counted.
type HealthTrend struct {
	Days   []HealthDay `json:"days"`
	Found7 int         `json:"found_7"`
	Fixed7 int         `json:"fixed_7"`
	// Verdict is converging, growing, steady or quiet (nothing either way).
	Verdict string `json:"verdict"`
}

// HealthDay is one day of the trend, in UTC.
type HealthDay struct {
	Date  string `json:"date"`
	Found int    `json:"found"`
	Fixed int    `json:"fixed"`
}

const healthTrendDays = 14

func healthTrend(ctx context.Context, root string, findings []project.UnifiedFinding, completions []project.FindingCompletion) HealthTrend {
	now := time.Now().UTC()
	first := now.AddDate(0, 0, -(healthTrendDays - 1)).Truncate(24 * time.Hour)
	trend := HealthTrend{Days: make([]HealthDay, healthTrendDays)}
	index := map[string]int{}
	for i := range trend.Days {
		trend.Days[i].Date = first.AddDate(0, 0, i).Format(time.DateOnly)
		index[trend.Days[i].Date] = i
	}
	for _, finding := range findings {
		if !countsInTrend(finding) {
			continue
		}
		if i, ok := index[finding.DetectedAt.UTC().Format(time.DateOnly)]; ok {
			trend.Days[i].Found++
		}
	}
	for _, at := range completionTimes(ctx, root, completions) {
		if i, ok := index[at.UTC().Format(time.DateOnly)]; ok {
			trend.Days[i].Fixed++
		}
	}
	for _, day := range trend.Days[healthTrendDays-7:] {
		trend.Found7 += day.Found
		trend.Fixed7 += day.Fixed
	}
	trend.Verdict = trendVerdict(trend.Found7, trend.Fixed7)
	return trend
}

func countsInTrend(finding project.UnifiedFinding) bool {
	if finding.Source == project.FindingSourceTest || finding.SeenBefore != nil {
		return false
	}
	return finding.OutOfScope == nil || finding.OutOfScope.Reason != ScopeTest && finding.OutOfScope.Reason != ScopePlatform
}

// trendVerdict: growing when the week found at least five more issues than
// it fixed and half again as many; converging when it fixed more than it
// found.
func trendVerdict(found, fixed int) string {
	switch {
	case found == 0 && fixed == 0:
		return "quiet"
	case found-fixed >= 5 && 2*found >= 3*fixed:
		return "growing"
	case fixed > found:
		return "converging"
	default:
		return "steady"
	}
}

// completionTimes are the commit times of the tasks that fixed issues, one
// per fixed issue; issues whose commit Git no longer has are left out.
func completionTimes(ctx context.Context, root string, completions []project.FindingCompletion) []time.Time {
	var oids []string
	seen := map[string]bool{}
	for _, completion := range completions {
		if completion.CommitOID != "" && !seen[completion.CommitOID] {
			seen[completion.CommitOID] = true
			oids = append(oids, completion.CommitOID)
		}
	}
	times := map[string]time.Time{}
	for start := 0; start < len(oids); start += 200 {
		batch := oids[start:min(start+200, len(oids))]
		out, err := gitops.Command(ctx, root, "", nil, append([]string{"log", "--no-walk=unsorted", "--ignore-missing", "--format=%H %ct"}, batch...)...)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			if seconds, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				times[fields[0]] = time.Unix(seconds, 0)
			}
		}
	}
	var result []time.Time
	for _, completion := range completions {
		if at, ok := times[completion.CommitOID]; ok {
			result = append(result, at)
		}
	}
	return result
}
