package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agentbox/internal/domain"
	storepkg "agentbox/internal/store"

	"github.com/jackc/pgx/v5"
)

const hostCommandSelect = `
    SELECT c.id, c.organization_id, c.host_id, COALESCE(c.box_id::text, ''),
           COALESCE(c.run_id::text, ''), COALESCE(c.runtime_instance_id::text, ''),
           c.command_type, c.payload, c.idempotency_key, c.status, c.created_at
    FROM host_commands c`

const approvalSelect = `
    SELECT a.id, a.organization_id, a.box_id, a.run_id, a.tool_name,
           a.risk_level, a.status, a.requested_payload,
           COALESCE(a.decision_payload, 'null'::jsonb), a.requested_at,
           a.expires_at, a.resolved_at, COALESCE(a.resolved_by_user_id::text, '')
    FROM approvals a`

func (s *Store) CreateHostCommand(ctx context.Context, user domain.User, command domain.HostCommand) (domain.HostCommand, error) {
	if command.BoxID == "" || command.CommandType == "" || strings.TrimSpace(command.IdempotencyKey) == "" {
		return domain.HostCommand{}, fmt.Errorf("%w: user command requires box, command type, and idempotency key", storepkg.ErrInvalidState)
	}
	var result domain.HostCommand
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var organizationID, hostID string
		if err := tx.QueryRow(ctx, `
            SELECT organization_id, host_id FROM boxes
            WHERE id = $1 AND organization_id = $2
            FOR UPDATE`, command.BoxID, user.OrganizationID).Scan(&organizationID, &hostID); err != nil {
			return mapError("lock command box", err)
		}
		allowed, err := canOperateBox(ctx, tx, user, command.BoxID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("%w: box operator access required", storepkg.ErrForbidden)
		}
		if command.OrganizationID != "" && command.OrganizationID != organizationID {
			return fmt.Errorf("%w: command organization does not own box", storepkg.ErrConflict)
		}
		if command.HostID != "" && command.HostID != hostID {
			return fmt.Errorf("%w: command host does not own box", storepkg.ErrConflict)
		}
		command.OrganizationID = organizationID
		command.HostID = hostID
		existing, err := scanHostCommand(tx.QueryRow(ctx, hostCommandSelect+`
            WHERE c.host_id = $1 AND c.idempotency_key = $2`, hostID, command.IdempotencyKey))
		if err == nil {
			if existing.BoxID != command.BoxID || existing.CommandType != command.CommandType {
				return fmt.Errorf("%w: idempotency key was used for a different command", storepkg.ErrConflict)
			}
			result = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return mapError("check idempotent user command", err)
		}
		if command.CommandType == "runtime.start" {
			command, err = prepareRuntimeStartCommandTx(ctx, tx, command)
			if err != nil {
				return err
			}
		}
		result, err = insertHostCommandTx(ctx, tx, command)
		if err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, organizationID, "user", user.ID, "",
			"box.command_created", "box", command.BoxID,
			map[string]any{"commandId": result.ID, "commandType": result.CommandType}); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

