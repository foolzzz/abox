package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"agentbox/internal/domain"
	storepkg "agentbox/internal/store"

	"github.com/jackc/pgx/v5"
)

const hostSelect = `
    SELECT h.id, h.organization_id, h.name, h.slug, h.status,
           COALESCE(h.system_hostname, ''), COALESCE(h.os, ''),
           COALESCE(h.arch, ''), COALESCE(h.daemon_version, ''), h.labels,
           h.last_seen_at, h.max_active_boxes,
           COALESCE(ARRAY(
               SELECT c.runtime_name
               FROM host_runtime_capabilities c
               WHERE c.host_id = h.id AND c.status = 'available'
               ORDER BY c.runtime_name
           ), ARRAY[]::text[])
    FROM hosts h`

const workspaceSelect = `
    SELECT w.id, w.organization_id, w.host_id, w.name, w.real_path,
           w.kind, w.status, w.created_at
    FROM workspaces w`

func (s *Store) ListHosts(ctx context.Context, user domain.User) ([]domain.Host, error) {
	if _, err := requireMembership(ctx, s.pool, user); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, hostSelect+`
        WHERE h.organization_id = $1 AND h.status <> 'revoked'
        ORDER BY h.name, h.id`, user.OrganizationID)
	if err != nil {
		return nil, mapError("list hosts", err)
	}
	defer rows.Close()
	result := make([]domain.Host, 0)
	for rows.Next() {
		host, err := scanHost(rows)
		if err != nil {
			return nil, mapError("scan host", err)
		}
		result = append(result, host)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list hosts", err)
	}
	return result, nil
}

