package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ResourceProvider abstracts the database manager so resource handlers do
// not directly import internal/database.
//
// All methods return raw JSON to be embedded in the resource text.
// Implementations must not include DSNs, usernames, or passwords in the
// returned JSON.
type ResourceProvider interface {
	// GetTargets returns a JSON array of configured target aliases.
	// Example: ["development", "staging", "production"]
	GetTargets() []string

	// GetTargetInfo returns a JSON object with database version, uptime, and
	// other server information for the given target.
	GetTargetInfo(ctx context.Context, target string) (json.RawMessage, error)

	// GetSchemas returns a JSON array of schema names for the given target.
	GetSchemas(ctx context.Context, target string) (json.RawMessage, error)

	// GetSchemaInfo returns a JSON object with schema-level metadata
	// (tables, indexes, etc.) for the given target and schema.
	GetSchemaInfo(ctx context.Context, target, schema string) (json.RawMessage, error)

	// GetMonitoringSummary returns a JSON object with a monitoring snapshot
	// (connections, locks, slow queries, etc.) for the given target.
	GetMonitoringSummary(ctx context.Context, target string) (json.RawMessage, error)

	// GetHealth returns a JSON object with the health status of the given
	// target.
	GetHealth(ctx context.Context, target string) (json.RawMessage, error)
}

// resourceURIPrefix is the scheme used for all MCP resource URIs in this server.
const resourceURIPrefix = "postgres://"

// registerResources registers all MCP resources on the given server.
//
// Resources:
//   - postgres://targets                    (static resource)
//   - postgres://database/{target}/info     (template)
//   - postgres://database/{target}/schemas  (template)
//   - postgres://database/{target}/schema/{schema}  (template)
//   - postgres://database/{target}/monitoring       (template)
//   - postgres://database/{target}/health           (template)
func registerResources(s *mcpsdk.Server, rp ResourceProvider, logger *slog.Logger) {
	// --- postgres://targets (static) ---
	s.AddResource(
		&mcpsdk.Resource{
			Name:        "targets",
			Description: "List of configured PostgreSQL target aliases",
			URI:         resourceURIPrefix + "targets",
			MIMEType:    "application/json",
		},
		targetsResourceHandler(rp, logger),
	)

	// --- postgres://database/{target}/info ---
	s.AddResourceTemplate(
		&mcpsdk.ResourceTemplate{
			Name:        "database_info",
			Description: "Database version, uptime, and server information for a target",
			URITemplate: resourceURIPrefix + "database/{target}/info",
			MIMEType:    "application/json",
		},
		targetInfoResourceHandler(rp, logger),
	)

	// --- postgres://database/{target}/schemas ---
	s.AddResourceTemplate(
		&mcpsdk.ResourceTemplate{
			Name:        "database_schemas",
			Description: "List of schemas for a target",
			URITemplate: resourceURIPrefix + "database/{target}/schemas",
			MIMEType:    "application/json",
		},
		schemasResourceHandler(rp, logger),
	)

	// --- postgres://database/{target}/schema/{schema} ---
	s.AddResourceTemplate(
		&mcpsdk.ResourceTemplate{
			Name:        "schema_metadata",
			Description: "Schema-level metadata (tables, indexes) for a target",
			URITemplate: resourceURIPrefix + "database/{target}/schema/{schema}",
			MIMEType:    "application/json",
		},
		schemaInfoResourceHandler(rp, logger),
	)

	// --- postgres://database/{target}/monitoring ---
	s.AddResourceTemplate(
		&mcpsdk.ResourceTemplate{
			Name:        "monitoring_summary",
			Description: "Monitoring snapshot (connections, locks, slow queries) for a target",
			URITemplate: resourceURIPrefix + "database/{target}/monitoring",
			MIMEType:    "application/json",
		},
		monitoringResourceHandler(rp, logger),
	)

	// --- postgres://database/{target}/health ---
	s.AddResourceTemplate(
		&mcpsdk.ResourceTemplate{
			Name:        "health_status",
			Description: "Health status of a target",
			URITemplate: resourceURIPrefix + "database/{target}/health",
			MIMEType:    "application/json",
		},
		healthResourceHandler(rp, logger),
	)
}

// targetsResourceHandler returns the handler for the static postgres://targets resource.
func targetsResourceHandler(rp ResourceProvider, logger *slog.Logger) mcpsdk.ResourceHandler {
	return func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		targets := rp.GetTargets()
		b, err := json.Marshal(map[string]interface{}{"targets": targets})
		if err != nil {
			return nil, fmt.Errorf("mcp: resource targets: marshal: %w", err)
		}
		return jsonResourceResult(req.Params.URI, string(b)), nil
	}
}

// targetInfoResourceHandler returns the handler for postgres://database/{target}/info.
func targetInfoResourceHandler(rp ResourceProvider, logger *slog.Logger) mcpsdk.ResourceHandler {
	return func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		target, schema, err := parseResourceURI(req.Params.URI)
		if err != nil {
			return nil, err
		}
		if schema != "info" {
			return nil, resourceURIMismatchError("info", schema)
		}
		data, err := rp.GetTargetInfo(ctx, target)
		if err != nil {
			return nil, resourceProviderError(target, err)
		}
		return jsonResourceResult(req.Params.URI, string(data)), nil
	}
}

