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

const messageSelect = `
    SELECT m.id, m.organization_id, m.box_id, COALESCE(m.run_id::text, ''),
           m.box_seq, m.author_type, COALESCE(m.author_user_id::text, ''),
           CASE WHEN m.author_type = 'user' THEN COALESCE(u.display_name, '') ELSE '' END,
           m.role, COALESCE(m.delivery, ''), m.status, m.content,
           COALESCE(m.plain_text, ''), m.created_at
    FROM messages m
    LEFT JOIN users u ON u.id = m.author_user_id`

const runSelect = `
    SELECT r.id, r.organization_id, r.box_id, COALESCE(r.runtime_instance_id::text, ''),
           r.trigger_message_id, r.status, r.queued_at, r.started_at, r.finished_at,
           COALESCE(r.terminal_reason, ''), COALESCE(r.error_message, '')
    FROM runs r`

func (s *Store) ListMessages(ctx context.Context, user domain.User, boxID string, limit int) ([]domain.Message, error) {
	allowed, err := canReadBox(ctx, s.pool, user, boxID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: box read access required", storepkg.ErrForbidden)
	}
	limit = boundedLimit(limit, 100, 500)
	rows, err := s.pool.Query(ctx, messageSelect+`
        WHERE m.organization_id = $1 AND m.box_id = $2
        ORDER BY m.box_seq DESC
        LIMIT $3`, user.OrganizationID, boxID, limit)
	if err != nil {
		return nil, mapError("list messages", err)
	}
	defer rows.Close()
	reverse := make([]domain.Message, 0)
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, mapError("scan message", err)
		}
		reverse = append(reverse, message)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list messages", err)
	}
	result := make([]domain.Message, len(reverse))
	for i := range reverse {
		result[len(reverse)-1-i] = reverse[i]
	}
	return result, nil
}