func (s *Store) DeleteHost(ctx context.Context, user domain.User, hostID string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := requireRole(ctx, tx, user, "admin"); err != nil {
			return err
		}
		var organizationID, name string
		var status domain.HostStatus
		if err := tx.QueryRow(ctx, `
            SELECT organization_id, name, status FROM hosts
            WHERE organization_id = $1 AND id = $2
            FOR UPDATE`, user.OrganizationID, hostID).Scan(&organizationID, &name, &status); err != nil {
			return mapError("lock host for deletion", err)
		}
		if status == domain.HostRevoked {
			return nil
		}
		if status != domain.HostOffline {
			return fmt.Errorf("%w: host must be offline before deletion", storepkg.ErrConflict)
		}
		var hasActiveBoxes, hasWorkspaces bool
		if err := tx.QueryRow(ctx, `
            SELECT
                EXISTS(SELECT 1 FROM boxes WHERE organization_id = $1 AND host_id = $2 AND status <> 'terminated'),
                EXISTS(SELECT 1 FROM workspaces WHERE organization_id = $1 AND host_id = $2 AND status <> 'archived')`,
			user.OrganizationID, hostID).Scan(&hasActiveBoxes, &hasWorkspaces); err != nil {
			return mapError("check host dependencies", err)
		}
		if hasActiveBoxes || hasWorkspaces {
			return fmt.Errorf("%w: host is referenced by active boxes or non-archived workspaces", storepkg.ErrConflict)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE hosts
            SET status = 'revoked', current_daemon_instance_id = NULL,
                revoked_at = now(), updated_at = now(), version = version + 1
            WHERE id = $1`, hostID); err != nil {
			return mapError("revoke deleted host", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE host_credentials SET status = 'revoked', revoked_at = now() WHERE host_id = $1 AND status <> 'revoked'`, hostID); err != nil {
			return mapError("revoke host credentials", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE host_runtime_capabilities SET status = 'unavailable', last_checked_at = now() WHERE host_id = $1`, hostID); err != nil {
			return mapError("disable host runtimes", err)
		}
		return insertAudit(ctx, tx, organizationID, "user", user.ID, "",
			"host.deleted", "host", hostID, map[string]any{"name": name})
	})
}

func (s *Store) UpsertHost(ctx context.Context, host domain.Host, daemonInstanceID string, lastAck uint64) (domain.Host, error) {
	if host.ID == "" || host.OrganizationID == "" || daemonInstanceID == "" {
		return domain.Host{}, fmt.Errorf("%w: host id, organization id, and daemon instance id are required", storepkg.ErrInvalidState)
	}
	ack, err := ackValue(lastAck)
	if err != nil {
		return domain.Host{}, err
	}
	labels, err := jsonValue(host.Labels, "{}")
	if err != nil {
		return domain.Host{}, err
	}
	if host.Name == "" {
		host.Name = host.ID
	}
	if host.Slug == "" {
		host.Slug = slugify(host.Name)
	}
	if host.Status == "" || host.Status == domain.HostEnrolling {
		host.Status = domain.HostOnline
	}
	if host.MaxActiveBoxes == 0 {
		host.MaxActiveBoxes = 4
	}

	var result domain.Host
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		var previousStatus domain.HostStatus
		previousErr := tx.QueryRow(ctx, `SELECT status FROM hosts WHERE id = $1 AND organization_id = $2 FOR UPDATE`, host.ID, host.OrganizationID).Scan(&previousStatus)
		if previousErr != nil && !errors.Is(previousErr, pgx.ErrNoRows) {
			return mapError("lock host state", previousErr)
		}
		var creatorID string
		err := tx.QueryRow(ctx, `
            SELECT user_id
            FROM organization_members
            WHERE organization_id = $1 AND status = 'active' AND role = 'admin'
            ORDER BY joined_at
            LIMIT 1`, host.OrganizationID).Scan(&creatorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: host organization has no active administrator", storepkg.ErrForbidden)
		}
		if err != nil {
			return mapError("find host creator", err)
		}
		tag, err := tx.Exec(ctx, `
            INSERT INTO hosts(
                id, organization_id, slug, name, status, system_hostname, os, arch, daemon_version,
                current_daemon_instance_id, last_acked_host_seq, labels,
                max_active_boxes, last_seen_at, created_by_user_id
            ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,now(),$14)
            ON CONFLICT (id) DO UPDATE SET
                slug = EXCLUDED.slug,
                name = EXCLUDED.name,
                status = CASE WHEN hosts.status = 'revoked' THEN hosts.status ELSE EXCLUDED.status END,
				system_hostname = EXCLUDED.system_hostname,
                os = EXCLUDED.os,
                arch = EXCLUDED.arch,
                daemon_version = EXCLUDED.daemon_version,
                last_acked_host_seq = CASE
                    WHEN hosts.current_daemon_instance_id IS DISTINCT FROM EXCLUDED.current_daemon_instance_id
                    THEN EXCLUDED.last_acked_host_seq
                    ELSE GREATEST(hosts.last_acked_host_seq, EXCLUDED.last_acked_host_seq)
                END,
                current_daemon_instance_id = EXCLUDED.current_daemon_instance_id,
                labels = EXCLUDED.labels,
                max_active_boxes = EXCLUDED.max_active_boxes,
                last_seen_at = now(),
                updated_at = now(),
                version = hosts.version + 1
            WHERE hosts.organization_id = EXCLUDED.organization_id`,
			host.ID, host.OrganizationID, host.Slug, host.Name, host.Status,
			nullableText(host.SystemHostname), nullableText(host.OS), nullableText(host.Arch), nullableText(host.DaemonVersion),
			daemonInstanceID, ack, labels, host.MaxActiveBoxes, creatorID,
		)
		if err != nil {
			return mapError("upsert host", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: host id belongs to another organization", storepkg.ErrConflict)
		}

		if _, err := tx.Exec(ctx, `
            UPDATE host_runtime_capabilities
            SET status = 'unavailable', last_checked_at = now(), error_message = NULL
            WHERE host_id = $1`, host.ID); err != nil {
			return mapError("reset host runtime capabilities", err)
		}
		for _, runtimeName := range host.Runtimes {
			runtimeName = strings.TrimSpace(runtimeName)
			if runtimeName == "" {
				continue
			}
			_, err := tx.Exec(ctx, `
                INSERT INTO host_runtime_capabilities(
                    organization_id, host_id, runtime_name, capabilities, status,
                    discovered_at, last_checked_at
                ) VALUES ($1,$2,$3,'{}'::jsonb,'available',now(),now())
                ON CONFLICT (host_id, runtime_name) DO UPDATE SET
                    organization_id = EXCLUDED.organization_id,
                    status = 'available', last_checked_at = now(), error_message = NULL`,
				host.OrganizationID, host.ID, runtimeName)
			if err != nil {
				return mapError("upsert host runtime capability", err)
			}
		}
		if host.Status == domain.HostOffline && previousStatus != domain.HostOffline {
			if _, err := tx.Exec(ctx, `
                UPDATE runs r
                SET status = 'disconnected', version = r.version + 1
                FROM boxes b
                WHERE r.box_id = b.id AND b.host_id = $1
                  AND r.status IN ('dispatching','running','interrupting')`, host.ID); err != nil {
				return mapError("disconnect host runs", err)
			}
			if err := notifyHostOfflineTx(ctx, tx, host.OrganizationID, host.ID, host.Name,
				"host-offline:"+host.ID+":"+daemonInstanceID); err != nil {
				return err
			}
			if err := insertAudit(ctx, tx, host.OrganizationID, "daemon", "", host.ID,
				"host.offline", "host", host.ID,
				map[string]any{"daemonInstanceId": daemonInstanceID}); err != nil {
				return err
			}
		} else if host.Status == domain.HostOnline && previousStatus == domain.HostOffline {
			if err := insertAudit(ctx, tx, host.OrganizationID, "daemon", "", host.ID,
				"host.online", "host", host.ID,
				map[string]any{"daemonInstanceId": daemonInstanceID}); err != nil {
				return err
			}
		}
		result, err = getHost(ctx, tx, host.OrganizationID, host.ID)
		return err
	})
	return result, err
}

func (s *Store) TouchHost(ctx context.Context, hostID, daemonInstanceID string, lastAck uint64) error {
	if hostID == "" || daemonInstanceID == "" {
		return fmt.Errorf("%w: host and daemon instance ids are required", storepkg.ErrInvalidState)
	}
	ack, err := ackValue(lastAck)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
        UPDATE hosts
        SET status = CASE WHEN status = 'revoked' THEN status ELSE 'online' END,
            last_acked_host_seq = GREATEST(last_acked_host_seq, $3),
            last_seen_at = now(), updated_at = now(), version = version + 1
        WHERE id = $1 AND current_daemon_instance_id = $2`, hostID, daemonInstanceID, ack)
	if err != nil {
		return mapError("touch host", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var current *string
	err = s.pool.QueryRow(ctx, `SELECT current_daemon_instance_id::text FROM hosts WHERE id = $1`, hostID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("touch host: %w", storepkg.ErrNotFound)
	}
	if err != nil {
		return mapError("read host daemon instance", err)
	}
	return fmt.Errorf("%w: stale daemon instance", storepkg.ErrConflict)
}

func (s *Store) GetHost(ctx context.Context, user domain.User, id string) (domain.Host, error) {
	if _, err := requireMembership(ctx, s.pool, user); err != nil {
		return domain.Host{}, err
	}
	return getHost(ctx, s.pool, user.OrganizationID, id)
}

func (s *Store) GetHostForOrganization(ctx context.Context, organizationID, id string) (domain.Host, error) {
	return getHost(ctx, s.pool, organizationID, id)
}

func getHost(ctx context.Context, q querier, organizationID, id string) (domain.Host, error) {
	result, err := scanHost(q.QueryRow(ctx, hostSelect+`
        WHERE h.organization_id = $1 AND h.id = $2`, organizationID, id))
	if err != nil {
		return domain.Host{}, mapError("get host", err)
	}
	return result, nil
}

func scanHost(row scanner) (domain.Host, error) {
	var result domain.Host
	var labels []byte
	err := row.Scan(
		&result.ID, &result.OrganizationID, &result.Name, &result.Slug, &result.Status,
		&result.SystemHostname, &result.OS, &result.Arch, &result.DaemonVersion, &labels,
		&result.LastSeenAt, &result.MaxActiveBoxes, &result.Runtimes,
	)
	if err != nil {
		return domain.Host{}, err
	}
	result.Labels = json.RawMessage(append([]byte(nil), labels...))
	return result, nil
}

func (s *Store) ListWorkspaces(ctx context.Context, user domain.User) ([]domain.Workspace, error) {
	role, err := requireMembership(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, workspaceSelect+`
        WHERE w.organization_id = $1 AND w.status <> 'archived'
          AND (
              $3 = 'admin'
              OR EXISTS (
                  SELECT 1 FROM workspace_acl wa
                  WHERE wa.workspace_id = w.id AND wa.user_id = $2
              )
              OR EXISTS (
                  SELECT 1 FROM workspace_acl wa
                  JOIN team_members tm ON tm.team_id = wa.team_id AND tm.organization_id = wa.organization_id
                  WHERE wa.workspace_id = w.id AND tm.user_id = $2
              )
          )
        ORDER BY w.name, w.id`, user.OrganizationID, user.ID, role)
	if err != nil {
		return nil, mapError("list workspaces", err)
	}
	defer rows.Close()
	result := make([]domain.Workspace, 0)
	for rows.Next() {
		workspace, err := scanWorkspace(rows)
		if err != nil {
			return nil, mapError("scan workspace", err)
		}
		result = append(result, workspace)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list workspaces", err)
	}
	return result, nil
}

func (s *Store) CreateWorkspace(ctx context.Context, user domain.User, input domain.CreateWorkspaceInput) (domain.Workspace, *domain.HostCommand, error) {
	input.Path = filepath.Clean(strings.TrimSpace(input.Path))
	input.Name = strings.TrimSpace(input.Name)
	if input.HostID == "" || input.Name == "" || input.Path == "" {
		return domain.Workspace{}, nil, fmt.Errorf("%w: host, name, and path are required", storepkg.ErrInvalidState)
	}
	if !filepath.IsAbs(input.Path) {
		return domain.Workspace{}, nil, fmt.Errorf("%w: workspace path must be absolute", storepkg.ErrInvalidState)
	}
	if input.Kind == "" {
		input.Kind = "existing"
	}
	var result domain.Workspace
	var command *domain.HostCommand
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := requireRole(ctx, tx, user, "admin"); err != nil {
			return err
		}
		var hostStatus domain.HostStatus
		var hostLabels []byte
		err := tx.QueryRow(ctx, `
            SELECT status, labels FROM hosts
            WHERE organization_id = $1 AND id = $2
            FOR SHARE`, user.OrganizationID, input.HostID).Scan(&hostStatus, &hostLabels)
		if err != nil {
			return mapError("get workspace host", err)
		}
		if hostStatus != domain.HostOnline {
			return fmt.Errorf("%w: host is %s", storepkg.ErrHostOffline, hostStatus)
		}
		if !workspacePathAdvertised(hostLabels, input.Path) {
			return fmt.Errorf("%w: path is outside the host user home", storepkg.ErrInvalidState)
		}

		var workspaceID, workspaceStatus string
		err = tx.QueryRow(ctx, `
            SELECT id, status FROM workspaces
            WHERE organization_id = $1 AND host_id = $2 AND real_path = $3
            FOR UPDATE`, user.OrganizationID, input.HostID, input.Path).Scan(&workspaceID, &workspaceStatus)
		if err == nil {
			result, err = getWorkspace(ctx, tx, user.OrganizationID, workspaceID)
			if err != nil || workspaceStatus == "ready" || workspaceStatus == "provisioning" {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE workspaces SET status = 'provisioning', updated_at = now(), version = version + 1 WHERE id = $1`, workspaceID); err != nil {
				return mapError("retry workspace validation", err)
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return mapError("find workspace by path", err)
		} else {
			rootID, err := newUUIDv7()
			if err != nil {
				return err
			}
			rootMode := "both"
			if input.Kind == "existing" {
				rootMode = "existing"
			}
			err = tx.QueryRow(ctx, `
                INSERT INTO host_workspace_roots(
                    id, organization_id, host_id, display_path, real_path, mode, enabled
                ) VALUES ($1,$2,$3,$4,$4,$5,true)
                ON CONFLICT (host_id, real_path) DO UPDATE
                SET enabled = true, updated_at = now()
                RETURNING id`, rootID, user.OrganizationID, input.HostID, input.Path, rootMode).Scan(&rootID)
			if err != nil {
				return mapError("ensure workspace root", err)
			}

			workspaceID, err = newUUIDv7()
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `
                INSERT INTO workspaces(
                    id, organization_id, host_id, workspace_root_id, name, kind,
                    display_path, real_path, status, created_by_user_id
                ) VALUES ($1,$2,$3,$4,$5,$6,$7,$7,'provisioning',$8)`,
				workspaceID, user.OrganizationID, input.HostID, rootID,
				input.Name, input.Kind, input.Path, user.ID); err != nil {
				return mapError("insert workspace", err)
			}
			aclID, err := newUUIDv7()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
                INSERT INTO workspace_acl(
                    id, organization_id, workspace_id, user_id, role, created_by_user_id
                ) VALUES ($1,$2,$3,$4,'owner',$4)`,
				aclID, user.OrganizationID, workspaceID, user.ID); err != nil {
				return mapError("insert workspace owner acl", err)
			}
		}

		payload, err := json.Marshal(map[string]string{"workspaceId": workspaceID, "path": input.Path})
		if err != nil {
			return fmt.Errorf("encode workspace validation command: %w", err)
		}
		attemptID, err := newUUIDv7()
		if err != nil {
			return err
		}
		createdCommand, err := insertHostCommandTx(ctx, tx, domain.HostCommand{
			OrganizationID: user.OrganizationID,
			HostID:         input.HostID,
			CommandType:    "workspace.validate",
			Payload:        payload,
			IdempotencyKey: "workspace:validate:" + workspaceID + ":" + attemptID,
			Status:         "pending",
		})
		if err != nil {
			return err
		}
		command = &createdCommand
		if err := insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"workspace.validation_requested", "workspace", workspaceID,
			map[string]any{"hostId": input.HostID, "path": input.Path}); err != nil {
			return err
		}
		result, err = getWorkspace(ctx, tx, user.OrganizationID, workspaceID)
		return err
	})
	return result, command, err
}

