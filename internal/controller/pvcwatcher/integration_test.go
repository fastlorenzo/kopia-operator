package pvcwatcher

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

var _ = Describe("PVC Watcher Integration (with SetupWithManager)", Ordered, func() {
	var (
		ctx       context.Context
		cancel    context.CancelFunc
		ns        string
		testNsIdx int
	)

	BeforeAll(func() {
		ctx, cancel = context.WithCancel(context.Background())

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: scheme.Scheme,
			Metrics: metricsserver.Options{
				BindAddress: "0", // disable metrics server
			},
		})
		Expect(err).NotTo(HaveOccurred())

		reconciler := &PVCWatcherReconciler{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorderFor("pvc-watcher"),
		}
		Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

		go func() {
			defer GinkgoRecover()
			Expect(mgr.Start(ctx)).To(Succeed())
		}()
	})

	AfterAll(func() {
		cancel()
	})

	BeforeEach(func() {
		testNsIdx++
		ns = fmt.Sprintf("integration-pvc-%d", testNsIdx)
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
	})

	createRepo := func(name string) {
		repo := &backupv1alpha1.KopiaRepository{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: backupv1alpha1.KopiaRepositorySpec{
				StorageType:        backupv1alpha1.StorageTypeFilesystem,
				Hostname:           "test-host",
				Username:           "admin",
				PasswordSecretName: "kopia-pass",
				FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
					Path: "/data/repo",
				},
				DefaultSchedule: "0 2 * * *",
			},
		}
		Expect(k8sClient.Create(ctx, repo)).To(Succeed())
	}

	createPVC := func(name string, labels map[string]string, annotations map[string]string) {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   ns,
				Labels:      labels,
				Annotations: annotations,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pvc)).To(Succeed())
	}

	It("auto-creates a KopiaBackup when PVC gets the repository label", func() {
		createRepo("my-repo")
		createPVC("auto-pvc", map[string]string{
			repositoryLabelKey: "my-repo",
		}, nil)

		// Wait for the KopiaBackup to be auto-created
		backupKey := types.NamespacedName{Name: "auto-pvc", Namespace: ns}
		Eventually(func() error {
			return k8sClient.Get(ctx, backupKey, &backupv1alpha1.KopiaBackup{})
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

		// Verify the backup has correct spec
		backup := &backupv1alpha1.KopiaBackup{}
		Expect(k8sClient.Get(ctx, backupKey, backup)).To(Succeed())
		Expect(backup.Spec.PVCName).To(Equal("auto-pvc"))
		Expect(backup.Spec.Repository).To(Equal("my-repo"))
		Expect(backup.Spec.Schedule).To(Equal("0 2 * * *"))
		Expect(backup.Status.AutoCreated).To(BeTrue())
	})

	It("uses schedule annotation override when present", func() {
		createRepo("my-repo")
		createPVC("sched-pvc", map[string]string{
			repositoryLabelKey: "my-repo",
		}, map[string]string{
			scheduleAnnotationKey: "0 5 * * *",
		})

		backupKey := types.NamespacedName{Name: "sched-pvc", Namespace: ns}
		Eventually(func() error {
			return k8sClient.Get(ctx, backupKey, &backupv1alpha1.KopiaBackup{})
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

		backup := &backupv1alpha1.KopiaBackup{}
		Expect(k8sClient.Get(ctx, backupKey, backup)).To(Succeed())
		Expect(backup.Spec.Schedule).To(Equal("0 5 * * *"))
	})

	It("deletes the KopiaBackup when the repository label is removed", func() {
		createRepo("my-repo")
		createPVC("del-pvc", map[string]string{
			repositoryLabelKey: "my-repo",
		}, nil)

		// Wait for auto-creation
		backupKey := types.NamespacedName{Name: "del-pvc", Namespace: ns}
		Eventually(func() error {
			return k8sClient.Get(ctx, backupKey, &backupv1alpha1.KopiaBackup{})
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

		// Remove the label
		pvc := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "del-pvc", Namespace: ns}, pvc)).To(Succeed())
		delete(pvc.Labels, repositoryLabelKey)
		Expect(k8sClient.Update(ctx, pvc)).To(Succeed())

		// Wait for auto-deletion
		Eventually(func() bool {
			err := k8sClient.Get(ctx, backupKey, &backupv1alpha1.KopiaBackup{})
			return err != nil
		}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())
	})

	It("does not delete a manually created KopiaBackup when label removed", func() {
		createRepo("my-repo")

		// Create a PVC without the label
		createPVC("manual-pvc", nil, nil)

		// Manually create a KopiaBackup (not auto-created)
		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{Name: "manual-pvc", Namespace: ns},
			Spec: backupv1alpha1.KopiaBackupSpec{
				PVCName:    "manual-pvc",
				Repository: "my-repo",
				Schedule:   "0 3 * * *",
			},
		}
		Expect(k8sClient.Create(ctx, backup)).To(Succeed())

		// Add then remove the label — should not delete the manual backup
		pvc := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "manual-pvc", Namespace: ns}, pvc)).To(Succeed())
		pvc.Labels = map[string]string{repositoryLabelKey: "my-repo"}
		Expect(k8sClient.Update(ctx, pvc)).To(Succeed())

		// Brief wait for any reconciliation
		time.Sleep(1 * time.Second)

		pvc = &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "manual-pvc", Namespace: ns}, pvc)).To(Succeed())
		delete(pvc.Labels, repositoryLabelKey)
		Expect(k8sClient.Update(ctx, pvc)).To(Succeed())

		// The manual backup should survive
		Consistently(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "manual-pvc", Namespace: ns}, &backupv1alpha1.KopiaBackup{})
		}, 3*time.Second, 500*time.Millisecond).Should(Succeed())
	})
})
