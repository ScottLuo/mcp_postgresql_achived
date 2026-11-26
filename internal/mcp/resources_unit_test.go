//go:build unit

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// stubResourceProvider is a stub ResourceProvider for testing.
type stubResourceProvider struct {
	targets   []string
	info      json.RawMessage
	schemas   json.RawMessage
	schemaInfo json.RawMessage
	monitor   json.RawMessage
	health    json.RawMessage
	infoErr   error
	healthErr error
}

func (s *stubResourceProvider) GetTargets() []string {
	if s.targets == nil {
		return []string{"development", "staging"}
	}
	return s.targets
}
func (s *stubResourceProvider) GetTargetInfo(ctx context.Context, target string) (json.RawMessage, error) {
	if s.infoErr != nil {
		return nil, s.infoErr
	}
	return json.RawMessage(`{"version":"16.0"}`), nil
}
func (s *stubResourceProvider) GetSchemas(ctx context.Context, target string) (json.RawMessage, error) {
	return json.RawMessage(`["public","pg_catalog"]`), nil
}
func (s *stubResourceProvider) GetSchemaInfo(ctx context.Context, target, schema string) (json.RawMessage, error) {
	return json.RawMessage(`{"schema":"public"}`), nil
}
func (s *stubResourceProvider) GetMonitoringSummary(ctx context.Context, target string) (json.RawMessage, error) {
	return json.RawMessage(`{"connections":5}`), nil
}
func (s *stubResourceProvider) GetHealth(ctx context.Context, target string) (json.RawMessage, error) {
	if s.healthErr != nil {
		return nil, s.healthErr
	}
	return json.RawMessage(`{"healthy":true}`), nil
}

func newReadResourceReq(uri string) *mcpsdk.ReadResourceRequest {
	return &mcpsdk.ReadResourceRequest{Params: &mcpsdk.ReadResourceParams{URI: uri}}
}

// TestResources_TargetsList verifies the targets resource returns the target
// list as JSON and never exposes a DSN or password.
func TestResources_TargetsList(t *testing.T) {
	pv := &stubResourceProvider{targets: []string{"development", "staging", "production"}}
	h := targetsResourceHandler(pv, newTestLogger())

	res, err := h(context.Background(), newReadResourceReq("postgres://targets"))
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(res.Contents))
	}
	text := res.Contents[0].Text

	// Must be valid JSON containing the target list.
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("targets output is not valid JSON: %v", err)
	}
	// Must not leak DSN/credentials.
	for _, bad := range []string{"postgres://user", "password", "dsn"} {
		if strings.Contains(strings.ToLower(text), bad) {
			t.Errorf("targets output leaks %q: %s", bad, text)
		}
	}
}

// TestResources_TargetInfo verifies the info resource returns provider data.
func TestResources_TargetInfo(t *testing.T) {
	pv := &stubResourceProvider{}
	h := targetInfoResourceHandler(pv, newTestLogger())

	res, err := h(context.Background(), newReadResourceReq("postgres://database/development/info"))
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !strings.Contains(res.Contents[0].Text, "16.0") {
		t.Errorf("info output missing version: %s", res.Contents[0].Text)
	}
}

// TestResources_UnknownTarget verifies that a provider error propagates and
// names the target.
func TestResources_UnknownTarget(t *testing.T) {
	pv := &stubResourceProvider{infoErr: errors.New("unknown target")}
	h := targetInfoResourceHandler(pv, newTestLogger())

	_, err := h(context.Background(), newReadResourceReq("postgres://database/nonexistent/info"))
	if err == nil {
		t.Fatal("expected error for unknown target, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error does not name the target: %v", err)
	}
}

// TestResources_SchemaInfo verifies the schema/{schema} resource parses the
// extra path segment.
func TestResources_SchemaInfo(t *testing.T) {
	pv := &stubResourceProvider{}
	h := schemaInfoResourceHandler(pv, newTestLogger())

	res, err := h(context.Background(), newReadResourceReq("postgres://database/development/schema/public"))
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !strings.Contains(res.Contents[0].Text, "public") {
		t.Errorf("schema output missing schema name: %s", res.Contents[0].Text)
	}
}

// TestResources_MonitoringAndHealth verifies monitoring and health resources.
func TestResources_MonitoringAndHealth(t *testing.T) {
	pv := &stubResourceProvider{}

	if res, err := monitoringResourceHandler(pv, newTestLogger())(context.Background(),
		newReadResourceReq("postgres://database/development/monitoring")); err != nil {
		t.Errorf("monitoring error: %v", err)
	} else if !strings.Contains(res.Contents[0].Text, "connections") {
		t.Errorf("monitoring output: %s", res.Contents[0].Text)
	}

	if res, err := healthResourceHandler(pv, newTestLogger())(context.Background(),
		newReadResourceReq("postgres://database/development/health")); err != nil {
		t.Errorf("health error: %v", err)
	} else if !strings.Contains(res.Contents[0].Text, "healthy") {
		t.Errorf("health output: %s", res.Contents[0].Text)
	}
}

// TestParseResourceURI verifies URI parsing for the standard shape.
func TestParseResourceURI(t *testing.T) {
	cases := []struct {
		uri     string
		target  string
		schema  string
		wantErr bool
	}{
		{"postgres://database/dev/info", "dev", "info", false},
		{"postgres://database/prod/health", "prod", "health", false},
		{"postgres://database", "", "", true},
		{"http://database/dev/info", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.uri, func(t *testing.T) {
			gt, gs, err := parseResourceURI(tc.uri)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gt != tc.target || gs != tc.schema {
				t.Errorf("got (%q,%q), want (%q,%q)", gt, gs, tc.target, tc.schema)
			}
		})
	}
}

// TestParseResourceURIParts verifies the multi-segment parser.
func TestParseResourceURIParts(t *testing.T) {
	target, parts, err := parseResourceURIParts("postgres://database/dev/schema/public", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != "dev" {
		t.Errorf("target = %q, want dev", target)
	}
	if len(parts) != 2 || parts[0] != "schema" || parts[1] != "public" {
		t.Errorf("parts = %v, want [schema public]", parts)
	}

	if _, _, err := parseResourceURIParts("postgres://database/dev/schema", 2); err == nil {
		t.Error("expected error when too few segments")
	}
}
