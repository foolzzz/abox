package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agentbox/internal/domain"
	storepkg "agentbox/internal/store"

	"github.com/jackc/pgx/v5"
)

const boxSelect = `
    SELECT b.id, b.organization_id, b.name, b.agent_id, b.agent_version_id,
           b.host_id, b.workspace_id, b.owner_user_id, b.runtime_type,
           b.status, b.version, b.next_event_seq - 1, b.created_at, b.updated_at
    FROM boxes b`

func (s *Store) ListBoxes(ctx context.Context, user domain.User) ([]domain.Box, error) {
	role, err := requireMembership(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, boxSelect+`
        WHERE b.organization_id = $1 AND b.status <> 'terminated'
          AND (
              $3 = 'admin'
              OR b.owner_user_id = $2
              OR b.visibility = 'org'
              OR EXISTS (
                  SELECT 1 FROM box_acl ba
                  WHERE ba.box_id = b.id AND ba.user_id = $2
              )
              OR EXISTS (
                  SELECT 1 FROM box_acl ba
                  JOIN team_members tm ON tm.team_id = ba.team_id AND tm.organization_id = ba.organization_id
                  WHERE ba.box_id = b.id AND tm.user_id = $2
              )
          )
        ORDER BY b.updated_at DESC, b.id`, user.OrganizationID, user.ID, role)
	if err != nil {
		return nil, mapError("list boxes", err)
	}
	defer rows.Close()
	result := make([]domain.Box, 0)
	for rows.Next() {
		box, err := scanBox(rows)
		if err != nil {
			return nil, mapError("scan box", err)
		}
		result = append(result, box)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list boxes", err)
	}
	return result, nil
}

