package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentbox/internal/domain"
	storepkg "agentbox/internal/store"

	"github.com/jackc/pgx/v5"
)

const accountUserSelect = `
    SELECT u.id, om.organization_id, u.username::text, u.display_name,
           om.role, u.status, u.must_change_password, u.created_at
    FROM users u
    JOIN organization_members om ON om.user_id = u.id
    JOIN organizations o ON o.id = om.organization_id
    WHERE o.slug = 'development'`

func (s *Store) EnsureBootstrapAdmin(ctx context.Context, username, displayName, passwordHash string) (domain.User, bool, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	displayName = strings.TrimSpace(displayName)
	var result domain.User
	created := false
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		organizationID, err := ensureDevelopmentOrganization(ctx, tx)
		if err != nil {
			return err
		}
		userID, err := newUUIDv7()
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
            INSERT INTO users(id, username, display_name, password_hash, status, must_change_password)
            VALUES ($1,$2,$3,$4,'active',true)
            ON CONFLICT (username) DO NOTHING
            RETURNING id, username::text, display_name, status, must_change_password, created_at`,
			userID, username, displayName, passwordHash,
		).Scan(&result.ID, &result.Login, &result.DisplayName, &result.Status, &result.MustChangePassword, &result.CreatedAt)
		switch {
		case err == nil:
			created = true
		case errors.Is(err, pgx.ErrNoRows):
			err = tx.QueryRow(ctx, `
                SELECT id, username::text, display_name, status, must_change_password, created_at
                FROM users WHERE username = $1`, username,
			).Scan(&result.ID, &result.Login, &result.DisplayName, &result.Status, &result.MustChangePassword, &result.CreatedAt)
		default:
			return mapError("insert bootstrap admin", err)
		}
		if err != nil {
			return mapError("load bootstrap admin", err)
		}
		if created {
			if _, err := tx.Exec(ctx, `
                INSERT INTO organization_members(organization_id, user_id, role, status)
                VALUES ($1,$2,'admin','active')`, organizationID, result.ID); err != nil {
				return mapError("insert bootstrap admin membership", err)
			}
			if err := insertAudit(ctx, tx, organizationID, "system", "", "",
				"account.bootstrap_created", "user", result.ID,
				map[string]any{"username": username, "role": "admin"}); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `
            SELECT role FROM organization_members
            WHERE organization_id = $1 AND user_id = $2 AND status = 'active'`, organizationID, result.ID,
		).Scan(&result.Role); err != nil {
			return mapError("load bootstrap admin membership", err)
		}
		result.OrganizationID = organizationID
		return nil
	})
	return result, created, err
}

func ensureDevelopmentOrganization(ctx context.Context, tx pgx.Tx) (string, error) {
	organizationID, err := newUUIDv7()
	if err != nil {
		return "", err
	}
	if err := tx.QueryRow(ctx, `
        INSERT INTO organizations(id, slug, name, status)
        VALUES ($1, 'development', 'Development', 'active')
        ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
        RETURNING id`, organizationID).Scan(&organizationID); err != nil {
		return "", mapError("upsert development organization", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM organizations WHERE id = $1 FOR UPDATE`, organizationID).Scan(&organizationID); err != nil {
		return "", mapError("lock development organization", err)
	}
	return organizationID, nil
}

func (s *Store) FindAccountCredentials(ctx context.Context, username string) (domain.AccountCredentials, error) {
	var result domain.AccountCredentials
	err := s.pool.QueryRow(ctx, accountUserSelect+`
      AND u.username = $1 AND u.status = 'active' AND om.status = 'active'`, username).Scan(
		&result.User.ID, &result.User.OrganizationID, &result.User.Login, &result.User.DisplayName,
		&result.User.Role, &result.User.Status, &result.User.MustChangePassword, &result.User.CreatedAt,
	)
	if err != nil {
		return domain.AccountCredentials{}, mapError("find account credentials", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, result.User.ID).Scan(&result.PasswordHash); err != nil {
		return domain.AccountCredentials{}, mapError("load account password", err)
	}
	return result, nil
}

func (s *Store) CreateSession(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time) error {
	id, err := newUUIDv7()
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE expires_at <= now()`); err != nil {
			return mapError("delete expired sessions", err)
		}
		var organizationID string
		if err := tx.QueryRow(ctx, `
            SELECT organization_id FROM organization_members
            WHERE user_id = $1 AND status = 'active'`, userID).Scan(&organizationID); err != nil {
			return mapError("load session organization", err)
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO user_sessions(id, user_id, token_hash, expires_at)
            VALUES ($1,$2,$3,$4)`, id, userID, tokenHash, expiresAt); err != nil {
			return mapError("create account session", err)
		}
		return insertAudit(ctx, tx, organizationID, "user", userID, "",
			"account.logged_in", "user", userID, nil)
	})
}

