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

package kopiarepository

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
	"github.com/fastlorenzo/kopia-operator/internal/kopia"
	kopiaMetrics "github.com/fastlorenzo/kopia-operator/internal/metrics"
	"github.com/fastlorenzo/kopia-operator/internal/naming"
)

// KopiaRepositoryReconciler reconciles a KopiaRepository object.
type KopiaRepositoryReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// ServerManager manages Kopia Server deployments (optional, for server mode).
	ServerManager kopia.ServerManager
}

// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiarepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiarepositories/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiarepositories/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *KopiaRepositoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	result, err := r.reconcile(ctx, req)
	if err != nil {
		kopiaMetrics.ReconcileErrors.WithLabelValues("kopiarepository").Inc()
	}
	return result, err
}

func (r *KopiaRepositoryReconciler) reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	log := ctrllog.FromContext(ctx)

	var repo backupv1alpha1.KopiaRepository
	if err := r.Get(ctx, req.NamespacedName, &repo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling KopiaRepository", "name", repo.Name)

	// Single deferred status update — all code paths set conditions in-memory.
	defer func() {
		if statusErr := r.Status().Update(ctx, &repo); statusErr != nil {
			log.Error(statusErr, "Failed to update status")
			if retErr == nil {
				retErr = statusErr
			}
		}
	}()

	// Validate password secret reference
	if repo.Spec.PasswordSecretName == "" {
		meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
			Type:               backupv1alpha1.ConditionTypeRepositoryReady,
			Status:             metav1.ConditionFalse,
			Reason:             backupv1alpha1.ReasonMissingPassword,
			Message:            "spec.passwordSecretName must be set",
			ObservedGeneration: repo.Generation,
		})
		return ctrl.Result{}, nil
	}

	// --- Server mode ---
	if repo.Spec.Server.Enabled {
		if r.ServerManager == nil {
			meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
				Type:               backupv1alpha1.ConditionTypeRepositoryReady,
				Status:             metav1.ConditionFalse,
				Reason:             backupv1alpha1.ReasonServerFailed,
				Message:            "Server mode requires ServerManager to be configured",
				ObservedGeneration: repo.Generation,
			})
			return ctrl.Result{}, fmt.Errorf("server manager not configured for server mode")
		}

		// Ensure TLS certificates
		fingerprint, err := r.ServerManager.EnsureTLSSecret(ctx, &repo)
		if err != nil {
			meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
				Type:               backupv1alpha1.ConditionTypeServerReady,
				Status:             metav1.ConditionFalse,
				Reason:             backupv1alpha1.ReasonServerFailed,
				Message:            fmt.Sprintf("Failed to ensure TLS: %v", err),
				ObservedGeneration: repo.Generation,
			})
			r.Recorder.Event(&repo, corev1.EventTypeWarning, "TLSFailed", err.Error())
			return ctrl.Result{}, fmt.Errorf("failed to ensure TLS secret: %w", err)
		}
		repo.Status.TLSCertFingerprint = fingerprint

		// Ensure server deployment
		if err := r.ServerManager.EnsureServerDeployment(ctx, &repo); err != nil {
			meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
				Type:               backupv1alpha1.ConditionTypeServerReady,
				Status:             metav1.ConditionFalse,
				Reason:             backupv1alpha1.ReasonServerFailed,
				Message:            fmt.Sprintf("Failed to ensure Deployment: %v", err),
				ObservedGeneration: repo.Generation,
			})
			r.Recorder.Event(&repo, corev1.EventTypeWarning, "DeploymentFailed", err.Error())
			return ctrl.Result{}, fmt.Errorf("failed to ensure server deployment: %w", err)
		}

		// Ensure server service
		if err := r.ServerManager.EnsureServerService(ctx, &repo); err != nil {
			meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
				Type:               backupv1alpha1.ConditionTypeServerReady,
				Status:             metav1.ConditionFalse,
				Reason:             backupv1alpha1.ReasonServerFailed,
				Message:            fmt.Sprintf("Failed to ensure Service: %v", err),
				ObservedGeneration: repo.Generation,
			})
			r.Recorder.Event(&repo, corev1.EventTypeWarning, "ServiceFailed", err.Error())
			return ctrl.Result{}, fmt.Errorf("failed to ensure server service: %w", err)
		}

		// Check readiness
		ready, err := r.ServerManager.IsServerReady(ctx, &repo)
		if err != nil {
			log.Error(err, "Failed to check server readiness")
		}

		repo.Status.ServerReady = ready
		repo.Status.ServerURL = r.ServerManager.GetServerURL(&repo)
		repo.Status.ServerDeployment = naming.ServerDeploymentName(repo.Name)
		repo.Status.ServerService = naming.ServerServiceName(repo.Name)

		// Update server readiness metric
		readyVal := float64(0)
		if ready {
			readyVal = 1
		}
		kopiaMetrics.ServerReady.WithLabelValues(repo.Name, repo.Namespace).Set(readyVal)

		if ready {
			meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
				Type:               backupv1alpha1.ConditionTypeServerReady,
				Status:             metav1.ConditionTrue,
				Reason:             backupv1alpha1.ReasonServerDeployed,
				Message:            "Kopia Server is deployed and ready",
				ObservedGeneration: repo.Generation,
			})
			r.Recorder.Event(&repo, corev1.EventTypeNormal, "ServerReady", "Kopia Server is deployed and ready")
		} else {
			meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
				Type:               backupv1alpha1.ConditionTypeServerReady,
				Status:             metav1.ConditionFalse,
				Reason:             backupv1alpha1.ReasonServerFailed,
				Message:            "Kopia Server deployment is not ready yet",
				ObservedGeneration: repo.Generation,
			})
			meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
				Type:               backupv1alpha1.ConditionTypeRepositoryReady,
				Status:             metav1.ConditionFalse,
				Reason:             backupv1alpha1.ReasonServerFailed,
				Message:            "Waiting for Kopia Server to be ready",
				ObservedGeneration: repo.Generation,
			})
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
	}

	// All checks passed
	meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
		Type:               backupv1alpha1.ConditionTypeRepositoryReady,
		Status:             metav1.ConditionTrue,
		Reason:             backupv1alpha1.ReasonConfigValid,
		Message:            "Repository configuration is valid",
		ObservedGeneration: repo.Generation,
	})

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopiaRepositoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupv1alpha1.KopiaRepository{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