func prepareRuntimeStartCommandTx(ctx context.Context, tx pgx.Tx, command domain.HostCommand) (domain.HostCommand, error) {
	if command.BoxID == "" || command.HostID == "" {
		return domain.HostCommand{}, fmt.Errorf("%w: runtime.start requires box and host ids", storepkg.ErrInvalidState)
	}
	var organizationID, hostID, runtimeType, daemonInstanceID string
	err := tx.QueryRow(ctx, `
        SELECT b.organization_id, b.host_id, b.runtime_type,
               COALESCE(h.current_daemon_instance_id::text, '')
        FROM boxes b
        JOIN hosts h ON h.id = b.host_id AND h.organization_id = b.organization_id
        WHERE b.id = $1
        FOR UPDATE OF b`, command.BoxID).Scan(
		&organizationID, &hostID, &runtimeType, &daemonInstanceID)
	if err != nil {
		return domain.HostCommand{}, mapError("load runtime start box", err)
	}
	if hostID != command.HostID {
		return domain.HostCommand{}, fmt.Errorf("%w: runtime.start host does not own box", storepkg.ErrConflict)
	}
	if command.OrganizationID != "" && command.OrganizationID != organizationID {
		return domain.HostCommand{}, fmt.Errorf("%w: runtime.start organization mismatch", storepkg.ErrConflict)
	}
	if daemonInstanceID == "" {
		return domain.HostCommand{}, fmt.Errorf("%w: host has no connected daemon", storepkg.ErrHostOffline)
	}
	command.OrganizationID = organizationID
	if command.RuntimeInstanceID == "" {
		command.RuntimeInstanceID, err = newUUIDv7()
		if err != nil {
			return domain.HostCommand{}, err
		}
	}
	snapshot, err := jsonValue(command.Payload, "{}")
	if err != nil {
		return domain.HostCommand{}, err
	}
	tag, err := tx.Exec(ctx, `
        INSERT INTO runtime_instances(
            id, organization_id, box_id, host_id, runtime_type,
            daemon_instance_id, status, config_snapshot
        ) VALUES ($1,$2,$3,$4,$5,$6,'starting',$7)
        ON CONFLICT (id) DO NOTHING`,
		command.RuntimeInstanceID, organizationID, command.BoxID, hostID,
		runtimeType, daemonInstanceID, snapshot)
	if err != nil {
		return domain.HostCommand{}, mapError("insert commanded runtime instance", err)
	}
	if tag.RowsAffected() == 0 {
		var matches bool
		err := tx.QueryRow(ctx, `
            SELECT EXISTS(
                SELECT 1 FROM runtime_instances
                WHERE id = $1 AND organization_id = $2 AND box_id = $3 AND host_id = $4
            )`, command.RuntimeInstanceID, organizationID, command.BoxID, hostID).Scan(&matches)
		if err != nil {
			return domain.HostCommand{}, mapError("validate commanded runtime instance", err)
		}
		if !matches {
			return domain.HostCommand{}, fmt.Errorf("%w: runtime instance id belongs to another box", storepkg.ErrConflict)
		}
	}
	if _, err := tx.Exec(ctx, `
        UPDATE boxes
        SET status = 'starting', updated_at = now(), last_activity_at = now(), version = version + 1
        WHERE id = $1 AND status IN ('created','idle','hibernated','error','starting')`, command.BoxID); err != nil {
		return domain.HostCommand{}, mapError("mark commanded box starting", err)
	}
	return command, nil
}

func insertHostCommandTx(ctx context.Context, tx pgx.Tx, command domain.HostCommand) (domain.HostCommand, error) {
	if command.HostID == "" || command.CommandType == "" || strings.TrimSpace(command.IdempotencyKey) == "" {
		return domain.HostCommand{}, fmt.Errorf("%w: host, command type, and idempotency key are required", storepkg.ErrInvalidState)
	}
	payload, err := jsonValue(command.Payload, "{}")
	if err != nil {
		return domain.HostCommand{}, err
	}
	if command.ID == "" {
		command.ID, err = newUUIDv7()
		if err != nil {
			return domain.HostCommand{}, err
		}
	}
	if command.Status == "" {
		command.Status = "pending"
	}
	if command.OrganizationID == "" {
		err = tx.QueryRow(ctx, `SELECT organization_id FROM hosts WHERE id = $1`, command.HostID).Scan(&command.OrganizationID)
		if err != nil {
			return domain.HostCommand{}, mapError("resolve command organization", err)
		}
	}
	tag, err := tx.Exec(ctx, `
        INSERT INTO host_commands(
            id, organization_id, host_id, box_id, run_id, runtime_instance_id,
            command_type, payload, idempotency_key, status
        ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
        ON CONFLICT (host_id, idempotency_key) DO NOTHING`,
		command.ID, command.OrganizationID, command.HostID, nullableUUID(command.BoxID),
		nullableUUID(command.RunID), nullableUUID(command.RuntimeInstanceID),
		command.CommandType, payload, command.IdempotencyKey, command.Status)
	if err != nil {
		return domain.HostCommand{}, mapError("insert host command", err)
	}
	if tag.RowsAffected() == 0 {
		result, err := scanHostCommand(tx.QueryRow(ctx, hostCommandSelect+`
            WHERE c.host_id = $1 AND c.idempotency_key = $2`, command.HostID, command.IdempotencyKey))
		if err != nil {
			return domain.HostCommand{}, mapError("get idempotent host command", err)
		}
		return result, nil
	}
	result, err := scanHostCommand(tx.QueryRow(ctx, hostCommandSelect+` WHERE c.id = $1`, command.ID))
	if err != nil {
		return domain.HostCommand{}, mapError("get inserted host command", err)
	}
	return result, nil
}

