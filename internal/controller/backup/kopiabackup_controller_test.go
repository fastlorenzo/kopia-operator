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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

var _ = Describe("KopiaBackup Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		kopiabackup := &backupv1alpha1.KopiaBackup{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind KopiaBackup")
			err := k8sClient.Get(ctx, typeNamespacedName, kopiabackup)
			if err != nil && errors.IsNotFound(err) {
				// Create the PVC first
				pvc := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pvc",
						Namespace: "default",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("1Gi"),
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, pvc)).To(Succeed())

				// Create the KopiaRepository
				repo := &backupv1alpha1.KopiaRepository{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-repo",
						Namespace: "default",
					},
					Spec: backupv1alpha1.KopiaRepositorySpec{
						Hostname:           "test-host",
						Username:           "test-user",
						StorageType:        "filesystem",
						RepositoryPassword: "test-password",
						FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
							Path: "/tmp/test-repo",
						},
					},
				}
				Expect(k8sClient.Create(ctx, repo)).To(Succeed())

				// Create the KopiaBackup
				backup := &backupv1alpha1.KopiaBackup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: backupv1alpha1.KopiaBackupSpec{
						PVCName:    "test-pvc",
						Schedule:   "0 2 * * *",
						Repository: "test-repo",
					},
				}
				Expect(k8sClient.Create(ctx, backup)).To(Succeed())
			}
		})

		AfterEach(func() {
			// Cleanup logic after each test, like removing the resource instance.
			resource := &backupv1alpha1.KopiaBackup{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance KopiaBackup")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// Clean up repository
			repo := &backupv1alpha1.KopiaRepository{}
			repoKey := types.NamespacedName{Name: "test-repo", Namespace: "default"}
			if err := k8sClient.Get(ctx, repoKey, repo); err == nil {
				Expect(k8sClient.Delete(ctx, repo)).To(Succeed())
			}

			// Clean up PVC
			pvc := &corev1.PersistentVolumeClaim{}
			pvcKey := types.NamespacedName{Name: "test-pvc", Namespace: "default"}
			if err := k8sClient.Get(ctx, pvcKey, pvc); err == nil {
				Expect(k8sClient.Delete(ctx, pvc)).To(Succeed())
			}
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &KopiaBackupReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
