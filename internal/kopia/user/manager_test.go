package user

import (
	"context"
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
	"github.com/fastlorenzo/kopia-operator/internal/kopia"
	"github.com/fastlorenzo/kopia-operator/internal/naming"
)

// newTestManager creates a KopiaUserManager wired to envtest and a mock executor.
func newTestManager(exec PodExecutor) *KopiaUserManager {
	return &KopiaUserManager{
		Client:      k8sClient,
		Scheme:      scheme.Scheme,
		podExecutor: exec,
	}
}

// successExecutor always succeeds.
func successExecutor(_ context.Context, _, _, _ string, _ []string) (string, string, error) {
	return "ok", "", nil
}

// failExecutor returns the given error.
func failExecutor(err error) PodExecutor {
	return func(_ context.Context, _, _, _ string, _ []string) (string, string, error) {
		return "", "error", err
	}
}

func createTestNamespace(ctx context.Context, name string) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	_ = k8sClient.Create(ctx, ns)
}

func testBackup(ns, name, pvc string) *backupv1alpha1.KopiaBackup {
	return &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("test-uid-" + name)},
		Spec:       backupv1alpha1.KopiaBackupSpec{PVCName: pvc, Repository: "test-repo"},
	}
}

func testRepo(ns string) *backupv1alpha1.KopiaRepository {
	return &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "test-repo", Namespace: ns},
		Spec:       backupv1alpha1.KopiaRepositorySpec{Hostname: "kopia-host"},
		Status:     backupv1alpha1.KopiaRepositoryStatus{TLSCertFingerprint: "abc123"},
	}
}

// createServerPod creates a pod with matching labels so getServerPodName finds it.
func createServerPod(ctx context.Context, ns string) {
	const repoName = "test-repo"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      naming.ServerDeploymentName(repoName),
			Namespace: ns,
			Labels:    naming.ServerLabels(repoName),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "kopia-server",
				Image: "kopia:test",
			}},
		},
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())

	// Mark pod as Running with Ready container
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "kopia-server",
		Ready: true,
	}}
	Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
}

var _ = Describe("User Manager Helpers", func() {
	Context("generateSecurePassword", func() {
		It("should generate a password of the correct length", func() {
			pw, err := generateSecurePassword()
			Expect(err).NotTo(HaveOccurred())
			Expect(pw).To(HaveLen(32))
		})

		It("should generate different passwords each time", func() {
			pw1, _ := generateSecurePassword()
			pw2, _ := generateSecurePassword()
			Expect(pw1).NotTo(Equal(pw2))
		})
	})

	Context("ServerNotReadyError", func() {
		It("should implement error interface", func() {
			err := &kopia.ServerNotReadyError{Message: "deployment not found"}
			Expect(err.Error()).To(ContainSubstring("deployment not found"))
		})
	})

	Context("buildCreateUserCommand", func() {
		It("should pass credentials as positional arguments, not in the script", func() {
			cmd := buildCreateUserCommand("ns-pvc@host", "secret123", "fp456")
			Expect(cmd[0]).To(Equal("/bin/sh"))
			Expect(cmd[1]).To(Equal("-c"))
			script := cmd[2]
			Expect(script).NotTo(ContainSubstring("ns-pvc@host"))
			Expect(script).NotTo(ContainSubstring("secret123"))
			Expect(script).NotTo(ContainSubstring("fp456"))
			Expect(cmd[3]).To(Equal("_"))
			Expect(cmd[4]).To(Equal("ns-pvc@host"))
			Expect(cmd[5]).To(Equal("secret123"))
			Expect(cmd[6]).To(Equal("fp456"))
		})

		It("should safely handle adversarial input with shell metacharacters", func() {
			adversarial := "'; rm -rf / #"
			cmd := buildCreateUserCommand(adversarial, adversarial, adversarial)
			script := cmd[2]
			Expect(script).NotTo(ContainSubstring(adversarial))
			Expect(cmd[4]).To(Equal(adversarial))
			Expect(cmd[5]).To(Equal(adversarial))
			Expect(cmd[6]).To(Equal(adversarial))
		})

		It("should handle passwords containing single quotes and backticks", func() {
			cmd := buildCreateUserCommand("user@host", "p'a`ss$word", "fp")
			script := cmd[2]
			Expect(script).NotTo(ContainSubstring("p'a`ss$word"))
			Expect(cmd[5]).To(Equal("p'a`ss$word"))
		})

		It("should be idempotent — the script checks if the user already exists before creating", func() {
			cmd := buildCreateUserCommand("ns-pvc@host", "secret123", "fp456")
			script := cmd[2]
			// The script greps for the user first, and uses 'set' to update if found
			Expect(script).To(ContainSubstring("grep"))
			Expect(script).To(ContainSubstring("server user set"))
			Expect(script).To(ContainSubstring("server user add"))
		})
	})

	Context("buildDeleteUserCommand", func() {
		It("should pass username as a positional argument", func() {
			cmd := buildDeleteUserCommand("test@host")
			Expect(cmd[0]).To(Equal("/bin/sh"))
			Expect(cmd[1]).To(Equal("-c"))
			Expect(cmd[3]).To(Equal("_"))
			Expect(cmd[4]).To(Equal("test@host"))
		})

		It("should not interpolate username into the shell script", func() {
			cmd := buildDeleteUserCommand("'; drop table users; --")
			script := cmd[2]
			Expect(script).NotTo(ContainSubstring("'; drop table users; --"))
			Expect(cmd[4]).To(Equal("'; drop table users; --"))
		})
	})
})

