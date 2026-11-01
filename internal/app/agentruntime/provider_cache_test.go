package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

func providerCacheContext(t *testing.T, root string) (context.Context, *Budget) {
	t.Helper()
	ctx, budget, _ := persistedContext(t, root)
	return configuredContext(t, ctx, config.AgentContext{ProviderCache: true, ProviderTTLSeconds: 600}), budget
}

func writeCacheReceipt(t *testing.T, w http.ResponseWriter, r *http.Request, mutate func(*genai.CachedContent)) {
	t.Helper()
	var request struct {
		ExpireTime time.Time `json:"expireTime"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Error(err)
	}
	if request.ExpireTime.IsZero() || request.ExpireTime.After(time.Now().Add(601*time.Second)) {
		t.Error("missing explicit bounded remote retention")
	}
	receipt := &genai.CachedContent{Name: "cachedContents/fixture", Model: "models/gemini-2.5-flash", ExpireTime: request.ExpireTime, UsageMetadata: &genai.CachedContentUsageMetadata{TotalTokenCount: 50}}
	if mutate != nil {
		mutate(receipt)
	}
	json.NewEncoder(w).Encode(receipt)
}

func TestProviderCacheReusesScopedPrefixWithExplicitRetention(t *testing.T) {
	var creations, generations, hits atomic.Int32
	client := googleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goog-Api-Key") != "configured-key" {
			t.Error("cache credentials not bound to selected endpoint")
		}
		if strings.HasSuffix(r.URL.Path, "/cachedContents") {
			creations.Add(1)
			writeCacheReceipt(t, w, r, nil)
			return
		}
		generations.Add(1)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"cachedContent":"cachedContents/fixture"`) {
			hits.Add(1)
			if strings.Contains(string(body), "CAPTURED_PREFIX") {
				t.Error("cached prefix sent twice")
			}
			io.WriteString(w, googleTextResponse)
			return
		}
		io.WriteString(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"id":"lookup-cache","name":"lookup","args":{"id":7}},"thoughtSignature":"c2ln"}]}}]}`)
	})
	client.profile.Model = "gemini-2.5-flash"
	root := t.TempDir()
	for i := range 2 {
		ctx, budget := providerCacheContext(t, root)
		request := Request{Messages: []llm.ChatMessage{{Role: "user", Content: strings.Repeat("CAPTURED_PREFIX ", 1200)}}, Investigation: fixtureInvestigation(t, nil, nil), ContextMaxTokens: 32000}
		result, err := Complete(ctx, client, request)
		if err != nil || result.Content != "complete" {
			t.Fatalf("cache result=%+v error=%v", result, err)
		}
		want := 2
		if i == 0 {
			want++
		}
		if budget.Snapshot().ModelCalls != want {
			t.Fatalf("cache operation escaped budget: %+v", budget.Snapshot())
		}
	}
	if creations.Load() != 1 || generations.Load() != 4 || hits.Load() != 2 {
		t.Fatalf("creates=%d generations=%d hits=%d", creations.Load(), generations.Load(), hits.Load())
	}
}

func cacheInput() *model.LLMRequest {
	return &model.LLMRequest{Config: &genai.GenerateContentConfig{}, Contents: []*genai.Content{genai.NewContentFromText(strings.Repeat("source ", 3000), genai.RoleUser), genai.NewContentFromText("current question", genai.RoleUser)}}
}

func TestProviderCacheRejectsChangedIdentityAndUnknownCreation(t *testing.T) {
	var calls atomic.Int32
	client := googleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeCacheReceipt(t, w, r, func(receipt *genai.CachedContent) { receipt.Name = "https://other.invalid/cache" })
	})
	client.profile.Model = "gemini-2.5-flash"
	root := t.TempDir()
	for i := range 2 {
		ctx, _ := providerCacheContext(t, root)
		_, err := (&googleModel{client: client}).cachedRequest(ctx, cacheInput())
		if err == nil {
			t.Fatal("foreign provider cache accepted")
		}
		if i == 1 && !errors.Is(err, agentstate.ErrContextPending) {
			t.Fatalf("unknown cache creation retried: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("uncertain creation was billed again")
	}
}

func TestProviderCacheRejectsUnsupportedModelsAndStaleAuthorityBeforeHTTP(t *testing.T) {
	client := googleFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("rejected cache request reached HTTP") })
	ctx, _ := providerCacheContext(t, t.TempDir())
	for _, name := range []string{"gemini-4-argon", "gemini-30-pro", "unknown"} {
		client.profile.Model = name
		if _, err := Complete(ctx, client, googleRequest()); !errors.Is(err, ErrPolicyDenied) {
			t.Fatalf("unsupported %s: %v", name, err)
		}
	}
	client.profile.Model = "gemini-2.5-flash"
	ctx = WithContextAuthority(ctx, func(context.Context) error { return errors.New("source changed") })
	if _, err := (&googleModel{client: client}).cachedRequest(ctx, cacheInput()); !errors.Is(err, ErrPolicyDenied) {
		t.Fatal("stale source cached", err)
	}
}

func TestProviderCacheReceiptPinsVertexResourceAndExpiry(t *testing.T) {
	profile := config.ModelProfile{Provider: config.VertexProvider, Model: "gemini-2.5-pro", VertexProject: "project", VertexLocation: "region"}
	expires := time.Now().Add(time.Minute)
	receipt := providerCacheReceipt{Name: "projects/project/locations/region/cachedContents/id", Model: "projects/project/locations/region/publishers/google/models/gemini-2.5-pro", Expires: expires}
	if err := validateProviderCacheReceipt(profile, receipt, expires); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*providerCacheReceipt){
		func(r *providerCacheReceipt) { r.Name = "projects/other/locations/region/cachedContents/id" },
		func(r *providerCacheReceipt) { r.Model = "models/gemini-2.5-pro" },
		func(r *providerCacheReceipt) { r.Name += "/../other" },
		func(r *providerCacheReceipt) { r.Expires = expires.Add(time.Second) },
		func(r *providerCacheReceipt) { r.Expires = time.Now().Add(-time.Second) },
	} {
		copy := receipt
		mutate(&copy)
		if err := validateProviderCacheReceipt(profile, copy, expires); err == nil {
			t.Fatalf("unbound receipt accepted: %+v", copy)
		}
	}
}

func TestProviderCacheVertexUsesConfiguredResourceAndCredentials(t *testing.T) {
	var caches, models atomic.Int32
	client := googleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer configured-token" || r.Header.Get("X-Goog-Api-Key") != "" {
			t.Error("Vertex cache credentials changed")
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/projects/project/locations/region/") {
			t.Errorf("Vertex resource escaped: %s", r.URL.Path)
		}
		if strings.HasSuffix(r.URL.Path, "/cachedContents") {
			caches.Add(1)
			writeCacheReceipt(t, w, r, func(receipt *genai.CachedContent) {
				receipt.Name = "projects/project/locations/region/cachedContents/fixture"
				receipt.Model = "projects/project/locations/region/publishers/google/models/gemini-2.5-flash"
			})
			return
		}
		models.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "cachedContents/fixture") {
			t.Error("Vertex generation did not use the validated cache")
		}
		io.WriteString(w, googleTextResponse)
	})
	client.profile.Provider, client.profile.Model = config.VertexProvider, "gemini-2.5-flash"
	client.profile.VertexProject, client.profile.VertexLocation = "project", "region"
	client.profile.APIKey, client.profile.AccessToken = "", "configured-token"
	ctx, budget := providerCacheContext(t, t.TempDir())
	for _, err := range (&googleModel{client: client}).GenerateContent(ctx, cacheInput(), false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if caches.Load() != 1 || models.Load() != 1 || budget.Snapshot().ModelCalls != 2 {
		t.Fatalf("Vertex cache=%d model=%d budget=%+v", caches.Load(), models.Load(), budget.Snapshot())
	}
}
