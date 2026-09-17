WITH ranked_development_owners AS (
    SELECT om.organization_id,
           om.user_id,
           row_number() OVER (
               PARTITION BY om.organization_id
               ORDER BY om.joined_at, om.user_id
           ) AS owner_rank
    FROM organization_members om
    JOIN organizations o ON o.id = om.organization_id
    WHERE o.slug = 'development'
      AND om.status = 'active'
      AND om.role = 'owner'
)
UPDATE organization_members om
SET role = 'viewer'
FROM ranked_development_owners ranked
WHERE om.organization_id = ranked.organization_id
  AND om.user_id = ranked.user_id
  AND ranked.owner_rank > 1;