var _ = Describe("EnsureUser", func() {
	var (
		ctx  context.Context
		ns   string
		nsID int
	)

	BeforeEach(func() {
		ctx = context.Background()
		nsID++
		ns = fmt.Sprintf("user-test-%d", nsID)
		createTestNamespace(ctx, ns)
	})

	It("creates a secret and calls the server when no secret exists", func() {
		var capturedCmd []string
		executor := func(_ context.Context, _, _, _ string, cmd []string) (string, string, error) {
			capturedCmd = cmd
			return "ok", "", nil
		}
		mgr := newTestManager(executor)

		backup := testBackup(ns, "b1", "my-pvc")
		repo := testRepo(ns)
		createServerPod(ctx, ns)

		secretName, err := mgr.EnsureUser(ctx, backup, repo)
		Expect(err).NotTo(HaveOccurred())
		Expect(secretName).To(Equal(naming.UserSecretName(ns, "my-pvc")))

		// Verify secret was created with correct keys
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, secret)).To(Succeed())
		Expect(secret.Data).To(HaveKey("KOPIA_SERVER_USERNAME"))
		Expect(secret.Data).To(HaveKey("KOPIA_SERVER_PASSWORD"))

		// Verify the exec command was called
		Expect(capturedCmd).NotTo(BeEmpty())
	})

	It("marks the secret as synced after creating the user", func() {
		mgr := newTestManager(successExecutor)

		backup := testBackup(ns, "b1b", "fresh-pvc")
		repo := testRepo(ns)
		createServerPod(ctx, ns)

		secretName, err := mgr.EnsureUser(ctx, backup, repo)
		Expect(err).NotTo(HaveOccurred())

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, secret)).To(Succeed())
		expected := credentialsFingerprint(string(secret.Data["KOPIA_SERVER_USERNAME"]), string(secret.Data["KOPIA_SERVER_PASSWORD"]))
		Expect(secret.Annotations).To(HaveKeyWithValue(UserSyncedAnnotation, expected))
	})

	It("reuses an existing unsynced secret, calls the server once, then skips subsequent calls", func() {
		calls := 0
		executor := func(_ context.Context, _, _, _ string, _ []string) (string, string, error) {
			calls++
			return "ok", "", nil
		}
		mgr := newTestManager(executor)

		backup := testBackup(ns, "b2", "existing-pvc")
		repo := testRepo(ns)
		createServerPod(ctx, ns)

		// Pre-create secret without the synced annotation (e.g. created by an older operator)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      naming.UserSecretName(ns, "existing-pvc"),
				Namespace: ns,
			},
			StringData: map[string]string{
				"KOPIA_SERVER_USERNAME": "preexisting-user",
				"KOPIA_SERVER_PASSWORD": "preexisting-pass",
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		secretName, err := mgr.EnsureUser(ctx, backup, repo)
		Expect(err).NotTo(HaveOccurred())
		Expect(secretName).To(Equal(naming.UserSecretName(ns, "existing-pvc")))
		Expect(calls).To(Equal(1))

		// The secret is now annotated with the credentials fingerprint
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, secret)).To(Succeed())
		Expect(secret.Annotations).To(HaveKeyWithValue(UserSyncedAnnotation,
			credentialsFingerprint("preexisting-user", "preexisting-pass")))

		// Subsequent reconciles must not exec into the server again
		_, err = mgr.EnsureUser(ctx, backup, repo)
		Expect(err).NotTo(HaveOccurred())
		_, err = mgr.EnsureUser(ctx, backup, repo)
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal(1))
	})

	It("re-syncs when the secret password changed since the last sync", func() {
		calls := 0
		mgr := newTestManager(func(_ context.Context, _, _, _ string, _ []string) (string, string, error) {
			calls++
			return "ok", "", nil
		})

		backup := testBackup(ns, "b2b", "rotated-pvc")
		repo := testRepo(ns)
		createServerPod(ctx, ns)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      naming.UserSecretName(ns, "rotated-pvc"),
				Namespace: ns,
				Annotations: map[string]string{
					UserSyncedAnnotation: credentialsFingerprint("rotated-user", "old-pass"),
				},
			},
			StringData: map[string]string{
				"KOPIA_SERVER_USERNAME": "rotated-user",
				"KOPIA_SERVER_PASSWORD": "new-pass",
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		_, err := mgr.EnsureUser(ctx, backup, repo)
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal(1))

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: ns}, secret)).To(Succeed())
		Expect(secret.Annotations).To(HaveKeyWithValue(UserSyncedAnnotation,
			credentialsFingerprint("rotated-user", "new-pass")))
	})

	It("does not mark the secret synced when the server call fails", func() {
		mgr := newTestManager(failExecutor(errors.New("boom")))

		backup := testBackup(ns, "b2c", "failing-pvc")
		repo := testRepo(ns)
		createServerPod(ctx, ns)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      naming.UserSecretName(ns, "failing-pvc"),
				Namespace: ns,
			},
			StringData: map[string]string{
				"KOPIA_SERVER_USERNAME": "u",
				"KOPIA_SERVER_PASSWORD": "p",
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		_, err := mgr.EnsureUser(ctx, backup, repo)
		Expect(err).To(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: ns}, secret)).To(Succeed())
		Expect(secret.Annotations).NotTo(HaveKey(UserSyncedAnnotation))
	})

	It("returns ServerNotReadyError when no server pod exists", func() {
		mgr := newTestManager(successExecutor)

		backup := testBackup(ns, "b3", "pvc3")
		repo := testRepo(ns)
		// No server pod created

		_, err := mgr.EnsureUser(ctx, backup, repo)
		Expect(err).To(HaveOccurred())
		var snr *kopia.ServerNotReadyError
		Expect(errors.As(err, &snr)).To(BeTrue())
	})

	It("returns ServerNotReadyError when exec fails with container not ready", func() {
		executor := failExecutor(fmt.Errorf("container not found"))
		mgr := newTestManager(executor)

		backup := testBackup(ns, "b4", "pvc4")
		repo := testRepo(ns)
		createServerPod(ctx, ns)

		_, err := mgr.EnsureUser(ctx, backup, repo)
		Expect(err).To(HaveOccurred())
		var snr *kopia.ServerNotReadyError
		Expect(errors.As(err, &snr)).To(BeTrue())
	})

	It("returns generic error when exec fails with non-server error", func() {
		executor := failExecutor(fmt.Errorf("network timeout"))
		mgr := newTestManager(executor)

		backup := testBackup(ns, "b5", "pvc5")
		repo := testRepo(ns)
		createServerPod(ctx, ns)

		_, err := mgr.EnsureUser(ctx, backup, repo)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("network timeout"))
		var snr *kopia.ServerNotReadyError
		Expect(errors.As(err, &snr)).To(BeFalse())
	})
})

