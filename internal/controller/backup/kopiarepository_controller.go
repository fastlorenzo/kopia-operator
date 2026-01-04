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

package backup

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
	"github.com/go-logr/logr"
)

// KopiaRepositoryReconciler reconciles a KopiaRepository object
type KopiaRepositoryReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	Log                  logr.Logger
	SupporedStorageTypes []string
}

//+kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiarepositories,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiarepositories/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiarepositories/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *KopiaRepositoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("kopiarepository", req.NamespacedName)

	// Fetch the KopiaRepository instance
	repo := &backupv1alpha1.KopiaRepository{}
	if err := r.Get(ctx, req.NamespacedName, repo); err != nil {
		if errors.IsNotFound(err) {
			// Repository deleted
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch KopiaRepository")
		return ctrl.Result{}, err
	}

	r.SupporedStorageTypes = []string{storageTypeFilesystem, storageTypeSFTP}

	// Check if Spec.StorageType is supported
	if !slices.Contains(r.SupporedStorageTypes, repo.Spec.StorageType) {
		log.Info("unsupported storage type", "storageType", repo.Spec.StorageType)
		r.updateCondition(repo, "Ready", metav1.ConditionFalse,
			"UnsupportedStorage",
			fmt.Sprintf("Storage type %s is not supported", repo.Spec.StorageType))
		return ctrl.Result{}, nil
	}

	// Check if password is configured
	if repo.Spec.RepositoryPasswordExistingSecret == "" && repo.Spec.RepositoryPassword == "" {
		log.Info("Either Spec.RepositoryPasswordExistingSecret or Spec.RepositoryPassword must be set")
		r.updateCondition(repo, "Ready", metav1.ConditionFalse,
			"MissingPassword",
			"Repository password not configured")
		return ctrl.Result{}, nil
	}

	// Check if server mode is enabled
	if repo.Spec.Server.Enabled {
		log.Info("Server mode enabled, deploying Kopia Server")

		// Create server manager
		serverManager := NewKopiaServerManager(r.Client, r.Scheme, log)

		// Ensure repository password secret (if repositoryPassword is set)
		if err := serverManager.EnsureRepositoryPasswordSecret(ctx, repo); err != nil {
			log.Error(err, "failed to ensure repository password secret")
			r.updateCondition(repo, "Ready", metav1.ConditionFalse,
				"SecretFailed",
				fmt.Sprintf("Failed to create/update password secret: %v", err))
			return ctrl.Result{}, err
		}

		// Ensure server admin password secret (if ServerAdminPassword is set)
		if err := serverManager.EnsureServerAdminPasswordSecret(ctx, repo); err != nil {
			log.Error(err, "failed to ensure server admin password secret")
			r.updateCondition(repo, "Ready", metav1.ConditionFalse,
				"SecretFailed",
				fmt.Sprintf("Failed to create/update server admin password secret: %v", err))
			return ctrl.Result{}, err
		}

		// Ensure server deployment
		deployment, err := serverManager.EnsureServerDeployment(ctx, repo)
		if err != nil {
			log.Error(err, "failed to ensure server deployment")
			r.updateCondition(repo, "ServerReady", metav1.ConditionFalse,
				"DeploymentFailed",
				fmt.Sprintf("Failed to create/update deployment: %v", err))
			return ctrl.Result{}, err
		}

		// Ensure server service
		service, err := serverManager.EnsureServerService(ctx, repo)
		if err != nil {
			log.Error(err, "failed to ensure server service")
			r.updateCondition(repo, "ServerReady", metav1.ConditionFalse,
				"ServiceFailed",
				fmt.Sprintf("Failed to create/update service: %v", err))
			return ctrl.Result{}, err
		}

		// Check if server is ready
		ready, err := serverManager.IsServerReady(ctx, repo)
		if err != nil {
			log.Error(err, "failed to check server readiness")
			return ctrl.Result{RequeueAfter: 10 * 1000000000}, nil // 10 seconds
		}

		// Update status
		repo.Status.ServerReady = ready
		repo.Status.ServerDeployment = deployment.Name
		repo.Status.ServerService = service.Name
		repo.Status.ServerURL = serverManager.GetServerURL(ctx, repo, service)

		if ready {
			log.Info("Kopia Server is ready", "url", repo.Status.ServerURL)
			r.updateCondition(repo, "ServerReady", metav1.ConditionTrue,
				"ServerRunning",
				"Kopia Server is running and ready")
			r.updateCondition(repo, "Ready", metav1.ConditionTrue,
				"RepositoryReady",
				"Repository is ready in server mode")
		} else {
			log.Info("Waiting for Kopia Server to be ready")
			r.updateCondition(repo, "ServerReady", metav1.ConditionFalse,
				"ServerStarting",
				"Kopia Server is starting")
			// Requeue to check again
			return ctrl.Result{RequeueAfter: 10 * 1000000000}, nil // 10 seconds
		}

		// Update status
		if err := r.Status().Update(ctx, repo); err != nil {
			log.Error(err, "failed to update repository status")
			return ctrl.Result{}, err
		}
	} else {
		log.Info("Server mode disabled, using direct storage access")
		r.updateCondition(repo, "Ready", metav1.ConditionTrue,
			"DirectAccess",
			"Repository configured for direct storage access")

		// Update status
		repo.Status.ServerReady = false
		repo.Status.ServerDeployment = ""
		repo.Status.ServerService = ""
		repo.Status.ServerURL = ""

		if err := r.Status().Update(ctx, repo); err != nil {
			log.Error(err, "failed to update repository status")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// updateCondition updates a condition in the repository status
func (r *KopiaRepositoryReconciler) updateCondition(
	repo *backupv1alpha1.KopiaRepository,
	conditionType string,
	status metav1.ConditionStatus,
	reason string,
	message string,
) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		ObservedGeneration: repo.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}

	// Find and update existing condition or append new one
	found := false
	for i, c := range repo.Status.Conditions {
		if c.Type == conditionType {
			if c.Status != status {
				repo.Status.Conditions[i] = condition
			}
			found = true
			break
		}
	}
	if !found {
		repo.Status.Conditions = append(repo.Status.Conditions, condition)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopiaRepositoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupv1alpha1.KopiaRepository{}).
		Complete(r)
}
