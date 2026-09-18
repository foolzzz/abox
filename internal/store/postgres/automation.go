package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentbox/internal/domain"
	storepkg "agentbox/internal/store"

	"github.com/jackc/pgx/v5"
)

const scheduleSelect = `
    SELECT s.id, s.organization_id, s.name, s.agent_id, s.agent_version_id,
           s.host_id, s.workspace_id, s.cron_expression, s.timezone,
           s.prompt_template, s.concurrency_policy, s.status,
           s.next_run_at, s.last_run_at, s.created_by_user_id, s.version,
           s.created_at, s.updated_at
    FROM schedules s`

const scheduleExecutionSelect = `
    SELECT se.id, se.organization_id, se.schedule_id, se.scheduled_for,
           COALESCE(se.box_id::text, ''), COALESCE(se.run_id::text, ''),
           se.status, COALESCE(se.reason, ''), se.created_at, se.updated_at,
           se.finished_at
    FROM schedule_executions se`

func (s *Store) ListSchedules(ctx context.Context, user domain.User) ([]domain.Schedule, error) {
	role, err := requireMembership(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, scheduleSelect+`
        WHERE s.organization_id = $1 AND s.status <> 'deleted'
          AND (
              $3 = 'admin'
              OR EXISTS (
                  SELECT 1 FROM workspace_acl wa
                  WHERE wa.workspace_id = s.workspace_id AND wa.user_id = $2
              )
              OR EXISTS (
                  SELECT 1 FROM workspace_acl wa
                  JOIN team_members tm ON tm.team_id = wa.team_id
                    AND tm.organization_id = wa.organization_id
                  WHERE wa.workspace_id = s.workspace_id AND tm.user_id = $2
              )
          )
        ORDER BY s.name, s.id`, user.OrganizationID, user.ID, role)
	if err != nil {
		return nil, mapError("list schedules", err)
	}
	defer rows.Close()
	result := make([]domain.Schedule, 0)
	for rows.Next() {
		schedule, err := scanSchedule(rows)
		if err != nil {
			return nil, mapError("scan schedule", err)
		}
		result = append(result, schedule)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list schedules", err)
	}
	return result, nil
}

func (s *Store) CreateSchedule(ctx context.Context, user domain.User, input domain.CreateScheduleInput) (domain.Schedule, error) {
	normalized, nextRunAt, err := normalizeScheduleInput(input, time.Now().UTC())
	if err != nil {
		return domain.Schedule{}, err
	}
	var result domain.Schedule
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		allowed, err := canOperateWorkspace(ctx, tx, user, normalized.WorkspaceID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("%w: workspace operator access required", storepkg.ErrForbidden)
		}
		agentVersionID, err := validateScheduleTargets(ctx, tx, user.OrganizationID, normalized.AgentID, normalized.HostID, normalized.WorkspaceID)
		if err != nil {
			return err
		}
		scheduleID, err := newUUIDv7()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
            INSERT INTO schedules(
                id, organization_id, name, agent_id, agent_version_id,
                host_id, workspace_id, cron_expression, timezone,
                prompt_template, concurrency_policy, status, next_run_at,
                created_by_user_id
            ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			scheduleID, user.OrganizationID, normalized.Name, normalized.AgentID,
			agentVersionID, normalized.HostID, normalized.WorkspaceID,
			normalized.CronExpression, normalized.Timezone, normalized.PromptTemplate,
			normalized.ConcurrencyPolicy, normalized.Status, nextRunAt, user.ID)
		if err != nil {
			return mapError("insert schedule", err)
		}
		if err := insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"schedule.created", "schedule", scheduleID,
			map[string]any{"workspaceId": normalized.WorkspaceID, "agentId": normalized.AgentID, "status": normalized.Status}); err != nil {
			return err
		}
		result, err = getSchedule(ctx, tx, user.OrganizationID, scheduleID)
		return err
	})
	return result, err
}

