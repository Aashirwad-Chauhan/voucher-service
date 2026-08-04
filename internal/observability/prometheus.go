package observability

import (
	"bytes"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
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
		client:   &http.Client{Timeout: 5 * time.Second},
		stopChan: make(chan struct{}),
	}

	if !bytes.HasSuffix([]byte(cfg.GrafanaPromURL), []byte("/push")) && !bytes.HasSuffix([]byte(cfg.GrafanaPromURL), []byte("/push/")) {
		pp.url = cfg.GrafanaPromURL + "/push"
	}

	pp.wg.Add(1)
	go pp.worker()

	return pp
}

func (pp *PromPusher) worker() {
	defer pp.wg.Done()

	// Standard 15-second ticker to minimize CPU and GC overhead
	ticker := time.NewTicker(15 * time.Second)
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
		baseName := mf.GetName()
		for _, m := range mf.GetMetric() {
			baseLabels := []prompb.Label{
				{Name: "app", Value: "voucher-service"},
				{Name: "env", Value: "production"},
			}
			for _, l := range m.GetLabel() {
				baseLabels = append(baseLabels, prompb.Label{Name: l.GetName(), Value: l.GetValue()})
			}

			if m.Counter != nil {
				labels := append([]prompb.Label{{Name: "__name__", Value: baseName}}, baseLabels...)
				timeSeries = append(timeSeries, prompb.TimeSeries{
					Labels:  labels,
					Samples: []prompb.Sample{{Value: m.Counter.GetValue(), Timestamp: nowMs}},
				})
			} else if m.Gauge != nil {
				labels := append([]prompb.Label{{Name: "__name__", Value: baseName}}, baseLabels...)
				timeSeries = append(timeSeries, prompb.TimeSeries{
					Labels:  labels,
					Samples: []prompb.Sample{{Value: m.Gauge.GetValue(), Timestamp: nowMs}},
				})
			} else if m.Histogram != nil {
				h := m.Histogram

				// 1. _count
				countLabels := append([]prompb.Label{{Name: "__name__", Value: baseName + "_count"}}, baseLabels...)
				timeSeries = append(timeSeries, prompb.TimeSeries{
					Labels:  countLabels,
					Samples: []prompb.Sample{{Value: float64(h.GetSampleCount()), Timestamp: nowMs}},
				})

				// 2. _sum
				sumLabels := append([]prompb.Label{{Name: "__name__", Value: baseName + "_sum"}}, baseLabels...)
				timeSeries = append(timeSeries, prompb.TimeSeries{
					Labels:  sumLabels,
					Samples: []prompb.Sample{{Value: h.GetSampleSum(), Timestamp: nowMs}},
				})

				// 3. _bucket
				hasInf := false
				for _, b := range h.GetBucket() {
					upperBound := b.GetUpperBound()
					leStr := strconv.FormatFloat(upperBound, 'f', -1, 64)
					if math.IsInf(upperBound, 1) {
						leStr = "+Inf"
						hasInf = true
					}

					bucketLabels := append([]prompb.Label{
						{Name: "__name__", Value: baseName + "_bucket"},
						{Name: "le", Value: leStr},
					}, baseLabels...)

					timeSeries = append(timeSeries, prompb.TimeSeries{
						Labels:  bucketLabels,
						Samples: []prompb.Sample{{Value: float64(b.GetCumulativeCount()), Timestamp: nowMs}},
					})
				}

				// Ensure +Inf bucket is present for PromQL histogram_quantile
				if !hasInf {
					infBucketLabels := append([]prompb.Label{
						{Name: "__name__", Value: baseName + "_bucket"},
						{Name: "le", Value: "+Inf"},
					}, baseLabels...)

					timeSeries = append(timeSeries, prompb.TimeSeries{
						Labels:  infBucketLabels,
						Samples: []prompb.Sample{{Value: float64(h.GetSampleCount()), Timestamp: nowMs}},
					})
				}
			}
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
	}
}

func (pp *PromPusher) Close() {
	if pp == nil {
		return
	}
	close(pp.stopChan)
	pp.wg.Wait()
}
