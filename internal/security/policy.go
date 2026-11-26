package security

import (
	"context"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// PolicyConfig mirrors the top-level structure of configs/policy.yaml.
type PolicyConfig struct {
	// Policies maps target alias names to their per-target access policies.
	Policies map[string]TargetPolicy `yaml:"policies"`
}

// TargetPolicy defines access control rules for a single database target.
// DefaultAccess represents the minimum access mode required (read_only, restricted_write, read_write).
// Tools not in Allowed and not in RequireApproval are denied by default (default-deny principle).
type TargetPolicy struct {
	// DefaultAccess is the minimum access mode required for this target.
	DefaultAccess string `yaml:"default_access"`
	// Allowed lists tools that can be invoked without approval.
	Allowed []string `yaml:"allowed"`
	// RequireApproval lists tools that require an explicit approval record before execution.
	RequireApproval []string `yaml:"require_approval"`
}

// Decision represents the outcome of a policy check.
type Decision string

const (
	// DecisionAllow means the tool can be executed immediately on the target.
	DecisionAllow Decision = "allow"
	// DecisionDeny means the tool is not permitted on the target (default-deny or unknown target).
	DecisionDeny Decision = "deny"
	// DecisionNeedsApproval means the tool may execute only after a valid approval record is presented.
	DecisionNeedsApproval Decision = "needs_approval"
)

// PolicyEngine evaluates whether a tool invocation on a given target is allowed, denied,
// or requires approval. It is the enforcement point that MCP tool handlers consult
// before executing a tool.
type PolicyEngine struct {
	cfg PolicyConfig
}

// NewPolicyEngine loads and parses the policy configuration from the YAML file at path.
// It returns an error if the file cannot be read or parsed.
func NewPolicyEngine(path string) (*PolicyEngine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	var cfg PolicyConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("policy: parse %s: %w", path, err)
	}
	return &PolicyEngine{cfg: cfg}, nil
}

// NewPolicyEngineFromConfig constructs a PolicyEngine directly from a PolicyConfig,
// bypassing file I/O. Useful for testing.
func NewPolicyEngineFromConfig(cfg PolicyConfig) *PolicyEngine {
	return &PolicyEngine{cfg: cfg}
}

// Check evaluates whether the given tool can be invoked on the given target.
// It returns a Decision and an error:
//   - DecisionAllow with nil error: tool is in the allowed list.
//   - DecisionNeedsApproval with nil error: tool is in the require_approval list.
//   - DecisionDeny with an error: target not found or tool not in either list (default-deny).
//
// The ctx parameter is accepted for future use (logging, tracing) but is not
// currently consumed beyond standard practice.
func (e *PolicyEngine) Check(ctx context.Context, target, tool string) (Decision, error) {
	targetPolicy, ok := e.cfg.Policies[target]
	if !ok {
		return DecisionDeny, fmt.Errorf("policy: unknown target %q", target)
	}

	// Check allowed list first.
	for _, t := range targetPolicy.Allowed {
		if t == tool {
			return DecisionAllow, nil
		}
	}

	// Check require_approval list.
	for _, t := range targetPolicy.RequireApproval {
		if t == tool {
			return DecisionNeedsApproval, nil
		}
	}

	// Default-deny: tool not in either list.
	return DecisionDeny, fmt.Errorf("policy: tool %q not in allowed or require_approval lists for target %q", tool, target)
}
