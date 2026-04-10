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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

// KopiaRepositoryReconciler reconciles a KopiaRepository object.
type KopiaRepositoryReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiarepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiarepositories/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiarepositories/finalizers,verbs=update

func (r *KopiaRepositoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)

	var repo backupv1alpha1.KopiaRepository
	if err := r.Get(ctx, req.NamespacedName, &repo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling KopiaRepository", "name", repo.Name)

	// Validate password secret reference
	if repo.Spec.PasswordSecretName == "" {
		meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
			Type:               backupv1alpha1.ConditionTypeRepositoryReady,
			Status:             metav1.ConditionFalse,
			Reason:             backupv1alpha1.ReasonMissingPassword,
			Message:            "spec.passwordSecretName must be set",
			ObservedGeneration: repo.Generation,
		})
		if err := r.Status().Update(ctx, &repo); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// All checks passed
	meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
		Type:               backupv1alpha1.ConditionTypeRepositoryReady,
		Status:             metav1.ConditionTrue,
		Reason:             backupv1alpha1.ReasonConfigValid,
		Message:            "Repository configuration is valid",
		ObservedGeneration: repo.Generation,
	})
	if err := r.Status().Update(ctx, &repo); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopiaRepositoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupv1alpha1.KopiaRepository{}).
		Complete(r)
}