func (s *Store) UpdateSchedule(ctx context.Context, user domain.User, scheduleID string, input domain.UpdateScheduleInput) (domain.Schedule, error) {
	if scheduleID == "" {
		return domain.Schedule{}, fmt.Errorf("%w: schedule id is required", storepkg.ErrInvalidState)
	}
	var result domain.Schedule
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		current, err := scanSchedule(tx.QueryRow(ctx, scheduleSelect+`
            WHERE s.organization_id = $1 AND s.id = $2 FOR UPDATE`, user.OrganizationID, scheduleID))
		if err != nil {
			return mapError("lock schedule", err)
		}
		if current.Status == "deleted" {
			return fmt.Errorf("%w: deleted schedule cannot be updated", storepkg.ErrInvalidState)
		}
		allowed, err := canOperateWorkspace(ctx, tx, user, current.WorkspaceID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("%w: workspace operator access required", storepkg.ErrForbidden)
		}

		updated := domain.CreateScheduleInput{
			Name: current.Name, AgentID: current.AgentID, HostID: current.HostID,
			WorkspaceID: current.WorkspaceID, CronExpression: current.CronExpression,
			Timezone: current.Timezone, PromptTemplate: current.PromptTemplate,
			ConcurrencyPolicy: current.ConcurrencyPolicy, Status: current.Status,
		}
		applySchedulePatch(&updated, input)
		normalized, computedNext, err := normalizeScheduleInput(updated, time.Now().UTC())
		if err != nil {
			return err
		}
		if normalized.Status == "deleted" {
			return fmt.Errorf("%w: use DELETE to remove a schedule", storepkg.ErrInvalidState)
		}
		if normalized.WorkspaceID != current.WorkspaceID {
			allowed, err = canOperateWorkspace(ctx, tx, user, normalized.WorkspaceID)
			if err != nil {
				return err
			}
			if !allowed {
				return fmt.Errorf("%w: destination workspace operator access required", storepkg.ErrForbidden)
			}
		}
		agentVersionID, err := validateScheduleTargets(ctx, tx, user.OrganizationID, normalized.AgentID, normalized.HostID, normalized.WorkspaceID)
		if err != nil {
			return err
		}
		var nextRunAt any
		if normalized.Status == "active" {
			timingChanged := current.Status != "active" || normalized.CronExpression != current.CronExpression || normalized.Timezone != current.Timezone
			if timingChanged || current.NextRunAt == nil {
				nextRunAt = computedNext
			} else {
				nextRunAt = *current.NextRunAt
			}
		}
		_, err = tx.Exec(ctx, `
            UPDATE schedules
            SET name = $3, agent_id = $4, agent_version_id = $5,
                host_id = $6, workspace_id = $7, cron_expression = $8,
                timezone = $9, prompt_template = $10, concurrency_policy = $11,
                status = $12, next_run_at = $13, updated_at = now(), version = version + 1
            WHERE organization_id = $1 AND id = $2`,
			user.OrganizationID, scheduleID, normalized.Name, normalized.AgentID,
			agentVersionID, normalized.HostID, normalized.WorkspaceID,
			normalized.CronExpression, normalized.Timezone, normalized.PromptTemplate,
			normalized.ConcurrencyPolicy, normalized.Status, nextRunAt)
		if err != nil {
			return mapError("update schedule", err)
		}
		if err := insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"schedule.updated", "schedule", scheduleID,
			map[string]any{"previousVersion": current.Version, "status": normalized.Status}); err != nil {
			return err
		}
		result, err = getSchedule(ctx, tx, user.OrganizationID, scheduleID)
		return err
	})
	return result, err
}

func (s *Store) DeleteSchedule(ctx context.Context, user domain.User, scheduleID string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		current, err := scanSchedule(tx.QueryRow(ctx, scheduleSelect+`
            WHERE s.organization_id = $1 AND s.id = $2 FOR UPDATE`, user.OrganizationID, scheduleID))
		if err != nil {
			return mapError("lock schedule for deletion", err)
		}
		allowed, err := canOperateWorkspace(ctx, tx, user, current.WorkspaceID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("%w: workspace operator access required", storepkg.ErrForbidden)
		}
		if current.Status != "deleted" {
			if _, err := tx.Exec(ctx, `
                UPDATE schedules SET status = 'deleted', next_run_at = NULL,
                    updated_at = now(), version = version + 1
                WHERE organization_id = $1 AND id = $2`, user.OrganizationID, scheduleID); err != nil {
				return mapError("delete schedule", err)
			}
		}
		return insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"schedule.deleted", "schedule", scheduleID, map[string]any{"previousStatus": current.Status})
	})
}

