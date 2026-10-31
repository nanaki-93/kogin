package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/adk/v2/session"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

const fixtureSkillManifest = "---\nname: review-code\ndescription: Review the admitted code.\nallowed-tools: lookup load_skill_resource\n---\nSKILL_BODY: Preserve task authority and inspect references/check.md.\n"

type skillFixtureSource struct {
	mu    sync.Mutex
	data  map[string]string
	reads map[string]int
}

func skillFile(path, content string) SkillFile {
	hash := sha256.Sum256([]byte(content))
	return SkillFile{Path: path, Hash: "sha256:" + hex.EncodeToString(hash[:]), SizeBytes: int64(len(content))}
}

func newSkillSource() (*skillFixtureSource, SkillPackage) {
	manifest, resource := "skills/review-code/SKILL.md", "skills/review-code/references/check.md"
	source := &skillFixtureSource{data: map[string]string{manifest: fixtureSkillManifest, resource: "RESOURCE_BODY"}, reads: map[string]int{}}
	return source, SkillPackage{Directory: "skills/review-code", Manifest: skillFile(manifest, fixtureSkillManifest), Resources: []SkillFile{skillFile(resource, "RESOURCE_BODY")}}
}

func (s *skillFixtureSource) read(ctx context.Context, path, _ string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads[path]++
	return []byte(s.data[path]), context.Cause(ctx)
}

func (s *skillFixtureSource) replace(path, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[path] = data
}

func skilledInvestigation(t *testing.T, source *skillFixtureSource, input SkillPackage) *Investigation {
	t.Helper()
	base := fixtureInvestigation(t, nil, nil)
	result, err := NewSkillInvestigation(t.Context(), base.scope, []ToolSpec{base.tools[0].spec}, base.authorize, []SkillPackage{input}, source.read)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func skillToolResponse(w http.ResponseWriter, google bool, name, args string, call int32) {
	if google {
		fmt.Fprintf(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"id":"call-%d","name":%q,"args":%s}}]}}]}`, call, name, args)
		return
	}
	fmt.Fprintf(w, `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call-%d","type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, call, name, args)
}

func TestSkillsNativeToolsLoadLazilyAndPersistProvenance(t *testing.T) {
	for _, google := range []bool{false, true} {
		t.Run(fmt.Sprint(google), func(t *testing.T) {
			source, input := newSkillSource()
			investigation := skilledInvestigation(t, source, input)
			if source.reads[input.Resources[0].Path] != 0 {
				t.Fatal("resource was eagerly read")
			}
			var calls atomic.Int32
			handler := func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if call < 3 && strings.Contains(string(body), "SKILL_BODY") {
					t.Error("instructions loaded before request")
				}
				if call < 4 && strings.Contains(string(body), "RESOURCE_BODY") {
					t.Error("resource loaded before request")
				}
				switch call {
				case 1:
					skillToolResponse(w, google, "list_skills", `{}`, call)
				case 2:
					skillToolResponse(w, google, "load_skill", `{"name":"review-code"}`, call)
				case 3:
					if !strings.Contains(string(body), "SKILL_BODY") || !strings.Contains(string(body), input.Manifest.Hash) {
						t.Error("native instructions lost provenance")
					}
					skillToolResponse(w, google, "load_skill_resource", `{"skill_name":"review-code","resource_path":"references/check.md"}`, call)
				default:
					if !strings.Contains(string(body), "RESOURCE_BODY") || !strings.Contains(string(body), input.Resources[0].Hash) {
						t.Error("resource or provenance missing")
					}
					if google {
						io.WriteString(w, googleTextResponse)
					} else {
						json.NewEncoder(w).Encode(fixtureResponse("complete"))
					}
				}
			}
			var client *ProviderClient
			if google {
				client = googleFixture(t, handler)
			} else {
				server := httptest.NewServer(http.HandlerFunc(handler))
				defer server.Close()
				client = NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 128})
			}
			ctx, budget, store := persistedContext(t, t.TempDir())
			request := Request{Messages: []llm.ChatMessage{{Role: "user", Content: "Inspect admitted source"}}, Investigation: investigation}
			result, err := Complete(ctx, client, request)
			if err != nil || result.Content != "complete" || calls.Load() != 4 || budget.Snapshot().ToolCalls != 3 {
				t.Fatalf("result=%+v calls=%d budget=%+v err=%v", result, calls.Load(), budget.Snapshot(), err)
			}
			if request.Messages[0].Content != "Inspect admitted source" {
				t.Fatal("skill preparation mutated caller prompt")
			}
			files, err := investigation.SkillFiles(ctx)
			if err != nil || len(files) != 2 {
				t.Fatalf("files=%+v %v", files, err)
			}
			sessions, err := store.Sessions(ctx, "mini-orca", "local")
			if err != nil || len(sessions) != 1 || !strings.Contains(string(sessions[0].State["mini_orca.skills"]), input.Resources[0].Hash) {
				t.Fatalf("skill inventory not persisted: %v", err)
			}
			data, _ := json.Marshal(sessions[0].Events)
			if !strings.Contains(string(data), "mini_orca.loaded_skill_files") {
				t.Fatal("loaded skill provenance missing from native events")
			}
			if parts := nativeToolParts(t, sessions[0].Events); parts < 6 {
				t.Fatalf("native tool calls and results = %d", parts)
			}
			history, err := store.History(ctx)
			if err != nil {
				t.Fatal(err)
			}
			public, _ := json.Marshal(history.Events)
			if strings.Contains(string(public), "SKILL_BODY") || strings.Contains(string(public), "RESOURCE_BODY") {
				t.Fatal("source entered public progress")
			}
		})
	}
}

