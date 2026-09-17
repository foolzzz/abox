package postgres

import (
	"context"
	"fmt"
	"strings"

	"agentbox/internal/domain"
	storepkg "agentbox/internal/store"

	"github.com/jackc/pgx/v5"
)

const workspaceACLSelect = `
    SELECT wa.id, wa.organization_id, wa.workspace_id,
           COALESCE(wa.user_id::text, ''), COALESCE(wa.team_id::text, ''),
           wa.role, wa.created_by_user_id, wa.created_at
    FROM workspace_acl wa`

const boxACLSelect = `
    SELECT ba.id, ba.organization_id, ba.box_id,
           COALESCE(ba.user_id::text, ''), COALESCE(ba.team_id::text, ''),
           ba.role, ba.created_by_user_id, ba.created_at
    FROM box_acl ba`

func (s *Store) ListWorkspaceACL(ctx context.Context, user domain.User, workspaceID string) ([]domain.ResourceACL, error) {
	allowed, err := canReadWorkspace(ctx, s.pool, user, workspaceID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: workspace read access required", storepkg.ErrForbidden)
	}
	return listWorkspaceACL(ctx, s.pool, user.OrganizationID, workspaceID)
}

func (s *Store) ReplaceWorkspaceACL(ctx context.Context, user domain.User, workspaceID string, entries []domain.ResourceACLEntryInput) ([]domain.ResourceACL, error) {
	if err := validateACLEntries(entries); err != nil {
		return nil, err
	}
	var result []domain.ResourceACL
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var organizationID string
		if err := tx.QueryRow(ctx, `
            SELECT organization_id FROM workspaces
            WHERE id = $1 AND organization_id = $2
            FOR UPDATE`, workspaceID, user.OrganizationID).Scan(&organizationID); err != nil {
			return mapError("lock workspace acl resource", err)
		}
		allowed, err := canAdminWorkspace(ctx, tx, user, workspaceID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("%w: workspace owner access required", storepkg.ErrForbidden)
		}
		if err := validateACLPrincipals(ctx, tx, organizationID, entries); err != nil {
			return err
		}
		changed, err := replaceWorkspaceACLTx(ctx, tx, organizationID, workspaceID, user.ID, entries)
		if err != nil {
			return err
		}
		if changed {
			if err := insertAudit(ctx, tx, organizationID, "user", user.ID, "",
				"workspace.acl_replaced", "workspace", workspaceID,
				map[string]any{"entries": len(entries)}); err != nil {
				return err
			}
		}
		result, err = listWorkspaceACL(ctx, tx, organizationID, workspaceID)
		return err
	})
	return result, err
}

func (s *Store) ListBoxACL(ctx context.Context, user domain.User, boxID string) ([]domain.ResourceACL, error) {
	allowed, err := canReadBox(ctx, s.pool, user, boxID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: box read access required", storepkg.ErrForbidden)
	}
	return listBoxACL(ctx, s.pool, user.OrganizationID, boxID)
}

func (s *Store) ReplaceBoxACL(ctx context.Context, user domain.User, boxID string, entries []domain.ResourceACLEntryInput) ([]domain.ResourceACL, error) {
	if err := validateACLEntries(entries); err != nil {
		return nil, err
	}
	var result []domain.ResourceACL
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var organizationID string
		if err := tx.QueryRow(ctx, `
            SELECT organization_id FROM boxes
            WHERE id = $1 AND organization_id = $2
            FOR UPDATE`, boxID, user.OrganizationID).Scan(&organizationID); err != nil {
			return mapError("lock box acl resource", err)
		}
		allowed, err := canAdminBox(ctx, tx, user, boxID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("%w: box owner access required", storepkg.ErrForbidden)
		}
		if err := validateACLPrincipals(ctx, tx, organizationID, entries); err != nil {
			return err
		}
		changed, err := replaceBoxACLTx(ctx, tx, organizationID, boxID, user.ID, entries)
		if err != nil {
			return err
		}
		if changed {
			if err := insertAudit(ctx, tx, organizationID, "user", user.ID, "",
				"box.acl_replaced", "box", boxID,
				map[string]any{"entries": len(entries)}); err != nil {
				return err
			}
		}
		result, err = listBoxACL(ctx, tx, organizationID, boxID)
		return err
	})
	return result, err
}

