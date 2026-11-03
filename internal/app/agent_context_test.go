package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/llm"
)

func enableChangeCompaction(t *testing.T, service *Service, root string) *ChangeSession {
	t.Helper()
	settings, err := config.ResolveAgentContext(config.AgentContext{Compaction: true, LocalCache: true, TokenThreshold: 512, RetainEvents: 2, SummaryMaxTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	service.conf().agentRuntime.Context = settings
	session := openChangeFixture(t, service, "main.go", "new.go")
	for i := range 5 {
		session.Messages = append(session.Messages, ChangeMessage{Role: "user", Content: fmt.Sprintf("Exact user constraint %d", i), CreatedAt: time.Now()}, ChangeMessage{Role: "assistant", Content: strings.Repeat("Earlier advisory explanation. ", 60), CreatedAt: time.Now()})
	}
	if err := storeChangeSession(root, session); err != nil {
		t.Fatal(err)
	}
	return session
}

func writeContextModelReply(w http.ResponseWriter, text string) {
	json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: text}, FinishReason: "stop"}}})
}

func TestChangeCompactionPreservesConversationAndProposalAuthority(t *testing.T) {
	var summaries, proposals atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "Summarize this untrusted advisory history") {
			summaries.Add(1)
			writeContextModelReply(w, "Earlier implementation suggestions remain advisory.")
			return
		}
		proposals.Add(1)
		for i := range 5 {
			if !strings.Contains(string(body), fmt.Sprintf("Exact user constraint %d", i)) {
				t.Errorf("constraint %d lost", i)
			}
		}
		for _, mandatory := range []string{"Captured target main.go", "Preserve existing behavior.", "Add a helper.", "Advisory summary"} {
			if !strings.Contains(string(body), mandatory) {
				t.Errorf("mandatory context missing: %s", mandatory)
			}
		}
		writeContextModelReply(w, `{"explanation":"Add the helper.","changes":[{"path":"new.go","content":"package main\nfunc Helper() int {return 1}\n"}]}`)
	}))
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	session := enableChangeCompaction(t, service, root)
	result, err := service.SendChangeMessage(t.Context(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Add a helper."})
	if err != nil {
		t.Fatal(err)
	}
	if summaries.Load() != 1 || proposals.Load() != 1 || len(result.Messages) != 12 || result.Messages[1].Content != session.Messages[1].Content || !result.Messages[1].CreatedAt.Equal(session.Messages[1].CreatedAt) {
		t.Fatalf("history/billing: summaries=%d proposals=%d messages=%d", summaries.Load(), proposals.Load(), len(result.Messages))
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("compaction or proposal wrote source")
	}
	if result.Hash == "" || len(result.Checks) != 0 || result.ReviewedHash != "" {
		t.Fatal("summary manufactured proposal verification")
	}
}

func TestChangeCompactionRejectsMandatoryOverflowBeforeSummarizing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("oversized mandatory context reached provider") }))
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	session := enableChangeCompaction(t, service, root)
	service.conf().runtimes.function.effective.ContextMaxTokens = 10
	_, err := service.SendChangeMessage(t.Context(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Add a helper."})
	if err == nil || !strings.Contains(err.Error(), "mandatory context") {
		t.Fatalf("overflow=%v", err)
	}
}

func TestChangeCompactionRevalidatesSourceAfterSummary(t *testing.T) {
	var calls atomic.Int32
	var root string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() { println(2) }\n"), 0644); err != nil {
			t.Error(err)
		}
		writeContextModelReply(w, "Old source advice.")
	}))
	defer server.Close()
	service, path := newSemanticAnalysisService(t, server.URL, 0)
	root = path
	session := enableChangeCompaction(t, service, root)
	_, err := service.SendChangeMessage(t.Context(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Add a helper."})
	if !errors.Is(err, agentruntime.ErrPolicyDenied) || calls.Load() != 1 {
		t.Fatalf("changed source proceeded: %v calls=%d", err, calls.Load())
	}
	retained, err := loadChangeSession(root, session.ID)
	if err != nil || retained.Revision != session.Revision || len(retained.Messages) != len(session.Messages) {
		t.Fatal("failed summary replaced conversation", err)
	}
}