func TestSkillsRejectChangedContentAtDispatchAndPublication(t *testing.T) {
	for _, when := range []string{"before", "load", "final"} {
		t.Run(when, func(t *testing.T) {
			source, input := newSkillSource()
			investigation := skilledInvestigation(t, source, input)
			if when == "before" {
				source.replace(input.Manifest.Path, "changed")
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				call := calls.Add(1)
				if when == "load" || call == 2 {
					source.replace(input.Resources[0].Path, "changed")
				}
				if call == 1 {
					skillToolResponse(w, false, "load_skill_resource", `{"skill_name":"review-code","resource_path":"references/check.md"}`, call)
				} else {
					json.NewEncoder(w).Encode(fixtureResponse("unsafe late result"))
				}
			}))
			defer server.Close()
			client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 128})
			ctx, _ := budgetContext(t, nil)
			_, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Role: "user", Content: "Inspect"}}, Investigation: investigation})
			want := map[string]int32{"before": 0, "load": 1, "final": 2}[when]
			if !errors.Is(err, ErrPolicyDenied) || calls.Load() != want {
				t.Fatalf("calls=%d want=%d error=%v", calls.Load(), want, err)
			}
			if _, err := investigation.SkillFiles(t.Context()); when == "final" && err == nil {
				t.Fatal("stale skill admitted at publication")
			}
		})
	}
}

func TestSkillsRejectInvalidManifestsAndUnavailableTools(t *testing.T) {
	for _, data := range []string{
		"missing frontmatter", strings.Replace(fixtureSkillManifest, "name: review-code", "name: another", 1),
		strings.Replace(fixtureSkillManifest, "lookup load_skill_resource", "Bash", 1),
		strings.Replace(fixtureSkillManifest, "lookup load_skill_resource", "lookup lookup", 1),
		strings.Replace(fixtureSkillManifest, "description:", "unknown: value\ndescription:", 1),
		strings.Replace(fixtureSkillManifest, "Review the admitted code.", strings.Repeat("a", 513), 1),
	} {
		source, input := newSkillSource()
		source.replace(input.Manifest.Path, data)
		input.Manifest = skillFile(input.Manifest.Path, data)
		base := fixtureInvestigation(t, nil, nil)
		if _, err := NewSkillInvestigation(t.Context(), base.scope, []ToolSpec{base.tools[0].spec}, base.authorize, []SkillPackage{input}, source.read); err == nil {
			t.Fatalf("invalid manifest accepted: %q", data)
		}
	}
}