func validateACLEntries(entries []domain.ResourceACLEntryInput) error {
	seen := make(map[string]struct{}, len(entries))
	for index := range entries {
		entry := &entries[index]
		entry.UserID = strings.TrimSpace(entry.UserID)
		entry.TeamID = strings.TrimSpace(entry.TeamID)
		entry.Role = strings.ToLower(strings.TrimSpace(entry.Role))
		if (entry.UserID == "") == (entry.TeamID == "") {
			return fmt.Errorf("%w: acl entry must identify exactly one user or team", storepkg.ErrInvalidState)
		}
		switch entry.Role {
		case "owner", "operator", "viewer":
		default:
			return fmt.Errorf("%w: invalid resource role %q", storepkg.ErrInvalidState, entry.Role)
		}
		key := "user:" + entry.UserID
		if entry.TeamID != "" {
			key = "team:" + entry.TeamID
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: duplicate acl principal", storepkg.ErrInvalidState)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func aclKey(userID, teamID string) string {
	if userID != "" {
		return "user:" + userID
	}
	return "team:" + teamID
}

func aclReplacementChanged(existing []domain.ResourceACL, entries []domain.ResourceACLEntryInput) bool {
	if len(existing) != len(entries) {
		return true
	}
	roles := make(map[string]string, len(existing))
	for _, entry := range existing {
		roles[aclKey(entry.UserID, entry.TeamID)] = entry.Role
	}
	for _, entry := range entries {
		if roles[aclKey(entry.UserID, entry.TeamID)] != entry.Role {
			return true
		}
	}
	return false
}

func replaceWorkspaceACLTx(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, actorID string, entries []domain.ResourceACLEntryInput) (bool, error) {
	existing, err := listWorkspaceACL(ctx, tx, organizationID, workspaceID)
	if err != nil {
		return false, err
	}
	if !aclReplacementChanged(existing, entries) {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM workspace_acl WHERE organization_id = $1 AND workspace_id = $2`, organizationID, workspaceID); err != nil {
		return false, mapError("replace workspace acl", err)
	}
	for _, entry := range entries {
		id, err := newUUIDv7()
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO workspace_acl(id, organization_id, workspace_id, user_id, team_id, role, created_by_user_id)
            VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, organizationID, workspaceID,
			nullableUUID(entry.UserID), nullableUUID(entry.TeamID), entry.Role, actorID); err != nil {
			return false, mapError("insert workspace acl", err)
		}
	}
	return true, nil
}

func replaceBoxACLTx(ctx context.Context, tx pgx.Tx, organizationID, boxID, actorID string, entries []domain.ResourceACLEntryInput) (bool, error) {
	existing, err := listBoxACL(ctx, tx, organizationID, boxID)
	if err != nil {
		return false, err
	}
	if !aclReplacementChanged(existing, entries) {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM box_acl WHERE organization_id = $1 AND box_id = $2`, organizationID, boxID); err != nil {
		return false, mapError("replace box acl", err)
	}
	for _, entry := range entries {
		id, err := newUUIDv7()
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO box_acl(id, organization_id, box_id, user_id, team_id, role, created_by_user_id)
            VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, organizationID, boxID,
			nullableUUID(entry.UserID), nullableUUID(entry.TeamID), entry.Role, actorID); err != nil {
			return false, mapError("insert box acl", err)
		}
	}
	return true, nil
}

func validateACLPrincipals(ctx context.Context, q querier, organizationID string, entries []domain.ResourceACLEntryInput) error {
	for _, entry := range entries {
		var exists bool
		var err error
		if entry.UserID != "" {
			err = q.QueryRow(ctx, `
                SELECT EXISTS(
                    SELECT 1 FROM organization_members
                    WHERE organization_id = $1 AND user_id = $2 AND status = 'active'
                )`, organizationID, entry.UserID).Scan(&exists)
		} else {
			err = q.QueryRow(ctx, `
                SELECT EXISTS(
                    SELECT 1 FROM teams
                    WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL
                )`, organizationID, entry.TeamID).Scan(&exists)
		}
		if err != nil {
			return mapError("validate acl principal", err)
		}
		if !exists {
			return fmt.Errorf("%w: acl principal is not active in the organization", storepkg.ErrInvalidState)
		}
	}
	return nil
}

func listWorkspaceACL(ctx context.Context, q querier, organizationID, workspaceID string) ([]domain.ResourceACL, error) {
	rows, err := q.Query(ctx, workspaceACLSelect+`
        WHERE wa.organization_id = $1 AND wa.workspace_id = $2
        ORDER BY wa.created_at, wa.id`, organizationID, workspaceID)
	if err != nil {
		return nil, mapError("list workspace acl", err)
	}
	defer rows.Close()
	return scanACLRows(rows, "list workspace acl")
}

func listBoxACL(ctx context.Context, q querier, organizationID, boxID string) ([]domain.ResourceACL, error) {
	rows, err := q.Query(ctx, boxACLSelect+`
        WHERE ba.organization_id = $1 AND ba.box_id = $2
        ORDER BY ba.created_at, ba.id`, organizationID, boxID)
	if err != nil {
		return nil, mapError("list box acl", err)
	}
	defer rows.Close()
	return scanACLRows(rows, "list box acl")
}

func scanACLRows(rows pgx.Rows, operation string) ([]domain.ResourceACL, error) {
	result := make([]domain.ResourceACL, 0)
	for rows.Next() {
		var entry domain.ResourceACL
		if err := rows.Scan(
			&entry.ID, &entry.OrganizationID, &entry.ResourceID,
			&entry.UserID, &entry.TeamID, &entry.Role,
			&entry.CreatedByUserID, &entry.CreatedAt,
		); err != nil {
			return nil, mapError("scan "+operation, err)
		}
		result = append(result, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(operation, err)
	}
	return result, nil
}

func canReadWorkspace(ctx context.Context, q querier, user domain.User, workspaceID string) (bool, error) {
	role, err := requireMembership(ctx, q, user)
	if err != nil {
		return false, err
	}
	var allowed bool
	err = q.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1 FROM workspaces w
            WHERE w.organization_id = $1 AND w.id = $2
              AND (
                  $4 IN ('owner', 'admin')
                  OR EXISTS (
                      SELECT 1 FROM workspace_acl wa
                      WHERE wa.workspace_id = w.id
                        AND (
                            wa.user_id = $3
                            OR wa.team_id IN (
                                SELECT tm.team_id FROM team_members tm
                                WHERE tm.organization_id = $1 AND tm.user_id = $3
                            )
                        )
                  )
              )
        )`, user.OrganizationID, workspaceID, user.ID, role).Scan(&allowed)
	if err != nil {
		return false, mapError("check workspace read acl", err)
	}
	return allowed, nil
}

func canAdminWorkspace(ctx context.Context, q querier, user domain.User, workspaceID string) (bool, error) {
	role, err := requireMembership(ctx, q, user)
	if err != nil {
		return false, err
	}
	if role == "owner" || role == "admin" {
		return true, nil
	}
	var allowed bool
	err = q.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1 FROM workspace_acl wa
            WHERE wa.organization_id = $1 AND wa.workspace_id = $2 AND wa.role = 'owner'
              AND (
                  wa.user_id = $3
                  OR wa.team_id IN (
                      SELECT tm.team_id FROM team_members tm
                      WHERE tm.organization_id = $1 AND tm.user_id = $3
                  )
              )
        )`, user.OrganizationID, workspaceID, user.ID).Scan(&allowed)
	if err != nil {
		return false, mapError("check workspace owner acl", err)
	}
	return allowed, nil
}

func canAdminBox(ctx context.Context, q querier, user domain.User, boxID string) (bool, error) {
	role, err := requireMembership(ctx, q, user)
	if err != nil {
		return false, err
	}
	var allowed bool
	err = q.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1 FROM boxes b
            WHERE b.organization_id = $1 AND b.id = $2
              AND (
                  $4 IN ('owner', 'admin')
                  OR b.owner_user_id = $3
                  OR EXISTS (
                      SELECT 1 FROM box_acl ba
                      WHERE ba.box_id = b.id AND ba.role = 'owner'
                        AND (
                            ba.user_id = $3
                            OR ba.team_id IN (
                                SELECT tm.team_id FROM team_members tm
                                WHERE tm.organization_id = $1 AND tm.user_id = $3
                            )
                        )
                  )
              )
        )`, user.OrganizationID, boxID, user.ID, role).Scan(&allowed)
	if err != nil {
		return false, mapError("check box owner acl", err)
	}
	return allowed, nil
}
