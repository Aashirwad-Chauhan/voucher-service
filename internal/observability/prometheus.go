package observability

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/aashirwad/voucher-service/internal/config"
	"github.com/gogo/protobuf/proto"
	"github.com/klauspost/compress/snappy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/prompb"
)

type PromPusher struct {
	url      string
	userID   string
	apiKey   string
	client   *http.Client
	stopChan chan struct{}
	wg       sync.WaitGroup
}

func NewPromPusher(cfg *config.Config) *PromPusher {
	if cfg.GrafanaPromURL == "" || cfg.GrafanaPromUser == "" || cfg.GrafanaPromKey == "" {
		return nil
	}

	pp := &PromPusher{
		url:      cfg.GrafanaPromURL,
		userID:   cfg.GrafanaPromUser,
		apiKey:   cfg.GrafanaPromKey,
		client:   &http.Client{Timeout: 10 * time.Second},
		stopChan: make(chan struct{}),
	}

	// Append /push if not present
	if !bytes.HasSuffix([]byte(cfg.GrafanaPromURL), []byte("/push")) && !bytes.HasSuffix([]byte(cfg.GrafanaPromURL), []byte("/push/")) {
		pp.url = cfg.GrafanaPromURL + "/push"
	}

	pp.wg.Add(1)
	go pp.worker()

	return pp
}

func (pp *PromPusher) worker() {
	defer pp.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			pp.pushMetrics()
		case <-pp.stopChan:
			pp.pushMetrics()
			return
		}
	}
}

func (pp *PromPusher) pushMetrics() {
	metricFamilies, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return
	}

	var timeSeries []prompb.TimeSeries
	nowMs := time.Now().UnixMilli()

	for _, mf := range metricFamilies {
		name := mf.GetName()
		for _, m := range mf.GetMetric() {
			var labels []prompb.Label
			labels = append(labels, prompb.Label{Name: "__name__", Value: name})
			labels = append(labels, prompb.Label{Name: "app", Value: "voucher-service"})
			labels = append(labels, prompb.Label{Name: "env", Value: "production"})

			for _, l := range m.GetLabel() {
				labels = append(labels, prompb.Label{Name: l.GetName(), Value: l.GetValue()})
			}

			var val float64
			if m.Counter != nil {
				val = m.Counter.GetValue()
			} else if m.Gauge != nil {
				val = m.Gauge.GetValue()
			} else if m.Histogram != nil {
				val = float64(m.Histogram.GetSampleCount())
			} else if m.Summary != nil {
				val = float64(m.Summary.GetSampleCount())
			} else {
				continue
			}

			timeSeries = append(timeSeries, prompb.TimeSeries{
				Labels: labels,
				Samples: []prompb.Sample{
					{Value: val, Timestamp: nowMs},
				},
			})
		}
	}

	if len(timeSeries) == 0 {
		return
	}

	writeReq := &prompb.WriteRequest{Timeseries: timeSeries}
	data, err := proto.Marshal(writeReq)
	if err != nil {
		return
	}

	compressed := snappy.Encode(nil, data)

	req, err := http.NewRequest("POST", pp.url, bytes.NewBuffer(compressed))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	req.SetBasicAuth(pp.userID, pp.apiKey)

	resp, err := pp.client.Do(req)
	if err != nil {
		slog.Warn("prom_remote_write_error", slog.Any("error", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		slog.Warn("prom_remote_write_rejected", slog.Int("status", resp.StatusCode), slog.String("body", string(respBody)))
	} else {
		slog.Debug("prom_remote_write_success", slog.Int("metrics_count", len(timeSeries)))
	}
}

func (pp *PromPusher) Close() {
	if pp == nil {
		return
	}
	close(pp.stopChan)
	pp.wg.Wait()
}