func (s *Store) DeleteWorkspace(ctx context.Context, user domain.User, workspaceID string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := requireRole(ctx, tx, user, "admin"); err != nil {
			return err
		}
		var organizationID, rootID, name, status string
		if err := tx.QueryRow(ctx, `
            SELECT organization_id, workspace_root_id, name, status
            FROM workspaces
            WHERE organization_id = $1 AND id = $2
            FOR UPDATE`, user.OrganizationID, workspaceID).Scan(
			&organizationID, &rootID, &name, &status,
		); err != nil {
			return mapError("lock workspace for deletion", err)
		}
		if status == "archived" {
			return nil
		}
		var hasActiveBoxes bool
		if err := tx.QueryRow(ctx, `
            SELECT EXISTS(
                SELECT 1 FROM boxes
                WHERE organization_id = $1 AND workspace_id = $2 AND status <> 'terminated'
            )`, user.OrganizationID, workspaceID).Scan(&hasActiveBoxes); err != nil {
			return mapError("check workspace dependencies", err)
		}
		if hasActiveBoxes {
			return fmt.Errorf("%w: workspace is referenced by active boxes", storepkg.ErrConflict)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE workspaces
            SET status = 'archived', archived_at = now(), updated_at = now(), version = version + 1
            WHERE id = $1`, workspaceID); err != nil {
			return mapError("archive workspace", err)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE host_workspace_roots root
            SET enabled = false, updated_at = now()
            WHERE root.id = $1
              AND NOT EXISTS (
                  SELECT 1 FROM workspaces workspace
                  WHERE workspace.workspace_root_id = root.id AND workspace.status <> 'archived'
              )`, rootID); err != nil {
			return mapError("disable unused workspace root", err)
		}
		return insertAudit(ctx, tx, organizationID, "user", user.ID, "",
			"workspace.deleted", "workspace", workspaceID, map[string]any{"name": name})
	})
}

