package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestCleanupBackupMetrics(t *testing.T) {
	LastSuccessfulBackup.WithLabelValues("backup1", "ns1", "pvc1").Set(1234)

	if n := collectCount(t, LastSuccessfulBackup); n == 0 {
		t.Fatal("expected metric to exist before cleanup")
	}

	CleanupBackupMetrics("backup1", "ns1", "pvc1")

	// After cleanup, the specific label set should be removed.
	if v := gaugeValue(t, LastSuccessfulBackup, "backup1", "ns1", "pvc1"); v != nil {
		t.Fatalf("expected metric to be removed after cleanup, got %f", v.GetGauge().GetValue())
	}
}

func TestCleanupRepositoryMetrics(t *testing.T) {
	BackupsPerRepository.WithLabelValues("repo1", "ns1").Set(5)
	ServerReady.WithLabelValues("repo1", "ns1").Set(1)

	CleanupRepositoryMetrics("repo1", "ns1")

	if v := gaugeValue(t, BackupsPerRepository, "repo1", "ns1"); v != nil {
		t.Fatalf("expected BackupsPerRepository to be removed, got %f", v.GetGauge().GetValue())
	}
	if v := gaugeValue(t, ServerReady, "repo1", "ns1"); v != nil {
		t.Fatalf("expected ServerReady to be removed, got %f", v.GetGauge().GetValue())
	}
}

// collectCount returns the total number of metric samples in the collector.
func collectCount(t *testing.T, c prometheus.Collector) int {
	t.Helper()
	ch := make(chan prometheus.Metric, 100)
	c.Collect(ch)
	close(ch)
	count := 0
	for range ch {
		count++
	}
	return count
}

// gaugeValue looks for a metric matching the given label values in the collector.
// Returns nil if not found.
func gaugeValue(t *testing.T, c prometheus.Collector, labels ...string) *dto.Metric {
	t.Helper()
	ch := make(chan prometheus.Metric, 100)
	c.Collect(ch)
	close(ch)
	for m := range ch {
		d := &dto.Metric{}
		if err := m.Write(d); err != nil {
			t.Fatalf("failed to write metric: %v", err)
		}
		if labelsMatch(d, labels) {
			return d
		}
	}
	return nil
}

func labelsMatch(m *dto.Metric, values []string) bool {
	if len(m.GetLabel()) != len(values) {
		return false
	}
	for i, lp := range m.GetLabel() {
		if lp.GetValue() != values[i] {
			return false
		}
	}
	return true
}