// schemasResourceHandler returns the handler for postgres://database/{target}/schemas.
func schemasResourceHandler(rp ResourceProvider, logger *slog.Logger) mcpsdk.ResourceHandler {
	return func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		target, schema, err := parseResourceURI(req.Params.URI)
		if err != nil {
			return nil, err
		}
		if schema != "schemas" {
			return nil, resourceURIMismatchError("schemas", schema)
		}
		data, err := rp.GetSchemas(ctx, target)
		if err != nil {
			return nil, resourceProviderError(target, err)
		}
		return jsonResourceResult(req.Params.URI, string(data)), nil
	}
}

// schemaInfoResourceHandler returns the handler for
// postgres://database/{target}/schema/{schema}.
func schemaInfoResourceHandler(rp ResourceProvider, logger *slog.Logger) mcpsdk.ResourceHandler {
	return func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		// This URI has an extra path segment beyond "schema".
		target, parts, err := parseResourceURIParts(req.Params.URI, 2)
		if err != nil {
			return nil, err
		}
		// parts[0] = "schema", parts[1] = schema name
		schemaName := parts[1]
		data, err := rp.GetSchemaInfo(ctx, target, schemaName)
		if err != nil {
			return nil, resourceProviderError(target, err)
		}
		return jsonResourceResult(req.Params.URI, string(data)), nil
	}
}

// monitoringResourceHandler returns the handler for
// postgres://database/{target}/monitoring.
func monitoringResourceHandler(rp ResourceProvider, logger *slog.Logger) mcpsdk.ResourceHandler {
	return func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		target, schema, err := parseResourceURI(req.Params.URI)
		if err != nil {
			return nil, err
		}
		if schema != "monitoring" {
			return nil, resourceURIMismatchError("monitoring", schema)
		}
		data, err := rp.GetMonitoringSummary(ctx, target)
		if err != nil {
			return nil, resourceProviderError(target, err)
		}
		return jsonResourceResult(req.Params.URI, string(data)), nil
	}
}

// healthResourceHandler returns the handler for postgres://database/{target}/health.
func healthResourceHandler(rp ResourceProvider, logger *slog.Logger) mcpsdk.ResourceHandler {
	return func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		target, schema, err := parseResourceURI(req.Params.URI)
		if err != nil {
			return nil, err
		}
		if schema != "health" {
			return nil, resourceURIMismatchError("health", schema)
		}
		data, err := rp.GetHealth(ctx, target)
		if err != nil {
			return nil, resourceProviderError(target, err)
		}
		return jsonResourceResult(req.Params.URI, string(data)), nil
	}
}

// parseResourceURI extracts (target, schema) from a resource URI of the form
// postgres://database/{target}/{schema}.
func parseResourceURI(uri string) (target, schema string, err error) {
	// Strip the fixed prefix "postgres://database/"
	rest, ok := strings.CutPrefix(uri, resourceURIPrefix+"database/")
	if !ok {
		return "", "", fmt.Errorf("mcp: resource URI %q does not start with %q", uri, resourceURIPrefix+"database/")
	}
	// rest is now "{target}/{schema}"
	idx := strings.IndexByte(rest, '/')
	if idx < 0 {
		return "", "", fmt.Errorf("mcp: resource URI %q missing schema segment", uri)
	}
	return rest[:idx], rest[idx+1:], nil
}

// parseResourceURIParts extracts the target and all extra path segments from
// a resource URI. It returns (target, extraSegments, error) where
// extraSegments has exactly `n` elements.
// Example: postgres://database/{target}/schema/{schema} with n=2
// returns target and ["schema", schema_name].
func parseResourceURIParts(uri string, n int) (string, []string, error) {
	rest, ok := strings.CutPrefix(uri, resourceURIPrefix+"database/")
	if !ok {
		return "", nil, fmt.Errorf("mcp: resource URI %q does not start with %q", uri, resourceURIPrefix+"database/")
	}
	segments := strings.Split(rest, "/")
	if len(segments) != n+1 {
		return "", nil, fmt.Errorf("mcp: resource URI %q expected %d path segments, got %d", uri, n, len(segments)-1)
	}
	return segments[0], segments[1:], nil
}

// jsonResourceResult builds a *mcpsdk.ReadResourceResult with a single
// JSON text content for the given URI.
func jsonResourceResult(uri, text string) *mcpsdk.ReadResourceResult {
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{
			{URI: uri, MIMEType: "application/json", Text: text},
		},
	}
}

// resourceProviderError wraps a provider error with target context.
func resourceProviderError(target string, err error) error {
	return fmt.Errorf("mcp: resource provider for target %q: %w", target, err)
}

// resourceURIMismatchError returns an error when the URI schema segment is
// not the expected value.
func resourceURIMismatchError(expected, actual string) error {
	return fmt.Errorf("mcp: resource URI segment %q: expected %q", actual, expected)
}