var _ = Describe("DeleteUser", func() {
	var (
		ctx  context.Context
		ns   string
		nsID int
	)

	BeforeEach(func() {
		ctx = context.Background()
		nsID++
		ns = fmt.Sprintf("delete-test-%d", nsID)
		createTestNamespace(ctx, ns)
	})

	It("returns error when server exec fails, keeping the secret", func() {
		executor := failExecutor(fmt.Errorf("server down"))
		mgr := newTestManager(executor)

		backup := testBackup(ns, "b1", "del-pvc")
		repo := testRepo(ns)
		createServerPod(ctx, ns)

		// Pre-create the secret
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      naming.UserSecretName(ns, "del-pvc"),
				Namespace: ns,
			},
			StringData: map[string]string{
				"KOPIA_SERVER_USERNAME": "user",
				"KOPIA_SERVER_PASSWORD": "pass",
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		err := mgr.DeleteUser(ctx, backup, repo)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("server down"))

		// Secret should still exist so cleanup can be retried
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: ns}, &corev1.Secret{})).
			To(Succeed())
	})

	It("succeeds when secret does not exist", func() {
		mgr := newTestManager(successExecutor)

		backup := testBackup(ns, "b2", "no-secret-pvc")
		repo := testRepo(ns)
		createServerPod(ctx, ns)

		err := mgr.DeleteUser(ctx, backup, repo)
		Expect(err).NotTo(HaveOccurred())
	})
})