func (s *Store) ListScheduleExecutions(ctx context.Context, user domain.User, scheduleID string, limit int) ([]domain.ScheduleExecution, error) {
	if err := requireScheduleRead(ctx, s.pool, user, scheduleID); err != nil {
		return nil, err
	}
	limit = boundedLimit(limit, 100, 500)
	rows, err := s.pool.Query(ctx, scheduleExecutionSelect+`
        WHERE se.organization_id = $1 AND se.schedule_id = $2
        ORDER BY se.scheduled_for DESC, se.id DESC LIMIT $3`, user.OrganizationID, scheduleID, limit)
	if err != nil {
		return nil, mapError("list schedule executions", err)
	}
	defer rows.Close()
	result := make([]domain.ScheduleExecution, 0)
	for rows.Next() {
		execution, err := scanScheduleExecution(rows)
		if err != nil {
			return nil, mapError("scan schedule execution", err)
		}
		result = append(result, execution)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list schedule executions", err)
	}
	return result, nil
}

func (s *Store) TriggerScheduleWebhook(ctx context.Context, scheduleID, idempotencyKey, bodySHA256 string) (domain.ScheduleExecution, []domain.HostCommand, error) {
	if scheduleID == "" || strings.TrimSpace(idempotencyKey) == "" || len(bodySHA256) != 64 {
		return domain.ScheduleExecution{}, nil, fmt.Errorf("%w: schedule, idempotency key, and body digest are required", storepkg.ErrInvalidState)
	}
	var result domain.ScheduleExecution
	commands := make([]domain.HostCommand, 0)
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		schedule, err := scanSchedule(tx.QueryRow(ctx, scheduleSelect+` WHERE s.id = $1 FOR UPDATE`, scheduleID))
		if err != nil {
			return mapError("lock webhook schedule", err)
		}
		if schedule.Status != "active" {
			return fmt.Errorf("%w: schedule is not active", storepkg.ErrInvalidState)
		}
		existing, err := scanScheduleExecution(tx.QueryRow(ctx, scheduleExecutionSelect+`
            JOIN webhook_deliveries wd ON wd.execution_id = se.id
            WHERE wd.schedule_id = $1 AND wd.idempotency_key = $2`, scheduleID, idempotencyKey))
		if err == nil {
			result = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return mapError("find webhook delivery", err)
		}
		execution, stopCommands, err := createAutomationExecutionTx(ctx, tx, schedule, time.Now().UTC(), "webhook", idempotencyKey)
		if err != nil {
			return err
		}
		commands = append(commands, stopCommands...)
		deliveryID, err := newUUIDv7()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO webhook_deliveries(
                id, organization_id, schedule_id, execution_id,
                idempotency_key, body_sha256
            ) VALUES ($1,$2,$3,$4,$5,$6)`, deliveryID, schedule.OrganizationID,
			schedule.ID, execution.ID, idempotencyKey, strings.ToLower(bodySHA256)); err != nil {
			return mapError("insert webhook delivery", err)
		}
		if err := insertAudit(ctx, tx, schedule.OrganizationID, "system", "", "",
			"schedule.webhook_triggered", "schedule", schedule.ID,
			map[string]any{"executionId": execution.ID, "idempotencyKey": idempotencyKey}); err != nil {
			return err
		}
		result = execution
		return nil
	})
	return result, commands, err
}

func (s *Store) ProcessDueSchedules(ctx context.Context, limit int) ([]domain.HostCommand, error) {
	limit = boundedLimit(limit, 100, 500)
	commands := make([]domain.HostCommand, 0)
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
            SELECT id FROM schedules
            WHERE status = 'active' AND next_run_at <= now()
            ORDER BY next_run_at, id
            FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
		if err != nil {
			return mapError("claim due schedules", err)
		}
		ids := make([]string, 0)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return mapError("scan due schedule", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return mapError("claim due schedules", err)
		}
		rows.Close()

		for _, id := range ids {
			schedule, err := getSchedule(ctx, tx, "", id)
			if err != nil {
				return err
			}
			if schedule.NextRunAt == nil || schedule.Status != "active" {
				continue
			}
			parsed, err := domain.ParseCronExpression(schedule.CronExpression)
			if err != nil {
				return fmt.Errorf("stored schedule %s has invalid cron: %w", schedule.ID, err)
			}
			location, err := time.LoadLocation(schedule.Timezone)
			if err != nil {
				return fmt.Errorf("stored schedule %s has invalid timezone: %w", schedule.ID, err)
			}
			scheduledFor := schedule.NextRunAt.UTC()
			nextRunAt, err := parsed.Next(scheduledFor, location)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
                UPDATE schedules SET last_run_at = $2, next_run_at = $3,
                    updated_at = now(), version = version + 1
                WHERE id = $1`, schedule.ID, scheduledFor, nextRunAt); err != nil {
				return mapError("advance schedule", err)
			}
			execution, stopCommands, err := createAutomationExecutionTx(ctx, tx, schedule, scheduledFor, "schedule", scheduledFor.Format(time.RFC3339Nano))
			if err != nil {
				return err
			}
			commands = append(commands, stopCommands...)
			if err := insertAudit(ctx, tx, schedule.OrganizationID, "system", "", "",
				"schedule.due_processed", "schedule", schedule.ID,
				map[string]any{"executionId": execution.ID, "scheduledFor": scheduledFor, "status": execution.Status}); err != nil {
				return err
			}
		}
		return nil
	})
	return commands, err
}

