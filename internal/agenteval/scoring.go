package agenteval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

const scoringVersion = "agent-task-scoring-v1"

type regression struct {
	A    int64  `json:"a"`
	B    int64  `json:"b"`
	Want string `json:"want"`
}

type trajectoryStep struct {
	Tool  string             `json:"tool"`
	Query project.AgentQuery `json:"query"`
}

type StepReceipt struct {
	Tool     string `json:"tool"`
	QueryKey string `json:"query_key"`
	State    string `json:"state"`
}

type Metrics struct {
	TrajectoryCorrect bool  `json:"trajectory_correct"`
	ScopeViolations   int   `json:"scope_violations"`
	UnsupportedClaims int   `json:"unsupported_claims"`
	RegressionPassed  *int  `json:"regression_passed,omitempty"`
	RegressionTotal   int   `json:"regression_total"`
	ModelCalls        int   `json:"model_calls"`
	ToolCalls         int   `json:"tool_calls"`
	Repairs           int   `json:"repairs"`
	InputCharges      int64 `json:"input_token_charges"`
	OutputCharges     int64 `json:"output_token_charges"`
	ReportedInput     int64 `json:"reported_input_tokens"`
	ReportedOutput    int64 `json:"reported_output_tokens"`
	UnknownUsageCalls int   `json:"unknown_usage_calls"`
}

type CaseReceipt struct {
	ID              string        `json:"id"`
	Control         bool          `json:"control"`
	State           string        `json:"state"`
	Failures        []string      `json:"failures"`
	ExpectationMet  *bool         `json:"expectation_met,omitempty"`
	ElapsedMillis   *int64        `json:"elapsed_millis,omitempty"`
	LatencyCensored bool          `json:"latency_censored"`
	Metrics         Metrics       `json:"metrics"`
	Steps           []StepReceipt `json:"steps"`
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func scoreCase(item taskCase, result app.AgentEvaluationResult) CaseReceipt {
	receipt := CaseReceipt{ID: item.ID, Control: item.Control, State: result.State, Failures: []string{}, Steps: []StepReceipt{}, ElapsedMillis: &result.ElapsedMillis, LatencyCensored: result.State != "completed"}
	receipt.Metrics = measuredUsage(result.Before, result.After)
	receipt.Metrics.RegressionTotal = len(item.Regressions)
	receipt.Metrics.TrajectoryCorrect = len(result.Attempts) > 0
	for _, attempt := range result.Attempts {
		receipt.Metrics.TrajectoryCorrect = receipt.Metrics.TrajectoryCorrect && matchesTrajectory(attempt.Steps, item.Trajectory)
		if attempt.Answer != nil {
			receipt.Metrics.UnsupportedClaims += unsupportedClaims(*attempt.Answer, item.Files, attempt.Steps)
		}
		for _, step := range attempt.Steps {
			query, _ := json.Marshal(step.Query) // Only fixed, marshalable fields.
			receipt.Steps = append(receipt.Steps, StepReceipt{Tool: step.Tool, QueryKey: digest(query), State: step.Status})
			if step.Status == "scope_required" {
				receipt.Metrics.ScopeViolations++
			}
		}
	}
	if len(result.Attempts) > 0 {
		last := result.Attempts[len(result.Attempts)-1]
		if last.Answer != nil {
			passed := regressionCount(last.Answer.Expression, item.Regressions)
			receipt.Metrics.RegressionPassed = &passed
		}
	}
	completeCaseScore(&receipt, result.Reason)
	return receipt
}

func completeCaseScore(receipt *CaseReceipt, reason string) {
	metrics := receipt.Metrics
	for _, check := range []struct {
		failed bool
		reason string
	}{
		{!metrics.TrajectoryCorrect, "trajectory"}, {metrics.ScopeViolations > 0, "scope"},
		{metrics.UnsupportedClaims > 0, "unsupported_claims"},
		{metrics.RegressionPassed != nil && *metrics.RegressionPassed != metrics.RegressionTotal, "regression"},
	} {
		if check.failed {
			receipt.Failures = append(receipt.Failures, check.reason)
		}
	}
	if reason != "" {
		receipt.Failures = append(receipt.Failures, reason)
	}
	slices.Sort(receipt.Failures)
	if receipt.State == "completed" {
		receipt.State = "passed"
		if len(receipt.Failures) > 0 {
			receipt.State = "failed"
		}
	}
}

func measuredUsage(before, after agentstate.BudgetUsage) Metrics {
	inputBefore, outputBefore := reportedUsage(before)
	inputAfter, outputAfter := reportedUsage(after)
	return Metrics{ModelCalls: after.ModelCalls - before.ModelCalls, ToolCalls: after.ToolCalls - before.ToolCalls, Repairs: after.Repairs - before.Repairs, InputCharges: after.InputTokens - before.InputTokens, OutputCharges: after.OutputTokens - before.OutputTokens, ReportedInput: inputAfter - inputBefore, ReportedOutput: outputAfter - outputBefore, UnknownUsageCalls: after.UnknownUsageCalls - before.UnknownUsageCalls}
}

func reportedUsage(usage agentstate.BudgetUsage) (input, output int64) {
	if usage.Accounting != nil {
		for _, provider := range usage.Accounting.Providers {
			input += provider.ReportedInputTokens
			output += provider.ReportedOutputTokens
		}
	}
	return input, output
}

func matchesTrajectory(actual []app.AgentEvaluationStep, expected []trajectoryStep) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i, step := range actual {
		if step.Tool != expected[i].Tool || step.Query != expected[i].Query {
			return false
		}
	}
	return true
}

