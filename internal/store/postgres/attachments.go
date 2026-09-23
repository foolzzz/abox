package postgres

import (
	"context"
	"fmt"
	"strings"

	"agentbox/internal/domain"
	storepkg "agentbox/internal/store"

	"github.com/jackc/pgx/v5"
)

func (s *Store) ListRuntimeSessionAttachments(ctx context.Context, user domain.User, boxID string) ([]domain.RuntimeSessionAttachment, error) {
	allowed, err := canReadBox(ctx, s.pool, user, boxID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: box access required", storepkg.ErrForbidden)
	}
	var organizationID, hostID, runtimeType, sessionRef string
	if err := s.pool.QueryRow(ctx, `
        SELECT organization_id, host_id, runtime_type, COALESCE(runtime_session_ref, '')
        FROM boxes WHERE id = $1 AND status <> 'terminated'`, boxID).Scan(&organizationID, &hostID, &runtimeType, &sessionRef); err != nil {
		return nil, mapError("load attachment session", err)
	}
	if sessionRef == "" {
		return []domain.RuntimeSessionAttachment{}, nil
	}
	rows, err := s.pool.Query(ctx, `
        SELECT a.id, a.box_id, b.name, a.host_id, a.runtime_type, a.session_ref,
               a.attach_mode, a.input_policy, a.status, a.attached_at, a.detached_at
        FROM runtime_session_attachments a
        JOIN boxes b ON b.organization_id = a.organization_id AND b.id = a.box_id
        WHERE a.organization_id = $1 AND a.host_id = $2 AND a.runtime_type = $3
          AND a.session_ref = $4 AND a.status = 'active' AND b.status <> 'terminated'
        ORDER BY a.attached_at, a.id`, organizationID, hostID, runtimeType, sessionRef)
	if err != nil {
		return nil, mapError("list runtime session attachments", err)
	}
	defer rows.Close()
	result := make([]domain.RuntimeSessionAttachment, 0)
	for rows.Next() {
		var attachment domain.RuntimeSessionAttachment
		if err := rows.Scan(&attachment.ID, &attachment.BoxID, &attachment.BoxName, &attachment.HostID,
			&attachment.RuntimeType, &attachment.SessionRef, &attachment.AttachMode, &attachment.InputPolicy,
			&attachment.Status, &attachment.AttachedAt, &attachment.DetachedAt); err != nil {
			return nil, mapError("scan runtime session attachment", err)
		}
		allowedAttachment, err := canReadBox(ctx, s.pool, user, attachment.BoxID)
		if err != nil {
			return nil, err
		}
		if !allowedAttachment {
			continue
		}
		result = append(result, attachment)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list runtime session attachments", err)
	}
	return result, nil
}

func (s *Store) DetachRuntimeSession(ctx context.Context, user domain.User, boxID string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		role, err := requireMembership(ctx, tx, user)
		if err != nil {
			return err
		}
		var organizationID, ownerUserID, sessionRef string
		if err := tx.QueryRow(ctx, `
            SELECT organization_id, owner_user_id, COALESCE(runtime_session_ref, '')
            FROM boxes WHERE organization_id = $1 AND id = $2 AND status <> 'terminated'
            FOR UPDATE`, user.OrganizationID, boxID).Scan(&organizationID, &ownerUserID, &sessionRef); err != nil {
			return mapError("lock box session attachment", err)
		}
		if role != "admin" && ownerUserID != user.ID {
			return fmt.Errorf("%w: only the box owner or an organization admin can detach this session", storepkg.ErrForbidden)
		}
		if sessionRef == "" {
			return nil
		}
		if _, err := tx.Exec(ctx, `
            UPDATE runtime_session_attachments SET status = 'detached', detached_at = COALESCE(detached_at, now())
            WHERE box_id = $1 AND status = 'active'`, boxID); err != nil {
			return mapError("detach runtime session", err)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE boxes SET runtime_session_mode = 'new', runtime_session_ref = NULL,
                updated_at = now(), version = version + 1
            WHERE id = $1`, boxID); err != nil {
			return mapError("clear box runtime session", err)
		}
		return insertAudit(ctx, tx, organizationID, "user", user.ID, "", "runtime_session.detached", "box", boxID,
			map[string]any{"sessionRefSuffix": sessionReferenceSuffix(sessionRef)})
	})
}

func (s *Store) StopRuntimeSession(ctx context.Context, user domain.User, boxID string) ([]domain.Box, error) {
	result := make([]domain.Box, 0)
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := requireRole(ctx, tx, user, "admin"); err != nil {
			return err
		}
		var organizationID, hostID, runtimeType, sessionRef string
		if err := tx.QueryRow(ctx, `
            SELECT organization_id, host_id, runtime_type, COALESCE(runtime_session_ref, '')
            FROM boxes WHERE organization_id = $1 AND id = $2 AND status <> 'terminated'
            FOR UPDATE`, user.OrganizationID, boxID).Scan(&organizationID, &hostID, &runtimeType, &sessionRef); err != nil {
			return mapError("lock shared runtime session", err)
		}
		if strings.TrimSpace(sessionRef) == "" {
			return fmt.Errorf("%w: box has no external runtime session", storepkg.ErrConflict)
		}
		rows, err := tx.Query(ctx, `
            SELECT box_id FROM runtime_session_attachments
            WHERE organization_id = $1 AND host_id = $2 AND runtime_type = $3
              AND session_ref = $4 AND status = 'active'
            FOR UPDATE`, organizationID, hostID, runtimeType, sessionRef)
		if err != nil {
			return mapError("lock shared session attachments", err)
		}
		boxIDs := make([]string, 0)
		for rows.Next() {
			var attachedBoxID string
			if err := rows.Scan(&attachedBoxID); err != nil {
				rows.Close()
				return mapError("scan shared session attachment", err)
			}
			boxIDs = append(boxIDs, attachedBoxID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return mapError("scan shared session attachments", err)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE runtime_session_attachments SET status = 'detached', detached_at = COALESCE(detached_at, now())
            WHERE organization_id = $1 AND host_id = $2 AND runtime_type = $3
              AND session_ref = $4 AND status = 'active'`, organizationID, hostID, runtimeType, sessionRef); err != nil {
			return mapError("detach stopped shared session", err)
		}
		for _, attachedBoxID := range boxIDs {
			if _, err := tx.Exec(ctx, `
                UPDATE boxes SET runtime_session_mode = 'resume', updated_at = now(), version = version + 1
                WHERE id = $1`, attachedBoxID); err != nil {
				return mapError("convert stopped shared session box", err)
			}
		}
		for _, attachedBoxID := range boxIDs {
			box, err := getBoxByOrganization(ctx, tx, organizationID, attachedBoxID)
			if err != nil {
				return err
			}
			result = append(result, box)
		}
		return insertAudit(ctx, tx, organizationID, "user", user.ID, "", "runtime_session.stopped", "box", boxID,
			map[string]any{"sessionRefSuffix": sessionReferenceSuffix(sessionRef), "attachmentCount": len(boxIDs)})
	})
	return result, err
}
