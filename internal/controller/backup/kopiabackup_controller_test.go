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
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

var _ = Describe("KopiaBackup Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			backupName = "test-backup"
			repoName   = "test-repo"
			pvcName    = "test-pvc"
			namespace  = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      backupName,
			Namespace: namespace,
		}

		BeforeEach(func() {
			By("creating prerequisite PVC")
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pvcName,
					Namespace: namespace,
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: *resource.NewQuantity(1<<30, resource.BinarySI),
						},
					},
				},
			}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc)
			if errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, pvc)).To(Succeed())
			}

			By("creating prerequisite KopiaRepository")
			repo := &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      repoName,
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
			err = k8sClient.Get(ctx, types.NamespacedName{Name: repoName, Namespace: namespace}, repo)
			if errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, repo)).To(Succeed())
			}

			By("creating the KopiaBackup resource")
			backup := &backupv1alpha1.KopiaBackup{}
			err = k8sClient.Get(ctx, typeNamespacedName, backup)
			if errors.IsNotFound(err) {
				backup = &backupv1alpha1.KopiaBackup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backupName,
						Namespace: namespace,
					},
					Spec: backupv1alpha1.KopiaBackupSpec{
						PVCName:    pvcName,
						Schedule:   "0 3 * * *",
						Repository: repoName,
					},
				}
				Expect(k8sClient.Create(ctx, backup)).To(Succeed())
			}
		})

		AfterEach(func() {
			By("cleaning up the KopiaBackup")
			backup := &backupv1alpha1.KopiaBackup{}
			if err := k8sClient.Get(ctx, typeNamespacedName, backup); err == nil {
				// Remove finalizer first so delete actually removes the object
				backup.Finalizers = nil
				_ = k8sClient.Update(ctx, backup)
				Expect(k8sClient.Delete(ctx, backup)).To(Succeed())
			}

			By("cleaning up the KopiaRepository")
			repo := &backupv1alpha1.KopiaRepository{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: repoName, Namespace: namespace}, repo); err == nil {
				Expect(k8sClient.Delete(ctx, repo)).To(Succeed())
			}

			By("cleaning up the PVC")
			pvc := &corev1.PersistentVolumeClaim{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc); err == nil {
				Expect(k8sClient.Delete(ctx, pvc)).To(Succeed())
			}
		})

		It("should add a finalizer on first reconcile", func() {
			controllerReconciler := &KopiaBackupReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			var backup backupv1alpha1.KopiaBackup
			Expect(k8sClient.Get(ctx, typeNamespacedName, &backup)).To(Succeed())
			Expect(backup.Finalizers).To(ContainElement(finalizerName))
		})

		It("should set NoPodFound condition when no pod uses the PVC", func() {
			controllerReconciler := &KopiaBackupReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			// Reconcile twice: first adds finalizer, second does actual work
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			var backup backupv1alpha1.KopiaBackup
			Expect(k8sClient.Get(ctx, typeNamespacedName, &backup)).To(Succeed())

			readyCond := meta.FindStatusCondition(backup.Status.Conditions, backupv1alpha1.ConditionTypeReady)
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal(backupv1alpha1.ReasonNoPodFound))
		})

		It("should handle missing KopiaBackup by checking for PVC", func() {
			controllerReconciler := &KopiaBackupReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "nonexistent-backup",
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("getScheduleFromPVC", func() {
		const defaultSchedule = "0 3/6 * * *"

		It("should return the annotation schedule when present", func() {
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						scheduleAnnotationKey: "0 3 * * *",
					},
				},
			}
			Expect(getScheduleFromPVC(pvc, defaultSchedule)).To(Equal("0 3 * * *"))
		})

		It("should return the default schedule when annotation is absent", func() {
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{},
			}
			Expect(getScheduleFromPVC(pvc, defaultSchedule)).To(Equal(defaultSchedule))
		})

		It("should return the default schedule when annotation is empty", func() {
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						scheduleAnnotationKey: "",
					},
				},
			}
			Expect(getScheduleFromPVC(pvc, defaultSchedule)).To(Equal(defaultSchedule))
		})

		It("should return the default schedule when PVC is nil", func() {
			Expect(getScheduleFromPVC(nil, defaultSchedule)).To(Equal(defaultSchedule))
		})

		It("should return the default schedule when annotations map is nil", func() {
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: nil,
				},
			}
			Expect(getScheduleFromPVC(pvc, defaultSchedule)).To(Equal(defaultSchedule))
		})
	})

	Context("getCronJobNameFromPVCName", func() {
		It("should prefix with snapshot- for short names", func() {
			Expect(getCronJobNameFromPVCName("my-pvc")).To(Equal("snapshot-my-pvc"))
		})

		It("should truncate long names", func() {
			longName := "this-is-a-very-long-pvc-name-that-exceeds-forty-two-chars-limit"
			result := getCronJobNameFromPVCName(longName)
			Expect(result).To(HavePrefix("snapshot-"))
			Expect(len(result)).To(BeNumerically("<=", 54))
		})
	})

	Context("buildConfigMap", func() {
		It("should produce valid JSON in repository.config", func() {
			repo := &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{Name: "test-repo"},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					StorageType: backupv1alpha1.StorageTypeFilesystem,
					Hostname:    "myhost",
					Username:    "myuser",
					Description: "test",
					FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
						Path: "/mnt/repo",
					},
					Caching: backupv1alpha1.KopiaRepositoryCachingSpec{
						CacheDirectory: "/cache",
					},
				},
			}
			cm := buildConfigMap("kopia-config-test-repo", "default", repo)
			Expect(cm.Data).To(HaveKey("repository.config"))
			Expect(cm.Data["repository.config"]).To(ContainSubstring(`"hostname": "myhost"`))
			Expect(cm.Data["repository.config"]).To(ContainSubstring(`"username": "myuser"`))
			Expect(cm.Data["repository.config"]).To(ContainSubstring(`"path": "/mnt/repo"`))
		})
	})
})
