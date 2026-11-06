package agentruntime

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
)

// Analysis admission and evidence remain in the host. The graph captures only
// immutable identities for one bounded window; restoring it never dispatches.
type AnalysisSnapshot struct {
	ProjectID           string `json:"project_id"`
	ProjectRevision     string `json:"project_revision"`
	RunID               string `json:"run_id"`
	Generation          string `json:"generation"`
	QueueID             string `json:"queue_id"`
	PolicyFingerprint   string `json:"policy_fingerprint"`
	ProviderFingerprint string `json:"provider_fingerprint"`
}

type AnalysisWork struct {
	Ordinal    int    `json:"ordinal"`
	FileIndex  int    `json:"file_index"`
	StageIndex int    `json:"stage_index"`
	Stage      string `json:"stage"`
	FileHash   string `json:"file_hash"`
}

// AnalysisGraphInput is one window of analysis work. Lanes run side by side:
// a file's stages stay in order on one lane, and files are dealt to the
// lanes in turn, so up to Lanes stages run at once.
type AnalysisGraphInput struct {
	Snapshot AnalysisSnapshot `json:"snapshot"`
	Work     []AnalysisWork   `json:"work"`
	Lanes    int              `json:"lanes"`
}

type AnalysisOutcome struct {
	Status   string
	Continue bool
}

type AnalysisNodeResult struct {
	Work   AnalysisWork `json:"work"`
	Status string       `json:"status"`
	Next   string       `json:"next"`
}

type AnalysisGraphResult struct {
	Nodes   []AnalysisNodeResult
	Stopped bool
}

type AnalysisHost func(context.Context, AnalysisWork) (AnalysisOutcome, error)

// analysisResultKey carries a node's settled result on its finishing event.
const analysisResultKey = "mini_orca.analysis_result"

var analysisNodeStages = []string{"semantic", "performance", "security_rules", "security_ai"}

// No SDK retry is enabled. Host stage errors stop dispatch; a recorded failed
// report may continue to the next stage without losing its failure status.
func RunAnalysisGraph(ctx context.Context, input AnalysisGraphInput, host AnalysisHost) (AnalysisGraphResult, error) {
	if err := validateAnalysisGraph(input, host); err != nil {
		return AnalysisGraphResult{}, err
	}
	input.Work = slices.Clone(input.Work)
	lanes := analysisLanes(input.Work, input.Lanes)
	var edges []workflow.Edge
	for _, lane := range lanes {
		previous := workflow.Start
		for i, work := range lane {
			node := &analysisWorkNode{BaseNode: workflow.NewBaseNode(analysisNodeID(work), "", workflow.NodeConfig{}), work: work, last: i == len(lane)-1, host: host}
			edge := workflow.Edge{From: previous, To: node}
			if i > 0 {
				edge.Route = workflow.StringRoute("continue")
			}
			edges = append(edges, edge)
			previous = node
		}
	}
	worker, err := graphAgent("project_analysis", edges, len(lanes), nil)
	if err != nil {
		return AnalysisGraphResult{}, err
	}
	return runGraph(ctx, "analysis_graph", "analysis-graph-v2", input, analysisNodeStages, worker, func(ctx context.Context, events iter.Seq2[*session.Event, error]) (AnalysisGraphResult, error) {
		return collectAnalysisGraph(ctx, events, lanes)
	})
}

// analysisLanes deals the window's files to at most lanes lanes in turn,
// each file's stages together and in order.
func analysisLanes(work []AnalysisWork, lanes int) [][]AnalysisWork {
	files := 0
	for i, item := range work {
		if i == 0 || item.FileIndex != work[i-1].FileIndex {
			files++
		}
	}
	result := make([][]AnalysisWork, max(1, min(lanes, files)))
	file := -1
	for i, item := range work {
		if i == 0 || item.FileIndex != work[i-1].FileIndex {
			file++
		}
		lane := file % len(result)
		result[lane] = append(result[lane], item)
	}
	return result
}

func validateAnalysisGraph(input AnalysisGraphInput, host AnalysisHost) error {
	if host == nil || len(input.Work) == 0 || len(input.Work) > 2000 || input.Lanes < 1 || input.Lanes > 32 {
		return fmt.Errorf("analysis graph requires a host, 1-2000 stage nodes and 1-32 lanes")
	}
	s := input.Snapshot
	if err := validateGraphLabels(s.ProjectID, s.ProjectRevision, s.RunID, s.Generation, s.QueueID, s.PolicyFingerprint, s.ProviderFingerprint); err != nil {
		return fmt.Errorf("analysis admission: %w", err)
	}
	files := 0
	previous := AnalysisWork{FileIndex: -1}
	for i, work := range input.Work {
		if err := validateAnalysisWork(work, previous, i); err != nil {
			return err
		}
		if work.FileIndex != previous.FileIndex {
			files++
		}
		previous = work
	}
	if files > 500 {
		return fmt.Errorf("analysis graph exceeds 500 files per window")
	}
	return nil
}

func validateAnalysisWork(work, previous AnalysisWork, ordinal int) error {
	if work.Ordinal != ordinal || work.FileIndex < 0 || work.StageIndex < 0 || work.StageIndex >= len(analysisNodeStages) || work.Stage != analysisNodeStages[work.StageIndex] {
		return fmt.Errorf("analysis graph has an invalid stage identity")
	}
	if work.FileIndex < previous.FileIndex || work.FileIndex == previous.FileIndex && (work.StageIndex <= previous.StageIndex || work.FileHash != previous.FileHash) {
		return fmt.Errorf("analysis graph work is reordered, duplicated or inconsistent")
	}
	return validateGraphLabels(work.FileHash)
}

