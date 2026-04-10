/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/tls"
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	webhookserver "sigs.k8s.io/controller-runtime/pkg/webhook"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
	"github.com/fastlorenzo/kopia-operator/internal/controller/kopiabackup"
	"github.com/fastlorenzo/kopia-operator/internal/controller/kopiarepository"
	"github.com/fastlorenzo/kopia-operator/internal/controller/pvcwatcher"
	"github.com/fastlorenzo/kopia-operator/internal/kopia/server"
	"github.com/fastlorenzo/kopia-operator/internal/kopia/user"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(backupv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var kopiaImage string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"If set the metrics endpoint is served securely")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&kopiaImage, "kopia-image", "",
		"Override the default Kopia container image used by backup CronJobs")
	opts := zap.Options{
		Development: false,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Disable HTTP/2 by default to mitigate CVEs (Stream Cancellation / Rapid Reset).
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	tlsOpts := []func(*tls.Config){}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
			TLSOpts:       tlsOpts,
		},
		WebhookServer: webhookserver.NewServer(webhookserver.Options{
			Port:    9443,
			TLSOpts: tlsOpts,
		}),
		HealthProbeBindAddress:        probeAddr,
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              "71b0af1d.cloudinfra.be",
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Create shared server manager for Kopia Server mode
	serverManager := server.NewKopiaServerManager(mgr.GetClient(), mgr.GetScheme())

	// Create user manager for Kopia Server mode (requires REST config for kubectl exec)
	userManager, err := user.NewKopiaUserManager(mgr.GetClient(), mgr.GetScheme(), mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "unable to create KopiaUserManager")
		os.Exit(1)
	}

	if err = (&kopiabackup.KopiaBackupReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		Recorder:      mgr.GetEventRecorderFor("kopiabackup-controller"),
		KopiaImage:    kopiaImage,
		ServerManager: serverManager,
		UserManager:   userManager,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KopiaBackup")
		os.Exit(1)
	}
	if err = (&kopiarepository.KopiaRepositoryReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		Recorder:      mgr.GetEventRecorderFor("kopiarepository-controller"),
		ServerManager: serverManager,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KopiaRepository")
		os.Exit(1)
	}
	if err = (&pvcwatcher.PVCWatcherReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("pvcwatcher-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PVCWatcher")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	// Register validating webhooks when enabled.
	if os.Getenv("ENABLE_WEBHOOKS") != "false" {
		if err = ctrl.NewWebhookManagedBy(mgr).
			For(&backupv1alpha1.KopiaBackup{}).
			WithValidator(&backupv1alpha1.KopiaBackupCustomValidator{}).
			Complete(); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "KopiaBackup")
			os.Exit(1)
		}
		if err = ctrl.NewWebhookManagedBy(mgr).
			For(&backupv1alpha1.KopiaRepository{}).
			WithValidator(&backupv1alpha1.KopiaRepositoryCustomValidator{}).
			Complete(); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "KopiaRepository")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
