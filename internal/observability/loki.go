package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
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
		logChan:  make(chan string, 1000),
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
		// channel full, drop log to avoid blocking app
	}
}

func (lp *LokiPusher) worker() {
	defer lp.wg.Done()

	ticker := time.NewTicker(2 * time.Second)
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
			if len(buffer) >= 50 {
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

// MultiHandler wraps slog.Handler and forwards logs to LokiPusher in addition to stdout.
type MultiHandler struct {
	slog.Handler
	pusher *LokiPusher
}

func NewMultiHandler(h slog.Handler, pusher *LokiPusher) *MultiHandler {
	return &MultiHandler{Handler: h, pusher: pusher}
}

func (m *MultiHandler) Handle(ctx context.Context, record slog.Record) error {
	err := m.Handler.Handle(ctx, record)
	if m.pusher != nil {
		// Quick JSON format for Loki
		buf := &bytes.Buffer{}
		_ = json.NewEncoder(buf).Encode(map[string]any{
			"time":  record.Time,
			"level": record.Level.String(),
			"msg":   record.Message,
		})
		m.pusher.Push(buf.String())
	}
	return err
}
