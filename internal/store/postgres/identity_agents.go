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

func (s *Store) EnsureDevelopmentTenant(ctx context.Context, login string) (domain.User, error) {
	login = strings.TrimSpace(login)
	if login == "" {
		return domain.User{}, fmt.Errorf("%w: development login is required", storepkg.ErrInvalidState)
	}
	var result domain.User
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		organizationID, err := newUUIDv7()
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
            INSERT INTO organizations(id, slug, name, status)
            VALUES ($1, 'development', 'Development', 'active')
            ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
            RETURNING id`, organizationID).Scan(&organizationID)
		if err != nil {
			return mapError("upsert development organization", err)
		}

		userID, err := newUUIDv7()
		if err != nil {
			return err
		}
		displayName := login
		if before, _, ok := strings.Cut(login, "@"); ok && before != "" {
			displayName = before
		}
		err = tx.QueryRow(ctx, `
            INSERT INTO users(id, tailscale_login, display_name, status, last_seen_at)
            VALUES ($1,$2,$3,'active',now())
            ON CONFLICT (tailscale_login) DO UPDATE
            SET status = 'active', last_seen_at = now(), updated_at = now()
            RETURNING id, tailscale_login::text, display_name, created_at`,
			userID, login, displayName,
		).Scan(&result.ID, &result.Login, &result.DisplayName, &result.CreatedAt)
		if err != nil {
			return mapError("upsert development user", err)
		}
		_, err = tx.Exec(ctx, `
            INSERT INTO organization_members(organization_id, user_id, role, status)
            VALUES ($1,$2,'owner','active')
            ON CONFLICT (organization_id, user_id) DO UPDATE
            SET role = 'owner', status = 'active'`, organizationID, result.ID)
		if err != nil {
			return mapError("upsert development membership", err)
		}
		result.OrganizationID = organizationID
		result.Role = "owner"
		return nil
	})
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
		if _, err := requireRole(ctx, tx, user, "owner", "admin"); err != nil {
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
