package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type CreateReq struct {
	Code           string `json:"code"`
	MaxRedemptions int    `json:"max_redemptions"`
}

type RedeemReq struct {
	UserID         string `json:"user_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func main() {
	baseURL := "http://localhost:8080"
	if len(os.Args) > 1 && os.Args[1] != "" {
		baseURL = os.Args[1]
	}

	concurrency := 50
	if len(os.Args) > 2 {
		if c, err := strconv.Atoi(os.Args[2]); err == nil && c > 0 {
			concurrency = c
		}
	}

	code := fmt.Sprintf("burst-go-%d", time.Now().Unix())

	fmt.Println("==========================================")
	fmt.Println("     Voucher Service Burst Gate (Go CLI)  ")
	fmt.Println("==========================================")
	fmt.Printf("Target URL:  %s\n", baseURL)
	fmt.Printf("Concurrency: %d\n", concurrency)
	fmt.Printf("Voucher Code: %s\n\n", code)

	client := &http.Client{Timeout: 15 * time.Second}

	// 1. Create Voucher (max_redemptions = 1)
	createBody, _ := json.Marshal(CreateReq{Code: code, MaxRedemptions: 1})
	resp, err := client.Post(baseURL+"/vouchers", "application/json", bytes.NewBuffer(createBody))
	if err != nil {
		fmt.Printf("❌ Failed to create voucher: %v\n", err)
		os.Exit(1)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		fmt.Printf("❌ Failed to create voucher, status code: %d\n", resp.StatusCode)
		os.Exit(1)
	}
	fmt.Println("✅ Voucher created successfully (remaining = 1)")
	fmt.Printf("🚀 Firing %d concurrent redemption requests...\n\n", concurrency)

	// 2. Fire 50 Goroutines Simultaneously
	var successes int64
	var exhausted int64
	var errors int64

	startSignal := make(chan struct{})
	var wg sync.WaitGroup

	for i := 1; i <= concurrency; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			redeemBody, _ := json.Marshal(RedeemReq{
				UserID:         fmt.Sprintf("user-%d", index),
				IdempotencyKey: fmt.Sprintf("burst-key-%s-%d", code, index),
			})

			// Wait for start signal so all 50 goroutines fire at the exact same instant
			<-startSignal

			r, err := client.Post(baseURL+"/vouchers/"+code+"/redeem", "application/json", bytes.NewBuffer(redeemBody))
			if err != nil {
				atomic.AddInt64(&errors, 1)
				return
			}
			_, _ = io.Copy(io.Discard, r.Body)
			r.Body.Close()

			switch r.StatusCode {
			case http.StatusOK:
				atomic.AddInt64(&successes, 1)
			case http.StatusUnprocessableEntity, http.StatusTooManyRequests:
				atomic.AddInt64(&exhausted, 1)
			default:
				if r.StatusCode >= 500 {
					atomic.AddInt64(&errors, 1)
				} else {
					atomic.AddInt64(&exhausted, 1)
				}
			}
		}(i)
	}

	// Release all 50 goroutines at the exact same microsecond!
	close(startSignal)
	wg.Wait()

	fmt.Println("==========================================")
	fmt.Println("              BURST RESULTS               ")
	fmt.Println("==========================================")
	fmt.Printf("Granted Redemptions (HTTP 200): %d\n", successes)
	fmt.Printf("Clean Rejections     (HTTP 422/429): %d\n", exhausted)
	fmt.Printf("Server Errors        (HTTP 5xx): %d\n\n", errors)

	if successes == 1 && errors == 0 {
		fmt.Println("🎉 SUCCESS: Exactly 1 redemption granted, 0 server errors! Zero over-redemption detected!")
	} else {
		fmt.Printf("❌ FAIL: Expected 1 success and 0 errors, got %d successes and %d errors\n", successes, errors)
	}
}
