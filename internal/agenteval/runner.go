// Package agenteval runs bounded synthetic tasks through Kogin's native
// agent boundary. Its receipts deliberately exclude prompts and model output.
package agenteval

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"sync"

	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/config"
)

//go:embed testdata/tasks.json
var corpusData []byte

type replayStep struct {
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Content string          `json:"content,omitempty"`
	Cancel  bool            `json:"cancel,omitempty"`
}

type taskCase struct {
	ID          string            `json:"id"`
	Control     bool              `json:"control"`
	Goal        string            `json:"goal"`
	Files       map[string]string `json:"files"`
	Repairs     int               `json:"repairs"`
	Trajectory  []trajectoryStep  `json:"trajectory"`
	Regressions []regression      `json:"regressions"`
	Replay      []replayStep      `json:"replay"`
	Expected    string            `json:"expected"`
	Failures    []string          `json:"failures"`
}

type corpus struct {
	Version string     `json:"version"`
	Cases   []taskCase `json:"cases"`
}

type Options struct {
	Mode, CaseID string
	Profile      config.ModelProfile
	Confirmed    bool
	Limits       config.AgentLimits
}

type RuntimeIdentity struct {
	Go       string `json:"go"`
	ADK      string `json:"adk"`
	Build    string `json:"build"`
	Revision string `json:"revision"`
	Modified *bool  `json:"modified,omitempty"`
}

type Receipt struct {
	Version       int                    `json:"version"`
	Mode          string                 `json:"mode"`
	State         string                 `json:"state"`
	CorpusVersion string                 `json:"corpus_version"`
	CorpusDigest  string                 `json:"corpus_digest"`
	PromptVersion string                 `json:"prompt_version"`
	Scoring       string                 `json:"scoring_version"`
	Runtime       RuntimeIdentity        `json:"runtime"`
	ModelKey      string                 `json:"model_key"`
	ProfileKey    string                 `json:"profile_key"`
	Limits        config.AgentLimits     `json:"limits"`
	Usage         agentstate.BudgetUsage `json:"usage"`
	Cases         []CaseReceipt          `json:"cases"`
}

func DefaultLimits() config.AgentLimits {
	return config.AgentLimits{ModelCalls: 64, ToolCalls: 192, InputTokens: 1_000_000, OutputTokens: 32768, ElapsedSeconds: 60, ConcurrentCalls: 1, Repairs: 2}
}

func CorpusIdentity() (string, string) { return "agent-tasks-v1", digest(corpusData) }

func Run(ctx context.Context, options Options) (Receipt, error) {
	items, err := prepareCases(&options)
	if err != nil {
		return Receipt{}, err
	}
	profile := options.Profile
	var replay *replayProvider
	if options.Mode == "replay" {
		replay = &replayProvider{}
		server := httptest.NewServer(replay)
		defer server.Close()
		profile = config.ModelProfile{Provider: config.OpenAIProvider, Scope: config.CodeModelScope, APIBaseURL: server.URL, Model: "replay", MaxTokens: 512, ContextMaxTokens: 16000}
	}
	evaluator, cancel, err := app.NewAgentEvaluator(ctx, profile, options.Limits, options.Confirmed)
	if err != nil {
		return Receipt{}, err
	}
	defer cancel()
	receipt, err := newReceipt(options, profile)
	if err != nil {
		return Receipt{}, err
	}
	stopped := false
	for _, item := range items {
		if stopped || context.Cause(ctx) != nil {
			receipt.Cases = append(receipt.Cases, CaseReceipt{ID: item.ID, Control: item.Control, State: "not_run", Failures: []string{"campaign_stopped"}, Steps: []StepReceipt{}})
			receipt.State, stopped = "incomplete", true
			continue
		}
		result := runCase(ctx, evaluator, replay, item)
		receipt.Cases = append(receipt.Cases, result)
		if result.State == "incomplete" || result.State == "canceled" {
			stopped = result.ExpectationMet == nil || !*result.ExpectationMet
		}
		updateReceiptState(&receipt, result, stopped)
	}
	receipt.Usage = evaluator.Usage()
	return receipt, nil
}

func prepareCases(options *Options) ([]taskCase, error) {
	if err := prepareOptions(options); err != nil {
		return nil, err
	}
	document, err := decodeCorpus(corpusData)
	if err != nil {
		return nil, err
	}
	var selected []taskCase
	for _, item := range document.Cases {
		if options.Mode == "live" && item.Control || options.CaseID != "" && item.ID != options.CaseID {
			continue
		}
		selected = append(selected, item)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("select an available case; negative controls are replay-only")
	}
	return selected, nil
}

func prepareOptions(options *Options) error {
	if options.Mode == "" {
		options.Mode = "replay"
	}
	if options.Mode != "replay" && options.Mode != "live" {
		return fmt.Errorf("mode must be replay or live")
	}
	if options.Mode == "replay" && (options.Profile != (config.ModelProfile{}) || options.Confirmed) {
		return fmt.Errorf("replay does not accept a live model or grant")
	}
	if options.Mode == "live" && (!options.Confirmed || options.Limits == (config.AgentLimits{})) {
		return fmt.Errorf("live evaluation requires explicit confirmation and request/token limits")
	}
	if options.Limits == (config.AgentLimits{}) {
		options.Limits = DefaultLimits()
	}
	return options.Limits.Validate()
}

