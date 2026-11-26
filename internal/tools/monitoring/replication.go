package monitoring

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"

	"postgres-dba-mcp/internal/mcp"
)

// Tool names for the replication tools (spec §7.2).
const (
	toolGetReplicationStatus = "db_get_replication_status"
	toolGetReplicationLag    = "db_get_replication_lag"
)

// roleQuery reports whether the current node is in recovery mode (a standby).
// It is the first step of topology detection for both replication tools.
const roleQuery = `SELECT pg_is_in_recovery() AS in_recovery`

// primaryStatusSQL returns one row per connected standby (streaming
// replication). An empty result on a primary means the target is not a
// replication setup from this node's perspective.
const primaryStatusSQL = `
SELECT
    application_name,
    client_addr,
    state,
    sync_state,
    sent_lsn,
    write_lsn,
    flush_lsn,
    replay_lsn,
    pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn) AS lag_lsn
FROM pg_stat_replication`

// standbyStatusSQL returns the WAL receiver status on a standby. An empty
// result means no streaming receiver is configured (not a replication setup).
const standbyStatusSQL = `
SELECT
    status,
    sender_host,
    sender_port,
    timeline,
    latest_wal_receive_lsn,
    latest_wal_receive_timestamp,
    latest_wal_replay_timestamp
FROM pg_stat_wal_receiver`

// primaryLagSQL computes, for each connected standby, the lag between the
// primary's current WAL LSN and the standby's replay position.
const primaryLagSQL = `
SELECT
    application_name,
    client_addr,
    state,
    replay_lsn,
    pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn) AS lag_lsn
FROM pg_stat_replication`

// standbyLagSQL computes the in-flight lag on a standby: the gap between the
// last WAL received and the last WAL replayed locally.
const standbyLagSQL = `
SELECT
    pg_last_wal_receive_lsn() AS received_lsn,
    pg_last_wal_replay_lsn()  AS replayed_lsn,
    pg_last_wal_apply_lsn()   AS applied_lsn,
    CASE
        WHEN pg_last_wal_receive_lsn() IS NULL THEN NULL
        ELSE pg_wal_lsn_diff(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn())
    END AS lag_lsn
FROM pg_stat_wal_receiver
WHERE status = 'streaming'`

// featureUnavailableReplication is the message returned when the target is not
// part of an active streaming-replication setup, so callers receive a
// structured DB_FEATURE_UNAVAILABLE result rather than a raw database error.
const featureUnavailableReplication = "target is not part of an active streaming-replication setup; replication status is unavailable"

// isStandby runs the role-detection query and reports whether the node is in
// recovery mode. A nil result or missing value is treated as "not a standby".
func (s *Service) isStandby(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	out, err := s.runQuery(ctx, pool, roleQuery, nil, 1)
	if err != nil {
		return false, err
	}
	if len(out.rows) == 0 {
		return false, nil
	}
	if v, ok := out.rows[0]["in_recovery"]; ok {
		if b, ok := v.(bool); ok {
			return b, nil
		}
	}
	return false, nil
}

