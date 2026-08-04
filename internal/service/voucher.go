package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aashirwad/voucher-service/internal/model"
	"github.com/aashirwad/voucher-service/internal/repository"
)

type VoucherService interface {
	CreateVoucher(ctx context.Context, req *model.CreateVoucherRequest) (*model.CreateVoucherResponse, error)
	RedeemVoucher(ctx context.Context, code string, req *model.RedeemRequest) (*model.RedeemResponse, bool, error)
	GetVoucher(ctx context.Context, code string) (*model.VoucherStatusResponse, error)
}

type Service struct {
	repo repository.Repository
}

func NewVoucherService(repo repository.Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) CreateVoucher(ctx context.Context, req *model.CreateVoucherRequest) (*model.CreateVoucherResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: request body required", model.ErrInvalidInput)
	}

	code := strings.TrimSpace(req.Code)
	if code == "" {
		return nil, fmt.Errorf("%w: voucher code is required", model.ErrInvalidInput)
	}

	maxRedemptions := 1
	if req.MaxRedemptions != nil {
		if *req.MaxRedemptions <= 0 {
			return nil, fmt.Errorf("%w: max_redemptions must be greater than 0", model.ErrInvalidInput)
		}
		maxRedemptions = *req.MaxRedemptions
	}

	v, err := s.repo.CreateVoucher(ctx, code, maxRedemptions)
	if err != nil {
		return nil, err
	}

	return &model.CreateVoucherResponse{
		ID:        v.ID,
		Code:      v.Code,
		Remaining: v.Remaining,
	}, nil
}

func (s *Service) RedeemVoucher(ctx context.Context, code string, req *model.RedeemRequest) (*model.RedeemResponse, bool, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, false, fmt.Errorf("%w: voucher code is required", model.ErrInvalidInput)
	}

	if req == nil {
		return nil, false, fmt.Errorf("%w: request body required", model.ErrInvalidInput)
	}

	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		return nil, false, fmt.Errorf("%w: user_id is required", model.ErrInvalidInput)
	}

	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		return nil, false, fmt.Errorf("%w: idempotency_key is required", model.ErrInvalidInput)
	}

	return s.repo.RedeemVoucher(ctx, code, userID, idempotencyKey)
}

func (s *Service) GetVoucher(ctx context.Context, code string) (*model.VoucherStatusResponse, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("%w: voucher code is required", model.ErrInvalidInput)
	}

	return s.repo.GetVoucher(ctx, code)
}
