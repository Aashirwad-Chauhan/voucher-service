package repository_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aashirwad/voucher-service/internal/model"
	"github.com/aashirwad/voucher-service/internal/repository"
	"github.com/aashirwad/voucher-service/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setupTestDB(t *testing.T) repository.Repository {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}
	if dbURL == "" {
		dbURL = "postgres://voucher:voucher@localhost:5432/voucher?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("Skipping integration test: failed to connect to Postgres at %s: %v", dbURL, err)
		return nil
	}

	if err := pool.Ping(ctx); err != nil {
		t.Skipf("Skipping integration test: Postgres not reachable at %s: %v", dbURL, err)
		return nil
	}

	// Apply migrations
	if _, err := pool.Exec(ctx, migrations.InitSQL); err != nil {
		t.Fatalf("Failed to run test migrations: %v", err)
	}

	return repository.NewPostgresRepository(pool)
}

func TestConcurrentRedeem_SingleUse(t *testing.T) {
	repo := setupTestDB(t)
	if repo == nil {
		return
	}

	ctx := context.Background()
	code := fmt.Sprintf("SINGLE-%s", uuid.New().String()[:8])

	// Create single-use voucher (max_redemptions = 1)
	_, err := repo.CreateVoucher(ctx, code, 1)
	if err != nil {
		t.Fatalf("Failed to create voucher: %v", err)
	}

	concurrency := 50
	var successes int32
	var exhausted int32
	var otherErrors int32

	var wg sync.WaitGroup
	gate := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-gate // Synchronization barrier

			userID := fmt.Sprintf("user-%d", idx)
			idempotencyKey := fmt.Sprintf("key-single-%d-%s", idx, code)

			resp, isReplay, err := repo.RedeemVoucher(ctx, code, userID, idempotencyKey)
			if err == nil {
				if !isReplay && resp != nil {
					atomic.AddInt32(&successes, 1)
				}
			} else if errors.Is(err, model.ErrVoucherExhausted) {
				atomic.AddInt32(&exhausted, 1)
			} else {
				t.Logf("Unexpected error in goroutine %d: %v", idx, err)
				atomic.AddInt32(&otherErrors, 1)
			}
		}(i)
	}

	close(gate) // Fire all goroutines simultaneously
	wg.Wait()

	if successes != 1 {
		t.Errorf("Expected exactly 1 success, got %d", successes)
	}
	if exhausted != int32(concurrency-1) {
		t.Errorf("Expected %d exhausted responses, got %d", concurrency-1, exhausted)
	}
	if otherErrors != 0 {
		t.Errorf("Expected 0 unexpected errors, got %d", otherErrors)
	}

	// Verify final voucher status
	status, err := repo.GetVoucher(ctx, code)
	if err != nil {
		t.Fatalf("Failed to get voucher status: %v", err)
	}
	if status.Remaining != 0 {
		t.Errorf("Expected remaining = 0, got %d", status.Remaining)
	}
	if status.RedemptionsCount != 1 {
		t.Errorf("Expected redemptions_count = 1, got %d", status.RedemptionsCount)
	}
}

func TestConcurrentRedeem_MultiUse(t *testing.T) {
	repo := setupTestDB(t)
	if repo == nil {
		return
	}

	ctx := context.Background()
	code := fmt.Sprintf("MULTI-%s", uuid.New().String()[:8])
	maxUses := 5
	concurrency := 50

	_, err := repo.CreateVoucher(ctx, code, maxUses)
	if err != nil {
		t.Fatalf("Failed to create voucher: %v", err)
	}

	var successes int32
	var exhausted int32
	var otherErrors int32

	var wg sync.WaitGroup
	gate := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-gate

			userID := fmt.Sprintf("user-%d", idx)
			idempotencyKey := fmt.Sprintf("key-multi-%d-%s", idx, code)

			resp, isReplay, err := repo.RedeemVoucher(ctx, code, userID, idempotencyKey)
			if err == nil {
				if !isReplay && resp != nil {
					atomic.AddInt32(&successes, 1)
				}
			} else if errors.Is(err, model.ErrVoucherExhausted) {
				atomic.AddInt32(&exhausted, 1)
			} else {
				atomic.AddInt32(&otherErrors, 1)
			}
		}(i)
	}

	close(gate)
	wg.Wait()

	if successes != int32(maxUses) {
		t.Errorf("Expected exactly %d successes, got %d", maxUses, successes)
	}
	if exhausted != int32(concurrency-maxUses) {
		t.Errorf("Expected %d exhausted responses, got %d", concurrency-maxUses, exhausted)
	}
	if otherErrors != 0 {
		t.Errorf("Expected 0 unexpected errors, got %d", otherErrors)
	}

	status, err := repo.GetVoucher(ctx, code)
	if err != nil {
		t.Fatalf("Failed to get voucher status: %v", err)
	}
	if status.Remaining != 0 {
		t.Errorf("Expected remaining = 0, got %d", status.Remaining)
	}
	if status.RedemptionsCount != maxUses {
		t.Errorf("Expected redemptions_count = %d, got %d", maxUses, status.RedemptionsCount)
	}
}

func TestIdempotency_ReplayAndConflict(t *testing.T) {
	repo := setupTestDB(t)
	if repo == nil {
		return
	}

	ctx := context.Background()
	code := fmt.Sprintf("IDEM-%s", uuid.New().String()[:8])
	idempotencyKey := fmt.Sprintf("key-idem-%s", uuid.New().String()[:8])

	_, err := repo.CreateVoucher(ctx, code, 5)
	if err != nil {
		t.Fatalf("Failed to create voucher: %v", err)
	}

	// First redeem
	resp1, isReplay1, err := repo.RedeemVoucher(ctx, code, "user-A", idempotencyKey)
	if err != nil {
		t.Fatalf("First redeem failed: %v", err)
	}
	if isReplay1 {
		t.Errorf("First redeem should not be a replay")
	}

	// Second redeem with SAME key and SAME user (Replay)
	resp2, isReplay2, err := repo.RedeemVoucher(ctx, code, "user-A", idempotencyKey)
	if err != nil {
		t.Fatalf("Replay redeem failed: %v", err)
	}
	if !isReplay2 {
		t.Errorf("Second redeem should be marked as replay")
	}
	if resp1.RedemptionID != resp2.RedemptionID {
		t.Errorf("Expected same redemption_id %s, got %s", resp1.RedemptionID, resp2.RedemptionID)
	}

	// Third redeem with SAME key but DIFFERENT user (Conflict)
	_, _, err = repo.RedeemVoucher(ctx, code, "user-B", idempotencyKey)
	if !errors.Is(err, model.ErrIdempotencyConflict) {
		t.Errorf("Expected ErrIdempotencyConflict, got %v", err)
	}
}

func TestUnknownVoucherCode(t *testing.T) {
	repo := setupTestDB(t)
	if repo == nil {
		return
	}

	ctx := context.Background()
	_, _, err := repo.RedeemVoucher(ctx, "NON_EXISTENT_CODE", "u1", "k1")
	if !errors.Is(err, model.ErrVoucherNotFound) {
		t.Errorf("Expected ErrVoucherNotFound, got %v", err)
	}

	_, err = repo.GetVoucher(ctx, "NON_EXISTENT_CODE")
	if !errors.Is(err, model.ErrVoucherNotFound) {
		t.Errorf("Expected ErrVoucherNotFound for GET, got %v", err)
	}
}