func (s *Store) CreateBox(ctx context.Context, user domain.User, input domain.CreateBoxInput) (domain.Box, error) {
	if strings.TrimSpace(input.Name) == "" || input.AgentID == "" || input.HostID == "" || input.WorkspaceID == "" {
		return domain.Box{}, fmt.Errorf("%w: name, agent, host, and workspace are required", storepkg.ErrInvalidState)
	}
	var result domain.Box
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		allowed, err := canOperateWorkspace(ctx, tx, user, input.WorkspaceID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("%w: workspace operator access required", storepkg.ErrForbidden)
		}
		var agentVersionID, runtimeType string
		var idleTimeout int
		err = tx.QueryRow(ctx, `
            SELECT av.id, av.runtime_type, av.idle_timeout_seconds
            FROM agents a
            JOIN agent_versions av ON av.id = a.published_version_id AND av.agent_id = a.id
            WHERE a.organization_id = $1 AND a.id = $2
              AND a.status = 'active' AND av.lifecycle_status = 'published'
            FOR SHARE OF a, av`, user.OrganizationID, input.AgentID).Scan(
			&agentVersionID, &runtimeType, &idleTimeout,
		)
		if err != nil {
			return mapError("get box agent version", err)
		}

		var hostStatus domain.HostStatus
		var maxActiveBoxes int
		var runtimeAvailable bool
		err = tx.QueryRow(ctx, `
            SELECT h.status, h.max_active_boxes,
                   EXISTS (
                       SELECT 1 FROM host_runtime_capabilities c
                       WHERE c.host_id = h.id AND c.runtime_name = $3 AND c.status = 'available'
                   )
            FROM hosts h
            WHERE h.organization_id = $1 AND h.id = $2
            FOR SHARE`, user.OrganizationID, input.HostID, runtimeType).Scan(
			&hostStatus, &maxActiveBoxes, &runtimeAvailable,
		)
		if err != nil {
			return mapError("get box host", err)
		}
		if hostStatus != domain.HostOnline {
			return fmt.Errorf("%w: host is %s", storepkg.ErrHostOffline, hostStatus)
		}
		if !runtimeAvailable {
			return fmt.Errorf("%w: runtime %s is unavailable", storepkg.ErrRuntimeMissing, runtimeType)
		}
		var activeCount int
		err = tx.QueryRow(ctx, `
            SELECT count(*)
            FROM runtime_instances
            WHERE host_id = $1 AND status IN ('starting', 'ready', 'busy', 'stopping')`,
			input.HostID).Scan(&activeCount)
		if err != nil {
			return mapError("count active host runtimes", err)
		}
		if activeCount >= maxActiveBoxes {
			return fmt.Errorf("%w: host runtime capacity reached", storepkg.ErrConflict)
		}

		var workspaceHostID, workspaceStatus string
		err = tx.QueryRow(ctx, `
            SELECT host_id, status
            FROM workspaces
            WHERE organization_id = $1 AND id = $2
            FOR SHARE`, user.OrganizationID, input.WorkspaceID).Scan(&workspaceHostID, &workspaceStatus)
		if err != nil {
			return mapError("get box workspace", err)
		}
		if workspaceHostID != input.HostID || workspaceStatus != "ready" {
			return fmt.Errorf("%w: workspace is not ready on the selected host", storepkg.ErrInvalidState)
		}

		boxID, err := newUUIDv7()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
            INSERT INTO boxes(
                id, organization_id, name, agent_id, agent_version_id,
                host_id, workspace_id, owner_user_id, visibility, status,
                runtime_type, idle_timeout_seconds
            ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'private','created',$9,$10)`,
			boxID, user.OrganizationID, strings.TrimSpace(input.Name), input.AgentID,
			agentVersionID, input.HostID, input.WorkspaceID, user.ID, runtimeType, idleTimeout,
		)
		if err != nil {
			return mapError("insert box", err)
		}
		if err := insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"box.created", "box", boxID,
			map[string]any{"agentId": input.AgentID, "agentVersionId": agentVersionID, "hostId": input.HostID, "workspaceId": input.WorkspaceID}); err != nil {
			return err
		}
		result, err = getBoxByOrganization(ctx, tx, user.OrganizationID, boxID)
		return err
	})
	return result, err
}

func (s *Store) DeleteBox(ctx context.Context, user domain.User, boxID string) (*domain.HostCommand, error) {
	if strings.TrimSpace(boxID) == "" {
		return nil, fmt.Errorf("%w: box id is required", storepkg.ErrInvalidState)
	}
	var command *domain.HostCommand
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		role, err := requireMembership(ctx, tx, user)
		if err != nil {
			return err
		}
		var organizationID, hostID, ownerUserID, status string
		var version int64
		err = tx.QueryRow(ctx, `
            SELECT organization_id, host_id, owner_user_id, status, version
            FROM boxes
            WHERE organization_id = $1 AND id = $2
            FOR UPDATE`, user.OrganizationID, boxID).Scan(
			&organizationID, &hostID, &ownerUserID, &status, &version,
		)
		if err != nil {
			return mapError("lock box for deletion", err)
		}
		if role != "admin" && ownerUserID != user.ID {
			return fmt.Errorf("%w: only the box owner or an organization admin can delete this session", storepkg.ErrForbidden)
		}
		if status == string(domain.BoxTerminated) {
			return nil
		}

		var runtimeID string
		if err := tx.QueryRow(ctx, `
            SELECT COALESCE((
                SELECT id::text FROM runtime_instances
                WHERE box_id = $1 AND status IN ('starting','ready','busy','stopping')
                ORDER BY started_at DESC NULLS LAST, id
                LIMIT 1
            ), '')`, boxID).Scan(&runtimeID); err != nil {
			return mapError("find box runtime for deletion", err)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE runs
            SET status = 'cancelled', finished_at = now(), terminal_reason = 'box_deleted', version = version + 1
            WHERE box_id = $1
              AND status IN ('queued','dispatching','running','waiting_approval','interrupting','disconnected')`, boxID); err != nil {
			return mapError("cancel deleted box runs", err)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE messages SET status = 'cancelled'
            WHERE box_id = $1 AND status IN ('queued','dispatched')`, boxID); err != nil {
			return mapError("cancel deleted box messages", err)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE approvals
            SET status = 'cancelled', resolved_at = now(),
                decision_payload = jsonb_build_object('decision','denied','reason','box_deleted'),
                version = version + 1
            WHERE box_id = $1 AND status = 'pending'`, boxID); err != nil {
			return mapError("cancel deleted box approvals", err)
		}
		if runtimeID != "" {
			if _, err := tx.Exec(ctx, `
                UPDATE runtime_instances SET status = 'stopping', version = version + 1
                WHERE id = $1 AND status IN ('starting','ready','busy')`, runtimeID); err != nil {
				return mapError("stop deleted box runtime", err)
			}
			created, err := insertHostCommandTx(ctx, tx, domain.HostCommand{
				OrganizationID:    organizationID,
				HostID:            hostID,
				BoxID:             boxID,
				RuntimeInstanceID: runtimeID,
				CommandType:       "runtime.stop",
				Payload:           json.RawMessage(`{"mode":"force"}`),
				IdempotencyKey:    fmt.Sprintf("runtime:delete:%s:%d", boxID, version),
				Status:            "pending",
			})
			if err != nil {
				return err
			}
			command = &created
		}
		if _, err := tx.Exec(ctx, `
            UPDATE boxes
            SET status = 'terminated', terminated_at = COALESCE(terminated_at, now()),
                updated_at = now(), last_activity_at = now(), version = version + 1
            WHERE id = $1`, boxID); err != nil {
			return mapError("terminate deleted box", err)
		}
		return insertAudit(ctx, tx, organizationID, "user", user.ID, "",
			"box.deleted", "box", boxID, map[string]any{"runtimeInstanceId": runtimeID})
	})
	return command, err
}

func (s *Store) GetBox(ctx context.Context, user domain.User, id string) (domain.Box, error) {
	allowed, err := canReadBox(ctx, s.pool, user, id)
	if err != nil {
		return domain.Box{}, err
	}
	if !allowed {
		var exists bool
		if err := s.pool.QueryRow(ctx, `
            SELECT EXISTS(SELECT 1 FROM boxes WHERE organization_id = $1 AND id = $2 AND status <> 'terminated')`,
			user.OrganizationID, id).Scan(&exists); err != nil {
			return domain.Box{}, mapError("check box existence", err)
		}
		if exists {
			return domain.Box{}, fmt.Errorf("%w: box access required", storepkg.ErrForbidden)
		}
		return domain.Box{}, fmt.Errorf("get box: %w", storepkg.ErrNotFound)
	}
	return getBoxByOrganization(ctx, s.pool, user.OrganizationID, id)
}

