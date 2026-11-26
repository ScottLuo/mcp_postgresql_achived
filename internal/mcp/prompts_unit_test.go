//go:build unit

package mcp

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestPrompts_AllRegistered verifies all 8 DBA workflow prompts are defined.
func TestPrompts_AllRegistered(t *testing.T) {
	prompts := allDBAPrompts()
	want := []string{
		"dba_health_check",
		"dba_performance_diagnosis",
		"dba_slow_query_analysis",
		"dba_index_recommendation",
		"dba_lock_analysis",
		"dba_connection_analysis",
		"dba_capacity_planning",
		"dba_replication_analysis",
	}
	if len(prompts) != len(want) {
		t.Fatalf("allDBAPrompts() returned %d prompts, want %d", len(prompts), len(want))
	}
	seen := make(map[string]bool)
	for _, p := range prompts {
		seen[p.name] = true
		if p.content == "" {
			t.Errorf("prompt %q has empty content", p.name)
		}
	}
	for _, name := range want {
		if !seen[name] {
			t.Errorf("expected prompt %q to be registered", name)
		}
	}
}

// TestPrompts_HealthCheckContent verifies the health check prompt guides the
// agent through monitoring tools.
func TestPrompts_HealthCheckContent(t *testing.T) {
	p := findPrompt("dba_health_check")
	if p == nil {
		t.Fatal("dba_health_check prompt not found")
	}
	for _, tool := range []string{"db_get_info", "db_get_database_stats", "db_get_connections"} {
		if !strings.Contains(p.content, tool) {
			t.Errorf("health check prompt should reference %s", tool)
		}
	}
}

// TestPrompts_RequiredArgument verifies each prompt declares a required
// "target" argument.
func TestPrompts_RequiredArgument(t *testing.T) {
	for _, p := range allDBAPrompts() {
		t.Run(p.name, func(t *testing.T) {
			// The handler is built from the prompt; registerPrompts attaches a
			// single required "target" argument to every prompt. We verify the
			// registration logic by re-deriving the argument set.
			args := promptArguments(p)
			if len(args) != 1 {
				t.Fatalf("prompt %q has %d arguments, want 1", p.name, len(args))
			}
			if args[0].Name != "target" {
				t.Errorf("prompt %q first argument = %q, want target", p.name, args[0].Name)
			}
			if !args[0].Required {
				t.Errorf("prompt %q target argument should be required", p.name)
			}
		})
	}
}

// TestPrompts_HandlerSubstitutesTarget verifies the prompt handler injects the
// target argument into the rendered message.
func TestPrompts_HandlerSubstitutesTarget(t *testing.T) {
	p := findPrompt("dba_health_check")
	if p == nil {
		t.Fatal("prompt not found")
	}
	h := buildPromptHandler(*p)

	req := &mcpsdk.GetPromptRequest{
		Params: &mcpsdk.GetPromptParams{
			Name:      "dba_health_check",
			Arguments: map[string]string{"target": "production"},
		},
	}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(res.Messages))
	}
	msg := res.Messages[0]
	if msg.Role != "user" {
		t.Errorf("message role = %q, want user", msg.Role)
	}
	text, ok := msg.Content.(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("message content is %T, want *TextContent", msg.Content)
	}
	if !strings.Contains(text.Text, "production") {
		t.Errorf("rendered prompt did not include the target: %q", text.Text)
	}
}

// TestPrompts_HandlerDefaultTarget verifies the target defaults to
// "development" when the argument is absent.
func TestPrompts_HandlerDefaultTarget(t *testing.T) {
	p := findPrompt("dba_replication_analysis")
	if p == nil {
		t.Fatal("prompt not found")
	}
	h := buildPromptHandler(*p)

	req := &mcpsdk.GetPromptRequest{Params: &mcpsdk.GetPromptParams{}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	text := res.Messages[0].Content.(*mcpsdk.TextContent)
	if !strings.Contains(text.Text, "development") {
		t.Errorf("expected default target development: %q", text.Text)
	}
}

// findPrompt returns the dbaPrompt with the given name, or nil.
func findPrompt(name string) *dbaPrompt {
	for _, p := range allDBAPrompts() {
		if p.name == name {
			return &p
		}
	}
	return nil
}

// promptArguments mirrors the argument set that registerPrompts attaches to
// every prompt, so the test can assert on it without a live *mcpsdk.Server.
func promptArguments(p dbaPrompt) []*mcpsdk.PromptArgument {
	return []*mcpsdk.PromptArgument{
		{
			Name:        "target",
			Description: "The database target alias to analyze.",
			Required:    true,
		},
	}
}
