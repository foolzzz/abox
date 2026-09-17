package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"agentbox/internal/domain"
	storepkg "agentbox/internal/store"
	migrationfs "agentbox/migrations"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationLockID int64 = 0x4147454e54424f58

type Store struct {
	pool *pgxpool.Pool
}

var _ storepkg.Store = (*Store)(nil)

type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type scanner interface {
	Scan(...any) error
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse postgres configuration: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	result := NewWithPool(pool)
	if err := result.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return result, nil
}

func NewWithPool(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("postgres store is not initialized")
	}
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	return nil
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) Migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrationfs.Files, ".")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapError("begin migration", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
		return mapError("lock migrations", err)
	}
	if _, err := tx.Exec(ctx, `
        CREATE TABLE IF NOT EXISTS schema_migrations (
            version TEXT PRIMARY KEY,
            checksum TEXT NOT NULL,
            applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
        )`); err != nil {
		return mapError("create migration ledger", err)
	}

	for _, name := range names {
		body, err := fs.ReadFile(migrationfs.Files, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		digest := sha256.Sum256(body)
		checksum := hex.EncodeToString(digest[:])
		var appliedChecksum string
		err = tx.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE version = $1`, name).Scan(&appliedChecksum)
		switch {
		case err == nil:
			if appliedChecksum != checksum {
				return fmt.Errorf("%w: applied migration %s checksum changed", storepkg.ErrConflict, name)
			}
			continue
		case !errors.Is(err, pgx.ErrNoRows):
			return mapError("read migration ledger", err)
		}
		if _, err := tx.Conn().PgConn().Exec(ctx, string(body)).ReadAll(); err != nil {
			return mapError("apply migration "+name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations(version, checksum) VALUES ($1, $2)`, name, checksum,
		); err != nil {
			return mapError("record migration "+name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return mapError("commit migrations", err)
	}
	return nil
}

func (s *Store) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapError("begin transaction", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return mapError("commit transaction", err)
	}
	return nil
}

func newUUIDv7() (string, error) {
	var raw [16]byte
	millis := time.Now().UnixMilli()
	raw[0] = byte(millis >> 40)
	raw[1] = byte(millis >> 32)
	raw[2] = byte(millis >> 24)
	raw[3] = byte(millis >> 16)
	raw[4] = byte(millis >> 8)
	raw[5] = byte(millis)
	if _, err := rand.Read(raw[6:]); err != nil {
		return "", fmt.Errorf("generate uuidv7: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80
	return uuid.UUID(raw).String(), nil
}

func mapError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, storepkg.ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		var sentinel error
		switch pgErr.Code {
		case "23505", "23503", "23P01":
			sentinel = storepkg.ErrConflict
		case "23514", "22P02", "22003", "22023", "22007":
			sentinel = storepkg.ErrInvalidState
		}
		if sentinel != nil {
			if pgErr.ConstraintName != "" {
				return fmt.Errorf("%s (%s): %w", op, pgErr.ConstraintName, sentinel)
			}
			return fmt.Errorf("%s: %w", op, sentinel)
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

func jsonValue(raw json.RawMessage, fallback string) ([]byte, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(fallback)
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("%w: invalid JSON", storepkg.ErrInvalidState)
	}
	return append([]byte(nil), raw...), nil
}

func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boundedLimit(value, defaultValue, maximum int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maximum {
		return maximum
	}
	return value
}

func ackValue(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("%w: host acknowledgement exceeds bigint", storepkg.ErrInvalidState)
	}
	return int64(value), nil
}

func slugify(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var result strings.Builder
	pendingDash := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if pendingDash && result.Len() > 0 {
				result.WriteByte('-')
			}
			result.WriteRune(r)
			pendingDash = false
		default:
			pendingDash = result.Len() > 0
		}
	}
	if result.Len() == 0 {
		return "resource"
	}
	return result.String()
}

func requireMembership(ctx context.Context, q querier, user domain.User) (string, error) {
	var role string
	err := q.QueryRow(ctx, `
        SELECT role
        FROM organization_members
        WHERE organization_id = $1 AND user_id = $2 AND status = 'active'`,
		user.OrganizationID, user.ID,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: active organization membership required", storepkg.ErrForbidden)
	}
	if err != nil {
		return "", mapError("check organization membership", err)
	}
	return role, nil
}

func requireRole(ctx context.Context, q querier, user domain.User, roles ...string) (string, error) {
	role, err := requireMembership(ctx, q, user)
	if err != nil {
		return "", err
	}
	for _, allowed := range roles {
		if role == allowed {
			return role, nil
		}
	}
	return "", fmt.Errorf("%w: organization role %s is not allowed", storepkg.ErrForbidden, role)
}

func insertAudit(ctx context.Context, q querier, organizationID, actorType, actorUserID, actorHostID, action, resourceType, resourceID string, metadata any) error {
	id, err := newUUIDv7()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	_, err = q.Exec(ctx, `
        INSERT INTO audit_logs(
            id, organization_id, actor_type, actor_user_id, actor_host_id,
            action, resource_type, resource_id, metadata
        ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		id, organizationID, actorType, nullableUUID(actorUserID), nullableUUID(actorHostID),
		action, resourceType, nullableUUID(resourceID), payload,
	)
	return mapError("insert audit log", err)
}
