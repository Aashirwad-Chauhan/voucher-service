package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aashirwad/voucher-service/internal/handler"
	"github.com/aashirwad/voucher-service/internal/model"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

type mockService struct {
	createFn func(ctx context.Context, req *model.CreateVoucherRequest) (*model.CreateVoucherResponse, error)
	redeemFn func(ctx context.Context, code string, req *model.RedeemRequest) (*model.RedeemResponse, bool, error)
	getFn    func(ctx context.Context, code string) (*model.VoucherStatusResponse, error)
}

func (m *mockService) CreateVoucher(ctx context.Context, req *model.CreateVoucherRequest) (*model.CreateVoucherResponse, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return nil, nil
}

func (m *mockService) RedeemVoucher(ctx context.Context, code string, req *model.RedeemRequest) (*model.RedeemResponse, bool, error) {
	if m.redeemFn != nil {
		return m.redeemFn(ctx, code, req)
	}
	return nil, false, nil
}

func (m *mockService) GetVoucher(ctx context.Context, code string) (*model.VoucherStatusResponse, error) {
	if m.getFn != nil {
		return m.getFn(ctx, code)
	}
	return nil, nil
}

func setupRouter(svc *mockService, pingErr error) *chi.Mux {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	vHandler := handler.NewVoucherHandler(svc, logger)
	hHandler := handler.NewHealthHandler(&mockRepoPing{pingErr: pingErr}, logger)

	r := chi.NewRouter()
	r.Use(handler.TraceIDMiddleware)
	r.Use(handler.RequestLogger(logger))

	r.Get("/healthz", hHandler.Healthz)
	r.Get("/readyz", hHandler.Readyz)
	r.Post("/vouchers", vHandler.CreateVoucher)
	r.Post("/vouchers/{code}/redeem", vHandler.RedeemVoucher)
	r.Get("/vouchers/{code}", vHandler.GetVoucher)

	return r
}

type mockRepoPing struct {
	pingErr error
}

func (m *mockRepoPing) CreateVoucher(ctx context.Context, code string, maxRedemptions int) (*model.Voucher, error) {
	return nil, nil
}
func (m *mockRepoPing) RedeemVoucher(ctx context.Context, code, userID, idempotencyKey string) (*model.RedeemResponse, bool, error) {
	return nil, false, nil
}
func (m *mockRepoPing) GetVoucher(ctx context.Context, code string) (*model.VoucherStatusResponse, error) {
	return nil, nil
}
func (m *mockRepoPing) Ping(ctx context.Context) error {
	return m.pingErr
}

func TestHealthzEndpoint(t *testing.T) {
	r := setupRouter(&mockService{}, nil)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

func TestReadyzEndpoint_Failure(t *testing.T) {
	r := setupRouter(&mockService{}, errors.New("db connection timeout"))
	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", rec.Code)
	}
}

func TestCreateVoucherEndpoint(t *testing.T) {
	id := uuid.New()
	svc := &mockService{
		createFn: func(ctx context.Context, req *model.CreateVoucherRequest) (*model.CreateVoucherResponse, error) {
			if req.Code == "DUPLICATE" {
				return nil, model.ErrDuplicateVoucher
			}
			return &model.CreateVoucherResponse{ID: id, Code: req.Code, Remaining: 1}, nil
		},
	}

	r := setupRouter(svc, nil)

	// Test 1: Successful Create (201)
	body := []byte(`{"code": "SUMMER2026"}`)
	req := httptest.NewRequest("POST", "/vouchers", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", rec.Code)
	}

	// Test 2: Duplicate Code (409)
	bodyDup := []byte(`{"code": "DUPLICATE"}`)
	reqDup := httptest.NewRequest("POST", "/vouchers", bytes.NewBuffer(bodyDup))
	reqDup.Header.Set("Content-Type", "application/json")
	recDup := httptest.NewRecorder()

	r.ServeHTTP(recDup, reqDup)
	if recDup.Code != http.StatusConflict {
		t.Errorf("Expected status 409, got %d", recDup.Code)
	}

	var errResp model.ErrorResponse
	_ = json.Unmarshal(recDup.Body.Bytes(), &errResp)
	if errResp.TraceID == "" {
		t.Errorf("Expected trace_id in ErrorResponse")
	}

	// Test 3: Malformed JSON (400)
	bodyBad := []byte(`{invalid_json}`)
	reqBad := httptest.NewRequest("POST", "/vouchers", bytes.NewBuffer(bodyBad))
	reqBad.Header.Set("Content-Type", "application/json")
	recBad := httptest.NewRecorder()

	r.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400 for bad json, got %d", recBad.Code)
	}
}

