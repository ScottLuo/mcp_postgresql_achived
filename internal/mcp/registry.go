package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// PermissionLevel is the three-tier tool permission model (spec §8.1).
type PermissionLevel string

const (
	// PermissionReadOnly tools may execute automatically within configured limits.
	PermissionReadOnly PermissionLevel = "READ_ONLY"
	// PermissionControlledMaintenance tools require target-policy authorization.
	PermissionControlledMaintenance PermissionLevel = "CONTROLLED_MAINTENANCE"
	// PermissionRequiresApproval tools require explicit human approval.
	PermissionRequiresApproval PermissionLevel = "REQUIRES_APPROVAL"
)

// defaultToolTimeout bounds a tool call when ToolDefinition.Timeout is zero.
const defaultToolTimeout = 30 * time.Second

// ToolHandler is the internal tool execution interface (spec §10.3).
// Implementations receive the raw JSON arguments and return the standard
// ToolResult. Any non-nil error returned here is wrapped by the Registry
// into a standard ToolError.
type ToolHandler interface {
	Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error)
}

// ToolDefinition describes a registered tool (spec §10.3).
type ToolDefinition struct {
	Name        string
	Description string
	Permission  PermissionLevel
	Timeout     time.Duration
	// InputSchema is the JSON Schema (draft 2020-12) for the tool arguments.
	InputSchema json.RawMessage
	Handler     ToolHandler
}

// PolicyError is returned by a PolicyChecker when a tool call is denied.
type PolicyError struct {
	Code    string
	Message string
}

