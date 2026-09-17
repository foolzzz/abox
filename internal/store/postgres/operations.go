package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"agentbox/internal/domain"
	storepkg "agentbox/internal/store"

	"github.com/jackc/pgx/v5"
)

const notificationSelect = `
    SELECT n.id, n.organization_id, n.user_id, n.type, n.title, n.body,
           n.status, COALESCE(n.box_id::text, ''), COALESCE(n.run_id::text, ''),
           COALESCE(n.schedule_id::text, ''), COALESCE(n.approval_id::text, ''),
           n.created_at, n.read_at
    FROM notifications n`

const artifactSelect = `
    SELECT a.id, a.organization_id, a.box_id, COALESCE(a.run_id::text, ''),
           a.host_id, a.kind, a.name, COALESCE(a.mime_type, ''), a.size_bytes,
           a.sha256, a.status, a.metadata, a.created_at, a.expires_at,
           COALESCE(a.host_path, ''), w.real_path
    FROM artifacts a
    JOIN boxes b ON b.id = a.box_id AND b.organization_id = a.organization_id
    JOIN workspaces w ON w.id = b.workspace_id AND w.organization_id = b.organization_id`

func (s *Store) ListNotifications(ctx context.Context, user domain.User, limit int) ([]domain.Notification, error) {
	if _, err := requireMembership(ctx, s.pool, user); err != nil {
		return nil, err
	}
	limit = boundedLimit(limit, 100, 500)
	rows, err := s.pool.Query(ctx, notificationSelect+`
        WHERE n.organization_id = $1 AND n.user_id = $2
        ORDER BY n.created_at DESC, n.id DESC LIMIT $3`, user.OrganizationID, user.ID, limit)
	if err != nil {
		return nil, mapError("list notifications", err)
	}
	defer rows.Close()
	result := make([]domain.Notification, 0)
	for rows.Next() {
		notification, err := scanNotification(rows)
		if err != nil {
			return nil, mapError("scan notification", err)
		}
		result = append(result, notification)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list notifications", err)
	}
	return result, nil
}

func (s *Store) MarkNotificationRead(ctx context.Context, user domain.User, notificationID string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := requireMembership(ctx, tx, user); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
            UPDATE notifications SET status = 'read', read_at = COALESCE(read_at, now())
            WHERE organization_id = $1 AND user_id = $2 AND id = $3`,
			user.OrganizationID, user.ID, notificationID)
		if err != nil {
			return mapError("mark notification read", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("mark notification read: %w", storepkg.ErrNotFound)
		}
		return insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"notification.read", "notification", notificationID, map[string]any{})
	})
}

func (s *Store) MarkAllNotificationsRead(ctx context.Context, user domain.User) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := requireMembership(ctx, tx, user); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
            UPDATE notifications SET status = 'read', read_at = COALESCE(read_at, now())
            WHERE organization_id = $1 AND user_id = $2 AND status = 'unread'`,
			user.OrganizationID, user.ID)
		if err != nil {
			return mapError("mark all notifications read", err)
		}
		return insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"notification.read_all", "notification", "", map[string]any{"count": tag.RowsAffected()})
	})
}

func (s *Store) ListSubagents(ctx context.Context, user domain.User, boxID string) ([]domain.SubagentInstance, error) {
	allowed, err := canReadBox(ctx, s.pool, user, boxID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: box read access required", storepkg.ErrForbidden)
	}
	rows, err := s.pool.Query(ctx, `
        SELECT id, box_id, run_id, runtime_instance_id, external_agent_id,
               COALESCE(parent_external_agent_id, ''), COALESCE(agent_type, ''),
               COALESCE(label, ''), status, COALESCE(session_ref, ''), metadata,
               started_at, finished_at
        FROM subagent_instances
        WHERE organization_id = $1 AND box_id = $2
        ORDER BY started_at, id`, user.OrganizationID, boxID)
	if err != nil {
		return nil, mapError("list subagents", err)
	}
	defer rows.Close()
	result := make([]domain.SubagentInstance, 0)
	for rows.Next() {
		var value domain.SubagentInstance
		var metadata []byte
		if err := rows.Scan(&value.ID, &value.BoxID, &value.RunID, &value.RuntimeInstanceID,
			&value.ExternalAgentID, &value.ParentExternalAgentID, &value.AgentType,
			&value.Label, &value.Status, &value.SessionRef, &metadata,
			&value.StartedAt, &value.FinishedAt); err != nil {
			return nil, mapError("scan subagent", err)
		}
		value.Metadata = json.RawMessage(append([]byte(nil), metadata...))
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list subagents", err)
	}
	return result, nil
}

