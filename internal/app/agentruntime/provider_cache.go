package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

var explicitCacheModel = regexp.MustCompile(`^gemini-(2\.5|3(\.[0-9]+)?)-(flash|pro)(-[a-z0-9]+)*$`)
var cacheResourceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

type providerCacheReceipt struct {
	Name    string    `json:"name"`
	Model   string    `json:"model"`
	Expires time.Time `json:"expires"`
}

func validateProviderCacheConfig(ctx context.Context, client CompletionClient) error {
	if !contextConfig(ctx).ProviderCache {
		return nil
	}
	provider, ok := client.(*ProviderClient)
	if !ok || !provider.profile.IsGoogle() || !explicitCacheModel.MatchString(strings.TrimPrefix(provider.profile.Model, "models/")) {
		return fmt.Errorf("%w: explicit provider caching requires a Gemini 2.5 or 3 generateContent profile", ErrPolicyDenied)
	}
	return nil
}

func hasContextAuthority(ctx context.Context) bool {
	_, captured := ctx.Value(contextAuthorityKey{}).(func(context.Context) error)
	_, investigation := ctx.Value(investigationKey{}).(*Investigation)
	return captured || investigation
}

// Only a complete initial text turn is cacheable. The current question/tool
// responses stay in the request; function-call pairs are never split by caching.
func providerCachePrefix(request *model.LLMRequest) *genai.CreateCachedContentConfig {
	if request == nil || len(request.Contents) < 2 || request.Config == nil {
		return nil
	}
	first := request.Contents[0]
	if first == nil || first.Role != genai.RoleUser || len(first.Parts) == 0 {
		return nil
	}
	bytes := 0
	for _, part := range first.Parts {
		if part == nil || onlyModelFields(part, "text") != nil {
			return nil
		}
		bytes += len(part.Text)
	}
	// The provider also enforces its actual model-specific minimum token count.
	if bytes < 16<<10 {
		return nil
	}
	return &genai.CreateCachedContentConfig{Contents: request.Contents[:1], SystemInstruction: request.Config.SystemInstruction, Tools: request.Config.Tools}
}

func (m *googleModel) cachedRequest(ctx context.Context, request *model.LLMRequest) (*model.LLMRequest, error) {
	configuration := contextConfig(ctx)
	if !configuration.ProviderCache || !hasContextAuthority(ctx) {
		return request, nil
	}
	prefix := providerCachePrefix(request)
	if prefix == nil {
		return request, nil
	}
	if err := requireContextAuthority(ctx); err != nil {
		return nil, err
	}
	if err := m.client.checkPolicy(ctx); err != nil {
		return nil, err
	}
	p, key, err := contextKey(ctx, "provider", []any{"google-prefix-v1", completionProviderBinding(m.client), prefix, request.Config, configuration})
	if err != nil {
		return nil, err
	}
	expires := time.Now().UTC().Add(time.Duration(configuration.ProviderTTLSeconds) * time.Second)
	guard := func() error { return requireContextAuthority(ctx) }
	record, claimed, err := p.store.ClaimContext(ctx, key, expires, guard)
	if err != nil {
		return nil, contextStorageError(ctx, err)
	}
	var receipt providerCacheReceipt
	if claimed {
		prefix.ExpireTime = record.ExpiresAt
		receipt, err = m.createProviderCache(ctx, prefix)
	} else {
		err = json.Unmarshal(record.Value, &receipt)
	}
	if err != nil {
		return nil, err
	}
	if err := validateProviderCacheReceipt(m.client.profile, receipt, record.ExpiresAt); err != nil {
		return nil, err
	}
	if err := guard(); err != nil {
		return nil, err
	}
	if claimed {
		data, err := json.Marshal(receipt)
		if err != nil {
			return nil, err
		}
		if err := p.store.SaveContext(ctx, key, record.Claim, data, receipt.Expires, guard); err != nil {
			return nil, contextStorageError(ctx, err)
		}
	}
	copy, generation := *request, *request.Config
	generation.CachedContent, generation.SystemInstruction, generation.Tools = receipt.Name, nil, nil
	copy.Contents, copy.Config = request.Contents[1:], &generation
	return &copy, nil
}

func (m *googleModel) createProviderCache(ctx context.Context, input *genai.CreateCachedContentConfig) (providerCacheReceipt, error) {
	configuration, err := m.sdkConfig()
	if err != nil {
		return providerCacheReceipt{}, err
	}
	sdk, err := genai.NewClient(ctx, configuration)
	if err != nil {
		return providerCacheReceipt{}, fmt.Errorf("%w: cannot initialize Google cache transport", llm.ErrRequestRejected)
	}
	data, err := json.Marshal(input)
	if err != nil || len(data) > nativeProviderByteLimit {
		return providerCacheReceipt{}, fmt.Errorf("%w: cache prefix exceeds its byte limit", llm.ErrRequestRejected)
	}
	if err := requireContextAuthority(ctx); err != nil {
		return providerCacheReceipt{}, err
	}
	if err := m.client.checkPolicy(ctx); err != nil {
		return providerCacheReceipt{}, err
	}
	budget := BudgetFromContext(ctx)
	if budget == nil {
		return providerCacheReceipt{}, fmt.Errorf("%w: cache creation requires an admitted budget", ErrPolicyDenied)
	}
	reservation, err := budget.reserveModel(ctx, int64(len(data))+256, 0)
	if err != nil {
		return providerCacheReceipt{}, err
	}
	defer reservation.settle(0, 0, false, false)
	created, err := sdk.Caches.Create(ctx, m.client.profile.Model, input)
	if err != nil {
		return providerCacheReceipt{}, nativeProviderError(ctx, err, false)
	}
	if created == nil {
		return providerCacheReceipt{}, fmt.Errorf("%w: cache creation returned no receipt", llm.ErrUnusableResponse)
	}
	if created.UsageMetadata != nil {
		if created.UsageMetadata.TotalTokenCount < 0 {
			return providerCacheReceipt{}, fmt.Errorf("%w: invalid cache token usage", llm.ErrUnusableResponse)
		}
		if err := reservation.settle(int64(created.UsageMetadata.TotalTokenCount), 0, true, true); err != nil {
			return providerCacheReceipt{}, err
		}
	}
	return providerCacheReceipt{Name: created.Name, Model: created.Model, Expires: created.ExpireTime}, nil
}

func validateProviderCacheReceipt(profile config.ModelProfile, receipt providerCacheReceipt, admittedExpiry time.Time) error {
	prefix := "cachedContents/"
	modelName := "models/" + strings.TrimPrefix(profile.Model, "models/")
	if profile.Provider == config.VertexProvider {
		prefix = "projects/" + profile.VertexProject + "/locations/" + profile.VertexLocation + "/cachedContents/"
		modelName = "projects/" + profile.VertexProject + "/locations/" + profile.VertexLocation + "/publishers/google/models/" + strings.TrimPrefix(profile.Model, "models/")
	}
	id, matches := strings.CutPrefix(receipt.Name, prefix)
	if !matches || !cacheResourceID.MatchString(id) || receipt.Model != modelName {
		return fmt.Errorf("%w: provider cache changed its resource or model identity", llm.ErrUnusableResponse)
	}
	if !receipt.Expires.After(time.Now()) || receipt.Expires.After(admittedExpiry) {
		return fmt.Errorf("%w: provider cache returned an expired or excessive retention period", llm.ErrUnusableResponse)
	}
	return nil
}
