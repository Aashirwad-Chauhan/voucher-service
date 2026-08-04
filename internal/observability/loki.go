package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/aashirwad/voucher-service/internal/config"
)

type LokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]string        `json:"values"`
}

type LokiPushRequest struct {
	Streams []LokiStream `json:"streams"`
}

type LokiPusher struct {
	url      string
	userID   string
	apiKey   string
	client   *http.Client
	logChan  chan string
	wg       sync.WaitGroup
	stopChan chan struct{}
}

func NewLokiPusher(cfg *config.Config) *LokiPusher {
	if cfg.GrafanaLokiURL == "" || cfg.GrafanaLokiUser == "" || cfg.GrafanaAPIKey == "" {
		return nil
	}

	lp := &LokiPusher{
		url:      cfg.GrafanaLokiURL + "/loki/api/v1/push",
		userID:   cfg.GrafanaLokiUser,
		apiKey:   cfg.GrafanaAPIKey,
		client:   &http.Client{Timeout: 5 * time.Second},
		logChan:  make(chan string, 2000),
		stopChan: make(chan struct{}),
	}

	lp.wg.Add(1)
	go lp.worker()

	return lp
}

func (lp *LokiPusher) Push(logLine string) {
	if lp == nil {
		return
	}
	select {
	case lp.logChan <- logLine:
	default:
		// Drop log under extreme channel saturation to avoid backpressure on main application
	}
}

func (lp *LokiPusher) worker() {
	defer lp.wg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var buffer []string

	for {
		select {
		case line, ok := <-lp.logChan:
			if !ok {
				lp.flush(buffer)
				return
			}
			buffer = append(buffer, line)
			if len(buffer) >= 100 {
				lp.flush(buffer)
				buffer = nil
			}
		case <-ticker.C:
			if len(buffer) > 0 {
				lp.flush(buffer)
				buffer = nil
			}
		case <-lp.stopChan:
			lp.flush(buffer)
			return
		}
	}
}

func (lp *LokiPusher) flush(lines []string) {
	if len(lines) == 0 {
		return
	}

	values := make([][]string, len(lines))
	nowNano := strconv.FormatInt(time.Now().UnixNano(), 10)
	for i, line := range lines {
		values[i] = []string{nowNano, line}
	}

	reqBody := LokiPushRequest{
		Streams: []LokiStream{
			{
				Stream: map[string]string{
					"app": "voucher-service",
					"env": "production",
				},
				Values: values,
			},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return
	}

	req, err := http.NewRequest("POST", lp.url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(lp.userID, lp.apiKey)

	resp, err := lp.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

func (lp *LokiPusher) Close() {
	if lp == nil {
		return
	}
	close(lp.stopChan)
	lp.wg.Wait()
}

type DualWriter struct {
	pusher *LokiPusher
	out    io.Writer
}

func NewDualWriter(pusher *LokiPusher) io.Writer {
	return &DualWriter{
		pusher: pusher,
		out:    os.Stdout,
	}
}

func (w *DualWriter) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	if w.pusher != nil {
		w.pusher.Push(string(p))
	}
	return n, err
}
