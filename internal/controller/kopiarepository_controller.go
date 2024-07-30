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

package controller

import (
	"context"
	"slices"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/v1alpha1"
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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the KopiaRepository object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.3/pkg/reconcile
func (r *KopiaRepositoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("kopiarepository", req.NamespacedName)

	r.SupporedStorageTypes = []string{"filesystem"}

	// Check that the storage type is one of the supported ones
	// Get all the KopiaRepository objects
	// For each KopiaRepository object, check if the storage type is supported
	// If the storage type is not supported, log an error and return
	// If the storage type is supported, continue with the reconciliation

	kopiaRepos := &backupv1alpha1.KopiaRepositoryList{}
	if err := r.List(ctx, kopiaRepos); err != nil {
		return ctrl.Result{}, err
	}

	for _, repo := range kopiaRepos.Items {
		if !slices.Contains(r.SupporedStorageTypes, repo.Spec.StorageType) {
			log.Info("unsupported storage type", "storageType", repo.Spec.StorageType, repo.Name)
			return ctrl.Result{}, nil
		}

		// Check if Spec.RepositoryPasswordExistingSecret or Spec.RepositoryPassword is set
		if repo.Spec.RepositoryPasswordExistingSecret == "" && repo.Spec.RepositoryPassword == "" {
			log.Info("Either Spec.RepositoryPasswordExistingSecret or Spec.RepositoryPassword must be set", repo.Name)
			return ctrl.Result{}, nil
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopiaRepositoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupv1alpha1.KopiaRepository{}).
		Complete(r)
}
