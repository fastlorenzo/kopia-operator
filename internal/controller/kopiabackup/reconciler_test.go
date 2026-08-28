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

package kopiabackup

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
	"github.com/fastlorenzo/kopia-operator/internal/naming"
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
						Path:      "/backup/repo",
						NFSServer: "nfs.example.com",
						NFSPath:   "/exports/backup",
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

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// Missing pods are usually a long-lived state; poll slowly.
			Expect(result.RequeueAfter).To(Equal(noPodRequeueDelay))

			var backup backupv1alpha1.KopiaBackup
			Expect(k8sClient.Get(ctx, typeNamespacedName, &backup)).To(Succeed())

			readyCond := meta.FindStatusCondition(backup.Status.Conditions, backupv1alpha1.ConditionTypeReady)
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal(backupv1alpha1.ReasonNoPodFound))
		})

		It("should ignore missing KopiaBackup (not found)", func() {
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

		It("should re-apply node affinity on an existing CronJob that lacks it", func() {
			// Regression test: reconcileCronJob used to compare the already-mutated
			// "existing" object against "desired", which made the equality check
			// always true and skipped the Update. A CronJob created while no pod was
			// running (no affinity) would therefore never get node affinity even
			// after a pod started, leaving snapshot pods stuck on the wrong node.
			controllerReconciler := &KopiaBackupReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			const targetNode = "worker-node-1"

			By("creating a running pod that mounts the PVC on a specific node")
			consumerPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pvc-consumer",
					Namespace: namespace,
					Labels:    map[string]string{"app.kubernetes.io/name": "consumer"},
				},
				Spec: corev1.PodSpec{
					NodeName: targetNode,
					Containers: []corev1.Container{{
						Name:  "app",
						Image: "busybox",
					}},
					Volumes: []corev1.Volume{{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: pvcName,
							},
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, consumerPod)).To(Succeed())
			// Pod phase is part of status and must be set after creation.
			consumerPod.Status.Phase = corev1.PodRunning
			Expect(k8sClient.Status().Update(ctx, consumerPod)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, consumerPod)
			})

			By("reconciling so the CronJob gets created with affinity")
			// First reconcile adds the finalizer, second does the real work.
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			cronJobKey := types.NamespacedName{
				Name:      naming.CronJobName(pvcName),
				Namespace: namespace,
			}
			cronJob := &batchv1.CronJob{}
			Expect(k8sClient.Get(ctx, cronJobKey, cronJob)).To(Succeed())
			Expect(cronJob.Spec.JobTemplate.Spec.Template.Spec.Affinity).NotTo(BeNil(),
				"CronJob should have node affinity after reconcile with a running pod")

			By("simulating the broken legacy state: stripping affinity from the CronJob")
			cronJob.Spec.JobTemplate.Spec.Template.Spec.Affinity = nil
			Expect(k8sClient.Update(ctx, cronJob)).To(Succeed())

			By("reconciling again should re-apply the affinity (Update must not be skipped)")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			fixed := &batchv1.CronJob{}
			Expect(k8sClient.Get(ctx, cronJobKey, fixed)).To(Succeed())
			affinity := fixed.Spec.JobTemplate.Spec.Template.Spec.Affinity
			Expect(affinity).NotTo(BeNil(), "affinity must be restored on reconcile")
			Expect(affinity.NodeAffinity).NotTo(BeNil())
			terms := affinity.NodeAffinity.
				RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
			Expect(terms).To(HaveLen(1))
			Expect(terms[0].MatchExpressions).To(HaveLen(1))
			Expect(terms[0].MatchExpressions[0].Key).To(Equal("kubernetes.io/hostname"))
			Expect(terms[0].MatchExpressions[0].Values).To(ContainElement(targetNode))
		})
	})
})