// getReplicationStatus implements db_get_replication_status (spec §7.2). It
// detects the node role (primary/standby) and reports the corresponding
// streaming-replication view. On a primary with no connected standbys, it
// returns success with role="primary" and an empty standby list. On a
// standby with no WAL receiver, it returns DB_FEATURE_UNAVAILABLE.
func (s *Service) getReplicationStatus(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args)
	if err != nil {
		return s.fail(ctx, toolGetReplicationStatus, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetReplicationStatus, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	standby, err := s.isStandby(ctx, pool)
	if err != nil {
		return s.fail(ctx, toolGetReplicationStatus, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	var sql string
	if standby {
		sql = standbyStatusSQL
	} else {
		sql = primaryStatusSQL
	}

	out, err := s.runQuery(ctx, pool, sql, nil, a.Limit)
	if err != nil {
		return s.fail(ctx, toolGetReplicationStatus, a.Target, errCodeForErr(err), err.Error(), out.duration), nil
	}

	// On a primary with no connected standbys, return success with an
	// empty standby list (spec: "not a standby, no crash").
	if !standby && len(out.rows) == 0 {
		data := map[string]any{
			"role":     "primary",
			"standbys": []any{},
		}
		return s.ok(ctx, toolGetReplicationStatus, a.Target, data, out.duration, false, 0), nil
	}

	// On a standby with no WAL receiver, the feature is unavailable.
	if standby && len(out.rows) == 0 {
		return s.fail(ctx, toolGetReplicationStatus, a.Target, mcp.ErrCodeFeatureUnavailable,
			featureUnavailableReplication, out.duration), nil
	}

	data := map[string]any{
		"role": "standby",
		"status": out.rows,
	}
	if !standby {
		data["role"] = "primary"
	}

	return s.ok(ctx, toolGetReplicationStatus, a.Target, data, out.duration, out.execResult.Truncated, out.execResult.RowsReturned), nil
}

// getReplicationLag implements db_get_replication_lag (spec §7.2). It reports
// replication lag for the detected node role. On a single-node primary (no
// connected standbys) it returns success with an empty lag list. On a standby
// with no WAL receiver it returns DB_FEATURE_UNAVAILABLE.
func (s *Service) getReplicationLag(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args)
	if err != nil {
		return s.fail(ctx, toolGetReplicationLag, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetReplicationLag, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	standby, err := s.isStandby(ctx, pool)
	if err != nil {
		return s.fail(ctx, toolGetReplicationLag, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	var sql string
	if standby {
		sql = standbyLagSQL
	} else {
		sql = primaryLagSQL
	}

	out, err := s.runQuery(ctx, pool, sql, nil, a.Limit)
	if err != nil {
		return s.fail(ctx, toolGetReplicationLag, a.Target, errCodeForErr(err), err.Error(), out.duration), nil
	}

	// On a single-node primary (no connected standbys) return success with
	// an empty lag list: there is no lag to report.
	if !standby && len(out.rows) == 0 {
		data := map[string]any{
			"role": "primary",
			"lag":  []any{},
		}
		return s.ok(ctx, toolGetReplicationLag, a.Target, data, out.duration, false, 0), nil
	}

	// On a standby with no WAL receiver, the feature is unavailable.
	if standby && len(out.rows) == 0 {
		return s.fail(ctx, toolGetReplicationLag, a.Target, mcp.ErrCodeFeatureUnavailable, featureUnavailableReplication, out.duration), nil
	}

	return s.ok(ctx, toolGetReplicationLag, a.Target, out.rows, out.duration, out.execResult.Truncated, out.execResult.RowsReturned), nil
}

func schemaGetReplicationStatus() json.RawMessage {
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"target":{"type":"string","description":"Configured database target alias"}},"required":["target"],"additionalProperties":false}`)
}

const replicationStatusDescription = "Return streaming-replication status: on a primary, one row per connected standby (state, sync state, LSN positions, lag); on a standby, the WAL receiver status. When the target is not part of an active replication setup the tool reports DB_FEATURE_UNAVAILABLE. READ-ONLY."

func registerReplicationStatus(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetReplicationStatus,
		Description: replicationStatusDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetReplicationStatus(),
		Handler:     adapt(s.getReplicationStatus),
	})
}

func schemaGetReplicationLag() json.RawMessage {
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"target":{"type":"string","description":"Configured database target alias"}},"required":["target"],"additionalProperties":false}`)
}

const replicationLagDescription = "Return replication lag. On a primary this is the gap between the current WAL LSN and each connected standby's replay position; on a standby this is the gap between the last WAL received and replayed. When the target is not part of an active replication setup the tool reports DB_FEATURE_UNAVAILABLE. READ-ONLY."

func registerReplicationLag(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetReplicationLag,
		Description: replicationLagDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetReplicationLag(),
		Handler:     adapt(s.getReplicationLag),
	})
}
