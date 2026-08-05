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
	if code == "" || userID == "" || idempotencyKey == "" {
		return nil, false, model.ErrInvalidInput
	}

	fingerprint := computeFingerprint(code, userID)
	query := `
		SELECT out_result_status, out_redemption_id, out_remaining, out_response_body
		FROM redeem_voucher($1, $2, $3, $4)
	`

	var (
		resultStatus string
		redemptionID *uuid.UUID
		remaining    int
		respBody     []byte
	)

	err := r.pool.QueryRow(ctx, query, code, userID, idempotencyKey, fingerprint).Scan(
		&resultStatus, &redemptionID, &remaining, &respBody,
	)
	if err != nil {
		return nil, false, fmt.Errorf("redeem_voucher query failed: %w", err)
	}

	switch resultStatus {
	case "granted":
		if redemptionID == nil {
			return nil, false, fmt.Errorf("missing redemption_id for granted status")
		}
		return &model.RedeemResponse{
			RedemptionID: *redemptionID,
			Remaining:    remaining,
		}, false, nil

	case "replay":
		var resp model.RedeemResponse
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return nil, false, fmt.Errorf("failed to unmarshal replay response: %w", err)
		}
		return &resp, true, nil

	case "conflict":
		return nil, false, model.ErrIdempotencyConflict

	case "exhausted":
		return nil, false, model.ErrVoucherExhausted

	case "not_found":
		return nil, false, model.ErrVoucherNotFound

	default:
		return nil, false, fmt.Errorf("unknown result status: %s", resultStatus)
	}
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