func (s *Store) ListTodos(ctx context.Context, user domain.User, boxID string) ([]domain.TodoItem, error) {
	allowed, err := canReadBox(ctx, s.pool, user, boxID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: box read access required", storepkg.ErrForbidden)
	}
	rows, err := s.pool.Query(ctx, `
        SELECT id, box_id, COALESCE(run_id::text, ''), runtime_instance_id,
               external_todo_id, COALESCE(phase_name, ''), content, position,
               status, COALESCE(block_reason, ''), updated_at
        FROM todo_items
        WHERE organization_id = $1 AND box_id = $2
        ORDER BY position, updated_at, id`, user.OrganizationID, boxID)
	if err != nil {
		return nil, mapError("list todos", err)
	}
	defer rows.Close()
	result := make([]domain.TodoItem, 0)
	for rows.Next() {
		var value domain.TodoItem
		if err := rows.Scan(&value.ID, &value.BoxID, &value.RunID, &value.RuntimeInstanceID,
			&value.ExternalTodoID, &value.PhaseName, &value.Content, &value.Position,
			&value.Status, &value.BlockReason, &value.UpdatedAt); err != nil {
			return nil, mapError("scan todo", err)
		}
		if value.Status == "completed" {
			completedAt := value.UpdatedAt
			value.CompletedAt = &completedAt
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list todos", err)
	}
	return result, nil
}

func (s *Store) ListArtifacts(ctx context.Context, user domain.User, boxID string) ([]domain.Artifact, error) {
	allowed, err := canReadBox(ctx, s.pool, user, boxID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: box read access required", storepkg.ErrForbidden)
	}
	rows, err := s.pool.Query(ctx, artifactSelect+`
        WHERE a.organization_id = $1 AND a.box_id = $2 AND a.status = 'ready'
        ORDER BY a.created_at DESC, a.id DESC`, user.OrganizationID, boxID)
	if err != nil {
		return nil, mapError("list artifacts", err)
	}
	defer rows.Close()
	result := make([]domain.Artifact, 0)
	for rows.Next() {
		artifact, err := scanArtifact(rows)
		if err != nil {
			return nil, mapError("scan artifact", err)
		}
		artifact.DownloadURL = "/api/v1/boxes/" + artifact.BoxID + "/artifacts/" + artifact.ID + "/download"
		result = append(result, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list artifacts", err)
	}
	return result, nil
}

func (s *Store) GetArtifactForDownload(ctx context.Context, user domain.User, boxID, artifactID string) (domain.Artifact, error) {
	var result domain.Artifact
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		allowed, err := canReadBox(ctx, tx, user, boxID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("%w: box read access required", storepkg.ErrForbidden)
		}
		result, err = scanArtifact(tx.QueryRow(ctx, artifactSelect+`
            WHERE a.organization_id = $1 AND a.box_id = $2 AND a.id = $3
              AND a.status = 'ready'`, user.OrganizationID, boxID, artifactID))
		if err != nil {
			return mapError("get artifact", err)
		}
		if err := validateArtifactPath(result.WorkspacePath, result.HostPath); err != nil {
			return err
		}
		return insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"artifact.download_requested", "artifact", artifactID, map[string]any{"boxId": boxID})
	})
	return result, err
}

func validateArtifactPath(workspacePath, artifactPath string) error {
	workspacePath = filepath.Clean(strings.TrimSpace(workspacePath))
	artifactPath = filepath.Clean(strings.TrimSpace(artifactPath))
	if workspacePath == "." || artifactPath == "." || !filepath.IsAbs(workspacePath) || !filepath.IsAbs(artifactPath) {
		return fmt.Errorf("%w: artifact path is not an absolute workspace path", storepkg.ErrInvalidState)
	}
	relative, err := filepath.Rel(workspacePath, artifactPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: artifact path escapes its workspace", storepkg.ErrForbidden)
	}
	return nil
}