func (s *Store) DispatchAutomationRuns(ctx context.Context, limit int) ([]domain.HostCommand, error) {
	limit = boundedLimit(limit, 100, 500)
	commands := make([]domain.HostCommand, 0)
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
            SELECT se.id
            FROM schedule_executions se
            JOIN runs r ON r.id = se.run_id AND r.status = 'queued'
            JOIN boxes b ON b.id = se.box_id
            JOIN hosts h ON h.id = b.host_id
            JOIN workspaces w ON w.id = b.workspace_id
            WHERE se.status = 'claimed'
              AND h.status = 'online' AND h.current_daemon_instance_id IS NOT NULL
              AND w.status = 'ready'
              AND NOT EXISTS (
                  SELECT 1 FROM schedule_executions active
                  WHERE active.schedule_id = se.schedule_id AND active.status = 'dispatched'
              )
              AND NOT EXISTS (
                  SELECT 1 FROM schedule_executions earlier
                  WHERE earlier.schedule_id = se.schedule_id AND earlier.status = 'claimed'
                    AND (earlier.scheduled_for, earlier.id) < (se.scheduled_for, se.id)
              )
            ORDER BY se.scheduled_for, se.id
            FOR UPDATE OF se SKIP LOCKED LIMIT $1`, limit)
		if err != nil {
			return mapError("claim automation runs", err)
		}
		ids := make([]string, 0)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return mapError("scan automation run", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return mapError("claim automation runs", err)
		}
		rows.Close()
		for _, executionID := range ids {
			var boxID, organizationID, scheduleID string
			if err := tx.QueryRow(ctx, `
                SELECT organization_id, schedule_id, box_id
                FROM schedule_executions WHERE id = $1 FOR UPDATE`, executionID).Scan(
				&organizationID, &scheduleID, &boxID); err != nil {
				return mapError("lock automation execution", err)
			}
			run, command, found, err := claimNextRunTx(ctx, tx, boxID)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			if _, err := tx.Exec(ctx, `
                UPDATE schedule_executions SET status = 'dispatched', updated_at = now()
                WHERE id = $1 AND status = 'claimed'`, executionID); err != nil {
				return mapError("mark automation dispatched", err)
			}
			if err := insertAudit(ctx, tx, organizationID, "system", "", "",
				"schedule.execution_dispatched", "schedule", scheduleID,
				map[string]any{"executionId": executionID, "boxId": boxID, "runId": run.ID, "commandId": command.ID}); err != nil {
				return err
			}
			commands = append(commands, command)
		}
		return nil
	})
	return commands, err
}

func createAutomationExecutionTx(ctx context.Context, tx pgx.Tx, schedule domain.Schedule, scheduledFor time.Time, triggerType, triggerKey string) (domain.ScheduleExecution, []domain.HostCommand, error) {
	executionID, err := newUUIDv7()
	if err != nil {
		return domain.ScheduleExecution{}, nil, err
	}
	_, err = tx.Exec(ctx, `
        INSERT INTO schedule_executions(
            id, organization_id, schedule_id, scheduled_for, status,
            trigger_type, trigger_key
        ) VALUES ($1,$2,$3,$4,'claimed',$5,$6)`, executionID, schedule.OrganizationID,
		schedule.ID, scheduledFor, triggerType, nullableText(triggerKey))
	if err != nil {
		return domain.ScheduleExecution{}, nil, mapError("insert schedule execution", err)
	}

	var activeCount int
	if err := tx.QueryRow(ctx, `
        SELECT count(*) FROM schedule_executions
        WHERE schedule_id = $1 AND id <> $2 AND status IN ('claimed','dispatched')`,
		schedule.ID, executionID).Scan(&activeCount); err != nil {
		return domain.ScheduleExecution{}, nil, mapError("count active schedule executions", err)
	}
	if activeCount > 0 && schedule.ConcurrencyPolicy == "skip" {
		if _, err := tx.Exec(ctx, `
            UPDATE schedule_executions SET status = 'skipped', reason = 'concurrency_policy',
                finished_at = now(), updated_at = now() WHERE id = $1`, executionID); err != nil {
			return domain.ScheduleExecution{}, nil, mapError("skip concurrent schedule execution", err)
		}
		execution, err := getScheduleExecution(ctx, tx, executionID)
		return execution, nil, err
	}

	stopCommands := make([]domain.HostCommand, 0)
	if activeCount > 0 && schedule.ConcurrencyPolicy == "replace" {
		rows, err := tx.Query(ctx, `
            SELECT se.id, COALESCE(se.box_id::text, ''), COALESCE(se.run_id::text, ''),
                   COALESCE(r.runtime_instance_id::text, ''), COALESCE(b.host_id::text, '')
            FROM schedule_executions se
            LEFT JOIN runs r ON r.id = se.run_id
            LEFT JOIN boxes b ON b.id = se.box_id
            WHERE se.schedule_id = $1 AND se.id <> $2
              AND se.status IN ('claimed','dispatched')
            FOR UPDATE OF se`, schedule.ID, executionID)
		if err != nil {
			return domain.ScheduleExecution{}, nil, mapError("lock replaced executions", err)
		}
		type replacedExecution struct{ id, boxID, runID, runtimeID, hostID string }
		replaced := make([]replacedExecution, 0)
		for rows.Next() {
			var value replacedExecution
			if err := rows.Scan(&value.id, &value.boxID, &value.runID, &value.runtimeID, &value.hostID); err != nil {
				rows.Close()
				return domain.ScheduleExecution{}, nil, mapError("scan replaced execution", err)
			}
			replaced = append(replaced, value)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return domain.ScheduleExecution{}, nil, mapError("scan replaced executions", err)
		}
		rows.Close()
		for _, old := range replaced {
			if old.runID != "" {
				if _, err := tx.Exec(ctx, `
                    UPDATE runs SET status = 'cancelled', finished_at = now(),
                        terminal_reason = 'schedule_replaced', version = version + 1
                    WHERE id = $1 AND status IN ('queued','dispatching','running','waiting_approval','interrupting','disconnected')`, old.runID); err != nil {
					return domain.ScheduleExecution{}, nil, mapError("cancel replaced run", err)
				}
				if _, err := tx.Exec(ctx, `UPDATE messages SET status = 'cancelled' WHERE run_id = $1 AND status IN ('queued','dispatched')`, old.runID); err != nil {
					return domain.ScheduleExecution{}, nil, mapError("cancel replaced message", err)
				}
			}
			if _, err := tx.Exec(ctx, `
                UPDATE schedule_executions SET status = 'failed', reason = 'replaced',
                    finished_at = now(), updated_at = now() WHERE id = $1`, old.id); err != nil {
				return domain.ScheduleExecution{}, nil, mapError("replace schedule execution", err)
			}
			if old.runtimeID != "" && old.hostID != "" && old.boxID != "" {
				if _, err := tx.Exec(ctx, `UPDATE runtime_instances SET status = 'stopping', version = version + 1 WHERE id = $1 AND status IN ('starting','ready','busy')`, old.runtimeID); err != nil {
					return domain.ScheduleExecution{}, nil, mapError("stop replaced runtime", err)
				}
				if _, err := tx.Exec(ctx, `UPDATE boxes SET status = 'hibernating', updated_at = now(), version = version + 1 WHERE id = $1 AND status <> 'terminated'`, old.boxID); err != nil {
					return domain.ScheduleExecution{}, nil, mapError("hibernate replaced box", err)
				}
				payload := json.RawMessage(`{"mode":"graceful"}`)
				command, err := insertHostCommandTx(ctx, tx, domain.HostCommand{
					OrganizationID: schedule.OrganizationID, HostID: old.hostID, BoxID: old.boxID,
					RunID: old.runID, RuntimeInstanceID: old.runtimeID, CommandType: "runtime.stop",
					Payload: payload, IdempotencyKey: "schedule:replace:" + old.id, Status: "pending",
				})
				if err != nil {
					return domain.ScheduleExecution{}, nil, err
				}
				stopCommands = append(stopCommands, command)
			}
		}
	}

	boxID, err := newUUIDv7()
	if err != nil {
		return domain.ScheduleExecution{}, nil, err
	}
	boxName := schedule.Name + " · " + scheduledFor.In(time.UTC).Format("2006-01-02 15:04 UTC")
	var runtimeType string
	var idleTimeout int
	if err := tx.QueryRow(ctx, `SELECT runtime_type, idle_timeout_seconds FROM agent_versions WHERE id = $1`, schedule.AgentVersionID).Scan(&runtimeType, &idleTimeout); err != nil {
		return domain.ScheduleExecution{}, nil, mapError("load scheduled agent version", err)
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO boxes(
            id, organization_id, name, agent_id, agent_version_id, host_id,
            workspace_id, owner_user_id, visibility, status, runtime_type,
            idle_timeout_seconds, next_message_seq
        ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'private','created',$9,$10,2)`,
		boxID, schedule.OrganizationID, boxName, schedule.AgentID, schedule.AgentVersionID,
		schedule.HostID, schedule.WorkspaceID, schedule.CreatedByUserID, runtimeType, idleTimeout); err != nil {
		return domain.ScheduleExecution{}, nil, mapError("insert scheduled box", err)
	}

	messageID, err := newUUIDv7()
	if err != nil {
		return domain.ScheduleExecution{}, nil, err
	}
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": schedule.PromptTemplate}})
	if err != nil {
		return domain.ScheduleExecution{}, nil, fmt.Errorf("encode scheduled prompt: %w", err)
	}
	authorType := "scheduler"
	if triggerType == "webhook" {
		authorType = "webhook"
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO messages(
            id, organization_id, box_id, box_seq, author_type, role,
            delivery, status, content, plain_text, idempotency_key
        ) VALUES ($1,$2,$3,1,$4,'user','prompt','queued',$5,$6,$7)`,
		messageID, schedule.OrganizationID, boxID, authorType, content,
		schedule.PromptTemplate, "automation:"+executionID); err != nil {
		return domain.ScheduleExecution{}, nil, mapError("insert scheduled message", err)
	}
	runID, err := newUUIDv7()
	if err != nil {
		return domain.ScheduleExecution{}, nil, err
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO runs(
            id, organization_id, box_id, trigger_message_id, source_type,
            source_ref_id, status, priority
        ) VALUES ($1,$2,$3,$4,$5,$6,'queued',100)`, runID, schedule.OrganizationID,
		boxID, messageID, triggerType, schedule.ID); err != nil {
		return domain.ScheduleExecution{}, nil, mapError("insert scheduled run", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE messages SET run_id = $2 WHERE id = $1`, messageID, runID); err != nil {
		return domain.ScheduleExecution{}, nil, mapError("attach scheduled run", err)
	}
	if _, err := tx.Exec(ctx, `
        UPDATE schedule_executions SET box_id = $2, run_id = $3, updated_at = now()
        WHERE id = $1`, executionID, boxID, runID); err != nil {
		return domain.ScheduleExecution{}, nil, mapError("attach schedule execution", err)
	}
	if err := insertAudit(ctx, tx, schedule.OrganizationID, "system", "", "",
		"schedule.execution_queued", "schedule", schedule.ID,
		map[string]any{"executionId": executionID, "boxId": boxID, "runId": runID, "triggerType": triggerType}); err != nil {
		return domain.ScheduleExecution{}, nil, err
	}
	execution, err := getScheduleExecution(ctx, tx, executionID)
	return execution, stopCommands, err
}

func normalizeScheduleInput(input domain.CreateScheduleInput, after time.Time) (domain.CreateScheduleInput, any, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.HostID = strings.TrimSpace(input.HostID)
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	input.CronExpression = strings.TrimSpace(input.CronExpression)
	input.Timezone = strings.TrimSpace(input.Timezone)
	input.PromptTemplate = strings.TrimSpace(input.PromptTemplate)
	input.ConcurrencyPolicy = strings.ToLower(strings.TrimSpace(input.ConcurrencyPolicy))
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	if input.Status == "" {
		input.Status = "active"
	}
	if input.Timezone == "" {
		input.Timezone = "UTC"
	}
	if input.ConcurrencyPolicy == "" {
		input.ConcurrencyPolicy = "skip"
	}
	if input.Name == "" || input.AgentID == "" || input.HostID == "" || input.WorkspaceID == "" || input.CronExpression == "" || input.PromptTemplate == "" {
		return input, nil, fmt.Errorf("%w: name, agent, host, workspace, cron expression, and prompt are required", storepkg.ErrInvalidState)
	}
	if input.Status != "active" && input.Status != "paused" {
		return input, nil, fmt.Errorf("%w: schedule status must be active or paused", storepkg.ErrInvalidState)
	}
	switch input.ConcurrencyPolicy {
	case "skip", "queue", "replace":
	default:
		return input, nil, fmt.Errorf("%w: concurrency policy must be skip, queue, or replace", storepkg.ErrInvalidState)
	}
	parsed, err := domain.ParseCronExpression(input.CronExpression)
	if err != nil {
		return input, nil, fmt.Errorf("%w: %v", storepkg.ErrInvalidState, err)
	}
	location, err := time.LoadLocation(input.Timezone)
	if err != nil {
		return input, nil, fmt.Errorf("%w: invalid timezone %q", storepkg.ErrInvalidState, input.Timezone)
	}
	next, err := parsed.Next(after, location)
	if err != nil {
		return input, nil, fmt.Errorf("%w: %v", storepkg.ErrInvalidState, err)
	}
	if input.Status == "paused" {
		return input, nil, nil
	}
	return input, next, nil
}

func applySchedulePatch(target *domain.CreateScheduleInput, patch domain.UpdateScheduleInput) {
	if patch.Name != nil {
		target.Name = *patch.Name
	}
	if patch.AgentID != nil {
		target.AgentID = *patch.AgentID
	}
	if patch.HostID != nil {
		target.HostID = *patch.HostID
	}
	if patch.WorkspaceID != nil {
		target.WorkspaceID = *patch.WorkspaceID
	}
	if patch.CronExpression != nil {
		target.CronExpression = *patch.CronExpression
	}
	if patch.Timezone != nil {
		target.Timezone = *patch.Timezone
	}
	if patch.PromptTemplate != nil {
		target.PromptTemplate = *patch.PromptTemplate
	}
	if patch.ConcurrencyPolicy != nil {
		target.ConcurrencyPolicy = *patch.ConcurrencyPolicy
	}
	if patch.Status != nil {
		target.Status = *patch.Status
	}
}

func validateScheduleTargets(ctx context.Context, q querier, organizationID, agentID, hostID, workspaceID string) (string, error) {
	var agentVersionID string
	err := q.QueryRow(ctx, `
        SELECT av.id
        FROM agents a
        JOIN agent_versions av ON av.id = a.published_version_id AND av.agent_id = a.id
        JOIN workspaces w ON w.organization_id = a.organization_id AND w.id = $4
        JOIN hosts h ON h.organization_id = a.organization_id AND h.id = $3
        WHERE a.organization_id = $1 AND a.id = $2 AND a.status = 'active'
          AND av.lifecycle_status = 'published'
          AND w.host_id = h.id AND w.status = 'ready' AND h.status <> 'revoked'`,
		organizationID, agentID, hostID, workspaceID).Scan(&agentVersionID)
	if err != nil {
		return "", mapError("validate schedule targets", err)
	}
	return agentVersionID, nil
}

func requireScheduleRead(ctx context.Context, q querier, user domain.User, scheduleID string) error {
	var workspaceID string
	err := q.QueryRow(ctx, `SELECT workspace_id FROM schedules WHERE organization_id = $1 AND id = $2`, user.OrganizationID, scheduleID).Scan(&workspaceID)
	if err != nil {
		return mapError("get schedule access target", err)
	}
	allowed, err := canReadWorkspace(ctx, q, user, workspaceID)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w: schedule workspace read access required", storepkg.ErrForbidden)
	}
	return nil
}

func getSchedule(ctx context.Context, q querier, organizationID, scheduleID string) (domain.Schedule, error) {
	query := scheduleSelect + ` WHERE s.id = $1`
	args := []any{scheduleID}
	if organizationID != "" {
		query = scheduleSelect + ` WHERE s.organization_id = $1 AND s.id = $2`
		args = []any{organizationID, scheduleID}
	}
	result, err := scanSchedule(q.QueryRow(ctx, query, args...))
	if err != nil {
		return domain.Schedule{}, mapError("get schedule", err)
	}
	return result, nil
}

func scanSchedule(row scanner) (domain.Schedule, error) {
	var result domain.Schedule
	err := row.Scan(&result.ID, &result.OrganizationID, &result.Name, &result.AgentID,
		&result.AgentVersionID, &result.HostID, &result.WorkspaceID,
		&result.CronExpression, &result.Timezone, &result.PromptTemplate,
		&result.ConcurrencyPolicy, &result.Status, &result.NextRunAt, &result.LastRunAt,
		&result.CreatedByUserID, &result.Version, &result.CreatedAt, &result.UpdatedAt)
	return result, err
}

func getScheduleExecution(ctx context.Context, q querier, executionID string) (domain.ScheduleExecution, error) {
	result, err := scanScheduleExecution(q.QueryRow(ctx, scheduleExecutionSelect+` WHERE se.id = $1`, executionID))
	if err != nil {
		return domain.ScheduleExecution{}, mapError("get schedule execution", err)
	}
	return result, nil
}

func scanScheduleExecution(row scanner) (domain.ScheduleExecution, error) {
	var result domain.ScheduleExecution
	err := row.Scan(&result.ID, &result.OrganizationID, &result.ScheduleID,
		&result.ScheduledFor, &result.BoxID, &result.RunID, &result.Status,
		&result.Reason, &result.CreatedAt, &result.UpdatedAt, &result.FinishedAt)
	return result, err
}