type analysisWorkNode struct {
	workflow.BaseNode
	work AnalysisWork
	last bool
	host AnalysisHost
}

func (n *analysisWorkNode) Run(ctx agent.Context, _ any) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		started := session.NewEvent(ctx, ctx.InvocationID())
		started.CustomMetadata = map[string]any{"mini_orca.analysis_node": analysisNodeID(n.work), "mini_orca.node_status": "running"}
		if !yield(started, nil) {
			return
		}
		if err := context.Cause(ctx); err != nil {
			yield(nil, err)
			return
		}
		outcome, err := tracedHost(ctx, "node", analysisNodeID(n.work), func(ctx context.Context) (AnalysisOutcome, error) { return n.host(ctx, n.work) })
		if err == nil {
			err = context.Cause(ctx)
		}
		if err == nil {
			err = validateAnalysisOutcome(outcome)
		}
		if err != nil {
			yield(nil, fmt.Errorf("analysis node %s: %w", analysisNodeID(n.work), err))
			return
		}
		next := "continue"
		if !outcome.Continue {
			next = "stop"
		} else if n.last {
			next = "complete"
		}
		finished := session.NewEvent(ctx, ctx.InvocationID())
		// The result travels as metadata, not output: lanes end on their own
		// nodes, and a workflow may have at most one terminal output.
		finished.CustomMetadata = map[string]any{analysisResultKey: AnalysisNodeResult{Work: n.work, Status: outcome.Status, Next: next}}
		finished.Routes = []string{next}
		yield(finished, nil)
	}
}

func validateAnalysisOutcome(outcome AnalysisOutcome) error {
	if outcome.Status == "stopped" && !outcome.Continue {
		return nil
	}
	if slices.Contains([]string{"completed", "completed_empty", "partial", "failed", "skipped", "unavailable"}, outcome.Status) {
		return nil
	}
	return fmt.Errorf("analysis host returned an unsettled stage")
}

// collectAnalysisGraph settles the window when every lane completed its
// work or stopped; lanes interleave, a lane's stages arrive in its order.
func collectAnalysisGraph(ctx context.Context, events iter.Seq2[*session.Event, error], lanes [][]AnalysisWork) (AnalysisGraphResult, error) {
	var result AnalysisGraphResult
	progress := newLaneProgress(lanes)
	for event, err := range events {
		if err != nil {
			return result, err
		}
		if event == nil {
			return result, fmt.Errorf("analysis graph returned an empty event")
		}
		value, settledNode := event.CustomMetadata[analysisResultKey]
		if !settledNode {
			continue
		}
		node, ok := value.(AnalysisNodeResult)
		if !ok || !progress.settle(node) {
			return result, fmt.Errorf("analysis graph returned an unexpected stage")
		}
		result.Nodes = append(result.Nodes, node)
		result.Stopped = result.Stopped || node.Next == "stop"
	}
	if err := context.Cause(ctx); err != nil {
		return result, err
	}
	if !progress.finished() {
		return result, fmt.Errorf("analysis graph ended before settling its window")
	}
	return result, nil
}

// laneProgress follows how far each lane settled.
type laneProgress struct {
	lanes   [][]AnalysisWork
	laneOf  map[int]int
	settled []int
	done    []bool
}

func newLaneProgress(lanes [][]AnalysisWork) *laneProgress {
	progress := &laneProgress{lanes: lanes, laneOf: map[int]int{}, settled: make([]int, len(lanes)), done: make([]bool, len(lanes))}
	for lane, work := range lanes {
		for _, item := range work {
			progress.laneOf[item.Ordinal] = lane
		}
	}
	return progress
}

// settle records a node's result; false when it is not its lane's next node.
func (p *laneProgress) settle(node AnalysisNodeResult) bool {
	lane, known := p.laneOf[node.Work.Ordinal]
	if !known || p.done[lane] || node.Work != p.lanes[lane][p.settled[lane]] {
		return false
	}
	p.settled[lane]++
	p.done[lane] = node.Next == "stop" || node.Next == "complete" && p.settled[lane] == len(p.lanes[lane])
	return true
}

func (p *laneProgress) finished() bool {
	return !slices.Contains(p.done, false)
}

func analysisNodeID(work AnalysisWork) string {
	return fmt.Sprintf("analysis_%04d_%s", work.Ordinal, work.Stage)
}

func knownAnalysisNode(name string) bool {
	value, ok := strings.CutPrefix(name, "analysis_")
	if !ok {
		return false
	}
	ordinal, stage, _ := strings.Cut(value, "_")
	index, err := strconv.Atoi(ordinal)
	return err == nil && index >= 0 && index < 2000 && slices.Contains(analysisNodeStages, stage) && name == analysisNodeID(AnalysisWork{Ordinal: index, Stage: stage})
}

func analysisProgressState(status string) string {
	switch status {
	case "completed", "completed_empty":
		return "completed"
	case "failed":
		return "failed"
	default:
		return "recorded" // Partial, unavailable and stopped work is not success.
	}
}
