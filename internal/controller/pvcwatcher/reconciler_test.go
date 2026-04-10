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

package pvcwatcher

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

var _ = Describe("PVC Watcher Controller", func() {
	const (
		pvcName   = "watcher-pvc"
		repoName  = "watcher-repo"
		namespace = "default"
	)

	ctx := context.Background()

	newPVC := func(labels map[string]string, annotations map[string]string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:        pvcName,
				Namespace:   namespace,
				Labels:      labels,
				Annotations: annotations,
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
	}

	reconciler := func() *PVCWatcherReconciler {
		return &PVCWatcherReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(10),
		}
	}

	AfterEach(func() {
		// Clean up KopiaBackup
		backup := &backupv1alpha1.KopiaBackup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, backup); err == nil {
			backup.Finalizers = nil
			_ = k8sClient.Update(ctx, backup)
			_ = k8sClient.Delete(ctx, backup)
		}

		// Clean up KopiaRepository
		repo := &backupv1alpha1.KopiaRepository{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: repoName, Namespace: namespace}, repo); err == nil {
			_ = k8sClient.Delete(ctx, repo)
		}

		// Clean up PVC (remove protection finalizer first)
		pvc := &corev1.PersistentVolumeClaim{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc); err == nil {
			pvc.Finalizers = nil
			_ = k8sClient.Update(ctx, pvc)
			_ = k8sClient.Delete(ctx, pvc)
		}
	})

	Context("When PVC has no repository label", func() {
		It("should do nothing", func() {
			pvc := newPVC(nil, nil)
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())

			r := reconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pvcName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// No KopiaBackup should be created
			var backup backupv1alpha1.KopiaBackup
			err = k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, &backup)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("When PVC has repository label and repo exists", func() {
		BeforeEach(func() {
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
					DefaultSchedule:    "0 3 * * *",
					FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
						Path: "/backup/repo",
					},
				},
			}
			Expect(k8sClient.Create(ctx, repo)).To(Succeed())
		})

		It("should create a KopiaBackup with the default schedule", func() {
			pvc := newPVC(map[string]string{repositoryLabelKey: repoName}, nil)
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())

			r := reconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pvcName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var backup backupv1alpha1.KopiaBackup
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, &backup)).To(Succeed())
			Expect(backup.Spec.PVCName).To(Equal(pvcName))
			Expect(backup.Spec.Repository).To(Equal(repoName))
			Expect(backup.Spec.Schedule).To(Equal("0 3 * * *"))
			Expect(backup.Status.AutoCreated).To(BeTrue())
		})

		It("should use the PVC schedule annotation when present", func() {
			pvc := newPVC(
				map[string]string{repositoryLabelKey: repoName},
				map[string]string{scheduleAnnotationKey: "0 6 * * *"},
			)
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())

			r := reconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pvcName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var backup backupv1alpha1.KopiaBackup
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, &backup)).To(Succeed())
			Expect(backup.Spec.Schedule).To(Equal("0 6 * * *"))
		})

		It("should not create a duplicate KopiaBackup", func() {
			pvc := newPVC(map[string]string{repositoryLabelKey: repoName}, nil)
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())

			r := reconciler()
			// First reconcile creates the backup
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pvcName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile is a no-op
			_, err = r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pvcName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When PVC label is removed from an auto-created backup", func() {
		It("should delete the auto-created KopiaBackup", func() {
			// Create repo
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
					DefaultSchedule:    "0 3 * * *",
					FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
						Path: "/backup/repo",
					},
				},
			}
			Expect(k8sClient.Create(ctx, repo)).To(Succeed())

			// Create PVC with label
			pvc := newPVC(map[string]string{repositoryLabelKey: repoName}, nil)
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())

			r := reconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pvcName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify backup exists
			var backup backupv1alpha1.KopiaBackup
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, &backup)).To(Succeed())

			// Remove the label
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc)).To(Succeed())
			pvc.Labels = nil
			Expect(k8sClient.Update(ctx, pvc)).To(Succeed())

			// Reconcile should delete the backup
			_, err = r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pvcName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, &backup)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("When PVC does not exist", func() {
		It("should not error", func() {
			r := reconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When repository does not exist", func() {
		It("should not create a backup", func() {
			pvc := newPVC(map[string]string{repositoryLabelKey: "nonexistent-repo"}, nil)
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())

			r := reconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pvcName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var backup backupv1alpha1.KopiaBackup
			err = k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, &backup)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("getScheduleFromPVC", func() {
		It("should return annotation schedule when present", func() {
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{scheduleAnnotationKey: "0 1 * * *"},
				},
			}
			Expect(getScheduleFromPVC(pvc, "default")).To(Equal("0 1 * * *"))
		})

		It("should return default when annotation is absent", func() {
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(getScheduleFromPVC(pvc, "0 3 * * *")).To(Equal("0 3 * * *"))
		})

		It("should return default when PVC is nil", func() {
			Expect(getScheduleFromPVC(nil, "0 3 * * *")).To(Equal("0 3 * * *"))
		})
	})
})