func (s *Store) GetBoxForHost(ctx context.Context, hostID, boxID string) (domain.Box, error) {
	result, err := scanBox(s.pool.QueryRow(ctx, boxSelect+`
        WHERE b.host_id = $1 AND b.id = $2`, hostID, boxID))
	if err != nil {
		return domain.Box{}, mapError("get box for host", err)
	}
	return result, nil
}

func getBoxByOrganization(ctx context.Context, q querier, organizationID, id string) (domain.Box, error) {
	result, err := scanBox(q.QueryRow(ctx, boxSelect+`
        WHERE b.organization_id = $1 AND b.id = $2 AND b.status <> 'terminated'`, organizationID, id))
	if err != nil {
		return domain.Box{}, mapError("get box", err)
	}
	return result, nil
}

func scanBox(row scanner) (domain.Box, error) {
	var result domain.Box
	err := row.Scan(
		&result.ID, &result.OrganizationID, &result.Name, &result.AgentID,
		&result.AgentVersionID, &result.HostID, &result.WorkspaceID,
		&result.OwnerUserID, &result.RuntimeType, &result.Status,
		&result.Version, &result.LastEventSeq, &result.CreatedAt, &result.UpdatedAt,
	)
	return result, err
}

func canReadBox(ctx context.Context, q querier, user domain.User, boxID string) (bool, error) {
	role, err := requireMembership(ctx, q, user)
	if err != nil {
		return false, err
	}
	var allowed bool
	err = q.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1
            FROM boxes b
            WHERE b.organization_id = $1 AND b.id = $2 AND b.status <> 'terminated'
              AND (
                  $4 = 'admin'
                  OR b.owner_user_id = $3
                  OR b.visibility = 'org'
                  OR EXISTS (
                      SELECT 1 FROM box_acl ba
                      WHERE ba.box_id = b.id AND ba.user_id = $3
                  )
                  OR EXISTS (
                      SELECT 1 FROM box_acl ba
                      JOIN team_members tm ON tm.team_id = ba.team_id AND tm.organization_id = ba.organization_id
                      WHERE ba.box_id = b.id AND tm.user_id = $3
                  )
              )
        )`, user.OrganizationID, boxID, user.ID, role).Scan(&allowed)
	if err != nil {
		return false, mapError("check box read acl", err)
	}
	return allowed, nil
}

func canOperateBox(ctx context.Context, q querier, user domain.User, boxID string) (bool, error) {
	role, err := requireMembership(ctx, q, user)
	if err != nil {
		return false, err
	}
	var allowed bool
	err = q.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1
            FROM boxes b
            WHERE b.organization_id = $1 AND b.id = $2 AND b.status <> 'terminated'
              AND (
                  $4 = 'admin'
                  OR b.owner_user_id = $3
                  OR EXISTS (
                      SELECT 1 FROM box_acl ba
                      WHERE ba.box_id = b.id AND ba.user_id = $3
                        AND ba.role IN ('owner', 'operator')
                  )
                  OR EXISTS (
                      SELECT 1 FROM box_acl ba
                      JOIN team_members tm ON tm.team_id = ba.team_id AND tm.organization_id = ba.organization_id
                      WHERE ba.box_id = b.id AND tm.user_id = $3
                        AND ba.role IN ('owner', 'operator')
                  )
              )
        )`, user.OrganizationID, boxID, user.ID, role).Scan(&allowed)
	if err != nil {
		return false, mapError("check box operator acl", err)
	}
	return allowed, nil
}

func (s *Store) SetBoxStatus(ctx context.Context, boxID string, from []domain.BoxStatus, to domain.BoxStatus) error {
	if boxID == "" || len(from) == 0 || to == "" {
		return fmt.Errorf("%w: box id, source statuses, and target status are required", storepkg.ErrInvalidState)
	}
	source := make([]string, len(from))
	for i := range from {
		source[i] = string(from[i])
	}
	tag, err := s.pool.Exec(ctx, `
        UPDATE boxes
        SET status = $3,
            version = version + 1,
            updated_at = now(),
            last_activity_at = now(),
            terminated_at = CASE WHEN $3 = 'terminated' THEN now() ELSE terminated_at END
        WHERE id = $1 AND status = ANY($2::text[])`, boxID, source, string(to))
	if err != nil {
		return mapError("transition box status", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM boxes WHERE id = $1)`, boxID).Scan(&exists); err != nil {
		return mapError("check box transition", err)
	}
	if !exists {
		return fmt.Errorf("transition box status: %w", storepkg.ErrNotFound)
	}
	return fmt.Errorf("%w: box is not in an allowed source status", storepkg.ErrInvalidState)
}