func unsupportedClaims(answer app.AgentEvaluationAnswer, files map[string]string, steps []app.AgentEvaluationStep) int {
	count := len(answer.Checks) // There is deliberately no executable check tool.
	if len(answer.Evidence) == 0 {
		count++
	}
	for _, claim := range answer.Evidence {
		if !validCitation(claim, files) || !observedCitation(claim, steps) {
			count++
		}
	}
	return count
}

func validCitation(claim app.AgentEvaluationCitation, files map[string]string) bool {
	source, ok := files[claim.Path]
	lines := strings.Split(source, "\n")
	return ok && claim.Hash == digest([]byte(source)) && claim.StartLine > 0 && claim.EndLine >= claim.StartLine && claim.EndLine <= len(lines) && claim.Quote != "" && claim.Quote == strings.Join(lines[claim.StartLine-1:claim.EndLine], "\n")
}

func observedCitation(claim app.AgentEvaluationCitation, steps []app.AgentEvaluationStep) bool {
	for _, step := range steps {
		for _, evidence := range step.Evidence {
			if step.Status == "ok" && evidence.Path == claim.Path && evidence.Hash == claim.Hash && evidence.StartLine <= claim.StartLine && evidence.EndLine >= claim.EndLine {
				return true
			}
		}
	}
	return false
}

// Regression inputs and expected values are held out from model prompts and
// replay answers. Go's constant evaluator cannot execute code or access files.
func regressionCount(expression string, checks []regression) int {
	if !boundedExpression(expression) {
		return 0
	}
	passed := 0
	for _, check := range checks {
		pkg := types.NewPackage("fixture", "fixture")
		pkg.Scope().Insert(types.NewConst(token.NoPos, pkg, "a", types.Typ[types.Int], constant.MakeInt64(check.A)))
		pkg.Scope().Insert(types.NewConst(token.NoPos, pkg, "b", types.Typ[types.Int], constant.MakeInt64(check.B)))
		value, err := types.Eval(token.NewFileSet(), pkg, token.NoPos, expression)
		if err == nil && value.Value != nil && value.Value.ExactString() == check.Want {
			passed++
		}
	}
	return passed
}

func boundedExpression(expression string) bool {
	if len(expression) < 1 || len(expression) > 256 {
		return false
	}
	expr, err := parser.ParseExpr(expression)
	if err != nil {
		return false
	}
	valid, nodes := true, 0
	ast.Inspect(expr, func(node ast.Node) bool {
		if node == nil {
			return false
		}
		nodes++
		valid = valid && allowedExpressionNode(node)
		return valid && nodes <= 64
	})
	return valid && nodes <= 64
}

func allowedExpressionNode(node ast.Node) bool {
	switch value := node.(type) {
	case *ast.BinaryExpr:
		// Bounded shifts prevent expensive constant allocation.
		return value.Op != token.SHL && value.Op != token.SHR
	case *ast.ParenExpr, *ast.UnaryExpr:
		return true
	case *ast.BasicLit:
		return value.Kind == token.INT && len(value.Value) <= 20
	case *ast.Ident:
		return value.Name == "a" || value.Name == "b"
	default:
		return false
	}
}
