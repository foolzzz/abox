package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"agentbox/internal/domain"
	storepkg "agentbox/internal/store"

	"github.com/jackc/pgx/v5"
)

const boxEventSelect = `
    SELECT e.organization_id, e.box_id, e.seq, e.event_id,
           COALESCE(e.host_id::text, ''), COALESCE(e.daemon_event_id::text, ''),
           COALESCE(e.run_id::text, ''), COALESCE(e.runtime_instance_id::text, ''),
           e.event_type, e.actor_kind, COALESCE(e.actor_id, ''), e.payload,
           e.occurred_at, e.ingested_at
    FROM box_events e`

func (s *Store) ListEvents(ctx context.Context, user domain.User, boxID string, afterSeq int64, limit int) ([]domain.BoxEvent, error) {
	allowed, err := canReadBox(ctx, s.pool, user, boxID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: box read access required", storepkg.ErrForbidden)
	}
	limit = boundedLimit(limit, 200, 1000)
	rows, err := s.pool.Query(ctx, boxEventSelect+`
        WHERE e.organization_id = $1 AND e.box_id = $2 AND e.seq > $3
        ORDER BY e.seq
        LIMIT $4`, user.OrganizationID, boxID, afterSeq, limit)
	if err != nil {
		return nil, mapError("list box events", err)
	}
	defer rows.Close()
	result := make([]domain.BoxEvent, 0)
	for rows.Next() {
		event, err := scanBoxEvent(rows)
		if err != nil {
			return nil, mapError("scan box event", err)
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list box events", err)
	}
	return result, nil
}

func (s *Store) AppendEvents(ctx context.Context, events []domain.BoxEvent) ([]domain.BoxEvent, error) {
	if len(events) == 0 {
		return []domain.BoxEvent{}, nil
	}
	result := make([]domain.BoxEvent, len(events))
	inserted := make([]bool, len(events))
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		hostIDs := make(map[string]struct{})
		for _, event := range events {
			if event.HostID != "" {
				hostIDs[event.HostID] = struct{}{}
			}
		}
		orderedHosts := make([]string, 0, len(hostIDs))
		for id := range hostIDs {
			orderedHosts = append(orderedHosts, id)
		}
		sort.Strings(orderedHosts)
		for _, id := range orderedHosts {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, id); err != nil {
				return mapError("lock host event stream", err)
			}
		}

		pendingByBox := make(map[string][]int)
		for i, event := range events {
			if event.BoxID == "" || event.EventType == "" {
				return fmt.Errorf("%w: event box and type are required", storepkg.ErrInvalidState)
			}
			if event.ActorKind == "" {
				event.ActorKind = "daemon"
				events[i].ActorKind = event.ActorKind
			}
			if _, err := jsonValue(event.Payload, "{}"); err != nil {
				return err
			}
			if event.EventID != "" {
				existing, found, err := findEventByID(ctx, tx, event.EventID)
				if err != nil {
					return err
				}
				if found {
					result[i] = existing
					continue
				}
			}
			if event.DaemonEventID != "" {
				existing, found, err := findDaemonEvent(ctx, tx, event.HostID, event.DaemonEventID)
				if err != nil {
					return err
				}
				if found {
					result[i] = existing
					continue
				}
			}
			pendingByBox[event.BoxID] = append(pendingByBox[event.BoxID], i)
		}

		boxIDs := make([]string, 0, len(pendingByBox))
		for boxID := range pendingByBox {
			boxIDs = append(boxIDs, boxID)
		}
		sort.Strings(boxIDs)
		for _, boxID := range boxIDs {
			indexes := pendingByBox[boxID]
			firstEvent := events[indexes[0]]
			var organizationID, hostID string
			err := tx.QueryRow(ctx, `
                SELECT organization_id, host_id
                FROM boxes WHERE id = $1
                FOR UPDATE`, boxID).Scan(&organizationID, &hostID)
			if err != nil {
				return mapError("lock event box", err)
			}
			if firstEvent.OrganizationID != "" && firstEvent.OrganizationID != organizationID {
				return fmt.Errorf("%w: event organization does not own box", storepkg.ErrConflict)
			}
			for _, index := range indexes {
				if events[index].OrganizationID != "" && events[index].OrganizationID != organizationID {
					return fmt.Errorf("%w: event batch crosses organizations", storepkg.ErrConflict)
				}
				if events[index].HostID != "" && events[index].HostID != hostID {
					return fmt.Errorf("%w: event host does not own box", storepkg.ErrConflict)
				}
				events[index].OrganizationID = organizationID
			}
			var firstSeq int64
			err = tx.QueryRow(ctx, `
                UPDATE boxes
                SET next_event_seq = next_event_seq + $2,
                    last_activity_at = now(), updated_at = now(), version = version + 1
                WHERE id = $1
                RETURNING next_event_seq - $2`, boxID, len(indexes)).Scan(&firstSeq)
			if err != nil {
				return mapError("allocate event sequence", err)
			}
			for offset, index := range indexes {
				event := events[index]
				event.Seq = firstSeq + int64(offset)
				if event.EventID == "" {
					event.EventID, err = newUUIDv7()
					if err != nil {
						return err
					}
				}
				if event.OccurredAt.IsZero() {
					event.OccurredAt = time.Now().UTC()
				}
				payload, err := jsonValue(event.Payload, "{}")
				if err != nil {
					return err
				}
				err = tx.QueryRow(ctx, `
                    INSERT INTO box_events(
                        organization_id, box_id, seq, event_id, host_id, daemon_event_id,
                        run_id, runtime_instance_id, event_type, actor_kind, actor_id,
                        payload, occurred_at
                    ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
                    RETURNING ingested_at`,
					event.OrganizationID, event.BoxID, event.Seq, event.EventID,
					nullableUUID(event.HostID), nullableUUID(event.DaemonEventID),
					nullableUUID(event.RunID), nullableUUID(event.RuntimeInstanceID),
					event.EventType, event.ActorKind, nullableText(event.ActorID), payload,
					event.OccurredAt).Scan(&event.IngestedAt)
				if err != nil {
					return mapError("insert box event", err)
				}
				event.Payload = json.RawMessage(payload)
				if err := applyEventProjectionTx(ctx, tx, event); err != nil {
					return err
				}
				result[index] = event
				inserted[index] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	newEvents := make([]domain.BoxEvent, 0, len(events))
	for index := range result {
		if inserted[index] {
			newEvents = append(newEvents, result[index])
		}
	}
	return newEvents, nil
}

func (s *Store) ApplyRuntimeEvent(ctx context.Context, event domain.BoxEvent) error {
	_, err := s.AppendEvents(ctx, []domain.BoxEvent{event})
	return err
}

func findDaemonEvent(ctx context.Context, q querier, hostID, daemonEventID string) (domain.BoxEvent, bool, error) {
	if hostID == "" || daemonEventID == "" {
		return domain.BoxEvent{}, false, nil
	}
	event, err := scanBoxEvent(q.QueryRow(ctx, boxEventSelect+`
        WHERE e.host_id = $1 AND e.daemon_event_id = $2`, hostID, daemonEventID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BoxEvent{}, false, nil
	}
	if err != nil {
		return domain.BoxEvent{}, false, mapError("find duplicate daemon event", err)
	}
	return event, true, nil
}

func findEventByID(ctx context.Context, q querier, eventID string) (domain.BoxEvent, bool, error) {
	event, err := scanBoxEvent(q.QueryRow(ctx, boxEventSelect+` WHERE e.event_id = $1`, eventID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BoxEvent{}, false, nil
	}
	if err != nil {
		return domain.BoxEvent{}, false, mapError("find duplicate event", err)
	}
	return event, true, nil
}
func scanBoxEvent(row scanner) (domain.BoxEvent, error) {
	var result domain.BoxEvent
	var payload []byte
	err := row.Scan(
		&result.OrganizationID, &result.BoxID, &result.Seq, &result.EventID,
		&result.HostID, &result.DaemonEventID, &result.RunID,
		&result.RuntimeInstanceID, &result.EventType, &result.ActorKind,
		&result.ActorID, &payload, &result.OccurredAt, &result.IngestedAt,
	)
	if err != nil {
		return domain.BoxEvent{}, err
	}
	result.Payload = json.RawMessage(append([]byte(nil), payload...))
	return result, nil
}

func applyEventProjectionTx(ctx context.Context, tx pgx.Tx, event domain.BoxEvent) error {
	switch event.EventType {
	case "runtime.starting":
		if _, err := tx.Exec(ctx, `
            UPDATE runtime_instances
            SET status = 'starting', last_event_at = $2, version = version + 1
            WHERE id = $1 AND status IN ('starting','ready')`, event.RuntimeInstanceID, event.OccurredAt); err != nil {
			return mapError("project runtime starting", err)
		}
	case "runtime.ready":
		if _, err := tx.Exec(ctx, `
            UPDATE runtime_instances
            SET status = 'ready', ready_at = COALESCE(ready_at, $2),
                last_event_at = $2, version = version + 1
            WHERE id = $1 AND status IN ('starting','ready','busy')`, event.RuntimeInstanceID, event.OccurredAt); err != nil {
			return mapError("project runtime ready", err)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE boxes b
            SET status = 'idle', updated_at = now(), last_activity_at = now(), version = b.version + 1
            WHERE b.id = $1 AND b.status = 'starting'
              AND NOT EXISTS (
                  SELECT 1 FROM runs r
                  WHERE r.box_id = b.id
                    AND r.status IN ('dispatching','running','waiting_approval','interrupting','disconnected')
              )`, event.BoxID); err != nil {
			return mapError("release resumed box", err)
		}
	case "run.started":
		if _, err := tx.Exec(ctx, `
            UPDATE runs SET status = 'running', started_at = COALESCE(started_at, $2), version = version + 1
            WHERE id = $1 AND status IN ('dispatching','disconnected')`, event.RunID, event.OccurredAt); err != nil {
			return mapError("project run started", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime_instances SET status = 'busy', last_event_at = $2, version = version + 1 WHERE id = $1`, event.RuntimeInstanceID, event.OccurredAt); err != nil {
			return mapError("project runtime busy", err)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE messages SET status = 'applied', applied_at = COALESCE(applied_at, $2)
            WHERE id = (SELECT trigger_message_id FROM runs WHERE id = $1)
              AND status = 'dispatched'`, event.RunID, event.OccurredAt); err != nil {
			return mapError("project applied trigger message", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE boxes SET status = 'running', updated_at = now(), version = version + 1 WHERE id = $1 AND status <> 'terminated'`, event.BoxID); err != nil {
			return mapError("project running box", err)
		}
	case "run.completed", "run.failed":
		status := "succeeded"
		executionStatus := "completed"
		notificationType := "run_completed"
		title := "Run completed"
		if event.EventType == "run.failed" {
			status = "failed"
			executionStatus = "failed"
			notificationType = "run_failed"
			title = "Run failed"
		}
		reason := payloadString(event.Payload, "reason", "terminalReason", "error")
		if _, err := tx.Exec(ctx, `
            UPDATE runs
            SET status = $2, finished_at = $3, terminal_reason = NULLIF($4,''), version = version + 1
            WHERE id = $1 AND status IN ('dispatching','running','waiting_approval','interrupting','disconnected')`,
			event.RunID, status, event.OccurredAt, reason); err != nil {
			return mapError("project terminal run", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime_instances SET status = 'ready', last_event_at = $2, version = version + 1 WHERE id = $1 AND status IN ('starting','ready','busy')`, event.RuntimeInstanceID, event.OccurredAt); err != nil {
			return mapError("release runtime after run", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE boxes SET status = 'idle', updated_at = now(), last_activity_at = now(), version = version + 1 WHERE id = $1 AND status <> 'terminated'`, event.BoxID); err != nil {
			return mapError("release box after run", err)
		}
		var scheduleID string
		scheduleErr := tx.QueryRow(ctx, `
            UPDATE schedule_executions
            SET status = $2, reason = NULLIF($3,''), finished_at = $4, updated_at = now()
            WHERE run_id = $1 AND status IN ('claimed','dispatched')
            RETURNING schedule_id`, event.RunID, executionStatus, reason, event.OccurredAt).Scan(&scheduleID)
		if scheduleErr != nil && !errors.Is(scheduleErr, pgx.ErrNoRows) {
			return mapError("project schedule execution", scheduleErr)
		}
		body := "The agent run completed successfully."
		if executionStatus == "failed" {
			body = "The agent run failed."
			if reason != "" {
				body += " " + reason
			}
		}
		if err := notifyBoxAudienceTx(ctx, tx, event.OrganizationID, event.BoxID,
			notificationType, title, body, "run-terminal:"+event.RunID,
			event.RunID, scheduleID, ""); err != nil {
			return err
		}
		if event.HostID != "" {
			if err := insertAudit(ctx, tx, event.OrganizationID, "daemon", "", event.HostID,
				"run."+executionStatus, "run", event.RunID,
				map[string]any{"boxId": event.BoxID, "reason": reason}); err != nil {
				return err
			}
		}
	case "runtime.exited":
		reason := payloadString(event.Payload, "reason", "terminalReason", "stopReason")
		var currentBoxStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM boxes WHERE id = $1 FOR UPDATE`, event.BoxID).Scan(&currentBoxStatus); err != nil {
			return mapError("lock exited runtime box", err)
		}
		if currentBoxStatus == string(domain.BoxHibernating) {
			reason = "hibernated"
		}
		if _, err := tx.Exec(ctx, `
            UPDATE runtime_instances
            SET status = 'exited', stopped_at = $2, last_event_at = $2,
                terminal_reason = NULLIF($3,''), version = version + 1
            WHERE id = $1`, event.RuntimeInstanceID, event.OccurredAt, reason); err != nil {
			return mapError("project runtime exited", err)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE runs SET status = 'lost', finished_at = $2, terminal_reason = 'runtime_exited', version = version + 1
            WHERE runtime_instance_id = $1
              AND status IN ('dispatching','running','waiting_approval','interrupting','disconnected')`,
			event.RuntimeInstanceID, event.OccurredAt); err != nil {
			return mapError("release exited runtime runs", err)
		}
		boxStatus := "error"
		if reason == "hibernated" || reason == "hibernate" {
			boxStatus = "hibernated"
		}
		if _, err := tx.Exec(ctx, `
            UPDATE boxes SET status = $2, updated_at = now(), version = version + 1
            WHERE id = $1 AND status <> 'terminated'`, event.BoxID, boxStatus); err != nil {
			return mapError("project exited runtime box", err)
		}
	case "approval.requested":
		if err := projectApprovalRequest(ctx, tx, event); err != nil {
			return err
		}
	case "approval.resolved":
		if err := projectApprovalResolution(ctx, tx, event); err != nil {
			return err
		}
	case "subagent.started", "subagent.progress", "subagent.completed":
		if err := projectSubagent(ctx, tx, event); err != nil {
			return err
		}
	case "todo.updated":
		if err := projectTodo(ctx, tx, event); err != nil {
			return err
		}
	case "artifact.created":
		if err := projectArtifact(ctx, tx, event); err != nil {
			return err
		}
	case "runtime.snapshot":
		if err := projectRuntimeSnapshot(ctx, tx, event); err != nil {
			return err
		}
	case "message.completed":
		if event.ActorKind == "user" {
			if _, err := tx.Exec(ctx, `
                WITH target AS (
                    SELECT id FROM messages
                    WHERE run_id = $1 AND role = 'user' AND status = 'dispatched'
                    ORDER BY box_seq
                    FOR UPDATE SKIP LOCKED
                    LIMIT 1
                )
                UPDATE messages SET status = 'applied', applied_at = $2
                WHERE id IN (SELECT id FROM target)`, event.RunID, event.OccurredAt); err != nil {
				return mapError("project applied user message", err)
			}
		} else if err := projectAssistantMessage(ctx, tx, event); err != nil {
			return err
		}
	}
	return nil
}

func enqueueReadyRunInput(ctx context.Context, tx pgx.Tx, event domain.BoxEvent) error {
	var organizationID, hostID, runID, messageID, delivery, text string
	var presentationContext []byte
	err := tx.QueryRow(ctx, `
        SELECT r.organization_id, b.host_id, r.id, m.id, m.delivery, COALESCE(m.plain_text, ''), m.presentation_context
        FROM runs r
        JOIN boxes b ON b.id = r.box_id
        JOIN messages m ON m.id = r.trigger_message_id
        WHERE r.box_id = $1 AND r.runtime_instance_id = $2 AND r.status = 'dispatching'
        ORDER BY r.queued_at LIMIT 1`, event.BoxID, event.RuntimeInstanceID).Scan(
		&organizationID, &hostID, &runID, &messageID, &delivery, &text, &presentationContext)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return mapError("load ready runtime run", err)
	}
	_, err = createInputCommandTx(ctx, tx, organizationID, hostID, event.BoxID,
		runID, event.RuntimeInstanceID, messageID, domain.Delivery(delivery), text, presentationContext)
	return err
}

func projectApprovalRequest(ctx context.Context, tx pgx.Tx, event domain.BoxEvent) error {
	if event.RunID == "" || event.RuntimeInstanceID == "" {
		return nil
	}
	requestID := payloadString(event.Payload, "runtimeRequestId", "requestId", "approvalId", "id")
	if requestID == "" {
		requestID = event.EventID
	}
	toolName := payloadString(event.Payload, "toolName", "tool")
	if toolName == "" {
		toolName = "runtime_tool"
	}
	risk := strings.ToLower(payloadString(event.Payload, "riskLevel", "risk"))
	switch risk {
	case "low", "medium", "high", "critical":
	default:
		risk = "medium"
	}
	expiresAt := event.OccurredAt.Add(5 * time.Minute)
	if value := payloadString(event.Payload, "expiresAt"); value != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			expiresAt = parsed
		}
	}
	approvalID, err := newUUIDv7()
	if err != nil {
		return err
	}
	payload, err := jsonValue(event.Payload, "{}")
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
        INSERT INTO approvals(
            id, organization_id, box_id, run_id, runtime_instance_id,
            runtime_request_id, tool_name, risk_level, status,
            requested_payload, requested_at, expires_at
        ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$11)
        ON CONFLICT (runtime_instance_id, runtime_request_id) DO NOTHING`,
		approvalID, event.OrganizationID, event.BoxID, event.RunID,
		event.RuntimeInstanceID, requestID, toolName, risk, payload,
		event.OccurredAt, expiresAt)
	if err != nil {
		return mapError("project approval request", err)
	}
	if tag.RowsAffected() == 0 {
		if err := tx.QueryRow(ctx, `SELECT id FROM approvals WHERE runtime_instance_id = $1 AND runtime_request_id = $2`, event.RuntimeInstanceID, requestID).Scan(&approvalID); err != nil {
			return mapError("get projected approval", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'waiting_approval', version = version + 1 WHERE id = $1 AND status = 'running'`, event.RunID); err != nil {
		return mapError("project approval run", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE boxes SET status = 'waiting_approval', updated_at = now(), version = version + 1 WHERE id = $1 AND status = 'running'`, event.BoxID); err != nil {
		return mapError("project approval box", err)
	}
	if err := notifyBoxAudienceTx(ctx, tx, event.OrganizationID, event.BoxID,
		"approval_required", "Approval required", "An agent is waiting for approval to use "+toolName+".",
		"approval-requested:"+approvalID, event.RunID, "", approvalID); err != nil {
		return err
	}
	if tag.RowsAffected() > 0 && event.HostID != "" {
		if err := insertAudit(ctx, tx, event.OrganizationID, "daemon", "", event.HostID,
			"approval.requested", "approval", approvalID,
			map[string]any{"boxId": event.BoxID, "runId": event.RunID, "runtimeRequestId": requestID, "toolName": toolName}); err != nil {
			return err
		}
	}
	return nil
}

func projectApprovalResolution(ctx context.Context, tx pgx.Tx, event domain.BoxEvent) error {
	if event.RuntimeInstanceID == "" {
		return nil
	}
	requestID := payloadString(event.Payload, "runtimeRequestId", "requestId", "approvalId", "id")
	if requestID == "" {
		return nil
	}
	decision := strings.ToLower(payloadString(event.Payload, "decision", "status", "reason"))
	status := "denied"
	if payloadBool(event.Payload, "approved", "allow") || decision == "approved" || decision == "approve" || decision == "allowed" {
		status = "approved"
	}
	if payloadBool(event.Payload, "cancelled", "canceled") || decision == "cancelled" || decision == "canceled" || decision == "interrupted" || decision == "stopped" || decision == "process_exit" || decision == "runtime_failure" || decision == "runtime_cancelled" {
		status = "cancelled"
	}
	payload, err := jsonValue(event.Payload, "{}")
	if err != nil {
		return err
	}
	var approvalID, runID, boxID string
	err = tx.QueryRow(ctx, `
        UPDATE approvals
        SET status = $3, decision_payload = $4, resolved_at = $5, version = version + 1
        WHERE runtime_instance_id = $1
          AND (runtime_request_id = $2 OR id::text = $2)
          AND status = 'pending'
        RETURNING id, run_id, box_id`, event.RuntimeInstanceID, requestID, status, payload, event.OccurredAt).Scan(
		&approvalID, &runID, &boxID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return mapError("project approval resolution", err)
	}
	if status != "cancelled" {
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'running', version = version + 1 WHERE id = $1 AND status = 'waiting_approval'`, runID); err != nil {
			return mapError("project resolved approval run", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE boxes SET status = 'running', updated_at = now(), version = version + 1 WHERE id = $1 AND status = 'waiting_approval'`, boxID); err != nil {
			return mapError("project resolved approval box", err)
		}
	}
	if event.HostID != "" {
		return insertAudit(ctx, tx, event.OrganizationID, "daemon", "", event.HostID,
			"approval.runtime_resolved", "approval", approvalID,
			map[string]any{"boxId": boxID, "runId": runID, "decision": status, "runtimeRequestId": requestID})
	}
	return nil
}

func projectArtifact(ctx context.Context, tx pgx.Tx, event domain.BoxEvent) error {
	name := payloadString(event.Payload, "name")
	hostPath := payloadString(event.Payload, "path", "hostPath", "host_path")
	sha256Value := strings.ToLower(payloadString(event.Payload, "sha256"))
	if name == "" || hostPath == "" || len(sha256Value) != 64 {
		return fmt.Errorf("%w: artifact name, path, and sha256 are required", storepkg.ErrInvalidState)
	}
	mimeType := payloadString(event.Payload, "mimeType", "mime_type")
	kind := "file"
	if strings.HasPrefix(mimeType, "image/") {
		kind = "image"
	} else if strings.Contains(mimeType, "zip") || strings.Contains(mimeType, "tar") || strings.Contains(mimeType, "archive") {
		kind = "archive"
	} else if strings.HasPrefix(mimeType, "text/") && (strings.Contains(strings.ToLower(name), "report") || strings.HasSuffix(strings.ToLower(name), ".md")) {
		kind = "report"
	} else if strings.Contains(strings.ToLower(name), ".log") {
		kind = "log"
	}
	payload, err := jsonValue(event.Payload, "{}")
	if err != nil {
		return err
	}
	artifactID := event.EventID
	_, err = tx.Exec(ctx, `
        INSERT INTO artifacts(
            id, organization_id, box_id, run_id, host_id, kind, name,
            mime_type, storage_backend, host_path, size_bytes, sha256,
            status, metadata, created_at
        ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'host',$9,$10,$11,'ready',$12,$13)
        ON CONFLICT (id) DO NOTHING`, artifactID, event.OrganizationID, event.BoxID,
		nullableUUID(event.RunID), event.HostID, kind, name, nullableText(mimeType), hostPath,
		payloadInt64(event.Payload, "sizeBytes", "size_bytes"), sha256Value, payload, event.OccurredAt)
	if err != nil {
		return mapError("project artifact", err)
	}
	if event.HostID != "" {
		return insertAudit(ctx, tx, event.OrganizationID, "daemon", "", event.HostID,
			"artifact.created", "artifact", artifactID,
			map[string]any{"boxId": event.BoxID, "runId": event.RunID, "name": name})
	}
	return nil
}

func projectRuntimeSnapshot(ctx context.Context, tx pgx.Tx, event domain.BoxEvent) error {
	if event.RuntimeInstanceID == "" {
		return nil
	}
	runtimeStatus := strings.ToLower(payloadString(event.Payload, "status"))
	projected := "ready"
	switch runtimeStatus {
	case "running", "busy":
		projected = "busy"
	case "starting":
		projected = "starting"
	case "stopping":
		projected = "stopping"
	case "exited", "stopped":
		projected = "exited"
	}
	if _, err := tx.Exec(ctx, `
        UPDATE runtime_instances
        SET status = $2, last_event_at = $3,
            ready_at = CASE WHEN $2 IN ('ready','busy') THEN COALESCE(ready_at, $3) ELSE ready_at END,
            stopped_at = CASE WHEN $2 = 'exited' THEN COALESCE(stopped_at, $3) ELSE stopped_at END,
            version = version + 1
        WHERE id = $1 AND box_id = $4`, event.RuntimeInstanceID, projected, event.OccurredAt, event.BoxID); err != nil {
		return mapError("project runtime snapshot", err)
	}
	if projected == "busy" {
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'running', version = version + 1 WHERE box_id = $1 AND runtime_instance_id = $2 AND status = 'disconnected'`, event.BoxID, event.RuntimeInstanceID); err != nil {
			return mapError("reconnect running run", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE boxes SET status = 'running', updated_at = now(), version = version + 1 WHERE id = $1 AND status <> 'terminated'`, event.BoxID); err != nil {
			return mapError("reconnect running box", err)
		}
	} else if projected == "ready" {
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'lost', finished_at = $3, terminal_reason = 'host_reconnected_runtime_idle', version = version + 1 WHERE box_id = $1 AND runtime_instance_id = $2 AND status = 'disconnected'`, event.BoxID, event.RuntimeInstanceID, event.OccurredAt); err != nil {
			return mapError("resolve disconnected run", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE boxes SET status = 'idle', updated_at = now(), version = version + 1 WHERE id = $1 AND status <> 'terminated'`, event.BoxID); err != nil {
			return mapError("reconnect idle box", err)
		}
	}
	return nil
}

func projectSubagent(ctx context.Context, tx pgx.Tx, event domain.BoxEvent) error {
	if event.ActorID == "" || event.RunID == "" || event.RuntimeInstanceID == "" {
		return nil
	}
	status := "running"
	var finished any
	if event.EventType == "subagent.completed" {
		status = "completed"
		state := strings.ToLower(payloadString(event.Payload, "status", "state", "result"))
		if strings.Contains(state, "fail") {
			status = "failed"
		} else if strings.Contains(state, "cancel") {
			status = "cancelled"
		} else if strings.Contains(state, "park") {
			status = "parked"
		}
		finished = event.OccurredAt
	}
	id, err := newUUIDv7()
	if err != nil {
		return err
	}
	payload, err := jsonValue(event.Payload, "{}")
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
        INSERT INTO subagent_instances(
            id, organization_id, box_id, run_id, runtime_instance_id,
            external_agent_id, parent_external_agent_id, agent_type, label,
            status, metadata, started_at, finished_at
        ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
        ON CONFLICT (runtime_instance_id, external_agent_id) DO UPDATE SET
            parent_external_agent_id = COALESCE(EXCLUDED.parent_external_agent_id, subagent_instances.parent_external_agent_id),
            agent_type = COALESCE(EXCLUDED.agent_type, subagent_instances.agent_type),
            label = COALESCE(EXCLUDED.label, subagent_instances.label),
            status = EXCLUDED.status, metadata = EXCLUDED.metadata,
            finished_at = COALESCE(EXCLUDED.finished_at, subagent_instances.finished_at)`,
		id, event.OrganizationID, event.BoxID, event.RunID, event.RuntimeInstanceID,
		event.ActorID, nullableText(payloadString(event.Payload, "parentAgentId", "parentId")),
		nullableText(payloadString(event.Payload, "agentType", "type")),
		nullableText(payloadString(event.Payload, "label", "name")), status, payload,
		event.OccurredAt, finished)
	return mapError("project subagent", err)
}

func projectTodo(ctx context.Context, tx pgx.Tx, event domain.BoxEvent) error {
	if event.RuntimeInstanceID == "" {
		return nil
	}
	externalID := payloadString(event.Payload, "todoId", "id")
	content := payloadString(event.Payload, "content", "text", "title")
	if externalID == "" || content == "" {
		return nil
	}
	status := strings.ToLower(payloadString(event.Payload, "status", "state"))
	switch status {
	case "pending", "in_progress", "completed", "blocked", "abandoned":
	default:
		status = "pending"
	}
	id, err := newUUIDv7()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
        INSERT INTO todo_items(
            id, organization_id, box_id, run_id, runtime_instance_id,
            external_todo_id, phase_name, content, position, status,
            block_reason, updated_at
        ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,0,$9,$10,$11)
        ON CONFLICT (runtime_instance_id, external_todo_id) DO UPDATE SET
            run_id = EXCLUDED.run_id, phase_name = COALESCE(EXCLUDED.phase_name, todo_items.phase_name),
            content = EXCLUDED.content, status = EXCLUDED.status,
            block_reason = EXCLUDED.block_reason, updated_at = EXCLUDED.updated_at`,
		id, event.OrganizationID, event.BoxID, nullableUUID(event.RunID),
		event.RuntimeInstanceID, externalID,
		nullableText(payloadString(event.Payload, "phaseName", "phase")), content, status,
		nullableText(payloadString(event.Payload, "blockReason", "reason")), event.OccurredAt)
	return mapError("project todo", err)
}

func projectAssistantMessage(ctx context.Context, tx pgx.Tx, event domain.BoxEvent) error {
	messageID, err := newUUIDv7()
	if err != nil {
		return err
	}
	var boxSeq int64
	err = tx.QueryRow(ctx, `
        UPDATE boxes SET next_message_seq = next_message_seq + 1
        WHERE id = $1 RETURNING next_message_seq - 1`, event.BoxID).Scan(&boxSeq)
	if err != nil {
		return mapError("allocate assistant message sequence", err)
	}
	payload, err := jsonValue(event.Payload, "{}")
	if err != nil {
		return err
	}
	plainText := payloadString(event.Payload, "text", "content", "message")
	if plainText == "" {
		plainText, err = collectAssistantText(ctx, tx, event)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
        INSERT INTO messages(
            id, organization_id, box_id, run_id, box_seq, author_type,
            role, status, content, plain_text, idempotency_key, applied_at
        ) VALUES ($1,$2,$3,$4,$5,'agent','assistant','applied',$6,$7,$8,$9)
        ON CONFLICT (box_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING`,
		messageID, event.OrganizationID, event.BoxID, nullableUUID(event.RunID),
		boxSeq, payload, nullableText(plainText), "event:"+event.EventID, event.OccurredAt)
	return mapError("project assistant message", err)
}

func collectAssistantText(ctx context.Context, tx pgx.Tx, event domain.BoxEvent) (string, error) {
	rows, err := tx.Query(ctx, `
        SELECT payload
        FROM box_events
        WHERE box_id = $1 AND run_id = $2 AND event_type = 'message.delta' AND seq <= $3
        ORDER BY seq`, event.BoxID, event.RunID, event.Seq)
	if err != nil {
		return "", mapError("read assistant message deltas", err)
	}
	defer rows.Close()
	completedID := payloadString(event.Payload, "messageId")
	var text strings.Builder
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return "", mapError("scan assistant message delta", err)
		}
		payload := json.RawMessage(raw)
		if payloadString(payload, "channel") == "thinking" {
			continue
		}
		if candidateID := payloadString(payload, "messageId"); completedID != "" && candidateID != "" && candidateID != completedID {
			continue
		}
		text.WriteString(payloadString(payload, "text", "delta"))
	}
	if err := rows.Err(); err != nil {
		return "", mapError("read assistant message deltas", err)
	}
	return text.String(), nil
}

func payloadString(payload json.RawMessage, keys ...string) string {
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	var visit func(any) string
	visit = func(current any) string {
		switch typed := current.(type) {
		case map[string]any:
			for _, key := range keys {
				if candidate, ok := typed[key]; ok {
					if text, ok := candidate.(string); ok && text != "" {
						return text
					}
				}
			}
			for _, candidate := range typed {
				if text := visit(candidate); text != "" {
					return text
				}
			}
		case []any:
			for _, candidate := range typed {
				if text := visit(candidate); text != "" {
					return text
				}
			}
		}
		return ""
	}
	return visit(value)
}

func payloadBool(payload json.RawMessage, keys ...string) bool {
	value := payloadValue(payload, keys...)
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes")
	default:
		return false
	}
}

func payloadInt64(payload json.RawMessage, keys ...string) int64 {
	value := payloadValue(payload, keys...)
	switch typed := value.(type) {
	case float64:
		if typed >= 0 {
			return int64(typed)
		}
	case json.Number:
		parsed, _ := typed.Int64()
		if parsed >= 0 {
			return parsed
		}
	}
	return 0
}

func payloadValue(payload json.RawMessage, keys ...string) any {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil
	}
	var visit func(any) any
	visit = func(current any) any {
		switch typed := current.(type) {
		case map[string]any:
			for _, key := range keys {
				if candidate, ok := typed[key]; ok {
					return candidate
				}
			}
			for _, candidate := range typed {
				if found := visit(candidate); found != nil {
					return found
				}
			}
		case []any:
			for _, candidate := range typed {
				if found := visit(candidate); found != nil {
					return found
				}
			}
		}
		return nil
	}
	return visit(value)
}