func (s *Store) RequestWorkspaceDiff(ctx context.Context, user domain.User, boxID, baseRef, headRef string) (domain.WorkspaceDiff, *domain.HostCommand, error) {
	allowed, err := canReadBox(ctx, s.pool, user, boxID)
	if err != nil {
		return domain.WorkspaceDiff{}, nil, err
	}
	if !allowed {
		return domain.WorkspaceDiff{}, nil, fmt.Errorf("%w: box read access required", storepkg.ErrForbidden)
	}
	baseRef = strings.TrimSpace(baseRef)
	headRef = strings.TrimSpace(headRef)
	var result domain.WorkspaceDiff
	var resultCommand *domain.HostCommand
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		var organizationID, hostID, workspaceID, workspacePath string
		if err := tx.QueryRow(ctx, `
            SELECT b.organization_id, b.host_id, b.workspace_id, w.real_path
            FROM boxes b
            JOIN workspaces w ON w.id = b.workspace_id AND w.organization_id = b.organization_id
            WHERE b.organization_id = $1 AND b.id = $2
            FOR SHARE OF b, w`, user.OrganizationID, boxID).Scan(
			&organizationID, &hostID, &workspaceID, &workspacePath); err != nil {
			return mapError("load diff workspace", err)
		}

		var requestID, status, storedBase, storedHead, errorMessage string
		var raw []byte
		var generatedAt *time.Time
		var createdAt time.Time
		err := tx.QueryRow(ctx, `
            SELECT id, status, COALESCE(base_ref, ''), COALESCE(head_ref, ''),
                   result, COALESCE(error_message, ''), generated_at, created_at
            FROM workspace_diff_requests
            WHERE box_id = $1 AND COALESCE(base_ref, '') = $2 AND COALESCE(head_ref, '') = $3
            ORDER BY created_at DESC LIMIT 1`, boxID, baseRef, headRef).Scan(
			&requestID, &status, &storedBase, &storedHead, &raw, &errorMessage, &generatedAt, &createdAt)
		if err == nil {
			fresh := status == "pending" || (status == "completed" && time.Since(createdAt) < time.Minute)
			if fresh {
				result, err = decodeWorkspaceDiff(requestID, status, storedBase, storedHead, raw, generatedAt, errorMessage)
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return mapError("load workspace diff", err)
		}

		payload, err := json.Marshal(map[string]any{
			"operation": "git.diff", "workspace": workspacePath,
			"baseRef": baseRef, "headRef": headRef, "includePatch": true,
			"includeUntracked": true,
		})
		if err != nil {
			return fmt.Errorf("encode workspace diff command: %w", err)
		}
		requestID, err = newUUIDv7()
		if err != nil {
			return err
		}
		command, err := insertHostCommandTx(ctx, tx, domain.HostCommand{
			OrganizationID: organizationID, HostID: hostID, BoxID: boxID,
			CommandType: "workspace.git_diff", Payload: payload,
			IdempotencyKey: "workspace:git-diff:" + requestID, Status: "pending",
		})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO workspace_diff_requests(
                id, organization_id, box_id, workspace_id, host_id, command_id,
                requested_by_user_id, status, base_ref, head_ref
            ) VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',$8,$9)`, requestID,
			organizationID, boxID, workspaceID, hostID, command.ID, user.ID,
			nullableText(baseRef), nullableText(headRef)); err != nil {
			return mapError("insert workspace diff request", err)
		}
		if err := insertAudit(ctx, tx, organizationID, "user", user.ID, "",
			"workspace.diff_requested", "workspace", workspaceID,
			map[string]any{"boxId": boxID, "requestId": requestID, "commandId": command.ID, "baseRef": baseRef, "headRef": headRef}); err != nil {
			return err
		}
		result = domain.WorkspaceDiff{RequestID: requestID, Status: "pending", BaseRef: baseRef, HeadRef: headRef}
		resultCommand = &command
		return nil
	})
	return result, resultCommand, err
}

func decodeWorkspaceDiff(requestID, status, baseRef, headRef string, raw []byte, generatedAt *time.Time, errorMessage string) (domain.WorkspaceDiff, error) {
	result := domain.WorkspaceDiff{RequestID: requestID, Status: status, BaseRef: baseRef, HeadRef: headRef, GeneratedAt: generatedAt, Error: errorMessage}
	if len(raw) > 0 {
		var projected domain.WorkspaceDiff
		if err := json.Unmarshal(raw, &projected); err != nil {
			return domain.WorkspaceDiff{}, fmt.Errorf("decode workspace diff result: %w", err)
		}
		if projected.BaseRef != "" {
			result.BaseRef = projected.BaseRef
		}
		if projected.HeadRef != "" {
			result.HeadRef = projected.HeadRef
		}
		if projected.GeneratedAt != nil {
			result.GeneratedAt = projected.GeneratedAt
		}
		result.Files = projected.Files
	}
	return result, nil
}

func scanNotification(row scanner) (domain.Notification, error) {
	var result domain.Notification
	err := row.Scan(&result.ID, &result.OrganizationID, &result.UserID, &result.Type,
		&result.Title, &result.Body, &result.Status, &result.BoxID, &result.RunID,
		&result.ScheduleID, &result.ApprovalID, &result.CreatedAt, &result.ReadAt)
	return result, err
}

