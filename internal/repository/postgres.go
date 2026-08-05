package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aashirwad/voucher-service/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository interface {
	CreateVoucher(ctx context.Context, code string, maxRedemptions int) (*model.Voucher, error)
	RedeemVoucher(ctx context.Context, code, userID, idempotencyKey string) (*model.RedeemResponse, bool, error)
	GetVoucher(ctx context.Context, code string) (*model.VoucherStatusResponse, error)
	Ping(ctx context.Context) error
}

type PostgresRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
}

func (r *PostgresRepository) CreateVoucher(ctx context.Context, code string, maxRedemptions int) (*model.Voucher, error) {
	query := `
		INSERT INTO vouchers (code, max_redemptions, remaining)
		VALUES ($1, $2, $2)
		RETURNING id, code, max_redemptions, remaining, created_at
	`

	var v model.Voucher
	err := r.pool.QueryRow(ctx, query, code, maxRedemptions).Scan(
		&v.ID, &v.Code, &v.MaxRedemptions, &v.Remaining, &v.CreatedAt,
	)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return nil, model.ErrDuplicateVoucher
		}
		return nil, fmt.Errorf("failed to create voucher: %w", err)
	}

	return &v, nil
}

func (r *PostgresRepository) RedeemVoucher(ctx context.Context, code, userID, idempotencyKey string) (*model.RedeemResponse, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Check idempotency_keys table
	var existingFingerprint string
	var existingRespCode int
	var existingRespBody []byte

	idempotencyQuery := `
		SELECT fingerprint, response_code, response_body
		FROM idempotency_keys
		WHERE key = $1 AND created_at > NOW() - INTERVAL '24 hours'
	`
	err = tx.QueryRow(ctx, idempotencyQuery, idempotencyKey).Scan(
		&existingFingerprint, &existingRespCode, &existingRespBody,
	)

	if err == nil {
		// Key exists — verify fingerprint
		expectedFingerprint := computeFingerprint(code, userID)
		if existingFingerprint != expectedFingerprint {
			return nil, false, model.ErrIdempotencyConflict
		}

		// Replay original response
		var resp model.RedeemResponse
		if err := json.Unmarshal(existingRespBody, &resp); err != nil {
			return nil, false, fmt.Errorf("failed to unmarshal idempotency response: %w", err)
		}
		return &resp, true, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("error checking idempotency key: %w", err)
	}

	// 2. Atomic check and decrement (CAS query)
	var voucherID uuid.UUID
	var remaining int

	casQuery := `
		UPDATE vouchers
		SET remaining = remaining - 1
		WHERE code = $1 AND remaining > 0
		RETURNING id, remaining
	`
	err = tx.QueryRow(ctx, casQuery, code).Scan(&voucherID, &remaining)
	if errors.Is(err, pgx.ErrNoRows) {
		// Distinguish between unknown voucher code vs exhausted voucher
		var exists bool
		checkQuery := `SELECT EXISTS(SELECT 1 FROM vouchers WHERE code = $1)`
		if err := tx.QueryRow(ctx, checkQuery, code).Scan(&exists); err != nil {
			return nil, false, fmt.Errorf("failed to check voucher existence: %w", err)
		}

		if !exists {
			return nil, false, model.ErrVoucherNotFound
		}
		return nil, false, model.ErrVoucherExhausted
	} else if err != nil {
		return nil, false, fmt.Errorf("atomic decrement failed: %w", err)
	}

	// 3. Record redemption
	redemptionID := uuid.New()
	insertRedemptionQuery := `
		INSERT INTO redemptions (id, voucher_id, user_id, idempotency_key)
		VALUES ($1, $2, $3, $4)
	`
	if _, err := tx.Exec(ctx, insertRedemptionQuery, redemptionID, voucherID, userID, idempotencyKey); err != nil {
		return nil, false, fmt.Errorf("failed to record redemption: %w", err)
	}

	// 4. Save idempotency record
	resp := model.RedeemResponse{
		RedemptionID: redemptionID,
		Remaining:    remaining,
	}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		return nil, false, fmt.Errorf("failed to marshal redeem response: %w", err)
	}

	fingerprint := computeFingerprint(code, userID)
	insertIdempotencyQuery := `
		INSERT INTO idempotency_keys (key, fingerprint, voucher_code, response_code, response_body)
		VALUES ($1, $2, $3, $4, $5)
	`
	if _, err := tx.Exec(ctx, insertIdempotencyQuery, idempotencyKey, fingerprint, code, 200, respBytes); err != nil {
		return nil, false, fmt.Errorf("failed to record idempotency key: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return &resp, false, nil
}

func (r *PostgresRepository) GetVoucher(ctx context.Context, code string) (*model.VoucherStatusResponse, error) {
	query := `
		SELECT v.id, v.code, v.remaining, v.max_redemptions,
		       COUNT(red.id) AS redemptions_count
		FROM vouchers v
		LEFT JOIN redemptions red ON red.voucher_id = v.id
		WHERE v.code = $1
		GROUP BY v.id, v.code, v.remaining, v.max_redemptions
	`

	var resp model.VoucherStatusResponse
	err := r.pool.QueryRow(ctx, query, code).Scan(
		&resp.ID, &resp.Code, &resp.Remaining, &resp.MaxRedemptions, &resp.RedemptionsCount,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, model.ErrVoucherNotFound
	} else if err != nil {
		return nil, fmt.Errorf("failed to get voucher status: %w", err)
	}

	return &resp, nil
}

func (r *PostgresRepository) CleanExpiredIdempotencyKeys(ctx context.Context) (int64, error) {
	query := `DELETE FROM idempotency_keys WHERE created_at < NOW() - INTERVAL '24 hours'`
	tag, err := r.pool.Exec(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("failed to clean expired idempotency keys: %w", err)
	}
	return tag.RowsAffected(), nil
}

func computeFingerprint(voucherCode, userID string) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s:%s", voucherCode, userID)))
	return hex.EncodeToString(h.Sum(nil))
}
