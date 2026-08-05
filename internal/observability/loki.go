package observability

import (
	"bytes"
	"encoding/json"
	"io"
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

type logEntry struct {
	line      string
	timestamp time.Time
}

type LokiPusher struct {
	url      string
	userID   string
	apiKey   string
	client   *http.Client
	logChan  chan logEntry
	wg       sync.WaitGroup
	stopChan chan struct{}
	stopOnce sync.Once
}

func NewLokiPusher(cfg *config.Config) *LokiPusher {
	if cfg.GrafanaLokiURL == "" || cfg.GrafanaLokiUser == "" || cfg.GrafanaAPIKey == "" {
		return nil
	}

	lp := &LokiPusher{
		url:    cfg.GrafanaLokiURL + "/loki/api/v1/push",
		userID: cfg.GrafanaLokiUser,
		apiKey: cfg.GrafanaAPIKey,
		client: &http.Client{
			Timeout:   5 * time.Second,
			Transport: SharedTransport,
		},
		logChan:  make(chan logEntry, 2000),
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
	entry := logEntry{
		line:      logLine,
		timestamp: time.Now(),
	}
	select {
	case lp.logChan <- entry:
	default:
		// Drop log under extreme channel saturation to avoid backpressure on main application
	}
}

func (lp *LokiPusher) worker() {
	defer lp.wg.Done()

	// 5-second ticker for log buffer flushing to reduce CPU & network overhead
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var buffer []logEntry

	for {
		select {
		case entry, ok := <-lp.logChan:
			if !ok {
				lp.flush(buffer)
				return
			}
			buffer = append(buffer, entry)
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

func (lp *LokiPusher) flush(entries []logEntry) {
	if len(entries) == 0 {
		return
	}

	values := make([][]string, len(entries))
	for i, entry := range entries {
		values[i] = []string{strconv.FormatInt(entry.timestamp.UnixNano(), 10), entry.line}
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
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
}

func (lp *LokiPusher) Close() {
	if lp == nil {
		return
	}
	lp.stopOnce.Do(func() {
		close(lp.stopChan)
	})
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