func (s *Store) UserBySession(ctx context.Context, tokenHash []byte, now time.Time) (domain.User, error) {
	var result domain.User
	err := s.pool.QueryRow(ctx, accountUserSelect+`
      AND u.status = 'active' AND om.status = 'active'
      AND EXISTS (
          SELECT 1 FROM user_sessions session
          WHERE session.user_id = u.id AND session.token_hash = $1 AND session.expires_at > $2
      )`, tokenHash, now).Scan(
		&result.ID, &result.OrganizationID, &result.Login, &result.DisplayName,
		&result.Role, &result.Status, &result.MustChangePassword, &result.CreatedAt,
	)
	if err != nil {
		return domain.User{}, mapError("load account session", err)
	}
	_, _ = s.pool.Exec(ctx, `UPDATE user_sessions SET last_seen_at = $2 WHERE token_hash = $1`, tokenHash, now)
	return result, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		var userID, organizationID string
		err := tx.QueryRow(ctx, `
            SELECT session.user_id, om.organization_id
            FROM user_sessions session
            JOIN organization_members om ON om.user_id = session.user_id
            WHERE session.token_hash = $1`, tokenHash).Scan(&userID, &organizationID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return mapError("load logout session", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE token_hash = $1`, tokenHash); err != nil {
			return mapError("delete account session", err)
		}
		return insertAudit(ctx, tx, organizationID, "user", userID, "",
			"account.logged_out", "user", userID, nil)
	})
}

func (s *Store) ChangePassword(ctx context.Context, user domain.User, passwordHash string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `
            UPDATE users
            SET password_hash = $2, must_change_password = false,
                password_changed_at = now(), updated_at = now()
            WHERE id = $1 AND status = 'active'`, user.ID, passwordHash)
		if err != nil {
			return mapError("change account password", err)
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("%w: account not found", storepkg.ErrNotFound)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id = $1`, user.ID); err != nil {
			return mapError("invalidate account sessions", err)
		}
		return insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"account.password_changed", "user", user.ID, nil)
	})
}

func lockAccountOrganization(ctx context.Context, tx pgx.Tx, organizationID string) error {
	var lockedID string
	if err := tx.QueryRow(ctx, `SELECT id FROM organizations WHERE id = $1 FOR UPDATE`, organizationID).Scan(&lockedID); err != nil {
		return mapError("lock account organization", err)
	}
	return nil
}

func (s *Store) CreateAccount(ctx context.Context, user domain.User, input domain.CreateAccountInput) (domain.Member, error) {
	if input.Role != "admin" && input.Role != "user" {
		return domain.Member{}, fmt.Errorf("%w: invalid account role %q", storepkg.ErrInvalidState, input.Role)
	}
	var result domain.Member
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockAccountOrganization(ctx, tx, user.OrganizationID); err != nil {
			return err
		}
		if _, err := requireRole(ctx, tx, user, "admin"); err != nil {
			return err
		}
		id, err := newUUIDv7()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO users(id, username, display_name, password_hash, status, must_change_password)
            VALUES ($1,$2,$3,$4,'active',true)`, id, input.Username, input.DisplayName, input.PasswordHash); err != nil {
			return mapError("create account", err)
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO organization_members(organization_id, user_id, role, status)
            VALUES ($1,$2,$3,'active')`, user.OrganizationID, id, input.Role); err != nil {
			return mapError("create account membership", err)
		}
		if err := insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"account.created", "user", id,
			map[string]any{"username": input.Username, "role": input.Role}); err != nil {
			return err
		}
		result, err = getMember(ctx, tx, user.OrganizationID, id)
		return err
	})
	return result, err
}

