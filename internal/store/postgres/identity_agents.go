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

const agentSelect = `
    SELECT a.id, a.organization_id, a.name, a.slug, av.version_no,
           av.runtime_type, COALESCE(av.model, ''), av.system_prompt,
           av.tool_policy, av.skill_policy, av.approval_policy, av.runtime_config,
           a.created_at, a.updated_at
    FROM agents a
    JOIN agent_versions av ON av.id = a.published_version_id AND av.agent_id = a.id`

const memberSelect = `
    SELECT u.id, om.organization_id,
           COALESCE(u.username::text, u.tailscale_login::text, ''), u.display_name,
           om.role, u.status, u.password_hash IS NOT NULL, u.must_change_password, u.created_at
    FROM organization_members om
    JOIN users u ON u.id = om.user_id`

func (s *Store) ListMembers(ctx context.Context, user domain.User) ([]domain.Member, error) {
	if _, err := requireMembership(ctx, s.pool, user); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, memberSelect+`
        WHERE om.organization_id = $1 AND om.status = 'active'
        ORDER BY u.display_name, u.id`, user.OrganizationID)
	if err != nil {
		return nil, mapError("list members", err)
	}
	defer rows.Close()
	result := make([]domain.Member, 0)
	for rows.Next() {
		member, err := scanMember(rows)
		if err != nil {
			return nil, mapError("scan member", err)
		}
		result = append(result, member)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list members", err)
	}
	return result, nil
}

func (s *Store) ListTeams(ctx context.Context, user domain.User) ([]domain.Team, error) {
	if _, err := requireMembership(ctx, s.pool, user); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
        SELECT id, organization_id, slug::text, name, COALESCE(description, ''), created_at, updated_at
        FROM teams
        WHERE organization_id = $1 AND deleted_at IS NULL
        ORDER BY name, id`, user.OrganizationID)
	if err != nil {
		return nil, mapError("list teams", err)
	}
	defer rows.Close()
	result := make([]domain.Team, 0)
	for rows.Next() {
		var team domain.Team
		if err := rows.Scan(&team.ID, &team.OrganizationID, &team.Slug, &team.Name, &team.Description, &team.CreatedAt, &team.UpdatedAt); err != nil {
			return nil, mapError("scan team", err)
		}
		result = append(result, team)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list teams", err)
	}
	return result, nil
}

func getMember(ctx context.Context, q querier, organizationID, memberID string) (domain.Member, error) {
	result, err := scanMember(q.QueryRow(ctx, memberSelect+`
        WHERE om.organization_id = $1 AND om.user_id = $2 AND om.status = 'active'`, organizationID, memberID))
	if err != nil {
		return domain.Member{}, mapError("get member", err)
	}
	return result, nil
}

func scanMember(row scanner) (domain.Member, error) {
	var result domain.Member
	err := row.Scan(
		&result.ID, &result.OrganizationID, &result.Login, &result.DisplayName,
		&result.Role, &result.Status, &result.HasPassword, &result.MustChangePassword, &result.CreatedAt,
	)
	return result, err
}

func (s *Store) ListAgents(ctx context.Context, user domain.User) ([]domain.Agent, error) {
	if _, err := requireMembership(ctx, s.pool, user); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, agentSelect+`
        WHERE a.organization_id = $1 AND a.status = 'active'
          AND av.lifecycle_status = 'published'
        ORDER BY a.updated_at DESC, a.id`, user.OrganizationID)
	if err != nil {
		return nil, mapError("list agents", err)
	}
	defer rows.Close()
	result := make([]domain.Agent, 0)
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, mapError("scan agent", err)
		}
		result = append(result, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("list agents", err)
	}
	return result, nil
}

func (s *Store) CreateAgent(ctx context.Context, user domain.User, input domain.CreateAgentInput) (domain.Agent, error) {
	if strings.TrimSpace(input.Name) == "" {
		return domain.Agent{}, fmt.Errorf("%w: agent name is required", storepkg.ErrInvalidState)
	}
	if input.RuntimeType == "" {
		input.RuntimeType = "omp"
	}
	var result domain.Agent
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := requireRole(ctx, tx, user, "admin"); err != nil {
			return err
		}
		agentID, err := newUUIDv7()
		if err != nil {
			return err
		}
		versionID, err := newUUIDv7()
		if err != nil {
			return err
		}
		slug := slugify(input.Name)
		_, err = tx.Exec(ctx, `
            INSERT INTO agents(
                id, organization_id, slug, name, status, created_by_user_id
            ) VALUES ($1,$2,$3,$4,'active',$5)`,
			agentID, user.OrganizationID, slug, strings.TrimSpace(input.Name), user.ID,
		)
		if err != nil {
			return mapError("insert agent", err)
		}
		_, err = tx.Exec(ctx, `
            INSERT INTO agent_versions(
                id, organization_id, agent_id, version_no, lifecycle_status,
                runtime_type, model, prompt_mode, system_prompt, created_by_user_id, published_at
            ) VALUES ($1,$2,$3,1,'published',$4,$5,'append',$6,$7,now())`,
			versionID, user.OrganizationID, agentID, input.RuntimeType,
			nullableText(input.Model), input.SystemPrompt, user.ID,
		)
		if err != nil {
			return mapError("insert published agent version", err)
		}
		_, err = tx.Exec(ctx, `
            UPDATE agents
            SET published_version_id = $2, updated_at = now(), version = version + 1
            WHERE id = $1`, agentID, versionID)
		if err != nil {
			return mapError("publish agent version", err)
		}
		if err := insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"agent.created", "agent", agentID, map[string]any{"versionId": versionID}); err != nil {
			return err
		}
		result, err = getAgent(ctx, tx, user.OrganizationID, agentID)
		return err
	})
	return result, err
}

func (s *Store) DeleteAgent(ctx context.Context, user domain.User, agentID string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := requireRole(ctx, tx, user, "admin"); err != nil {
			return err
		}
		var name, status string
		if err := tx.QueryRow(ctx, `
            SELECT name, status FROM agents
            WHERE organization_id = $1 AND id = $2
            FOR UPDATE`, user.OrganizationID, agentID).Scan(&name, &status); err != nil {
			return mapError("lock agent for deletion", err)
		}
		if status == "archived" {
			return nil
		}
		var hasActiveBoxes, hasSchedules bool
		if err := tx.QueryRow(ctx, `
            SELECT
                EXISTS(SELECT 1 FROM boxes WHERE organization_id = $1 AND agent_id = $2 AND status <> 'terminated'),
                EXISTS(SELECT 1 FROM schedules WHERE organization_id = $1 AND agent_id = $2 AND status <> 'deleted')`,
			user.OrganizationID, agentID).Scan(&hasActiveBoxes, &hasSchedules); err != nil {
			return mapError("check agent dependencies", err)
		}
		if hasActiveBoxes || hasSchedules {
			return fmt.Errorf("%w: agent is referenced by active boxes or non-deleted schedules", storepkg.ErrConflict)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE agents SET status = 'archived', archived_at = now(), updated_at = now(), version = version + 1
            WHERE id = $1`, agentID); err != nil {
			return mapError("archive agent", err)
		}
		return insertAudit(ctx, tx, user.OrganizationID, "user", user.ID, "",
			"agent.deleted", "agent", agentID, map[string]any{"name": name})
	})
}