func (s *Store) PendingHostCommands(ctx context.Context, hostID string, limit int) ([]domain.HostCommand, error) {
	limit = boundedLimit(limit, 100, 500)
	rows, err := s.pool.Query(ctx, hostCommandSelect+`
        WHERE c.host_id = $1
          AND c.status IN ('pending','leased','accepted','running')
          AND c.available_at <= now()
          AND (c.status <> 'leased' OR c.lease_until IS NULL OR c.lease_until <= now())
        ORDER BY c.created_at, c.id
        LIMIT $2`, hostID, limit)
	if err != nil {
		return nil, mapError("list pending host commands", err)
	}
	defer rows.Close()
	result := make([]domain.HostCommand, 0)
	for rows.Next() {
		command, err := scanHostCommand(rows)
		if err != nil {
			return nil, mapError("scan host command", err)
		}
		result = append(result, command)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list pending host commands", err)
	}
	return result, nil
}

func (s *Store) UpdateHostCommand(ctx context.Context, commandID, status, errorCode, errorMessage string, result []byte) error {
	if commandID == "" {
		return fmt.Errorf("%w: command id is required", storepkg.ErrInvalidState)
	}
	if status == "started" {
		status = "running"
	}
	switch status {
	case "accepted", "running", "completed", "failed", "cancelled":
	default:
		return fmt.Errorf("%w: invalid command status %q", storepkg.ErrInvalidState, status)
	}
	var resultValue any
	if len(result) > 0 {
		if !json.Valid(result) {
			return fmt.Errorf("%w: command result is not valid JSON", storepkg.ErrInvalidState)
		}
		resultValue = json.RawMessage(append([]byte(nil), result...))
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		var current, commandType, boxID, runID, runtimeID, organizationID, hostID string
		err := tx.QueryRow(ctx, `
            SELECT status, command_type, COALESCE(box_id::text, ''),
                   COALESCE(run_id::text, ''), COALESCE(runtime_instance_id::text, ''),
                   organization_id, host_id
            FROM host_commands WHERE id = $1 FOR UPDATE`, commandID).Scan(
			&current, &commandType, &boxID, &runID, &runtimeID, &organizationID, &hostID)
		if err != nil {
			return mapError("lock host command", err)
		}
		if current == status {
			return nil
		}
		if !validCommandTransition(current, status) {
			return fmt.Errorf("%w: command cannot transition from %s to %s", storepkg.ErrInvalidState, current, status)
		}
		_, err = tx.Exec(ctx, `
            UPDATE host_commands
            SET status = $2,
                accepted_at = CASE WHEN $2 = 'accepted' THEN COALESCE(accepted_at, now()) ELSE accepted_at END,
                started_at = CASE WHEN $2 = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
                completed_at = CASE WHEN $2 IN ('completed','failed','cancelled') THEN COALESCE(completed_at, now()) ELSE completed_at END,
                error_code = NULLIF($3, ''), error_message = NULLIF($4, ''),
                result_json = COALESCE($5::jsonb, result_json), updated_at = now()
            WHERE id = $1`, commandID, status, errorCode, errorMessage, resultValue)
		if err != nil {
			return mapError("update host command", err)
		}

		if commandType == "runtime.approval_response" && status == "completed" {
			if runID != "" {
				if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'running', version = version + 1 WHERE id = $1 AND status = 'waiting_approval'`, runID); err != nil {
					return mapError("resume approved run", err)
				}
			}
			if boxID != "" {
				if _, err := tx.Exec(ctx, `UPDATE boxes SET status = 'running', updated_at = now(), version = version + 1 WHERE id = $1 AND status = 'waiting_approval'`, boxID); err != nil {
					return mapError("resume approved box", err)
				}
			}
		}

		if commandType == "runtime.stop" && status == "completed" && boxID != "" {
			if runtimeID != "" {
				if _, err := tx.Exec(ctx, `
                    UPDATE runtime_instances SET status = 'exited', stopped_at = COALESCE(stopped_at, now()),
                        terminal_reason = COALESCE(terminal_reason, 'hibernated'), version = version + 1
                    WHERE id = $1 AND status = 'stopping'`, runtimeID); err != nil {
					return mapError("complete hibernated runtime", err)
				}
			}
			if _, err := tx.Exec(ctx, `
                UPDATE boxes SET status = 'hibernated', updated_at = now(), version = version + 1
                WHERE id = $1 AND status = 'hibernating'`, boxID); err != nil {
				return mapError("complete hibernation", err)
			}
		}

		runFailed := false
		if status == "failed" || status == "cancelled" {
			switch commandType {
			case "runtime.start":
				if runtimeID != "" {
					if _, err := tx.Exec(ctx, `
                        UPDATE runtime_instances
                        SET status = 'exited', stopped_at = now(), terminal_reason = $2, version = version + 1
                        WHERE id = $1 AND status IN ('starting','ready','busy','stopping')`,
						runtimeID, "command_"+status); err != nil {
						return mapError("fail runtime start", err)
					}
				}
				if runID != "" {
					tag, err := tx.Exec(ctx, `
                        UPDATE runs SET status = 'failed', finished_at = now(), terminal_reason = $2,
                            error_code = NULLIF($3,''), error_message = NULLIF($4,''), version = version + 1
                        WHERE id = $1 AND status IN ('dispatching','running')`,
						runID, "command_"+status, errorCode, errorMessage)
					if err != nil {
						return mapError("fail start run", err)
					}
					runFailed = tag.RowsAffected() > 0
				}
				if boxID != "" {
					if _, err := tx.Exec(ctx, `UPDATE boxes SET status = 'error', updated_at = now(), version = version + 1 WHERE id = $1 AND status <> 'terminated'`, boxID); err != nil {
						return mapError("fail start box", err)
					}
				}
			case "runtime.prompt", "runtime.follow_up", "runtime.steer":
				if runID != "" && commandType != "runtime.steer" {
					tag, err := tx.Exec(ctx, `
                        UPDATE runs SET status = 'failed', finished_at = now(), terminal_reason = $2,
                            error_code = NULLIF($3,''), error_message = NULLIF($4,''), version = version + 1
                        WHERE id = $1 AND status = 'dispatching'`,
						runID, "command_"+status, errorCode, errorMessage)
					if err != nil {
						return mapError("fail input run", err)
					}
					runFailed = tag.RowsAffected() > 0
				}
				if boxID != "" && commandType != "runtime.steer" {
					if _, err := tx.Exec(ctx, `UPDATE boxes SET status = 'idle', updated_at = now(), version = version + 1 WHERE id = $1 AND status = 'running'`, boxID); err != nil {
						return mapError("release failed input box", err)
					}
				}
			case "runtime.stop":
				if boxID != "" {
					if _, err := tx.Exec(ctx, `UPDATE boxes SET status = 'error', updated_at = now(), version = version + 1 WHERE id = $1 AND status = 'hibernating'`, boxID); err != nil {
						return mapError("fail hibernation", err)
					}
				}
			}
		}

		if commandType == "workspace.git_diff" && (status == "completed" || status == "failed" || status == "cancelled") {
			diffStatus := "failed"
			if status == "completed" && resultValue != nil {
				diffStatus = "completed"
			}
			diffError := errorMessage
			if status == "completed" && resultValue == nil {
				diffError = "host returned no diff result"
			}
			if _, err := tx.Exec(ctx, `
                UPDATE workspace_diff_requests
                SET status = $2, result = CASE WHEN $2 = 'completed' THEN $3::jsonb ELSE NULL END,
                    error_code = NULLIF($4,''), error_message = NULLIF($5,''),
                    generated_at = now()
                WHERE command_id = $1 AND status = 'pending'`, commandID, diffStatus,
				resultValue, errorCode, diffError); err != nil {
				return mapError("project workspace diff result", err)
			}
		}

		if runFailed && boxID != "" && runID != "" {
			var scheduleID string
			scanErr := tx.QueryRow(ctx, `
                UPDATE schedule_executions SET status = 'failed', reason = $2,
                    finished_at = now(), updated_at = now()
                WHERE run_id = $1 AND status IN ('claimed','dispatched')
                RETURNING schedule_id`, runID, "command_"+status).Scan(&scheduleID)
			if scanErr != nil && !errors.Is(scanErr, pgx.ErrNoRows) {
				return mapError("fail schedule execution", scanErr)
			}
			body := "The agent run failed before it could complete."
			if errorMessage != "" {
				body += " " + errorMessage
			}
			if err := notifyBoxAudienceTx(ctx, tx, organizationID, boxID,
				"run_failed", "Run failed", body, "run-terminal:"+runID,
				runID, scheduleID, ""); err != nil {
				return err
			}
		}

		if status == "completed" || status == "failed" || status == "cancelled" {
			if err := insertAudit(ctx, tx, organizationID, "daemon", "", hostID,
				"host_command."+status, "host_command", commandID,
				map[string]any{"commandType": commandType, "boxId": boxID, "runId": runID, "errorCode": errorCode}); err != nil {
				return err
			}
		}
		return nil
	})
}

func validCommandTransition(from, to string) bool {
	if from == to {
		return true
	}
	switch from {
	case "pending", "leased":
		return to == "accepted" || to == "running" || to == "completed" || to == "failed" || to == "cancelled"
	case "accepted":
		return to == "running" || to == "completed" || to == "failed" || to == "cancelled"
	case "running":
		return to == "completed" || to == "failed" || to == "cancelled"
	default:
		return false
	}
}

func scanHostCommand(row scanner) (domain.HostCommand, error) {
	var result domain.HostCommand
	var payload []byte
	err := row.Scan(
		&result.ID, &result.OrganizationID, &result.HostID, &result.BoxID,
		&result.RunID, &result.RuntimeInstanceID, &result.CommandType,
		&payload, &result.IdempotencyKey, &result.Status, &result.CreatedAt,
	)
	if err != nil {
		return domain.HostCommand{}, err
	}
	result.Payload = json.RawMessage(append([]byte(nil), payload...))
	return result, nil
}

func (s *Store) ListApprovals(ctx context.Context, user domain.User) ([]domain.Approval, error) {
	role, err := requireMembership(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, approvalSelect+`
        JOIN boxes b ON b.id = a.box_id AND b.organization_id = a.organization_id
        WHERE a.organization_id = $1 AND a.status = 'pending'
          AND (
              $3 IN ('owner','admin') OR b.owner_user_id = $2
              OR EXISTS (
                  SELECT 1 FROM box_acl ba
                  WHERE ba.box_id = b.id AND ba.user_id = $2
                    AND ba.role IN ('owner','operator')
              )
              OR EXISTS (
                  SELECT 1 FROM box_acl ba
                  JOIN team_members tm ON tm.team_id = ba.team_id AND tm.organization_id = ba.organization_id
                  WHERE ba.box_id = b.id AND tm.user_id = $2
                    AND ba.role IN ('owner','operator')
              )
          )
        ORDER BY a.expires_at, a.id`, user.OrganizationID, user.ID, role)
	if err != nil {
		return nil, mapError("list approvals", err)
	}
	defer rows.Close()
	result := make([]domain.Approval, 0)
	for rows.Next() {
		approval, err := scanApproval(rows)
		if err != nil {
			return nil, mapError("scan approval", err)
		}
		result = append(result, approval)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list approvals", err)
	}
	return result, nil
}

func (s *Store) ResolveApproval(ctx context.Context, user domain.User, approvalID, decision string) (domain.Approval, *domain.HostCommand, error) {
	normalized, approved, err := normalizeDecision(decision)
	if err != nil {
		return domain.Approval{}, nil, err
	}
	var result domain.Approval
	var resultCommand *domain.HostCommand
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		var organizationID, boxID, runID, runtimeID, hostID, status string
		var expiresAtExpired bool
		err := tx.QueryRow(ctx, `
            SELECT a.organization_id, a.box_id, a.run_id, a.runtime_instance_id,
                   ri.host_id, a.status, a.expires_at <= now()
            FROM approvals a
            JOIN runtime_instances ri ON ri.id = a.runtime_instance_id
            WHERE a.id = $1
            FOR UPDATE OF a`, approvalID).Scan(
			&organizationID, &boxID, &runID, &runtimeID, &hostID, &status, &expiresAtExpired)
		if err != nil {
			return mapError("lock approval", err)
		}
		if organizationID != user.OrganizationID {
			return fmt.Errorf("%w: approval belongs to another organization", storepkg.ErrForbidden)
		}
		allowed, err := canOperateBox(ctx, tx, user, boxID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("%w: box operator access required", storepkg.ErrForbidden)
		}
		if status != "pending" || expiresAtExpired {
			return fmt.Errorf("%w: approval is no longer pending", storepkg.ErrConflict)
		}
		decisionPayload, err := json.Marshal(map[string]any{"decision": normalized})
		if err != nil {
			return fmt.Errorf("encode approval decision: %w", err)
		}
		tag, err := tx.Exec(ctx, `
            UPDATE approvals
            SET status = $2, decision_payload = $3, resolved_at = now(),
                resolved_by_user_id = $4, version = version + 1
            WHERE id = $1 AND status = 'pending' AND expires_at > now()`,
			approvalID, normalized, decisionPayload, user.ID)
		if err != nil {
			return mapError("resolve approval", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: approval resolution lost compare-and-set", storepkg.ErrConflict)
		}
		payload, err := json.Marshal(map[string]any{
			"approvalId": approvalID,
			"approved":   approved,
			"payload":    map[string]any{"decision": normalized},
		})
		if err != nil {
			return fmt.Errorf("encode approval command: %w", err)
		}
		command, err := insertHostCommandTx(ctx, tx, domain.HostCommand{
			OrganizationID:    organizationID,
			HostID:            hostID,
			BoxID:             boxID,
			RunID:             runID,
			RuntimeInstanceID: runtimeID,
			CommandType:       "runtime.approval_response",
			Payload:           payload,
			IdempotencyKey:    "approval:resolve:" + approvalID,
			Status:            "pending",
		})
		if err != nil {
			return err
		}
		resultCommand = &command
		if err := insertAudit(ctx, tx, organizationID, "user", user.ID, "",
			"approval.resolved", "approval", approvalID,
			map[string]any{"boxId": boxID, "runId": runID, "decision": normalized}); err != nil {
			return err
		}
		result, err = getApproval(ctx, tx, approvalID)
		return err
	})
	return result, resultCommand, err
}

func (s *Store) ExpireApprovals(ctx context.Context, limit int) ([]domain.HostCommand, error) {
	limit = boundedLimit(limit, 100, 500)
	commands := make([]domain.HostCommand, 0)
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
            SELECT id
            FROM approvals
            WHERE status = 'pending' AND expires_at <= now()
            ORDER BY expires_at, id
            FOR UPDATE SKIP LOCKED
            LIMIT $1`, limit)
		if err != nil {
			return mapError("claim expired approvals", err)
		}
		ids := make([]string, 0)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return mapError("scan expired approval", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return mapError("claim expired approvals", err)
		}
		rows.Close()

		for _, approvalID := range ids {
			var organizationID, boxID, runID, runtimeID, hostID, runtimeStatus string
			err := tx.QueryRow(ctx, `
                SELECT a.organization_id, a.box_id, a.run_id, a.runtime_instance_id,
                       ri.host_id, ri.status
                FROM approvals a
                JOIN runtime_instances ri ON ri.id = a.runtime_instance_id
                WHERE a.id = $1`, approvalID).Scan(
				&organizationID, &boxID, &runID, &runtimeID, &hostID, &runtimeStatus)
			if err != nil {
				return mapError("load expired approval", err)
			}
			tag, err := tx.Exec(ctx, `
                UPDATE approvals
                SET status = 'expired', decision_payload = '{"decision":"denied","reason":"expired"}'::jsonb,
                    resolved_at = now(), version = version + 1
                WHERE id = $1 AND status = 'pending'`, approvalID)
			if err != nil {
				return mapError("expire approval", err)
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			if runtimeStatus == "exited" {
				if _, err := tx.Exec(ctx, `
                    UPDATE runs SET status = 'failed', finished_at = now(), terminal_reason = 'approval_expired', version = version + 1
                    WHERE id = $1 AND status IN ('waiting_approval','running')`, runID); err != nil {
					return mapError("fail expired approval run", err)
				}
				if _, err := tx.Exec(ctx, `
                    UPDATE boxes SET status = 'error', updated_at = now(), version = version + 1
                    WHERE id = $1 AND status <> 'terminated'`, boxID); err != nil {
					return mapError("fail expired approval box", err)
				}
			} else {
				payload, err := json.Marshal(map[string]any{
					"approvalId": approvalID,
					"approved":   false,
					"payload":    map[string]any{"decision": "denied", "reason": "expired"},
				})
				if err != nil {
					return fmt.Errorf("encode expired approval command: %w", err)
				}
				command, err := insertHostCommandTx(ctx, tx, domain.HostCommand{
					OrganizationID:    organizationID,
					HostID:            hostID,
					BoxID:             boxID,
					RunID:             runID,
					RuntimeInstanceID: runtimeID,
					CommandType:       "runtime.approval_response",
					Payload:           payload,
					IdempotencyKey:    "approval:expire:" + approvalID,
					Status:            "pending",
				})
				if err != nil {
					return err
				}
				commands = append(commands, command)
			}
			if err := insertAudit(ctx, tx, organizationID, "system", "", "",
				"approval.expired", "approval", approvalID,
				map[string]any{"boxId": boxID, "runId": runID}); err != nil {
				return err
			}
		}
		return nil
	})
	return commands, err
}

func (s *Store) HibernateIdleBoxes(ctx context.Context, limit int) ([]domain.HostCommand, error) {
	limit = boundedLimit(limit, 100, 500)
	commands := make([]domain.HostCommand, 0)
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
            SELECT b.id
            FROM boxes b
            WHERE b.status = 'idle'
              AND b.last_activity_at + make_interval(secs => b.idle_timeout_seconds) <= now()
              AND EXISTS (
                  SELECT 1 FROM runtime_instances ri
                  WHERE ri.box_id = b.id AND ri.status IN ('ready','busy')
              )
            ORDER BY b.last_activity_at, b.id
            FOR UPDATE SKIP LOCKED
            LIMIT $1`, limit)
		if err != nil {
			return mapError("claim idle boxes", err)
		}
		ids := make([]string, 0)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return mapError("scan idle box", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return mapError("claim idle boxes", err)
		}
		rows.Close()

		for _, boxID := range ids {
			var organizationID, hostID, runtimeID string
			err := tx.QueryRow(ctx, `
                SELECT b.organization_id, b.host_id, ri.id
                FROM boxes b
                JOIN runtime_instances ri ON ri.box_id = b.id AND ri.status IN ('ready','busy')
                WHERE b.id = $1
                FOR UPDATE OF ri`, boxID).Scan(&organizationID, &hostID, &runtimeID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return mapError("load idle runtime", err)
			}
			tag, err := tx.Exec(ctx, `
                UPDATE boxes SET status = 'hibernating', updated_at = now(), version = version + 1
                WHERE id = $1 AND status = 'idle'`, boxID)
			if err != nil {
				return mapError("mark box hibernating", err)
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			if _, err := tx.Exec(ctx, `
                UPDATE runtime_instances SET status = 'stopping', version = version + 1
                WHERE id = $1 AND status IN ('ready','busy')`, runtimeID); err != nil {
				return mapError("mark runtime stopping", err)
			}
			payload, err := json.Marshal(map[string]any{"mode": "graceful"})
			if err != nil {
				return fmt.Errorf("encode hibernation command: %w", err)
			}
			command, err := insertHostCommandTx(ctx, tx, domain.HostCommand{
				OrganizationID:    organizationID,
				HostID:            hostID,
				BoxID:             boxID,
				RuntimeInstanceID: runtimeID,
				CommandType:       "runtime.stop",
				Payload:           payload,
				IdempotencyKey:    "runtime:hibernate:" + runtimeID,
				Status:            "pending",
			})
			if err != nil {
				return err
			}
			commands = append(commands, command)
			if err := insertAudit(ctx, tx, organizationID, "system", "", "",
				"box.hibernation_started", "box", boxID,
				map[string]any{"runtimeInstanceId": runtimeID}); err != nil {
				return err
			}
		}
		return nil
	})
	return commands, err
}

func normalizeDecision(decision string) (string, bool, error) {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "approve", "approved", "allow", "allowed", "yes":
		return "approved", true, nil
	case "deny", "denied", "reject", "rejected", "no":
		return "denied", false, nil
	default:
		return "", false, fmt.Errorf("%w: invalid approval decision %q", storepkg.ErrInvalidState, decision)
	}
}

func getApproval(ctx context.Context, q querier, id string) (domain.Approval, error) {
	result, err := scanApproval(q.QueryRow(ctx, approvalSelect+` WHERE a.id = $1`, id))
	if err != nil {
		return domain.Approval{}, mapError("get approval", err)
	}
	return result, nil
}

func scanApproval(row scanner) (domain.Approval, error) {
	var result domain.Approval
	var payload, decisionPayload []byte
	err := row.Scan(
		&result.ID, &result.OrganizationID, &result.BoxID, &result.RunID,
		&result.ToolName, &result.RiskLevel, &result.Status, &payload,
		&decisionPayload, &result.RequestedAt, &result.ExpiresAt,
		&result.ResolvedAt, &result.ResolvedByUserID,
	)
	if err != nil {
		return domain.Approval{}, err
	}
	result.Payload = json.RawMessage(append([]byte(nil), payload...))
	if string(decisionPayload) != "null" {
		result.DecisionPayload = json.RawMessage(append([]byte(nil), decisionPayload...))
	}
	return result, nil
}
