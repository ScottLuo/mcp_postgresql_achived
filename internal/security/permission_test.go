//go:build unit

package security

import "testing"

func TestGetPermissionLevel_ReadOnlyTools(t *testing.T) {
	tools := []string{
		"db_get_info",
		"db_list_databases",
		"db_list_schemas",
		"db_list_tables",
		"db_describe_table",
		"db_list_indexes",
		"db_list_constraints",
		"db_get_table_size",
		"db_get_column_stats",
		"db_get_activity",
		"db_get_connections",
		"db_get_locks",
		"db_get_slow_queries",
		"db_get_database_stats",
		"db_get_table_stats",
		"db_get_index_stats",
		"db_get_cache_stats",
		"db_get_replication_status",
		"db_get_replication_lag",
		"db_get_deadlocks",
		"db_query_readonly",
		"db_explain_query",
		"db_estimate_query_cost",
		"db_get_config",
		"db_get_config_list",
	}

	for _, tool := range tools {
		t.Run(tool, func(t *testing.T) {
			got := GetPermissionLevel(tool)
			if got != PermReadOnly {
				t.Errorf("GetPermissionLevel(%q) = %q, want %q", tool, got, PermReadOnly)
			}
		})
	}
}

func TestGetPermissionLevel_ControlledMaintenanceTools(t *testing.T) {
	tools := []string{
		"db_analyze_table",
		"db_vacuum_table",
		"db_reindex_table",
		"db_create_index",
		"db_cancel_query",
	}

	for _, tool := range tools {
		t.Run(tool, func(t *testing.T) {
			got := GetPermissionLevel(tool)
			if got != PermControlledMaint {
				t.Errorf("GetPermissionLevel(%q) = %q, want %q", tool, got, PermControlledMaint)
			}
		})
	}
}

func TestGetPermissionLevel_RequiresApprovalTools(t *testing.T) {
	tools := []string{
		"db_drop_index",
		"db_terminate_connection",
		"db_explain_analyze",
		"db_reload_config",
	}

	for _, tool := range tools {
		t.Run(tool, func(t *testing.T) {
			got := GetPermissionLevel(tool)
			if got != PermRequiresApproval {
				t.Errorf("GetPermissionLevel(%q) = %q, want %q", tool, got, PermRequiresApproval)
			}
		})
	}
}

func TestGetPermissionLevel_UnknownTool(t *testing.T) {
	got := GetPermissionLevel("db_unknown_tool_xyz")
	if got != PermRequiresApproval {
		t.Errorf("GetPermissionLevel(unknown) = %q, want %q (fail-safe)", got, PermRequiresApproval)
	}
}

func TestGetPermissionLevel_EmptyString(t *testing.T) {
	got := GetPermissionLevel("")
	if got != PermRequiresApproval {
		t.Errorf("GetPermissionLevel(\"\") = %q, want %q (fail-safe)", got, PermRequiresApproval)
	}
}
