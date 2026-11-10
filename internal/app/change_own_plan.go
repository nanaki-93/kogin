package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Deciding first: while the investigator's plan waits for a decision, the
// human writes their own plan (what is wrong, what they would do, what
// could break) before reading the agent's, so the two can be compared as
// independent answers. It is written once and kept as written; the only
// later change is their own mark of how close it was. Skipping is recorded
// too, so the comparison is never claimed where there was none.

// OwnPlan is the plan the human wrote before the investigator's was shown.
type OwnPlan struct {
	Wrong    string `json:"wrong,omitempty"`
	Approach string `json:"approach,omitempty"`
	Risk     string `json:"risk,omitempty"`
	// Skipped: the human chose to read the agent's plan without writing one.
	Skipped bool `json:"skipped,omitempty"`
	// PlanHash is the investigator plan that was waiting, unseen, when this
	// was written.
	PlanHash  string    `json:"plan_hash"`
	WrittenAt time.Time `json:"written_at"`
	// Match is the human's own mark, once they have seen both plans (and,
	// if they wait, the change): same, partly or missed.
	Match     string    `json:"match,omitempty"`
	MatchedAt time.Time `json:"matched_at,omitempty"`
}

// Own plan marks.
const (
	OwnPlanSame   = "same"
	OwnPlanPartly = "partly"
	OwnPlanMissed = "missed"
)

var ownPlanMatches = []string{OwnPlanSame, OwnPlanPartly, OwnPlanMissed}

const maxOwnPlanField = 600

// OwnPlanRequest writes the human's own plan (or skips it), or later marks
// how close it was.
type OwnPlanRequest struct {
	ChangeIdentity
	Wrong    string `json:"wrong,omitempty"`
	Approach string `json:"approach,omitempty"`
	Risk     string `json:"risk,omitempty"`
	Skip     bool   `json:"skip,omitempty"`
	Match    string `json:"match,omitempty"`
}

var (
	errOwnPlanNotWaiting = errors.New("your own plan is written while the investigator's plan waits for your decision")
	errOwnPlanWritten    = errors.New("your plan is kept as you wrote it before seeing the agent's; only how close it was can be marked")
)

// WriteOwnPlan records the human's own plan for a task whose investigator
// plan waits for their decision, or, once it is written, their mark of how
// close it was.
func (s *Service) WriteOwnPlan(ctx context.Context, id string, request OwnPlanRequest) (*ChangeSession, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if session.OwnPlan == nil {
		own, err := newOwnPlan(session, request)
		if err != nil {
			return nil, err
		}
		own.WrittenAt = now
		session.OwnPlan = own
	} else if err := markOwnPlan(session.OwnPlan, request, now); err != nil {
		return nil, err
	}
	session.UpdatedAt = now
	return session, storeChangeSession(root, session)
}

// newOwnPlan checks a first write: the investigator's plan must be waiting,
// and the plan is either skipped or says what is wrong and what to do.
func newOwnPlan(session *ChangeSession, request OwnPlanRequest) (*OwnPlan, error) {
	plan := session.Plan
	if plan == nil || plan.Source != planSourceInvestigator || session.Workflow == nil || session.Workflow.Status != "awaiting_plan_review" {
		return nil, errOwnPlanNotWaiting
	}
	if request.Match != "" {
		return nil, fmt.Errorf("write your plan first; how close it was is marked after")
	}
	own := &OwnPlan{PlanHash: plan.Hash, Skipped: request.Skip}
	wrong, approach, risk := strings.TrimSpace(request.Wrong), strings.TrimSpace(request.Approach), strings.TrimSpace(request.Risk)
	if request.Skip {
		if wrong != "" || approach != "" || risk != "" {
			return nil, fmt.Errorf("a skipped plan has no text")
		}
		return own, nil
	}
	if !validInvestigationText(wrong, maxOwnPlanField) || !validInvestigationText(approach, maxOwnPlanField) || (risk != "" && !validInvestigationText(risk, maxOwnPlanField)) {
		return nil, fmt.Errorf("say what is wrong and what you would do, each in at most %d bytes; what could break is optional", maxOwnPlanField)
	}
	own.Wrong, own.Approach, own.Risk = wrong, approach, risk
	return own, nil
}

// markOwnPlan records how close a written plan was; its text never changes.
func markOwnPlan(own *OwnPlan, request OwnPlanRequest, now time.Time) error {
	if request.Skip || strings.TrimSpace(request.Wrong+request.Approach+request.Risk) != "" {
		return errOwnPlanWritten
	}
	if own.Skipped {
		return fmt.Errorf("a skipped plan has nothing to compare")
	}
	if !slices.Contains(ownPlanMatches, request.Match) {
		return fmt.Errorf("mark your plan same, partly or missed")
	}
	own.Match, own.MatchedAt = request.Match, now
	return nil
}

// ownPlanText is a written plan as the learning log keeps it.
func ownPlanText(own *OwnPlan) string {
	if own == nil || own.Skipped {
		return ""
	}
	text := "Wrong: " + own.Wrong + "\nApproach: " + own.Approach
	if own.Risk != "" {
		text += "\nCould break: " + own.Risk
	}
	return text
}
