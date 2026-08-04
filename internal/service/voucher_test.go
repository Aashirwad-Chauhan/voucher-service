package service_test

import (
	"context"
	"testing"

	"github.com/aashirwad/voucher-service/internal/model"
	"github.com/aashirwad/voucher-service/internal/service"
)

type mockRepo struct {
	createVoucherFn func(ctx context.Context, code string, maxRedemptions int) (*model.Voucher, error)
	redeemVoucherFn func(ctx context.Context, code, userID, idempotencyKey string) (*model.RedeemResponse, bool, error)
	getVoucherFn    func(ctx context.Context, code string) (*model.VoucherStatusResponse, error)
}

func (m *mockRepo) CreateVoucher(ctx context.Context, code string, maxRedemptions int) (*model.Voucher, error) {
	if m.createVoucherFn != nil {
		return m.createVoucherFn(ctx, code, maxRedemptions)
	}
	return nil, nil
}

func (m *mockRepo) RedeemVoucher(ctx context.Context, code, userID, idempotencyKey string) (*model.RedeemResponse, bool, error) {
	if m.redeemVoucherFn != nil {
		return m.redeemVoucherFn(ctx, code, userID, idempotencyKey)
	}
	return nil, false, nil
}

func (m *mockRepo) GetVoucher(ctx context.Context, code string) (*model.VoucherStatusResponse, error) {
	if m.getVoucherFn != nil {
		return m.getVoucherFn(ctx, code)
	}
	return nil, nil
}

func (m *mockRepo) Ping(ctx context.Context) error {
	return nil
}

func TestCreateVoucher_Validation(t *testing.T) {
	tests := []struct {
		name    string
		req     *model.CreateVoucherRequest
		wantErr bool
	}{
		{"Nil request", nil, true},
		{"Empty code", &model.CreateVoucherRequest{Code: "  "}, true},
		{"Zero max redemptions", &model.CreateVoucherRequest{Code: "VALID", MaxRedemptions: intPtr(0)}, true},
		{"Negative max redemptions", &model.CreateVoucherRequest{Code: "VALID", MaxRedemptions: intPtr(-5)}, true},
		{"Valid code, default max", &model.CreateVoucherRequest{Code: "VALID"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockRepo{
				createVoucherFn: func(ctx context.Context, code string, maxRedemptions int) (*model.Voucher, error) {
					return &model.Voucher{Code: code, MaxRedemptions: maxRedemptions, Remaining: maxRedemptions}, nil
				},
			}
			s := service.NewVoucherService(mock)
			resp, err := s.CreateVoucher(context.Background(), tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateVoucher() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && resp.Remaining != 1 && tt.req.MaxRedemptions == nil {
				t.Errorf("expected default remaining to be 1, got %d", resp.Remaining)
			}
		})
	}
}

func TestRedeemVoucher_Validation(t *testing.T) {
	svc := service.NewVoucherService(&mockRepo{})

	tests := []struct {
		name    string
		code    string
		req     *model.RedeemRequest
		wantErr bool
	}{
		{"Empty code", "", &model.RedeemRequest{UserID: "u1", IdempotencyKey: "k1"}, true},
		{"Nil req", "CODE", nil, true},
		{"Empty user_id", "CODE", &model.RedeemRequest{UserID: "", IdempotencyKey: "k1"}, true},
		{"Empty idempotency_key", "CODE", &model.RedeemRequest{UserID: "u1", IdempotencyKey: "   "}, true},
		{"Valid request", "CODE", &model.RedeemRequest{UserID: "u1", IdempotencyKey: "k1"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := svc.RedeemVoucher(context.Background(), tt.code, tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("RedeemVoucher() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func intPtr(i int) *int {
	return &i
}
