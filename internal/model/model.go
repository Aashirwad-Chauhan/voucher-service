package model

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Domain Errors
var (
	ErrVoucherNotFound     = errors.New("voucher not found")
	ErrVoucherExhausted    = errors.New("voucher exhausted")
	ErrIdempotencyConflict = errors.New("idempotency key used with different request body")
	ErrDuplicateVoucher    = errors.New("voucher code already exists")
	ErrInvalidInput        = errors.New("invalid input")
)

// Voucher represents the voucher entity in Postgres.
type Voucher struct {
	ID             uuid.UUID `json:"id"`
	Code           string    `json:"code"`
	MaxRedemptions int       `json:"max_redemptions"`
	Remaining      int       `json:"remaining"`
	CreatedAt      time.Time `json:"created_at"`
}

// Redemption represents a record of a voucher redemption.
type Redemption struct {
	ID             uuid.UUID `json:"id"`
	VoucherID      uuid.UUID `json:"voucher_id"`
	UserID         string    `json:"user_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	CreatedAt      time.Time `json:"created_at"`
}

// IdempotencyRecord represents a saved HTTP outcome for an idempotency key.
type IdempotencyRecord struct {
	Key          string    `json:"key"`
	Fingerprint  string    `json:"fingerprint"`
	VoucherCode  string    `json:"voucher_code"`
	ResponseCode int       `json:"response_code"`
	ResponseBody []byte    `json:"response_body"`
	CreatedAt    time.Time `json:"created_at"`
}

// Request payloads
type CreateVoucherRequest struct {
	Code           string `json:"code"`
	MaxRedemptions *int   `json:"max_redemptions,omitempty"`
}

type RedeemRequest struct {
	UserID         string `json:"user_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// Response payloads
type CreateVoucherResponse struct {
	ID        uuid.UUID `json:"id"`
	Code      string    `json:"code"`
	Remaining int       `json:"remaining"`
}

type RedeemResponse struct {
	RedemptionID uuid.UUID `json:"redemption_id"`
	Remaining    int       `json:"remaining"`
}

type VoucherStatusResponse struct {
	ID               uuid.UUID `json:"id"`
	Code             string    `json:"code"`
	Remaining        int       `json:"remaining"`
	RedemptionsCount int       `json:"redemptions_count"`
	MaxRedemptions   int       `json:"max_redemptions"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	TraceID string `json:"trace_id,omitempty"`
}

type HealthResponse struct {
	Status   string `json:"status"`
	Database string `json:"database,omitempty"`
}
