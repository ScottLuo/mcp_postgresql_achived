package security

// PermissionLevel represents the minimum access level required to invoke a DBA tool.
type PermissionLevel string

const (
	// PermReadOnly allows tools that only read metadata or monitor state.
	PermReadOnly PermissionLevel = "READ_ONLY"

	// PermControlledMaint allows tools that perform controlled maintenance operations.
	PermControlledMaint PermissionLevel = "CONTROLLED_MAINTENANCE"

	// PermRequiresApproval indicates the tool requires an explicit approval record before execution.
	PermRequiresApproval PermissionLevel = "REQUIRES_APPROVAL"
)

// toolPermissions maps each DBA tool name to its required permission level.
// This is the single source of truth for tool-to-permission mapping.
var toolPermissions = map[string]PermissionLevel{
	// READ_ONLY tools (25)
	"db_get_info":               PermReadOnly,
	"db_list_databases":         PermReadOnly,
	"db_list_schemas":           PermReadOnly,
	"db_list_tables":            PermReadOnly,
	"db_describe_table":         PermReadOnly,
	"db_list_indexes":           PermReadOnly,
	"db_list_constraints":       PermReadOnly,
	"db_get_table_size":         PermReadOnly,
	"db_get_column_stats":       PermReadOnly,
	"db_get_activity":           PermReadOnly,
	"db_get_connections":        PermReadOnly,
	"db_get_locks":              PermReadOnly,
	"db_get_slow_queries":       PermReadOnly,
	"db_get_database_stats":     PermReadOnly,
	"db_get_table_stats":        PermReadOnly,
	"db_get_index_stats":        PermReadOnly,
	"db_get_cache_stats":        PermReadOnly,
	"db_get_replication_status": PermReadOnly,
	"db_get_replication_lag":    PermReadOnly,
	"db_get_deadlocks":          PermReadOnly,
	"db_query_readonly":         PermReadOnly,
	"db_explain_query":          PermReadOnly,
	"db_estimate_query_cost":    PermReadOnly,
	"db_get_config":             PermReadOnly,
	"db_get_config_list":        PermReadOnly,

	// CONTROLLED_MAINTENANCE tools (5)
	"db_analyze_table":  PermControlledMaint,
	"db_vacuum_table":   PermControlledMaint,
	"db_reindex_table":  PermControlledMaint,
	"db_create_index":   PermControlledMaint,
	"db_cancel_query":   PermControlledMaint,

	// REQUIRES_APPROVAL tools (4)
	"db_drop_index":          PermRequiresApproval,
	"db_terminate_connection": PermRequiresApproval,
	"db_explain_analyze":     PermRequiresApproval,
	"db_reload_config":       PermRequiresApproval,
}

// GetPermissionLevel returns the required permission level for the given tool name.
// Unknown tools return PermRequiresApproval (fail-safe default: most restrictive).
func GetPermissionLevel(tool string) PermissionLevel {
	if level, ok := toolPermissions[tool]; ok {
		return level
	}
	return PermRequiresApproval
}
