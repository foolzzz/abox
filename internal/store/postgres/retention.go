package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Store) PruneOperationalData(ctx context.Context, operationalBefore, auditBefore time.Time, limit int) (int64, error) {
	limit = boundedLimit(limit, 100, 10_000)
	var removed int64
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		statements := []struct {
			name   string
			query  string
			before time.Time
		}{
			{
				name: "read notifications",
				query: `WITH doomed AS (
                    SELECT id FROM notifications
                    WHERE status = 'read' AND created_at < $1
                    ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED
                ) DELETE FROM notifications WHERE id IN (SELECT id FROM doomed)`,
				before: operationalBefore,
			},
			{
				name: "workspace diff requests",
				query: `WITH doomed AS (
                    SELECT id FROM workspace_diff_requests
                    WHERE status IN ('completed','failed') AND created_at < $1
                    ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED
                ) DELETE FROM workspace_diff_requests WHERE id IN (SELECT id FROM doomed)`,
				before: operationalBefore,
			},
			{
				name: "schedule executions",
				query: `WITH doomed AS (
                    SELECT id FROM schedule_executions
                    WHERE status IN ('completed','failed','skipped') AND updated_at < $1
                      AND NOT EXISTS (SELECT 1 FROM webhook_deliveries w WHERE w.execution_id = schedule_executions.id)
                    ORDER BY updated_at LIMIT $2 FOR UPDATE SKIP LOCKED
                ) DELETE FROM schedule_executions WHERE id IN (SELECT id FROM doomed)`,
				before: operationalBefore,
			},
			{
				name: "resolved approvals",
				query: `WITH doomed AS (
                    SELECT id FROM approvals
                    WHERE status IN ('approved','denied','expired','cancelled') AND resolved_at < $1
                      AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.approval_id = approvals.id)
                    ORDER BY resolved_at LIMIT $2 FOR UPDATE SKIP LOCKED
                ) DELETE FROM approvals WHERE id IN (SELECT id FROM doomed)`,
				before: operationalBefore,
			},
			{
				name: "terminal host commands",
				query: `WITH doomed AS (
                    SELECT id FROM host_commands
                    WHERE status IN ('completed','failed','cancelled') AND updated_at < $1
                      AND NOT EXISTS (SELECT 1 FROM workspace_diff_requests d WHERE d.command_id = host_commands.id)
                    ORDER BY updated_at LIMIT $2 FOR UPDATE SKIP LOCKED
                ) DELETE FROM host_commands WHERE id IN (SELECT id FROM doomed)`,
				before: operationalBefore,
			},
			{
				name: "audit logs",
				query: `WITH doomed AS (
                    SELECT id FROM audit_logs
                    WHERE occurred_at < $1
                    ORDER BY occurred_at LIMIT $2 FOR UPDATE SKIP LOCKED
                ) DELETE FROM audit_logs WHERE id IN (SELECT id FROM doomed)`,
				before: auditBefore,
			},
		}
		for _, statement := range statements {
			tag, err := tx.Exec(ctx, statement.query, statement.before, limit)
			if err != nil {
				return mapError("prune "+statement.name, err)
			}
			removed += tag.RowsAffected()
		}
		return nil
	})
	return removed, err
}