func TestSkillsBoundInventoryPathsAndCancellation(t *testing.T) {
	source, input := newSkillSource()
	investigation := skilledInvestigation(t, source, input)
	if _, err := NewSkillInvestigation(t.Context(), "invalid-scope", []ToolSpec{investigation.tools[0].spec}, func(context.Context) error { return nil }, []SkillPackage{input}, source.read); !errors.Is(err, ErrPolicyDenied) {
		t.Fatal("skill wrapper accepted invalid host scope", err)
	}
	for _, resource := range []string{"../outside", "references/../../outside", "references//check.md", "/etc/passwd", "https://example.com", "scripts/missing.sh", "SKILL.md"} {
		if _, err := investigation.skills.LoadResource(t.Context(), "review-code", resource); err == nil {
			t.Fatalf("resource accepted: %s", resource)
		}
	}
	for _, mutate := range []func(*SkillPackage){
		func(p *SkillPackage) { p.Directory = "../outside" }, func(p *SkillPackage) { p.Manifest.Hash = "invalid" },
		func(p *SkillPackage) { p.Resources[0].Path = "other/references/check.md" }, func(p *SkillPackage) { p.Resources[0].SizeBytes = 8193 },
		func(p *SkillPackage) { p.Resources = append(p.Resources, p.Resources[0]) },
	} {
		_, altered := newSkillSource()
		mutate(&altered)
		if err := validateSkillPackage(altered); err == nil {
			t.Fatalf("invalid inventory accepted: %+v", altered)
		}
	}
	input.Resources[0].Hash = "caller-mutated"
	if _, err := investigation.skills.LoadResource(t.Context(), "review-code", "references/check.md"); err != nil {
		t.Fatal("caller changed captured resources", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := investigation.SkillFiles(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := investigation.skills.ListResources(t.Context(), "review-code", "../escape"); err == nil {
		t.Fatal("escaping resource listing")
	}
}

func TestSkillsShareToolBudgetAndRejectUnadmittedCalls(t *testing.T) {
	source, input := newSkillSource()
	investigation := skilledInvestigation(t, source, input)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		skillToolResponse(w, false, "list_skills", `{}`, calls.Add(1))
	}))
	defer server.Close()
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 128})
	ctx, budget := budgetContext(t, func(limits *config.AgentLimits) { limits.ToolCalls = 1 })
	_, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Role: "user", Content: "Inspect"}}, Investigation: investigation})
	if !errors.Is(err, ErrBudgetExceeded) || calls.Load() != 2 || budget.Snapshot().ToolCalls != 1 {
		t.Fatalf("calls=%d usage=%+v err=%v", calls.Load(), budget.Snapshot(), err)
	}
	for _, item := range investigation.tools {
		if item.Name() == "load_skill" {
			if _, err := item.execute(t.Context(), map[string]any{"name": "review-code"}); !errors.Is(err, ErrPolicyDenied) {
				t.Fatal("unguarded skill call", err)
			}
		}
	}
}

func TestSkillsConcurrentReadsKeepCapturedIdentity(t *testing.T) {
	source, input := newSkillSource()
	investigation := skilledInvestigation(t, source, input)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			reader, err := investigation.skills.LoadResource(t.Context(), "review-code", "references/check.md")
			if err != nil {
				t.Error(err)
				return
			}
			defer reader.Close()
			data, err := io.ReadAll(reader)
			if err != nil || string(data) != "RESOURCE_BODY" {
				t.Errorf("resource=%q %v", data, err)
			}
		})
	}
	workers.Wait()
	if files, err := investigation.SkillFiles(t.Context()); err != nil || len(files) != 2 {
		t.Fatalf("files=%+v %v", files, err)
	}
}

// nativeToolParts counts the tool calls and results recorded in persisted
// native session events.
func nativeToolParts(t *testing.T, events []agentstate.Event) int {
	t.Helper()
	count := 0
	for _, stored := range events {
		var event session.Event
		if err := json.Unmarshal(stored.Data, &event); err != nil {
			t.Fatalf("decode native event: %v", err)
		}
		if event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.FunctionCall != nil {
				count++
			}
			if part != nil && part.FunctionResponse != nil {
				count++
			}
		}
	}
	return count
}
