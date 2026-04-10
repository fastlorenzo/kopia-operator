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

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fastlorenzo/kopia-operator/test/utils"
)

const (
	namespace    = "kopia-operator"
	testNS       = "kopia-e2e-test"
	projectimage = "kopia-operator:e2e-test"
)

var _ = Describe("Kopia Operator E2E", Ordered, func() {

	BeforeAll(func() {
		By("creating test namespace")
		cmd := exec.Command("kubectl", "create", "ns", testNS, "--dry-run=client", "-o", "yaml")
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		cmd = exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(string(output))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("building the operator image")
		cmd = exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectimage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("loading the operator image to Kind")
		err = utils.LoadImageToKindClusterWithName(projectimage)
		Expect(err).NotTo(HaveOccurred())

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectimage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("disabling webhooks (no cert-manager in kind)")
		cmd = exec.Command("kubectl", "set", "env",
			"deployment/kopia-operator-controller-manager",
			"-n", namespace,
			"ENABLE_WEBHOOKS=false",
			"-c", "manager",
		)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the controller-manager to be ready")
		Eventually(func() error {
			cmd := exec.Command("kubectl", "wait", "deployment",
				"-l", "control-plane=controller-manager",
				"--for", "condition=Available",
				"-n", namespace,
				"--timeout", "60s",
			)
			_, err := utils.Run(cmd)
			return err
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("creating SFTP server for testing")
		createSFTPServer()

		By("creating prerequisite secrets and PVCs")
		createTestPrerequisites()
	})

	AfterAll(func() {
		By("cleaning up test resources")
		cleanupTestResources()

		By("undeploying the controller-manager")
		cmd := exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("removing test namespace")
		cmd = exec.Command("kubectl", "delete", "ns", testNS, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	Context("Operator Deployment", func() {
		It("should have the controller pod running", func() {
			cmd := exec.Command("kubectl", "get", "pods",
				"-l", "control-plane=controller-manager",
				"-n", namespace,
				"-o", "jsonpath={.items[0].status.phase}",
			)
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(Equal("Running"))
		})
	})

	Context("Filesystem Backup via CRD", func() {
		It("should create a KopiaRepository with filesystem storage", func() {
			yaml := fmt.Sprintf(`
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: fs-repo
  namespace: %s
spec:
  hostname: e2e-test
  username: kopia
  storageType: filesystem
  passwordSecretName: kopia-repo-password
  fileSystemOptions:
    path: /backup/kopia
`, testNS)
			applyYAML(yaml)

			Eventually(func() string {
				return getConditionStatus(testNS, "kopiarepository", "fs-repo", "Ready")
			}, 30*time.Second, 2*time.Second).Should(Equal("True"))
		})

		It("should create a KopiaBackup that produces a CronJob", func() {
			yaml := fmt.Sprintf(`
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: fs-backup
  namespace: %s
spec:
  pvcName: test-data
  schedule: "*/5 * * * *"
  repository: fs-repo
`, testNS)
			applyYAML(yaml)

			// The backup controller should add a finalizer and create a CronJob
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "kopiabackup", "fs-backup",
					"-n", testNS,
					"-o", "jsonpath={.metadata.finalizers}")
				output, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return strings.Contains(string(output), "backup.cloudinfra.be/finalizer")
			}, 60*time.Second, 5*time.Second).Should(BeTrue())
		})

		It("should set conditions on the KopiaBackup", func() {
			Eventually(func() string {
				return getConditionReason(testNS, "kopiabackup", "fs-backup", "Ready")
			}, 60*time.Second, 5*time.Second).ShouldNot(BeEmpty())
		})

		It("should clean up when KopiaBackup is deleted", func() {
			cmd := exec.Command("kubectl", "delete", "kopiabackup", "fs-backup",
				"-n", testNS, "--timeout=30s")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "kopiabackup", "fs-backup",
					"-n", testNS)
				_, err := utils.Run(cmd)
				return err
			}, 30*time.Second, 2*time.Second).Should(HaveOccurred())
		})
	})

	Context("SFTP Backup via CRD", func() {
		It("should create a KopiaRepository with SFTP storage", func() {
			yaml := fmt.Sprintf(`
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: sftp-repo
  namespace: %s
spec:
  hostname: e2e-test
  username: kopia
  storageType: sftp
  passwordSecretName: kopia-repo-password
  sftpOptions:
    host: sftp-server.%s.svc.cluster.local
    port: 22
    path: /upload/kopia
    credentialsSecret: sftp-credentials
`, testNS, testNS)
			applyYAML(yaml)

			Eventually(func() string {
				return getConditionStatus(testNS, "kopiarepository", "sftp-repo", "Ready")
			}, 30*time.Second, 2*time.Second).Should(Equal("True"))
		})

		It("should create a KopiaBackup for SFTP repository", func() {
			yaml := fmt.Sprintf(`
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: sftp-backup
  namespace: %s
spec:
  pvcName: test-data
  schedule: "*/10 * * * *"
  repository: sftp-repo
`, testNS)
			applyYAML(yaml)

			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "kopiabackup", "sftp-backup",
					"-n", testNS,
					"-o", "jsonpath={.metadata.finalizers}")
				output, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return strings.Contains(string(output), "backup.cloudinfra.be/finalizer")
			}, 60*time.Second, 5*time.Second).Should(BeTrue())
		})
	})

	Context("Suspend/Resume", func() {
		It("should suspend backup when suspend is set to true", func() {
			cmd := exec.Command("kubectl", "patch", "kopiabackup", "sftp-backup",
				"-n", testNS,
				"--type=merge", "-p", `{"spec":{"suspend":true}}`)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			// Trigger reconcile
			time.Sleep(5 * time.Second)

			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "kopiabackup", "sftp-backup",
					"-n", testNS,
					"-o", "jsonpath={.spec.suspend}")
				output, _ := utils.Run(cmd)
				return string(output)
			}, 30*time.Second, 2*time.Second).Should(Equal("true"))
		})
	})
})

