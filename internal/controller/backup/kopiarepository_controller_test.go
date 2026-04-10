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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

var _ = Describe("KopiaRepository Controller", func() {
	Context("When reconciling a valid repository", func() {
		const (
			resourceName = "test-repo"
			namespace    = "default"
		)

		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Name: resourceName, Namespace: namespace}

		BeforeEach(func() {
			By("creating a valid KopiaRepository resource")
			repo := &backupv1alpha1.KopiaRepository{}
			err := k8sClient.Get(ctx, typeNamespacedName, repo)
			if errors.IsNotFound(err) {
				repo = &backupv1alpha1.KopiaRepository{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: namespace,
					},
					Spec: backupv1alpha1.KopiaRepositorySpec{
						Hostname:           "test-host",
						Username:           "test-user",
						StorageType:        backupv1alpha1.StorageTypeFilesystem,
						PasswordSecretName: "kopia-password",
						FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
							Path: "/backup/repo",
						},
					},
				}
				Expect(k8sClient.Create(ctx, repo)).To(Succeed())
			}
		})

		AfterEach(func() {
			repo := &backupv1alpha1.KopiaRepository{}
			if err := k8sClient.Get(ctx, typeNamespacedName, repo); err == nil {
				Expect(k8sClient.Delete(ctx, repo)).To(Succeed())
			}
		})

		It("should set Ready condition to True", func() {
			controllerReconciler := &KopiaRepositoryReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			var repo backupv1alpha1.KopiaRepository
			Expect(k8sClient.Get(ctx, typeNamespacedName, &repo)).To(Succeed())

			readyCond := meta.FindStatusCondition(repo.Status.Conditions, backupv1alpha1.ConditionTypeRepositoryReady)
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCond.Reason).To(Equal(backupv1alpha1.ReasonConfigValid))
		})
	})

	Context("When reconciling a non-existent resource", func() {
		It("should not error", func() {
			controllerReconciler := &KopiaRepositoryReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
