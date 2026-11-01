package agentruntime

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"regexp"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool/toolutils"
	"google.golang.org/genai"
)

const maxToolPayload = 16 << 10

// ToolSpec exposes a host-owned operation, never an SDK filesystem capability.
type ToolSpec struct {
	Name         string                                                          `json:"name"`
	Description  string                                                          `json:"description"`
	InputSchema  json.RawMessage                                                 `json:"input_schema"`
	OutputSchema json.RawMessage                                                 `json:"output_schema"`
	Run          func(context.Context, json.RawMessage) (json.RawMessage, error) `json:"-"`
}

// Investigation freezes the scope, tool contracts and shared limits across
// provider retries. Authorize must recheck host identity without side effects.
type Investigation struct {
	scope                              string
	tools                              []*guardedTool
	authorize                          func(context.Context) error
	mu                                 sync.Mutex
	modelCalls, toolCalls, outputBytes int
}

type investigationKey struct{}

// Validate is the publication boundary for the host's captured investigation.
func (i *Investigation) Validate(ctx context.Context) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if i == nil {
		return nil
	}
	return i.authorize(ctx)
}

var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func NewInvestigation(scope string, specs []ToolSpec, authorize func(context.Context) error) (*Investigation, error) {
	digest, decodeErr := hex.DecodeString(scope)
	if decodeErr != nil || len(digest) != 32 || len(specs) == 0 || len(specs) > 16 || authorize == nil {
		return nil, fmt.Errorf("%w: bounded investigation admission is required", ErrPolicyDenied)
	}
	result := &Investigation{scope: scope, authorize: authorize}
	names := make(map[string]bool)
	for _, spec := range specs {
		if !toolNamePattern.MatchString(spec.Name) || names[spec.Name] || spec.Run == nil || len(spec.Description) > 1024 {
			return nil, fmt.Errorf("%w: invalid tool descriptor", ErrPolicyDenied)
		}
		names[spec.Name] = true
		spec.InputSchema, spec.OutputSchema = append(json.RawMessage(nil), spec.InputSchema...), append(json.RawMessage(nil), spec.OutputSchema...)
		input, err := resolveToolSchema(spec.InputSchema)
		if err != nil {
			return nil, err
		}
		output, err := resolveToolSchema(spec.OutputSchema)
		if err != nil {
			return nil, err
		}
		result.tools = append(result.tools, &guardedTool{spec: spec, input: input, output: output, owner: result})
	}
	return result, nil
}

func resolveToolSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	var schema map[string]any
	if len(raw) > maxToolPayload || !jsontext.Value(raw).IsValid() || json.Unmarshal(raw, &schema) != nil || schema["type"] != "object" {
		return nil, fmt.Errorf("invalid bounded object tool schema")
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(toolSchemaLoader{})
	if err := compiler.AddResource("urn:kasuri:tool", schema); err != nil {
		return nil, err
	}
	resolved, err := compiler.Compile("urn:kasuri:tool")
	if err != nil {
		return nil, fmt.Errorf("resolve tool schema: %w", err)
	}
	return resolved, nil
}

type toolSchemaLoader struct{}

func (toolSchemaLoader) Load(string) (any, error) {
	return nil, fmt.Errorf("external tool schema references are not allowed")
}

func authorizeInvestigation(ctx context.Context) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if scope, ok := ctx.Value(investigationKey{}).(*Investigation); ok {
		if err := scope.authorize(ctx); err != nil {
			err = fmt.Errorf("%w: investigation authority changed: %w", ErrPolicyDenied, err)
			CancelRun(ctx, err)
			return err
		}
	}
	return nil
}

func (i *Investigation) admitModel(ctx context.Context) error {
	if err := authorizeInvestigation(ctx); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.modelCalls >= 8 {
		return fmt.Errorf("%w: investigation reached eight model turns", ErrBudgetExceeded)
	}
	i.modelCalls++
	return nil
}

func (i *Investigation) admitTool(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.toolCalls >= 24 {
		return fmt.Errorf("%w: investigation reached 24 tool calls", ErrBudgetExceeded)
	}
	if budget := BudgetFromContext(ctx); budget != nil {
		if err := budget.AdmitTool(ctx); err != nil {
			return err
		}
	}
	i.toolCalls++
	return nil
}

func (i *Investigation) descriptors() []ToolSpec {
	result := make([]ToolSpec, 0, len(i.tools))
	for _, t := range i.tools {
		spec := t.spec
		spec.Run = nil
		result = append(result, spec)
	}
	return result
}

type guardedTool struct {
	spec          ToolSpec
	input, output *jsonschema.Schema
	owner         *Investigation
}

func (t *guardedTool) Name() string        { return t.spec.Name }
func (t *guardedTool) Description() string { return t.spec.Description }
func (*guardedTool) IsLongRunning() bool   { return false }
func (t *guardedTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name(), Description: t.Description(), ParametersJsonSchema: t.spec.InputSchema, ResponseJsonSchema: t.spec.OutputSchema}
}
func (t *guardedTool) ProcessRequest(_ agent.Context, request *model.LLMRequest) error {
	return toolutils.PackTool(request, t)
}
func (t *guardedTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	return t.execute(ctx, args)
}

func (t *guardedTool) execute(ctx context.Context, args any) (result map[string]any, runErr error) {
	defer func() {
		if runErr != nil {
			CancelRun(ctx, runErr)
		}
	}()
	if err := checkToolGrant(ctx, t); err != nil {
		return nil, err
	}
	data, err := json.Marshal(args)
	if err != nil || len(data) > maxToolPayload {
		return nil, fmt.Errorf("invalid or oversized tool arguments")
	}
	if _, err := decodeToolObject(data, t.input); err != nil {
		return nil, fmt.Errorf("tool arguments do not match the admitted schema")
	}
	if err := t.owner.admitTool(ctx); err != nil {
		return nil, err
	}
	ctx, trace, err := StartTrace(ctx, "tool", t.Name())
	if err != nil {
		return nil, err
	}
	defer func() { runErr = trace.Finish(ctx, runErr) }()
	output, err := t.spec.Run(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("execute %s: %w", t.Name(), err)
	}
	result, err = decodeToolObject(output, t.output)
	if err != nil {
		return nil, fmt.Errorf("tool result does not match the bounded output schema")
	}
	if err := authorizeInvestigation(ctx); err != nil {
		return nil, err
	}
	t.owner.mu.Lock()
	defer t.owner.mu.Unlock()
	if t.owner.outputBytes+len(output) > 128<<10 {
		return nil, fmt.Errorf("%w: investigation retrieval exceeded 128 KiB", ErrBudgetExceeded)
	}
	t.owner.outputBytes += len(output)
	return result, nil
}

func decodeToolObject(data []byte, schema *jsonschema.Schema) (map[string]any, error) {
	if len(data) > maxToolPayload || !jsontext.Value(data).IsValid() {
		return nil, fmt.Errorf("invalid bounded JSON")
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&result) != nil || result == nil || schema.Validate(result) != nil {
		return nil, fmt.Errorf("invalid tool object")
	}
	return result, nil
}

func checkToolGrant(ctx context.Context, candidate any) error {
	scope, ok := ctx.Value(investigationKey{}).(*Investigation)
	t, owned := candidate.(*guardedTool)
	if !ok || !owned || t.owner != scope {
		return ErrPolicyDenied
	}
	return authorizeInvestigation(ctx)
}