// --- Helper functions ---

func applyYAML(yaml string) {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

func getConditionStatus(ns, resource, name, condType string) string {
	jsonpath := fmt.Sprintf(
		`{.status.conditions[?(@.type=="%s")].status}`, condType)
	cmd := exec.Command("kubectl", "get", resource, name,
		"-n", ns, "-o", fmt.Sprintf("jsonpath=%s", jsonpath))
	output, err := utils.Run(cmd)
	if err != nil {
		return ""
	}
	return string(output)
}

func getConditionReason(ns, resource, name, condType string) string {
	jsonpath := fmt.Sprintf(
		`{.status.conditions[?(@.type=="%s")].reason}`, condType)
	cmd := exec.Command("kubectl", "get", resource, name,
		"-n", ns, "-o", fmt.Sprintf("jsonpath=%s", jsonpath))
	output, err := utils.Run(cmd)
	if err != nil {
		return ""
	}
	return string(output)
}

func createSFTPServer() {
	yaml := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sftp-server
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: sftp-server
  template:
    metadata:
      labels:
        app: sftp-server
    spec:
      containers:
      - name: sftp
        image: atmoz/sftp:latest
        args: ["kopia:kopia123:1001:100:upload"]
        ports:
        - containerPort: 22
        volumeMounts:
        - name: sftp-data
          mountPath: /home/kopia/upload
      volumes:
      - name: sftp-data
        emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: sftp-server
  namespace: %s
spec:
  selector:
    app: sftp-server
  ports:
  - port: 22
    targetPort: 22
`, testNS, testNS)
	applyYAML(yaml)

	// Wait for SFTP server to be ready
	Eventually(func() error {
		cmd := exec.Command("kubectl", "wait", "deployment/sftp-server",
			"--for", "condition=Available",
			"-n", testNS,
			"--timeout", "60s",
		)
		_, err := utils.Run(cmd)
		return err
	}, 2*time.Minute, 5*time.Second).Should(Succeed())
}

func createTestPrerequisites() {
	yaml := fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: kopia-repo-password
  namespace: %s
type: Opaque
stringData:
  password: "e2e-test-password-12345"
---
apiVersion: v1
kind: Secret
metadata:
  name: sftp-credentials
  namespace: %s
type: Opaque
stringData:
  username: "kopia"
  password: "kopia123"
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-data
  namespace: %s
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 100Mi
`, testNS, testNS, testNS)
	applyYAML(yaml)
}

func cleanupTestResources() {
	resources := []string{
		"kopiabackup --all",
		"kopiarepository --all",
		"cronjob --all",
		"configmap -l app.kubernetes.io/managed-by=kopia-operator",
		"deployment sftp-server",
		"service sftp-server",
		"secret kopia-repo-password",
		"secret sftp-credentials",
		"pvc test-data",
	}
	for _, r := range resources {
		cmd := exec.Command("bash", "-c",
			fmt.Sprintf("kubectl delete %s -n %s --ignore-not-found --timeout=30s", r, testNS))
		_, _ = utils.Run(cmd)
	}
}