func workspacePathAdvertised(labels []byte, candidate string) bool {
	var value struct {
		WorkspaceRoots []struct {
			Path     string `json:"path"`
			RealPath string `json:"real_path"`
		} `json:"workspaceRoots"`
	}
	if json.Unmarshal(labels, &value) != nil {
		return false
	}
	for _, root := range value.WorkspaceRoots {
		rootPath := root.RealPath
		if rootPath == "" {
			rootPath = root.Path
		}
		relative, err := filepath.Rel(filepath.Clean(rootPath), candidate)
		if err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
			return true
		}
	}
	return false
}

func (s *Store) GetWorkspace(ctx context.Context, user domain.User, id string) (domain.Workspace, error) {
	allowed, err := canReadWorkspace(ctx, s.pool, user, id)
	if err != nil {
		return domain.Workspace{}, err
	}
	if !allowed {
		return domain.Workspace{}, fmt.Errorf("%w: workspace read access required", storepkg.ErrForbidden)
	}
	return getWorkspace(ctx, s.pool, user.OrganizationID, id)
}

func (s *Store) GetWorkspaceForOrganization(ctx context.Context, organizationID, id string) (domain.Workspace, error) {
	return getWorkspace(ctx, s.pool, organizationID, id)
}

func getWorkspace(ctx context.Context, q querier, organizationID, id string) (domain.Workspace, error) {
	result, err := scanWorkspace(q.QueryRow(ctx, workspaceSelect+`
        WHERE w.organization_id = $1 AND w.id = $2`, organizationID, id))
	if err != nil {
		return domain.Workspace{}, mapError("get workspace", err)
	}
	return result, nil
}

func scanWorkspace(row scanner) (domain.Workspace, error) {
	var result domain.Workspace
	err := row.Scan(
		&result.ID, &result.OrganizationID, &result.HostID, &result.Name,
		&result.Path, &result.Kind, &result.Status, &result.CreatedAt,
	)
	return result, err
}

func canOperateWorkspace(ctx context.Context, q querier, user domain.User, workspaceID string) (bool, error) {
	role, err := requireMembership(ctx, q, user)
	if err != nil {
		return false, err
	}
	if role == "admin" {
		return true, nil
	}
	var allowed bool
	err = q.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1 FROM workspace_acl wa
            WHERE wa.organization_id = $1 AND wa.workspace_id = $2
              AND wa.role IN ('owner', 'operator')
              AND (
                  wa.user_id = $3
                  OR wa.team_id IN (
                      SELECT tm.team_id FROM team_members tm
                      WHERE tm.organization_id = $1 AND tm.user_id = $3
                  )
              )
        )`, user.OrganizationID, workspaceID, user.ID).Scan(&allowed)
	if err != nil {
		return false, mapError("check workspace acl", err)
	}
	return allowed, nil
}