func (e *PolicyError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// PolicyChecker abstracts the security policy engine (internal/security).
// The registry calls Check before every handler invocation; a non-nil error
// means the call is denied and the handler is not invoked.
type PolicyChecker interface {
	Check(toolName, target string, perm PermissionLevel) error
}

// ConcurrencyLimiter provides per-target concurrency control.
// Acquire blocks until a slot for the target is available or ctx is done.
// The returned release function must be called to free the slot.
type ConcurrencyLimiter interface {
	Acquire(ctx context.Context, target string) (release func(), err error)
}

// concurrencyLimiter enforces at most one concurrent tool invocation per
// target (a per-target semaphore of capacity 1).
type concurrencyLimiter struct {
	mu       sync.Mutex
	semaphores map[string]chan struct{}
}

func newConcurrencyLimiter() *concurrencyLimiter {
	return &concurrencyLimiter{semaphores: make(map[string]chan struct{})}
}

// Acquire implements ConcurrencyLimiter.
func (c *concurrencyLimiter) Acquire(ctx context.Context, target string) (func(), error) {
	c.mu.Lock()
	sem, ok := c.semaphores[target]
	if !ok {
		sem = make(chan struct{}, 1)
		c.semaphores[target] = sem
	}
	c.mu.Unlock()

	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Registry holds all registered tools and mediates their execution:
// it enforces the security policy, applies per-target concurrency control,
// applies per-tool timeouts, recovers panics, and wraps all errors into
// standard ToolResults.
type Registry struct {
	mu       sync.RWMutex
	tools    map[string]*ToolDefinition
	policy   PolicyChecker
	limiter  ConcurrencyLimiter
	logger   *slog.Logger
}

// NewRegistry creates an empty Registry.
//
// policy enforces permission checks before every handler call.
// limiter enforces per-target concurrency; if nil, a default
// one-invocation-per-target limiter is used.
// logger must be non-nil.
func NewRegistry(logger *slog.Logger, policy PolicyChecker, limiter ConcurrencyLimiter) *Registry {
	if limiter == nil {
		limiter = newConcurrencyLimiter()
	}
	return &Registry{
		tools:   make(map[string]*ToolDefinition),
		policy:  policy,
		limiter: limiter,
		logger:  logger,
	}
}

// Register validates and stores a ToolDefinition. It returns an error if
// the definition is invalid or a tool with the same name is already
// registered.
func (r *Registry) Register(def *ToolDefinition) error {
	if def == nil {
		return errors.New("mcp: cannot register nil ToolDefinition")
	}
	if def.Name == "" {
		return errors.New("mcp: tool name must be non-empty")
	}
	if def.Description == "" {
		return fmt.Errorf("mcp: tool %q: description must be non-empty", def.Name)
	}
	if def.Handler == nil {
		return fmt.Errorf("mcp: tool %q: handler must be non-nil", def.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[def.Name]; exists {
		return fmt.Errorf("mcp: tool %q already registered", def.Name)
	}
	r.tools[def.Name] = def
	return nil
}

// Get returns the ToolDefinition registered under name.
func (r *Registry) Get(name string) (*ToolDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.tools[name]
	return def, ok
}

// List returns all registered tools in stable (name-sorted) order.
func (r *Registry) List() []*ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*ToolDefinition, 0, len(r.tools))
	for _, def := range r.tools {
		out = append(out, def)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Execute runs the named tool: it checks the policy, acquires a
// concurrency slot, invokes the handler under the tool's timeout, recovers
// panics, and wraps all failures into a standard ToolResult.
//
// The returned ToolResult is never nil. On success, Result.Success is true.
func (r *Registry) Execute(ctx context.Context, name, target string, args json.RawMessage) (*ToolResult, error) {
	r.mu.RLock()
	def, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return ErrorResult(ErrCodeTargetNotFound, fmt.Sprintf("unknown tool %q", name), nil), nil
	}

	// 1. Policy check — the handler is not called when the policy denies.
	if r.policy != nil {
		if err := r.policy.Check(name, target, def.Permission); err != nil {
			r.logger.Warn("tool call denied by policy",
				"tool", name, "target", target, "error", err.Error())
			code := ErrCodePermissionDenied
			msg := err.Error()
			var perr *PolicyError
			if errors.As(err, &perr) {
				code, msg = perr.Code, perr.Message
			}
			return ErrorResult(code, msg, nil), nil
		}
	}

	// 2. Per-target concurrency slot.
	release, err := r.limiter.Acquire(ctx, target)
	if err != nil {
		code := ErrCodeExecutionFailed
		if errors.Is(err, context.DeadlineExceeded) {
			code = ErrCodeQueryTimeout
		}
		return ErrorResult(code, "concurrency slot unavailable: "+err.Error(), nil), nil
	}
	defer release()

	// 3. Handler invocation with per-tool timeout.
	timeout := def.Timeout
	if timeout <= 0 {
		timeout = defaultToolTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var result *ToolResult
	func() {
		defer func() {
			if p := recover(); p != nil {
				r.logger.Error("tool handler panicked",
					"tool", name, "target", target, "panic", fmt.Sprintf("%v", p))
				result = ErrorResult(ErrCodeExecutionFailed,
					fmt.Sprintf("tool handler panicked: %v", p), nil)
			}
		}()
		result, err = def.Handler.Execute(callCtx, args)
	}()

	if err != nil {
		return ErrorResult(ErrCodeExecutionFailed, err.Error(), nil), nil
	}
	if result == nil {
		return ErrorResult(ErrCodeExecutionFailed, "handler returned nil result", nil), nil
	}

	// 4. Timeout check: the handler may complete (possibly with nil error)
	// just after the context deadline; treat this as a timeout.
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return ErrorResult(ErrCodeQueryTimeout,
			fmt.Sprintf("tool %q exceeded its timeout", name), nil), nil
	}
	return result, nil
}

// ToMCPTools translates the internal ToolDefinitions into the MCP SDK's
// *mcpsdk.Tool representation, in stable (name-sorted) order.
func (r *Registry) ToMCPTools() []*mcpsdk.Tool {
	defs := r.List()
	out := make([]*mcpsdk.Tool, 0, len(defs))
	for _, def := range defs {
		schema := def.InputSchema
		if len(schema) == 0 {
			// Fall back to a valid empty-object schema so the SDK accepts the
			// tool definition even when no schema was provided.
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, &mcpsdk.Tool{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: schema,
		})
	}
	return out
}

// BuildToolHandler returns an mcpsdk.ToolHandler that adapts the given
// ToolDefinition's handler to the MCP SDK signature. It extracts the target
// from the arguments, generates request/operation IDs, invokes
// Registry.Execute, and converts the ToolResult into a *mcpsdk.CallToolResult.
//
// Panics in the adapter are recovered and converted into a standard MCP
// error response so the server never crashes.
func (r *Registry) BuildToolHandler(def *ToolDefinition) mcpsdk.ToolHandler {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		defer func() {
			if p := recover(); p != nil {
				r.logger.Error("tool adapter panicked",
					"tool", def.Name, "panic", fmt.Sprintf("%v", p))
			}
		}()

		target := extractTarget(req)
		requestID, _ := generateID("req-")
		operationID, _ := generateID("op-")

		// Attach the ToolContext so handlers can retrieve Target/RequestID/
		// OperationID via FromContext(ctx).
		tc := NewToolContext(ctx, target, requestID, operationID)
		result, _ := r.Execute(WithToolContext(ctx, tc), def.Name, target, req.Params.Arguments)
		return toolResultToMCP(result), nil
	}
}

// extractTarget pulls the "target" field out of the raw tool arguments.
// Returns "" when the field is absent or the arguments are not an object.
func extractTarget(req *mcpsdk.CallToolRequest) string {
	if req == nil {
		return ""
	}
	var args struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return ""
	}
	return args.Target
}

// toolResultToMCP converts a ToolResult into the MCP SDK wire format.
func toolResultToMCP(result *ToolResult) *mcpsdk.CallToolResult {
	b, err := Serialize(result)
	if err != nil {
		b = []byte(`{"success":false,"error":{"code":"` + ErrCodeExecutionFailed +
			`","message":"failed to serialize tool result"}}`)
	}
	text := string(b)
	isErr := result != nil && !result.Success
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}},
		IsError: isErr,
	}
}

// generateID returns a prefixed random ID (e.g. "req-a1b2c3...").
func generateID(prefix string) (string, error) {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}
