package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// dbaPrompts lists all DBA workflow prompts registered with the MCP server.
//
// Prompts are advisory: they guide the AI agent through a sequence of tool
// calls. They do NOT grant additional permissions; the tool-level permission
// model still applies.
type dbaPrompt struct {
	name        string
	description string
	content     string
}

// allDBAPrompts returns the static list of DBA workflow prompt definitions.
func allDBAPrompts() []dbaPrompt {
	return []dbaPrompt{
		{
			name:        "dba_health_check",
			description: "General database health check: gather database info, stats, connections, and health status.",
			content:     dbaHealthCheckContent,
		},
		{
			name:        "dba_performance_diagnosis",
			description: "Diagnose database performance: collect stats, activity, locks, slow queries, and explain plans.",
			content:     dbaPerformanceDiagnosisContent,
		},
		{
			name:        "dba_slow_query_analysis",
			description: "Investigate slow SQL: retrieve slow queries and generate execution plans.",
			content:     dbaSlowQueryAnalysisContent,
		},
		{
			name:        "dba_index_recommendation",
			description: "Analyze index opportunities: inspect indexes, table stats, and execution plans.",
			content:     dbaIndexRecommendationContent,
		},
		{
			name:        "dba_lock_analysis",
			description: "Investigate lock contention: examine locks, activity, and optionally cancel blocking queries.",
			content:     dbaLockAnalysisContent,
		},
		{
			name:        "dba_connection_analysis",
			description: "Analyze connection usage: inspect connections, activity, and optionally terminate connections.",
			content:     dbaConnectionAnalysisContent,
		},
		{
			name:        "dba_capacity_planning",
			description: "Analyze database capacity: inspect database stats, table sizes, and configuration.",
			content:     dbaCapacityPlanningContent,
		},
		{
			name:        "dba_replication_analysis",
			description: "Investigate replication status and lag.",
			content:     dbaReplicationAnalysisContent,
		},
	}
}

// registerPrompts registers all DBA workflow prompts on the MCP server.
func registerPrompts(s *mcpsdk.Server) {
	for _, p := range allDBAPrompts() {
		// Capture loop variable for the closure.
		promptDef := p
		s.AddPrompt(
			&mcpsdk.Prompt{
				Name:        promptDef.name,
				Description: promptDef.description,
				Arguments: []*mcpsdk.PromptArgument{
					{
						Name:        "target",
						Description: "The database target alias to analyze.",
						Required:    true,
					},
				},
			},
			buildPromptHandler(promptDef),
		)
	}
}

// buildPromptHandler returns the mcpsdk.PromptHandler for a DBA prompt.
// The handler renders the prompt content with the target argument substituted
// and returns it as a user-role prompt message.
func buildPromptHandler(p dbaPrompt) mcpsdk.PromptHandler {
	return func(ctx context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
		target := "development"
		if req != nil && req.Params != nil {
			if v, ok := req.Params.Arguments["target"]; ok && v != "" {
				target = v
			}
		}
		// Build the message with the target name included for context.
		message := "Target: " + target + "\n\n" + p.content
		return &mcpsdk.GetPromptResult{
			Description: p.description,
			Messages: []*mcpsdk.PromptMessage{
				{
					Role: "user",
					Content: &mcpsdk.TextContent{
						Text: message,
					},
				},
			},
		}, nil
	}
}

// Prompt content strings.
const dbaHealthCheckContent = `Perform a general database health check.

Steps:
1. Call db_get_info to retrieve database version, uptime, and server information.
2. Call db_get_database_stats to check transaction counts, cache hits, and I/O statistics.
3. Call db_get_connections to check connection counts and utilization.
4. Call the postgres://database/{target}/health resource to get health status.

Produce a structured report with:
- Database version and uptime
- Connection usage (current / max)
- Cache hit ratio
- Any unusual statistics or warnings`

const dbaPerformanceDiagnosisContent = `Diagnose database performance issues.

Steps:
1. Call db_get_database_stats to check I/O and cache statistics.
2. Call db_get_activity to see currently running queries.
3. Call db_get_locks to check for lock contention.
4. Call db_get_slow_queries to identify the slowest queries.
5. For the top slow queries, call db_explain_query to get execution plans.
6. Check index statistics with db_get_index_stats if relevant.

Produce a structured diagnosis report with:
- Observed facts (query plans, statistics)
- Hypotheses (possible causes)
- Recommendations (specific actions to consider)`

const dbaSlowQueryAnalysisContent = `Investigate slow SQL queries.

Steps:
1. Call db_get_slow_queries to retrieve the slowest queries.
2. For each slow query, call db_explain_query to get the execution plan.
3. If a query appears to have a bad plan, call db_explain_analyze to get timing data.
4. Check table statistics with db_get_table_stats for the involved tables.

Report the top slow queries with:
- The SQL text
- Execution time
- Execution plan summary
- Recommended improvements`

const dbaIndexRecommendationContent = `Analyze index opportunities.

Steps:
1. Call db_list_indexes to see existing indexes on the target database.
2. Call db_get_table_stats to check which tables have high sequential scan counts.
3. Call db_get_index_stats to check which indexes are unused or underused.
4. For tables with high sequential scans, call db_explain_query on representative queries.

Report:
- Tables with potentially missing indexes
- Indexes that appear unused (candidates for removal)
- Recommended new indexes with expected benefit`

const dbaLockAnalysisContent = `Investigate lock contention.

Steps:
1. Call db_get_locks to see current lock state.
2. Call db_get_activity to identify sessions holding or waiting for locks.
3. Identify the root blocking session.
4. If a session is blocking critical work and the user approves, call db_cancel_query on its PID.

Report:
- Current lock hierarchy
- Blocking chains
- Recommendations for resolving contention`

const dbaConnectionAnalysisContent = `Analyze connection usage.

Steps:
1. Call db_get_connections to see connection statistics.
2. Call db_get_activity to identify long-running sessions.
3. Identify idle sessions that are holding connections.
4. If the user approves, call db_terminate_connection on problematic PIDs.

Report:
- Current connection count vs. maximum
- Connection state breakdown
- Long-running or idle sessions
- Recommendations for connection management`

const dbaCapacityPlanningContent = `Analyze database capacity.

Steps:
1. Call db_get_database_stats to check I/O and cache statistics.
2. Call db_get_table_size to check the sizes of the largest tables.
3. Call db_get_config to check relevant configuration parameters (shared_buffers, work_mem, etc.).

Report:
- Database size and growth indicators
- Memory configuration analysis
- Recommendations for capacity planning`

const dbaReplicationAnalysisContent = `Investigate replication status.

Steps:
1. Call db_get_replication_status to check replication topology and state.
2. Call db_get_replication_lag to measure current replication lag.

Report:
- Replication status (primary/standby, WAL position)
- Replication lag (if applicable)
- Recommendations for maintaining replication health`
