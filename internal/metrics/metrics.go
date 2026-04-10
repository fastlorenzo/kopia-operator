// Package metrics provides Prometheus metrics for the kopia-operator.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// BackupsPerRepository tracks the number of KopiaBackup resources per repository.
	BackupsPerRepository = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kopia_operator",
			Name:      "backups_per_repository",
			Help:      "Number of KopiaBackup resources per repository",
		},
		[]string{"repository", "namespace"},
	)

	// LastSuccessfulBackup records the Unix timestamp of the last successful backup.
	LastSuccessfulBackup = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kopia_operator",
			Name:      "last_successful_backup_timestamp",
			Help:      "Unix timestamp of the last successful backup",
		},
		[]string{"backup", "namespace", "pvc"},
	)

	// ReconcileErrors counts reconcile errors by controller.
	ReconcileErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kopia_operator",
			Name:      "reconcile_errors_total",
			Help:      "Total number of reconcile errors",
		},
		[]string{"controller"},
	)

	// ServerReady tracks whether the Kopia Server is ready for each repository.
	ServerReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kopia_operator",
			Name:      "server_ready",
			Help:      "Whether the Kopia Server is ready (1=ready, 0=not ready)",
		},
		[]string{"repository", "namespace"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		BackupsPerRepository,
		LastSuccessfulBackup,
		ReconcileErrors,
		ServerReady,
	)
}
