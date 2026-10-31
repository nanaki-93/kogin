package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

func installSkillFixture(t *testing.T, service *Service, root string) {
	t.Helper()
	directory := filepath.Join(root, "project-skills", "review-code")
	if err := os.MkdirAll(filepath.Join(directory, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: review-code\ndescription: Review captured code.\nallowed-tools: read_snippet load_skill_resource\n---\nSKILL_INSTRUCTIONS: follow the existing scope and inspect scripts/check.sh as advisory text.\n"
	for name, text := range map[string]string{"SKILL.md": manifest, "scripts/check.sh": "touch SHOULD_NEVER_EXIST\n"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	service.conf().agentRuntime.Skills = []string{"project-skills/review-code"}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectSkillsReachDeclarationThroughScopedNativeTools(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if call == 1 {
			if strings.Contains(string(body), "SKILL_INSTRUCTIONS") || strings.Contains(string(body), "touch SHOULD_NEVER_EXIST") {
				t.Error("eager skill context")
			}
			repositoryToolCall(w, "load_skill", `{"name":"review-code"}`)
			return
		}
		if call == 2 {
			if !strings.Contains(string(body), "SKILL_INSTRUCTIONS") {
				t.Error("missing native skill body")
			}
			fmt.Fprintf(w, `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"resource-2","type":"function","function":{"name":"load_skill_resource","arguments":%q}}]}}]}`, `{"skill_name":"review-code","resource_path":"scripts/check.sh"}`)
			return
		}
		if !strings.Contains(string(body), "touch SHOULD_NEVER_EXIST") || !strings.Contains(string(body), "admitted_sources") {
			t.Error("missing bounded resource provenance")
		}
		json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: validDeclarationExplanation}}}})
	}))
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	installSkillFixture(t, service, root)
	result, err := service.ExplainDeclaration(t.Context(), declarationRequest(t, service))
	if err != nil {
		t.Fatal(err)
	}
	manifest := result.ContextManifest
	if calls.Load() != 3 {
		t.Fatalf("calls=%d", calls.Load())
	}
	found := 0
	for _, file := range manifest.Included {
		if strings.HasPrefix(file.Path, "project-skills/") && file.Hash != "" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("skill provenance missing: %+v", manifest)
	}
	if _, err := os.Stat(filepath.Join(root, "SHOULD_NEVER_EXIST")); !os.IsNotExist(err) {
		t.Fatal("skill script executed", err)
	}
	if len(service.changeAuthority) != 0 {
		t.Fatal("skill granted source authority")
	}
}

func TestProjectSkillsRejectUnindexedChangedAndEscapingManifestsBeforeDispatch(t *testing.T) {
	for _, condition := range []string{"unindexed", "changed", "symlink", "excluded"} {
		t.Run(condition, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer server.Close()
			service, root := newSemanticAnalysisService(t, server.URL, 0)
			installSkillFixture(t, service, root)
			manifest := filepath.Join(root, "project-skills/review-code/SKILL.md")
			switch condition {
			case "unindexed":
				service.conf().agentRuntime.Skills = []string{"unindexed/skill"}
			case "changed":
				if err := os.WriteFile(manifest, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "excluded":
				if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("project-skills/\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := service.Reindex(); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				data, err := os.ReadFile(manifest)
				if err != nil {
					t.Fatal(err)
				}
				external := filepath.Join(t.TempDir(), "SKILL.md")
				if err := os.WriteFile(external, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(manifest); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, manifest); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := service.ExplainDeclaration(t.Context(), declarationRequest(t, service)); err == nil {
				t.Fatal("unsafe skill accepted")
			}
			if calls.Load() != 0 {
				t.Fatal("unsafe skill dispatched")
			}
		})
	}
}