func (s *Store) UpdateAccount(ctx context.Context, user domain.User, memberID string, input domain.UpdateAccountInput) (domain.Member, error) {
	var result domain.Member
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockAccountOrganization(ctx, tx, user.OrganizationID); err != nil {
			return err
		}
		if _, err := requireRole(ctx, tx, user, "admin"); err != nil {
			return err
		}
		current, err := getMember(ctx, tx, user.OrganizationID, memberID)
		if err != nil {
			return err
		}
		displayName, role, status := current.DisplayName, current.Role, current.Status
		if input.DisplayName != nil {
			displayName = strings.TrimSpace(*input.DisplayName)
			if displayName == "" {
				return fmt.Errorf("%w: display name is required", storepkg.ErrInvalidState)
			}
		}
		if input.Role != nil {
			role = strings.ToLower(strings.TrimSpace(*input.Role))
			if role != "admin" && role != "user" {
				return fmt.Errorf("%w: invalid account role %q", storepkg.ErrInvalidState, role)
			}
		}
		if input.Status != nil {
			status = strings.ToLower(strings.TrimSpace(*input.Status))
			if status != "active" && status != "disabled" {
				return fmt.Errorf("%w: invalid account status %q", storepkg.ErrInvalidState, status)
			}
		}
		if input.Status != nil && status == "active" && !current.HasPassword {
			return fmt.Errorf("%w: an account without a local password cannot be activated", storepkg.ErrInvalidState)
		}
		if memberID == user.ID && (role != current.Role || status != current.Status) {
			return fmt.Errorf("%w: administrators cannot change their own role or status", storepkg.ErrForbidden)
		}
		if current.Role == "admin" && current.Status == "active" && (role != "admin" || status != "active") {
			var administrators int
			if err := tx.QueryRow(ctx, `
                SELECT count(*)
                FROM organization_members om
                JOIN users u ON u.id = om.user_id
                WHERE om.organization_id = $1 AND om.status = 'active'
                  AND om.role = 'admin' AND u.status = 'active'`, user.OrganizationID).Scan(&administrators); err != nil {
				return mapError("count active administrators", err)
			}
			if administrators <= 1 {
				return fmt.Errorf("%w: the last active administrator cannot be changed", storepkg.ErrConflict)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET display_name = $2, status = $3, updated_at = now() WHERE id = $1`, memberID, displayName, status); err != nil {
			return mapError("update account", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE organization_members SET role = $3 WHERE organization_id = $1 AND user_id = $2`, user.OrganizationID, memberID, role); err != nil {
			return mapError("update account role", err)
		}
		if status == "disabled" {
			if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id = $1`, memberID); err != nil {
				return mapError("invalidate disabled account sessions", err)
			}
		}
		if err := insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"account.updated", "user", memberID,
			map[string]any{"role": role, "status": status}); err != nil {
			return err
		}
		result, err = getMember(ctx, tx, user.OrganizationID, memberID)
		return err
	})
	return result, err
}

func (s *Store) DeleteAccount(ctx context.Context, user domain.User, memberID string) error {
	if memberID == user.ID {
		return fmt.Errorf("%w: administrators cannot delete their own account", storepkg.ErrForbidden)
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockAccountOrganization(ctx, tx, user.OrganizationID); err != nil {
			return err
		}
		if _, err := requireRole(ctx, tx, user, "admin"); err != nil {
			return err
		}
		current, err := getMember(ctx, tx, user.OrganizationID, memberID)
		if err != nil {
			return err
		}
		if current.Role == "admin" && current.Status == "active" {
			var administrators int
			if err := tx.QueryRow(ctx, `
                SELECT count(*)
                FROM organization_members om
                JOIN users u ON u.id = om.user_id
                WHERE om.organization_id = $1 AND om.status = 'active'
                  AND om.role = 'admin' AND u.status = 'active'`, user.OrganizationID).Scan(&administrators); err != nil {
				return mapError("count active administrators", err)
			}
			if administrators <= 1 {
				return fmt.Errorf("%w: the last active administrator cannot be deleted", storepkg.ErrConflict)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id = $1`, memberID); err != nil {
			return mapError("invalidate deleted account sessions", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET status = 'disabled', updated_at = now() WHERE id = $1`, memberID); err != nil {
			return mapError("disable deleted account", err)
		}
		if err := insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"account.deleted", "user", memberID,
			map[string]any{"login": current.Login, "role": current.Role}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
            UPDATE organization_members SET status = 'suspended'
            WHERE organization_id = $1 AND user_id = $2`, user.OrganizationID, memberID); err != nil {
			return mapError("suspend deleted account membership", err)
		}
		return nil
	})
}

func (s *Store) ResetAccountPassword(ctx context.Context, user domain.User, memberID, passwordHash string) (domain.Member, error) {
	if memberID == user.ID {
		return domain.Member{}, fmt.Errorf("%w: use change password for the current account", storepkg.ErrForbidden)
	}
	var result domain.Member
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockAccountOrganization(ctx, tx, user.OrganizationID); err != nil {
			return err
		}
		if _, err := requireRole(ctx, tx, user, "admin"); err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `
            UPDATE users
            SET password_hash = $2, must_change_password = true,
                password_changed_at = now(), updated_at = now()
            WHERE id = $1 AND username IS NOT NULL`, memberID, passwordHash)
		if err != nil {
			return mapError("reset account password", err)
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("%w: password account not found", storepkg.ErrNotFound)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id = $1`, memberID); err != nil {
			return mapError("invalidate reset account sessions", err)
		}
		if err := insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"account.password_reset", "user", memberID, nil); err != nil {
			return err
		}
		result, err = getMember(ctx, tx, user.OrganizationID, memberID)
		return err
	})
	return result, err
}