func (s *Store) SendMessage(ctx context.Context, user domain.User, boxID string, input domain.SendMessageInput) (domain.Message, *domain.Run, *domain.HostCommand, error) {
	if boxID == "" || strings.TrimSpace(input.Content) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		return domain.Message{}, nil, nil, fmt.Errorf("%w: box, content, and idempotency key are required", storepkg.ErrInvalidState)
	}
	if input.Delivery == "" {
		input.Delivery = domain.DeliveryPrompt
	}
	if input.Delivery != domain.DeliveryPrompt && input.Delivery != domain.DeliverySteer && input.Delivery != domain.DeliveryFollowUp {
		return domain.Message{}, nil, nil, fmt.Errorf("%w: invalid message delivery %q", storepkg.ErrInvalidState, input.Delivery)
	}
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": input.Content}})
	if err != nil {
		return domain.Message{}, nil, nil, fmt.Errorf("encode message content: %w", err)
	}

	var message domain.Message
	var run *domain.Run
	var command *domain.HostCommand
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		var organizationID, hostID, workspaceID, boxStatus, hostStatus, workspaceStatus string
		err := tx.QueryRow(ctx, `
            SELECT b.organization_id, b.host_id, b.workspace_id, b.status,
                   h.status, w.status
            FROM boxes b
            JOIN hosts h ON h.id = b.host_id AND h.organization_id = b.organization_id
            JOIN workspaces w ON w.id = b.workspace_id AND w.organization_id = b.organization_id
            WHERE b.id = $1 AND b.organization_id = $2
            FOR UPDATE OF b`, boxID, user.OrganizationID).Scan(
			&organizationID, &hostID, &workspaceID, &boxStatus, &hostStatus, &workspaceStatus,
		)
		if err != nil {
			return mapError("lock message box", err)
		}
		allowed, err := canOperateBox(ctx, tx, user, boxID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("%w: box operator access required", storepkg.ErrForbidden)
		}

		existing, existingRun, existingCommand, found, err := findIdempotentMessage(ctx, tx, boxID, input.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			message, run, command = existing, existingRun, existingCommand
			return nil
		}
		if boxStatus == string(domain.BoxTerminated) || boxStatus == string(domain.BoxError) || boxStatus == string(domain.BoxHibernating) {
			return fmt.Errorf("%w: box is %s", storepkg.ErrInvalidState, boxStatus)
		}
		if hostStatus != string(domain.HostOnline) {
			return fmt.Errorf("%w: host is %s", storepkg.ErrHostOffline, hostStatus)
		}
		if workspaceStatus != "ready" {
			return fmt.Errorf("%w: workspace is %s", storepkg.ErrInvalidState, workspaceStatus)
		}

		messageID, err := newUUIDv7()
		if err != nil {
			return err
		}
		var boxSeq int64
		err = tx.QueryRow(ctx, `
            UPDATE boxes
            SET next_message_seq = next_message_seq + 1,
                last_activity_at = now(), updated_at = now(), version = version + 1
            WHERE id = $1
            RETURNING next_message_seq - 1`, boxID).Scan(&boxSeq)
		if err != nil {
			return mapError("allocate message sequence", err)
		}

		var activeRunID, activeRuntimeID, activeRunStatus string
		err = tx.QueryRow(ctx, `
            SELECT id, COALESCE(runtime_instance_id::text, ''), status
            FROM runs
            WHERE box_id = $1
              AND status IN ('dispatching','running','waiting_approval','interrupting','disconnected')
            FOR UPDATE`, boxID).Scan(&activeRunID, &activeRuntimeID, &activeRunStatus)
		hasActiveRun := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return mapError("find active run", err)
		}
		switch input.Delivery {
		case domain.DeliveryPrompt:
			if hasActiveRun {
				return fmt.Errorf("%w: prompt requires no active run", storepkg.ErrInvalidState)
			}
			if boxStatus != string(domain.BoxCreated) && boxStatus != string(domain.BoxIdle) && boxStatus != string(domain.BoxHibernated) {
				return fmt.Errorf("%w: prompt is not allowed while box is %s", storepkg.ErrInvalidState, boxStatus)
			}
		case domain.DeliverySteer:
			if !hasActiveRun || activeRunStatus != string(domain.RunRunning) || boxStatus != string(domain.BoxRunning) {
				return fmt.Errorf("%w: steer requires a running run", storepkg.ErrInvalidState)
			}
			if activeRuntimeID == "" {
				return fmt.Errorf("%w: active run has no runtime", storepkg.ErrRuntimeMissing)
			}
		case domain.DeliveryFollowUp:
			if !hasActiveRun {
				return fmt.Errorf("%w: follow_up requires an active run", storepkg.ErrInvalidState)
			}
		}

		initialStatus := "queued"
		if input.Delivery != domain.DeliveryFollowUp {
			initialStatus = "dispatched"
		}
		_, err = tx.Exec(ctx, `
            INSERT INTO messages(
                id, organization_id, box_id, box_seq, author_type, author_user_id,
                role, delivery, status, content, plain_text, idempotency_key
            ) VALUES ($1,$2,$3,$4,'user',$5,'user',$6,$7,$8,$9,$10)`,
			messageID, organizationID, boxID, boxSeq, user.ID, string(input.Delivery),
			initialStatus, content, input.Content, input.IdempotencyKey,
		)
		if err != nil {
			return mapError("insert message", err)
		}

		if input.Delivery == domain.DeliverySteer {
			if _, err := tx.Exec(ctx, `UPDATE messages SET run_id = $2 WHERE id = $1`, messageID, activeRunID); err != nil {
				return mapError("attach steer message", err)
			}
			commandValue, err := createInputCommandTx(ctx, tx, organizationID, hostID, boxID,
				activeRunID, activeRuntimeID, messageID, input.Delivery, input.Content)
			if err != nil {
				return err
			}
			command = &commandValue
			runValue, err := getRun(ctx, tx, activeRunID)
			if err != nil {
				return err
			}
			run = &runValue
		} else {
			runID, err := newUUIDv7()
			if err != nil {
				return err
			}
			runStatus := domain.RunQueued
			if input.Delivery == domain.DeliveryPrompt {
				runStatus = domain.RunDispatching
			}
			priority := int16(100)
			if input.Delivery == domain.DeliveryFollowUp {
				priority = 90
			}
			_, err = tx.Exec(ctx, `
                INSERT INTO runs(
                    id, organization_id, box_id, trigger_message_id, source_type,
                    status, priority, dispatch_started_at
                ) VALUES ($1,$2,$3,$4,'interactive',$5,$6,
                    CASE WHEN $5 = 'dispatching' THEN now() ELSE NULL END)`,
				runID, organizationID, boxID, messageID, string(runStatus), priority)
			if err != nil {
				return mapError("insert run", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE messages SET run_id = $2 WHERE id = $1`, messageID, runID); err != nil {
				return mapError("attach message run", err)
			}
			if input.Delivery == domain.DeliveryPrompt {
				commandValue, err := dispatchRunTx(ctx, tx, boxID, runID)
				if err != nil {
					return err
				}
				command = &commandValue
			}
			runValue, err := getRun(ctx, tx, runID)
			if err != nil {
				return err
			}
			run = &runValue
		}

		message, err = getMessage(ctx, tx, messageID)
		if err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, organizationID, "user", user.ID, "",
			"message.created", "message", messageID,
			map[string]any{"boxId": boxID, "runId": message.RunID, "delivery": input.Delivery}); err != nil {
			return err
		}
		return nil
	})
	return message, run, command, err
}

func (s *Store) CancelQueuedMessage(ctx context.Context, user domain.User, boxID, messageID string) (domain.Message, error) {
	if boxID == "" || messageID == "" {
		return domain.Message{}, fmt.Errorf("%w: box and message ids are required", storepkg.ErrInvalidState)
	}
	var result domain.Message
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var organizationID string
		if err := tx.QueryRow(ctx, `
            SELECT organization_id FROM boxes
            WHERE id = $1 AND organization_id = $2
            FOR UPDATE`, boxID, user.OrganizationID).Scan(&organizationID); err != nil {
			return mapError("lock cancellation box", err)
		}
		allowed, err := canOperateBox(ctx, tx, user, boxID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("%w: box operator access required", storepkg.ErrForbidden)
		}
		var messageStatus, delivery, runID, runStatus string
		err = tx.QueryRow(ctx, `
            SELECT m.status, COALESCE(m.delivery, ''), COALESCE(m.run_id::text, ''), COALESCE(r.status, '')
            FROM messages m
            JOIN runs r ON r.id = m.run_id AND r.box_id = m.box_id
            WHERE m.organization_id = $1 AND m.box_id = $2 AND m.id = $3
            FOR UPDATE OF m, r`, organizationID, boxID, messageID).Scan(
			&messageStatus, &delivery, &runID, &runStatus)
		if err != nil {
			return mapError("lock queued message", err)
		}
		if messageStatus == "cancelled" && runStatus == string(domain.RunCancelled) {
			result, err = getMessage(ctx, tx, messageID)
			return err
		}
		if domain.Delivery(delivery) != domain.DeliveryFollowUp || messageStatus != "queued" || runID == "" || runStatus != string(domain.RunQueued) {
			return fmt.Errorf("%w: only queued follow_up messages can be cancelled", storepkg.ErrConflict)
		}
		messageTag, err := tx.Exec(ctx, `
            UPDATE messages SET status = 'cancelled'
            WHERE id = $1 AND status = 'queued'`, messageID)
		if err != nil {
			return mapError("cancel queued message", err)
		}
		if messageTag.RowsAffected() != 1 {
			return fmt.Errorf("%w: queued message cancellation lost compare-and-set", storepkg.ErrConflict)
		}
		runTag, err := tx.Exec(ctx, `
            UPDATE runs
            SET status = 'cancelled', finished_at = now(), terminal_reason = 'user_cancelled', version = version + 1
            WHERE id = $1 AND status = 'queued'`, runID)
		if err != nil {
			return mapError("cancel queued run", err)
		}
		if runTag.RowsAffected() != 1 {
			return fmt.Errorf("%w: queued run cancellation lost compare-and-set", storepkg.ErrConflict)
		}
		if err := insertAudit(ctx, tx, organizationID, "user", user.ID, "",
			"message.cancelled", "message", messageID,
			map[string]any{"boxId": boxID, "runId": runID}); err != nil {
			return err
		}
		result, err = getMessage(ctx, tx, messageID)
		return err
	})
	return result, err
}

func (s *Store) ClaimNextRun(ctx context.Context, boxID string) (*domain.Run, *domain.HostCommand, error) {
	var resultRun *domain.Run
	var resultCommand *domain.HostCommand
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var lockedID string
		if err := tx.QueryRow(ctx, `SELECT id FROM boxes WHERE id = $1 FOR UPDATE`, boxID).Scan(&lockedID); err != nil {
			return mapError("lock run box", err)
		}
		runValue, commandValue, found, err := claimNextRunTx(ctx, tx, boxID)
		if err != nil {
			return err
		}
		if found {
			resultRun = &runValue
			resultCommand = &commandValue
		}
		return nil
	})
	return resultRun, resultCommand, err
}

func claimNextRunTx(ctx context.Context, tx pgx.Tx, boxID string) (domain.Run, domain.HostCommand, bool, error) {
	var runID string
	err := tx.QueryRow(ctx, `
        SELECT r.id
        FROM runs r
        WHERE r.box_id = $1 AND r.status = 'queued'
        ORDER BY r.priority ASC, r.queued_at ASC
        FOR UPDATE SKIP LOCKED
        LIMIT 1`, boxID).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Run{}, domain.HostCommand{}, false, nil
	}
	if err != nil {
		return domain.Run{}, domain.HostCommand{}, false, mapError("claim queued run", err)
	}
	tag, err := tx.Exec(ctx, `
        UPDATE runs
        SET status = 'dispatching', dispatch_started_at = now(), version = version + 1
        WHERE id = $1 AND status = 'queued'`, runID)
	if err != nil {
		return domain.Run{}, domain.HostCommand{}, false, mapError("dispatch queued run", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.Run{}, domain.HostCommand{}, false, nil
	}
	if _, err := tx.Exec(ctx, `
        UPDATE messages SET status = 'dispatched'
        WHERE id = (SELECT trigger_message_id FROM runs WHERE id = $1)
          AND status = 'queued'`, runID); err != nil {
		return domain.Run{}, domain.HostCommand{}, false, mapError("dispatch queued message", err)
	}
	command, err := dispatchRunTx(ctx, tx, boxID, runID)
	if err != nil {
		return domain.Run{}, domain.HostCommand{}, false, err
	}
	run, err := getRun(ctx, tx, runID)
	if err != nil {
		return domain.Run{}, domain.HostCommand{}, false, err
	}
	return run, command, true, nil
}

func dispatchRunTx(ctx context.Context, tx pgx.Tx, boxID, runID string) (domain.HostCommand, error) {
	var organizationID, hostID, runtimeType, workspacePath, model, delivery, messageID, messageText string
	var daemonInstanceID *string
	var configSnapshot []byte
	err := tx.QueryRow(ctx, `
        SELECT b.organization_id, b.host_id, b.runtime_type, w.real_path,
               COALESCE(av.model, ''), h.current_daemon_instance_id::text,
               jsonb_build_object(
                   'agentVersionId', av.id,
                   'systemPrompt', av.system_prompt,
                   'toolPolicy', av.tool_policy,
                   'skillPolicy', av.skill_policy,
                   'approvalPolicy', av.approval_policy,
                   'runtimeConfig', av.runtime_config
               ), m.delivery, m.id, COALESCE(m.plain_text, '')
        FROM runs r
        JOIN boxes b ON b.id = r.box_id AND b.organization_id = r.organization_id
        JOIN workspaces w ON w.id = b.workspace_id
        JOIN hosts h ON h.id = b.host_id
        JOIN agent_versions av ON av.id = b.agent_version_id
        JOIN messages m ON m.id = r.trigger_message_id
        WHERE r.id = $1 AND b.id = $2
        FOR UPDATE OF r`, runID, boxID).Scan(
		&organizationID, &hostID, &runtimeType, &workspacePath, &model,
		&daemonInstanceID, &configSnapshot, &delivery, &messageID, &messageText,
	)
	if err != nil {
		return domain.HostCommand{}, mapError("load run dispatch", err)
	}

	var runtimeInstanceID string
	err = tx.QueryRow(ctx, `
        SELECT id
        FROM runtime_instances
        WHERE box_id = $1 AND status IN ('starting','ready','busy','stopping')
        FOR UPDATE`, boxID).Scan(&runtimeInstanceID)
	if err == nil {
		if _, err := tx.Exec(ctx, `
            UPDATE runs SET runtime_instance_id = $2, version = version + 1
            WHERE id = $1`, runID, runtimeInstanceID); err != nil {
			return domain.HostCommand{}, mapError("attach active runtime to run", err)
		}
		return createInputCommandTx(ctx, tx, organizationID, hostID, boxID,
			runID, runtimeInstanceID, messageID, domain.Delivery(delivery), messageText)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostCommand{}, mapError("find active runtime", err)
	}
	if daemonInstanceID == nil || *daemonInstanceID == "" {
		return domain.HostCommand{}, fmt.Errorf("%w: host has no connected daemon", storepkg.ErrHostOffline)
	}

	runtimeInstanceID, err = newUUIDv7()
	if err != nil {
		return domain.HostCommand{}, err
	}
	_, err = tx.Exec(ctx, `
        INSERT INTO runtime_instances(
            id, organization_id, box_id, host_id, runtime_type,
            daemon_instance_id, status, config_snapshot
        ) VALUES ($1,$2,$3,$4,$5,$6,'starting',$7)`,
		runtimeInstanceID, organizationID, boxID, hostID, runtimeType,
		*daemonInstanceID, configSnapshot)
	if err != nil {
		return domain.HostCommand{}, mapError("insert runtime instance", err)
	}
	if _, err := tx.Exec(ctx, `
        UPDATE runs SET runtime_instance_id = $2, version = version + 1 WHERE id = $1`,
		runID, runtimeInstanceID); err != nil {
		return domain.HostCommand{}, mapError("attach new runtime to run", err)
	}
	startPayload := map[string]any{
		"runtime":           runtimeType,
		"workspace":         workspacePath,
		"subagentEventMode": "events",
		"initialInput": map[string]any{
			"id":       messageID,
			"message":  messageText,
			"delivery": delivery,
		},
	}
	var runtimeSnapshot struct {
		SystemPrompt string `json:"systemPrompt"`
	}
	if err := json.Unmarshal(configSnapshot, &runtimeSnapshot); err != nil {
		return domain.HostCommand{}, fmt.Errorf("decode runtime config snapshot: %w", err)
	}
	if runtimeSnapshot.SystemPrompt != "" {
		startPayload["systemPrompt"] = runtimeSnapshot.SystemPrompt
	}
	if model != "" {
		startPayload["model"] = model
	}
	payload, err := json.Marshal(startPayload)
	if err != nil {
		return domain.HostCommand{}, fmt.Errorf("encode runtime start command: %w", err)
	}
	command, err := insertHostCommandTx(ctx, tx, domain.HostCommand{
		OrganizationID:    organizationID,
		HostID:            hostID,
		BoxID:             boxID,
		RunID:             runID,
		RuntimeInstanceID: runtimeInstanceID,
		CommandType:       "runtime.start",
		Payload:           payload,
		IdempotencyKey:    "runtime:start:" + runtimeInstanceID,
		Status:            "pending",
	})
	if err != nil {
		return domain.HostCommand{}, err
	}
	if _, err := tx.Exec(ctx, `
        UPDATE boxes SET status = 'starting', updated_at = now(), version = version + 1
        WHERE id = $1 AND status IN ('created','idle','hibernated','starting')`, boxID); err != nil {
		return domain.HostCommand{}, mapError("mark box starting", err)
	}
	return command, nil
}

func createInputCommandTx(ctx context.Context, tx pgx.Tx, organizationID, hostID, boxID, runID, runtimeInstanceID, messageID string, delivery domain.Delivery, messageText string) (domain.HostCommand, error) {
	commandType := "runtime.prompt"
	switch delivery {
	case domain.DeliveryFollowUp:
		commandType = "runtime.follow_up"
	case domain.DeliverySteer:
		commandType = "runtime.steer"
	}
	payload, err := json.Marshal(map[string]any{"message": messageText})
	if err != nil {
		return domain.HostCommand{}, fmt.Errorf("encode runtime input command: %w", err)
	}
	command, err := insertHostCommandTx(ctx, tx, domain.HostCommand{
		OrganizationID:    organizationID,
		HostID:            hostID,
		BoxID:             boxID,
		RunID:             runID,
		RuntimeInstanceID: runtimeInstanceID,
		CommandType:       commandType,
		Payload:           payload,
		IdempotencyKey:    "runtime:" + string(delivery) + ":" + messageID,
		Status:            "pending",
	})
	if err != nil {
		return domain.HostCommand{}, err
	}
	if _, err := tx.Exec(ctx, `
        UPDATE boxes SET status = 'running', updated_at = now(), last_activity_at = now(), version = version + 1
        WHERE id = $1 AND status IN ('created','starting','idle','running','hibernated')`, boxID); err != nil {
		return domain.HostCommand{}, mapError("mark box run dispatching", err)
	}
	return command, nil
}

func (s *Store) SetMessageApplied(ctx context.Context, messageID, runID string) error {
	tag, err := s.pool.Exec(ctx, `
        UPDATE messages
        SET status = 'applied', applied_at = now()
        WHERE id = $1 AND run_id = $2 AND status = 'dispatched'`, messageID, runID)
	if err != nil {
		return mapError("set message applied", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var status, existingRunID string
	err = s.pool.QueryRow(ctx, `
        SELECT status, COALESCE(run_id::text, '') FROM messages WHERE id = $1`, messageID).Scan(&status, &existingRunID)
	if err != nil {
		return mapError("get message application state", err)
	}
	if status == "applied" && existingRunID == runID {
		return nil
	}
	return fmt.Errorf("%w: message cannot transition from %s to applied", storepkg.ErrInvalidState, status)
}

func findIdempotentMessage(ctx context.Context, q querier, boxID, key string) (domain.Message, *domain.Run, *domain.HostCommand, bool, error) {
	message, err := scanMessage(q.QueryRow(ctx, messageSelect+`
        WHERE m.box_id = $1 AND m.idempotency_key = $2`, boxID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Message{}, nil, nil, false, nil
	}
	if err != nil {
		return domain.Message{}, nil, nil, false, mapError("get idempotent message", err)
	}
	var run *domain.Run
	if message.RunID != "" {
		value, err := getRun(ctx, q, message.RunID)
		if err != nil {
			return domain.Message{}, nil, nil, false, err
		}
		run = &value
	}
	var command *domain.HostCommand
	if message.RunID != "" {
		value, err := scanHostCommand(q.QueryRow(ctx, hostCommandSelect+`
            WHERE c.run_id = $1
            ORDER BY c.created_at DESC LIMIT 1`, message.RunID))
		if err == nil {
			command = &value
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, nil, nil, false, mapError("get idempotent command", err)
		}
	}
	return message, run, command, true, nil
}

func getMessage(ctx context.Context, q querier, id string) (domain.Message, error) {
	result, err := scanMessage(q.QueryRow(ctx, messageSelect+` WHERE m.id = $1`, id))
	if err != nil {
		return domain.Message{}, mapError("get message", err)
	}
	return result, nil
}

func scanMessage(row scanner) (domain.Message, error) {
	var result domain.Message
	var content []byte
	err := row.Scan(
		&result.ID, &result.OrganizationID, &result.BoxID, &result.RunID,
		&result.BoxSeq, &result.AuthorType, &result.AuthorUserID, &result.AuthorName,
		&result.Role, &result.Delivery, &result.Status, &content,
		&result.PlainText, &result.CreatedAt,
	)
	if err != nil {
		return domain.Message{}, err
	}
	result.Content = json.RawMessage(append([]byte(nil), content...))
	return result, nil
}

func getRun(ctx context.Context, q querier, id string) (domain.Run, error) {
	result, err := scanRun(q.QueryRow(ctx, runSelect+` WHERE r.id = $1`, id))
	if err != nil {
		return domain.Run{}, mapError("get run", err)
	}
	return result, nil
}

func scanRun(row scanner) (domain.Run, error) {
	var result domain.Run
	err := row.Scan(
		&result.ID, &result.OrganizationID, &result.BoxID, &result.RuntimeInstanceID,
		&result.TriggerMessageID, &result.Status, &result.QueuedAt,
		&result.StartedAt, &result.FinishedAt, &result.TerminalReason, &result.ErrorMessage,
	)
	return result, err
}