func scanArtifact(row scanner) (domain.Artifact, error) {
	var result domain.Artifact
	var metadata []byte
	err := row.Scan(&result.ID, &result.OrganizationID, &result.BoxID, &result.RunID,
		&result.HostID, &result.Kind, &result.Name, &result.MIMEType, &result.SizeBytes,
		&result.SHA256, &result.Status, &metadata, &result.CreatedAt, &result.ExpiresAt,
		&result.HostPath, &result.WorkspacePath)
	if err != nil {
		return domain.Artifact{}, err
	}
	result.Metadata = json.RawMessage(append([]byte(nil), metadata...))
	return result, nil
}

func notifyBoxAudienceTx(ctx context.Context, tx pgx.Tx, organizationID, boxID, notificationType, title, body, dedupeKey, runID, scheduleID, approvalID string) error {
	rows, err := tx.Query(ctx, `
        SELECT DISTINCT audience.user_id
        FROM (
            SELECT om.user_id
            FROM organization_members om
            WHERE om.organization_id = $1 AND om.status = 'active' AND om.role IN ('owner','admin')
            UNION
            SELECT b.owner_user_id FROM boxes b WHERE b.organization_id = $1 AND b.id = $2
            UNION
            SELECT ba.user_id FROM box_acl ba WHERE ba.organization_id = $1 AND ba.box_id = $2 AND ba.user_id IS NOT NULL
            UNION
            SELECT tm.user_id
            FROM box_acl ba
            JOIN team_members tm ON tm.team_id = ba.team_id AND tm.organization_id = ba.organization_id
            WHERE ba.organization_id = $1 AND ba.box_id = $2
        ) audience`, organizationID, boxID)
	if err != nil {
		return mapError("load notification audience", err)
	}
	userIDs := make([]string, 0)
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			rows.Close()
			return mapError("scan notification audience", err)
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return mapError("load notification audience", err)
	}
	rows.Close()
	for _, userID := range userIDs {
		id, err := newUUIDv7()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO notifications(
                id, organization_id, user_id, type, title, body, box_id,
                run_id, schedule_id, approval_id, dedupe_key
            ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
            ON CONFLICT (organization_id, user_id, dedupe_key) DO NOTHING`,
			id, organizationID, userID, notificationType, title, body,
			nullableUUID(boxID), nullableUUID(runID), nullableUUID(scheduleID),
			nullableUUID(approvalID), dedupeKey); err != nil {
			return mapError("insert notification", err)
		}
	}
	return nil
}

func notifyHostOfflineTx(ctx context.Context, tx pgx.Tx, organizationID, hostID, hostName, dedupeKey string) error {
	rows, err := tx.Query(ctx, `
        SELECT DISTINCT audience.user_id
        FROM (
            SELECT om.user_id
            FROM organization_members om
            WHERE om.organization_id = $1 AND om.status = 'active' AND om.role IN ('owner','admin')
            UNION
            SELECT b.owner_user_id FROM boxes b WHERE b.organization_id = $1 AND b.host_id = $2
            UNION
            SELECT ba.user_id
            FROM box_acl ba JOIN boxes b ON b.id = ba.box_id AND b.organization_id = ba.organization_id
            WHERE ba.organization_id = $1 AND b.host_id = $2 AND ba.user_id IS NOT NULL
            UNION
            SELECT tm.user_id
            FROM box_acl ba
            JOIN boxes b ON b.id = ba.box_id AND b.organization_id = ba.organization_id
            JOIN team_members tm ON tm.team_id = ba.team_id AND tm.organization_id = ba.organization_id
            WHERE ba.organization_id = $1 AND b.host_id = $2
        ) audience`, organizationID, hostID)
	if err != nil {
		return mapError("load host notification audience", err)
	}
	userIDs := make([]string, 0)
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			rows.Close()
			return mapError("scan host notification audience", err)
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return mapError("load host notification audience", err)
	}
	rows.Close()
	for _, userID := range userIDs {
		id, err := newUUIDv7()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO notifications(id, organization_id, user_id, type, title, body, dedupe_key)
            VALUES ($1,$2,$3,'host_offline','Host offline',$4,$5)
            ON CONFLICT (organization_id, user_id, dedupe_key) DO NOTHING`,
			id, organizationID, userID, fmt.Sprintf("Host %s is offline.", hostName), dedupeKey); err != nil {
			return mapError("insert host offline notification", err)
		}
	}
	return nil
}