func TestGetVoucherEndpoint(t *testing.T) {
	id := uuid.New()
	svc := &mockService{
		getFn: func(ctx context.Context, code string) (*model.VoucherStatusResponse, error) {
			if code == "UNKNOWN" {
				return nil, model.ErrVoucherNotFound
			}
			return &model.VoucherStatusResponse{
				ID:               id,
				Code:             code,
				Remaining:        3,
				RedemptionsCount: 2,
				MaxRedemptions:   5,
			}, nil
		},
	}

	r := setupRouter(svc, nil)

	// Test 1: Found (200)
	req := httptest.NewRequest("GET", "/vouchers/SUMMER2026", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	// Test 2: Not Found (404)
	req404 := httptest.NewRequest("GET", "/vouchers/UNKNOWN", nil)
	rec404 := httptest.NewRecorder()
	r.ServeHTTP(rec404, req404)

	if rec404.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rec404.Code)
	}
}

func TestRedeemVoucherEndpoint_StatusCodes(t *testing.T) {
	redemptionID := uuid.New()

	svc := &mockService{
		redeemFn: func(ctx context.Context, code string, req *model.RedeemRequest) (*model.RedeemResponse, bool, error) {
			switch code {
			case "EXHAUSTED":
				return nil, false, model.ErrVoucherExhausted
			case "UNKNOWN":
				return nil, false, model.ErrVoucherNotFound
			case "CONFLICT":
				return nil, false, model.ErrIdempotencyConflict
			default:
				return &model.RedeemResponse{RedemptionID: redemptionID, Remaining: 0}, false, nil
			}
		},
	}

	r := setupRouter(svc, nil)

	tests := []struct {
		code       string
		wantStatus int
	}{
		{"VALID", http.StatusOK},                      // 200
		{"EXHAUSTED", http.StatusUnprocessableEntity}, // 422
		{"UNKNOWN", http.StatusNotFound},               // 404
		{"CONFLICT", http.StatusConflict},              // 409
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			body, _ := json.Marshal(model.RedeemRequest{UserID: "u1", IdempotencyKey: "k1"})
			req := httptest.NewRequest("POST", "/vouchers/"+tt.code+"/redeem", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			r.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("For code %s expected status %d, got %d", tt.code, tt.wantStatus, rec.Code)
			}
			if rec.Header().Get("X-Trace-ID") == "" {
				t.Errorf("X-Trace-ID header should be present")
			}
		})
	}
}

func TestAdminKeyMiddleware(t *testing.T) {
	r := chi.NewRouter()
	r.Use(handler.AdminKeyMiddleware("secret-admin-key"))
	r.Get("/protected", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Case 1: Missing Key (403)
	req1 := httptest.NewRequest("GET", "/protected", nil)
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusForbidden {
		t.Errorf("Expected 403 Forbidden when key missing, got %d", rec1.Code)
	}

	// Case 2: Wrong Key (403)
	req2 := httptest.NewRequest("GET", "/protected", nil)
	req2.Header.Set("X-Admin-Key", "wrong-key")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("Expected 403 Forbidden when key wrong, got %d", rec2.Code)
	}

	// Case 3: Valid Key (200)
	req3 := httptest.NewRequest("GET", "/protected", nil)
	req3.Header.Set("X-Admin-Key", "secret-admin-key")
	rec3 := httptest.NewRecorder()
	r.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("Expected 200 OK when key valid, got %d", rec3.Code)
	}
}

func TestRateLimiterMiddleware(t *testing.T) {
	rl := handler.NewRateLimiter(rate.Limit(1), 1)
	defer rl.Stop()

	r := chi.NewRouter()
	r.Use(handler.RateLimiterMiddleware(rl))
	r.Get("/limited", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Req 1: Allowed (200)
	req1 := httptest.NewRequest("GET", "/limited", nil)
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rec1.Code)
	}

	// Req 2: Exceeded (429)
	req2 := httptest.NewRequest("GET", "/limited", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Errorf("Expected 429 Too Many Requests, got %d", rec2.Code)
	}
}
