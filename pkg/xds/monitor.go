package xds

import (
	"strings"
	"sync"
	"time"

	metricsservice "github.com/envoyproxy/go-control-plane/envoy/service/metrics/v3"
	prom "github.com/prometheus/client_model/go"

	"github.com/rueian/zenvoy/pkg/kube"
)

type Stat struct {
	Val float64
	Tms int64
}

const TriggerMetric = "upstream_rq_total"

type MonitorOptions struct {
	ScaleToZeroAfter time.Duration
	ScaleToZeroCheck time.Duration
}

func NewMonitorServer(scaler kube.Scaler, options MonitorOptions) *MonitorServer {
	s := &MonitorServer{
		clusters: make(map[string]Stat),
		scaler:   scaler,
	}
	go func() {
		for {
			time.Sleep(options.ScaleToZeroCheck)
			now := time.Now().UnixNano() / 1e6

			s.mu.Lock()
			for cluster, stat := range s.clusters {
				if stat.Tms != 0 && now-stat.Tms > options.ScaleToZeroAfter.Milliseconds() {
					stat.Tms = 0
					s.clusters[cluster] = stat
					go scaler.ScaleToZero(cluster)
				}
			}
			s.mu.Unlock()
		}
	}()
	return s
}

type MonitorServer struct {
	mu       sync.Mutex
	clusters map[string]Stat
	scaler   kube.Scaler
}

func (s *MonitorServer) StreamMetrics(server metricsservice.MetricsService_StreamMetricsServer) error {
	defer server.SendAndClose(&metricsservice.StreamMetricsResponse{})
	for {
		msg, err := server.Recv()
		if err != nil {
			return err
		}
		s.mu.Lock()
		for _, m := range msg.EnvoyMetrics {
			if *m.Type == prom.MetricType_COUNTER && len(m.Metric) > 0 {
				s.processCounter(m)
			}
		}
		s.mu.Unlock()
	}
}

func (s *MonitorServer) processCounter(m *prom.MetricFamily) {
	if mn := *m.Name; strings.HasSuffix(mn, TriggerMetric) {
		if parts := strings.Split(mn, "."); len(parts) == 3 {
			name := parts[1]
			curr := Stat{Val: *m.Metric[0].Counter.Value, Tms: *m.Metric[0].TimestampMs}
			if curr.Val == s.clusters[name].Val {
				return
			}
			go s.scaler.ScaleFromZero(name)
			s.clusters[name] = curr
		}
	}
}