func (s *Store) GetAgent(ctx context.Context, user domain.User, id string) (domain.Agent, error) {
	if _, err := requireMembership(ctx, s.pool, user); err != nil {
		return domain.Agent{}, err
	}
	return getAgent(ctx, s.pool, user.OrganizationID, id)
}

func getAgent(ctx context.Context, q querier, organizationID, id string) (domain.Agent, error) {
	result, err := scanAgent(q.QueryRow(ctx, agentSelect+`
        WHERE a.organization_id = $1 AND a.id = $2
          AND a.status = 'active' AND av.lifecycle_status = 'published'`, organizationID, id))
	if err != nil {
		return domain.Agent{}, mapError("get agent", err)
	}
	return result, nil
}

func scanAgent(row scanner) (domain.Agent, error) {
	var result domain.Agent
	var toolPolicy, skillPolicy, approvalPolicy, runtimeConfig []byte
	err := row.Scan(
		&result.ID, &result.OrganizationID, &result.Name, &result.Slug, &result.Version,
		&result.RuntimeType, &result.Model, &result.SystemPrompt,
		&toolPolicy, &skillPolicy, &approvalPolicy, &runtimeConfig,
		&result.CreatedAt, &result.UpdatedAt,
	)
	if err != nil {
		return domain.Agent{}, err
	}
	result.ToolPolicy = json.RawMessage(append([]byte(nil), toolPolicy...))
	result.SkillPolicy = json.RawMessage(append([]byte(nil), skillPolicy...))
	result.ApprovalPolicy = json.RawMessage(append([]byte(nil), approvalPolicy...))
	result.RuntimeConfig = json.RawMessage(append([]byte(nil), runtimeConfig...))
	return result, nil
}