func decodeCorpus(data []byte) (corpus, error) {
	var result corpus
	if len(data) > 128<<10 || !jsontext.Value(data).IsValid() {
		return result, fmt.Errorf("invalid evaluation corpus")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF || result.Version != "agent-tasks-v1" || len(result.Cases) < 1 || len(result.Cases) > 16 {
		return result, fmt.Errorf("invalid versioned evaluation corpus")
	}
	seen := map[string]bool{}
	for _, item := range result.Cases {
		if !validCorpusCase(item) || seen[item.ID] {
			return result, fmt.Errorf("invalid bounded evaluation case")
		}
		seen[item.ID] = true
	}
	return result, nil
}

var caseID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

func validCorpusCase(item taskCase) bool {
	if !caseID.MatchString(item.ID) || len(item.Goal) < 1 || len(item.Goal) > 2048 || len(item.Files) < 1 || len(item.Files) > 8 || item.Repairs < 0 || item.Repairs > 2 {
		return false
	}
	if len(item.Replay) < 1 || len(item.Replay) > 16 || len(item.Trajectory) < 1 || len(item.Trajectory) > 8 || len(item.Regressions) < 1 || len(item.Regressions) > 16 {
		return false
	}
	return slices.Contains([]string{"passed", "failed", "incomplete", "canceled"}, item.Expected) && item.Failures != nil
}

func runCase(ctx context.Context, evaluator *app.AgentEvaluator, replay *replayProvider, item taskCase) CaseReceipt {
	owned, cancel := context.WithCancel(ctx)
	defer cancel()
	if replay != nil {
		replay.selectCase(item, cancel)
	}
	result, _ := evaluator.Evaluate(owned, app.AgentEvaluationTask{Goal: item.Goal, Files: item.Files, Repairs: item.Repairs}, func(answer app.AgentEvaluationAnswer) bool {
		return regressionCount(answer.Expression, item.Regressions) == len(item.Regressions)
	}) // All execution errors retain their fixed classification in result.
	receipt := scoreCase(item, result)
	if replay != nil {
		expected := slices.Clone(item.Failures)
		slices.Sort(expected)
		matched := receipt.State == item.Expected && slices.Equal(receipt.Failures, expected) && receipt.Metrics.Repairs == item.Repairs
		receipt.ExpectationMet = &matched
	}
	return receipt
}

func updateReceiptState(receipt *Receipt, result CaseReceipt, stopped bool) {
	if stopped {
		receipt.State = "incomplete"
		return
	}
	passed := result.State == "passed"
	if result.ExpectationMet != nil {
		passed = *result.ExpectationMet
	}
	if !passed && receipt.State != "incomplete" {
		receipt.State = "failed"
	}
}

func newReceipt(options Options, profile config.ModelProfile) (Receipt, error) {
	version, hash := CorpusIdentity()
	model, _ := json.Marshal([]string{string(profile.Provider), profile.Model})
	profile.APIKey, profile.AccessToken = "", ""
	if options.Mode == "replay" {
		profile.APIBaseURL = "loopback-replay"
	}
	settings, err := json.Marshal(profile)
	if err != nil {
		return Receipt{}, fmt.Errorf("invalid evaluation profile identity")
	}
	return Receipt{Version: 1, Mode: options.Mode, State: "passed", CorpusVersion: version, CorpusDigest: hash, PromptVersion: app.AgentEvaluationPromptVersion, Scoring: scoringVersion, Runtime: runtimeIdentity(), ModelKey: digest(model), ProfileKey: digest(settings), Limits: options.Limits, Cases: []CaseReceipt{}}, nil
}

func runtimeIdentity() RuntimeIdentity {
	result := RuntimeIdentity{Go: runtime.Version(), ADK: "unavailable", Build: "unavailable", Revision: "unavailable"}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return result
	}
	result.Build = digest([]byte(info.String()))
	for _, dependency := range info.Deps {
		if dependency.Path == "google.golang.org/adk/v2" {
			result.ADK = dependency.Version
			if dependency.Replace != nil {
				result.ADK = "replaced"
			}
		}
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			result.Revision = setting.Value
		case "vcs.modified":
			value := setting.Value == "true"
			result.Modified = &value
		}
	}
	return result
}

// The replay transport exercises production HTTP decoding, ADK tool turns and
// shared accounting. It listens only on httptest's owned loopback socket.
type replayProvider struct {
	mu     sync.Mutex
	item   taskCase
	next   int
	cancel context.CancelFunc
}

func (r *replayProvider) selectCase(item taskCase, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.item, r.next, r.cancel = item, 0, cancel
}

func (r *replayProvider) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	var body map[string]json.RawMessage
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 128<<10))
	if request.Method != http.MethodPost || request.URL.Path != "/chat/completions" || decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid replay request", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	if r.next >= len(r.item.Replay) {
		r.mu.Unlock()
		http.Error(w, "replay exhausted", http.StatusBadRequest)
		return
	}
	step, sequence, cancel := r.item.Replay[r.next], r.next, r.cancel
	r.next++
	r.mu.Unlock()
	if step.Cancel {
		cancel()
		<-request.Context().Done()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	message := map[string]any{"role": "assistant", "content": step.Content}
	finish := "stop"
	if step.Tool != "" {
		finish = "tool_calls"
		message["tool_calls"] = []any{map[string]any{"id": fmt.Sprintf("replay-%d", sequence), "type": "function", "function": map[string]any{"name": step.Tool, "arguments": string(step.Args)}}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"model": "replay", "choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": message}}, "usage": map[string]any{"prompt_tokens": 101, "completion_tokens": 17, "total_tokens": 118}})
}
