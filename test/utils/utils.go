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

package utils

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:golint,revive
)

const (
	// SFTPNamespace is the namespace where the SFTP server is deployed
	SFTPNamespace = "sftp-server"
	// SFTPServiceName is the name of the SFTP service
	SFTPServiceName = "sftp-server"
	// SFTPImage is the image used for SFTP server
	SFTPImage = "atmoz/sftp:latest"
	// SFTPUser is the SFTP username
	SFTPUser = "foo"
	// SFTPPassword is the SFTP password
	SFTPPassword = "pass"
)

func warnError(err error) {
	fmt.Fprintf(GinkgoWriter, "warning: %v\n", err)
}

// Run executes the provided command within this context
func Run(cmd *exec.Cmd) ([]byte, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir

	if err := os.Chdir(cmd.Dir); err != nil {
		fmt.Fprintf(GinkgoWriter, "chdir dir: %s\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed with error: (%v) %s", command, err, string(output))
	}

	return output, nil
}

// RunWithoutDir executes the provided command without changing the directory
func RunWithoutDir(cmd *exec.Cmd) ([]byte, error) {
	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed with error: (%v) %s", command, err, string(output))
	}
	return output, nil
}

// DeploySFTPServer deploys an SFTP server inside the Kubernetes cluster
func DeploySFTPServer() error {
	// First, ensure the namespace is fully deleted if it exists
	CleanupSFTPServer()

	// Wait for namespace to be fully deleted
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		cmd := exec.Command("kubectl", "get", "namespace", SFTPNamespace, "-o", "name")
		output, err := RunWithoutDir(cmd)
		if err != nil || strings.TrimSpace(string(output)) == "" {
			// Namespace doesn't exist, we can proceed
			break
		}
		fmt.Fprintf(GinkgoWriter, "Waiting for namespace %s to be deleted...\n", SFTPNamespace)
		time.Sleep(2 * time.Second)
	}

	// Create the SFTP namespace
	if err := CreateNamespace(SFTPNamespace); err != nil {
		return fmt.Errorf("failed to create SFTP namespace: %w", err)
	}

	// Deploy the SFTP server
	// The atmoz/sftp image format is user:password:uid:gid:dir
	// The 'dir' part creates a subdirectory that the user can write to
	// We specify 'upload' as the writable directory
	sftpManifest := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
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
        image: %s
        args: ["%s:%s:1001:1001:upload"]
        ports:
        - containerPort: 22
          name: ssh
        readinessProbe:
          tcpSocket:
            port: 22
          initialDelaySeconds: 5
          periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
spec:
  selector:
    app: sftp-server
  ports:
  - port: 22
    targetPort: 22
    name: ssh
`, SFTPServiceName, SFTPNamespace, SFTPImage, SFTPUser, SFTPPassword, SFTPServiceName, SFTPNamespace)

	if err := ApplyManifest(sftpManifest); err != nil {
		return fmt.Errorf("failed to deploy SFTP server: %w", err)
	}

	// Wait for the SFTP server to be ready
	if err := WaitForDeploymentReady(SFTPServiceName, SFTPNamespace, 2*time.Minute); err != nil {
		return fmt.Errorf("SFTP server deployment not ready: %w", err)
	}

	fmt.Fprintf(GinkgoWriter, "SFTP server deployed successfully at %s.%s.svc.cluster.local:22\n", SFTPServiceName, SFTPNamespace)
	return nil
}

// CleanupSFTPServer removes the SFTP server from the cluster
func CleanupSFTPServer() {
	// Delete the namespace (this will clean up all resources)
	_ = DeleteNamespace(SFTPNamespace)
}

// GetSFTPServiceHost returns the in-cluster DNS name for the SFTP service
func GetSFTPServiceHost() string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", SFTPServiceName, SFTPNamespace)
}

// GetSFTPHostKey retrieves the SSH host key from the SFTP server pod
// It returns only the actual key lines (filtering out comments and debug messages)
func GetSFTPHostKey() (string, error) {
	// Wait a moment for SSH to be fully ready
	time.Sleep(2 * time.Second)

	// Get the host key by executing ssh-keyscan from within a pod
	// Use kubectl run to create a temporary pod for the scan
	cmd := exec.Command("kubectl", "run", "ssh-keyscan-tmp", "--rm", "-i", "--restart=Never",
		"--image=alpine", "-n", SFTPNamespace, "--",
		"sh", "-c",
		fmt.Sprintf("apk add --no-cache openssh-client >/dev/null 2>&1 && ssh-keyscan -H %s 2>/dev/null", GetSFTPServiceHost()))
	output, err := Run(cmd)
	if err != nil {
		// Fallback: try to get the pod IP and scan from host
		podIP, ipErr := getSFTPPodIP()
		if ipErr != nil {
			return "", fmt.Errorf("failed to get SSH host key: %w (also failed to get pod IP: %v)", err, ipErr)
		}
		cmd = exec.Command("ssh-keyscan", "-H", podIP)
		output, err = RunWithoutDir(cmd)
		if err != nil {
			return "", fmt.Errorf("failed to get SSH host key via fallback: %w", err)
		}
	}

	// Filter output to only include actual key lines
	// Key lines start with |1| (hashed host) or hostname and contain ssh-rsa, ssh-ed25519, etc.
	return filterSSHKeyLines(string(output)), nil
}

// filterSSHKeyLines filters ssh-keyscan output to only include actual key lines
func filterSSHKeyLines(output string) string {
	var keyLines []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// Skip empty lines, comment lines, and kubectl messages
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Skip kubectl run output messages
		if strings.Contains(line, "don't see a command prompt") ||
			strings.Contains(line, "pod \"") ||
			strings.Contains(line, "deleted") {
			continue
		}
		// Valid key lines start with |1| (hashed) or contain ssh- key types
		if strings.HasPrefix(line, "|1|") && (strings.Contains(line, "ssh-rsa") ||
			strings.Contains(line, "ssh-ed25519") ||
			strings.Contains(line, "ecdsa-sha2")) {
			keyLines = append(keyLines, line)
		}
	}
	return strings.Join(keyLines, "\n")
}

// getSFTPPodIP returns the IP of the SFTP pod
func getSFTPPodIP() (string, error) {
	cmd := exec.Command("kubectl", "get", "pods", "-l", "app=sftp-server",
		"-n", SFTPNamespace, "-o", "jsonpath={.items[0].status.podIP}")
	output, err := Run(cmd)
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(output))
	if ip == "" {
		return "", fmt.Errorf("SFTP pod IP is empty")
	}
	return ip, nil
}

// LoadImageToKindCluster loads a local docker image to the kind cluster
func LoadImageToKindClusterWithName(name string) error {
	cluster := "kind"
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		cluster = v
	}
	kindOptions := []string{"load", "docker-image", name, "--name", cluster}
	cmd := exec.Command("kind", kindOptions...)
	_, err := Run(cmd)
	return err
}

// GetNonEmptyLines converts given command output string into individual objects
// according to line breakers, and ignores the empty elements in it.
func GetNonEmptyLines(output string) []string {
	var res []string
	elements := strings.Split(output, "\n")
	for _, element := range elements {
		if element != "" {
			res = append(res, element)
		}
	}

	return res
}

// GetProjectDir will return the directory where the project is
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, err
	}
	wd = strings.Replace(wd, "/test/e2e", "", -1)
	return wd, nil
}

// WaitForDeploymentReady waits for a deployment to be ready
func WaitForDeploymentReady(name, namespace string, timeout time.Duration) error {
	cmd := exec.Command("kubectl", "rollout", "status",
		"deployment/"+name,
		"-n", namespace,
		"--timeout", fmt.Sprintf("%.0fs", timeout.Seconds()),
	)
	_, err := Run(cmd)
	return err
}

// WaitForPodReady waits for pods matching a label selector to be ready
func WaitForPodReady(labelSelector, namespace string, timeout time.Duration) error {
	cmd := exec.Command("kubectl", "wait", "pod",
		"-l", labelSelector,
		"--for", "condition=Ready",
		"-n", namespace,
		"--timeout", fmt.Sprintf("%.0fs", timeout.Seconds()),
	)
	_, err := Run(cmd)
	return err
}

// WaitForCronJobToRun waits for a CronJob to create at least one job
// It looks up the CronJob using the backup.cloudinfra.be/backup label
func WaitForCronJobToRun(backupName, namespace string, timeout time.Duration) error {
	labelSelector := fmt.Sprintf("backup.cloudinfra.be/backup=%s", backupName)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Get the CronJob by label
		cmd := exec.Command("kubectl", "get", "cronjob",
			"-n", namespace,
			"-l", labelSelector,
			"-o", "jsonpath={.items[0].status.lastScheduleTime}",
		)
		output, err := Run(cmd)
		if err != nil {
			fmt.Fprintf(GinkgoWriter, "CronJob for backup %s not found yet, waiting...\n", backupName)
			time.Sleep(5 * time.Second)
			continue
		}

		lastScheduleTime := strings.TrimSpace(string(output))
		if lastScheduleTime != "" {
			fmt.Fprintf(GinkgoWriter, "CronJob for backup %s has run (lastScheduleTime: %s)\n", backupName, lastScheduleTime)
			return nil
		}

		fmt.Fprintf(GinkgoWriter, "CronJob for backup %s exists but hasn't run yet, waiting...\n", backupName)
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timeout waiting for CronJob for backup %s to run", backupName)
}

// WaitForJobCompletion waits for a job to complete
func WaitForJobCompletion(labelSelector, namespace string, timeout time.Duration) error {
	cmd := exec.Command("kubectl", "wait", "job",
		"-l", labelSelector,
		"--for", "condition=Complete",
		"-n", namespace,
		"--timeout", fmt.Sprintf("%.0fs", timeout.Seconds()),
	)
	_, err := Run(cmd)
	return err
}

// ApplyManifest applies a YAML manifest using kubectl
func ApplyManifest(manifest string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := Run(cmd)
	return err
}

// DeleteManifest deletes resources defined in a YAML manifest
func DeleteManifest(manifest string) error {
	cmd := exec.Command("kubectl", "delete", "-f", "-", "--ignore-not-found")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := Run(cmd)
	return err
}

// GetResource gets a Kubernetes resource in JSON format
func GetResource(resourceType, name, namespace string) (string, error) {
	cmd := exec.Command("kubectl", "get", resourceType, name, "-n", namespace, "-o", "json")
	output, err := Run(cmd)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// CreateNamespace creates a Kubernetes namespace
func CreateNamespace(name string) error {
	cmd := exec.Command("kubectl", "create", "namespace", name)
	_, err := Run(cmd)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

// DeleteNamespace deletes a Kubernetes namespace
func DeleteNamespace(name string) error {
	cmd := exec.Command("kubectl", "delete", "namespace", name, "--ignore-not-found")
	_, err := Run(cmd)
	return err
}

// GetKopiaBackupStatus returns the status of a KopiaBackup resource
func GetKopiaBackupStatus(name, namespace string) (string, error) {
	cmd := exec.Command("kubectl", "get", "kopiabackup", name, "-n", namespace,
		"-o", "jsonpath={.status}")
	output, err := Run(cmd)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// GetKopiaRepositoryStatus returns the status of a KopiaRepository resource
func GetKopiaRepositoryStatus(name, namespace string) (string, error) {
	cmd := exec.Command("kubectl", "get", "kopiarepository", name, "-n", namespace,
		"-o", "jsonpath={.status}")
	output, err := Run(cmd)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// DeployWithTestOverlay deploys the controller using the test kustomize overlay
// which sets imagePullPolicy to IfNotPresent for local Kind testing
func DeployWithTestOverlay(image, namespace string) error {
	projectDir, err := GetProjectDir()
	if err != nil {
		return fmt.Errorf("failed to get project dir: %w", err)
	}

	// First, update the kustomize image reference
	cmd := exec.Command("bash", "-c",
		fmt.Sprintf("cd %s/config/manager && kustomize edit set image controller=%s", projectDir, image))
	if _, err := Run(cmd); err != nil {
		// Try with the local kustomize binary
		cmd = exec.Command("bash", "-c",
			fmt.Sprintf("cd %s/config/manager && %s/bin/kustomize edit set image controller=%s", projectDir, projectDir, image))
		if _, err := Run(cmd); err != nil {
			return fmt.Errorf("failed to set kustomize image: %w", err)
		}
	}

	// Build and apply the test overlay
	cmd = exec.Command("bash", "-c",
		fmt.Sprintf("kustomize build %s/config/test | kubectl apply -f -", projectDir))
	if _, err := Run(cmd); err != nil {
		// Try with the local kustomize binary
		cmd = exec.Command("bash", "-c",
			fmt.Sprintf("%s/bin/kustomize build %s/config/test | kubectl apply -f -", projectDir, projectDir))
		if _, err := Run(cmd); err != nil {
			return fmt.Errorf("failed to deploy with test overlay: %w", err)
		}
	}

	return nil
}

// UndeployWithTestOverlay removes the controller deployed with test overlay
func UndeployWithTestOverlay() error {
	projectDir, err := GetProjectDir()
	if err != nil {
		return fmt.Errorf("failed to get project dir: %w", err)
	}

	cmd := exec.Command("bash", "-c",
		fmt.Sprintf("kustomize build %s/config/test | kubectl delete --ignore-not-found -f -", projectDir))
	if _, err := Run(cmd); err != nil {
		// Try with the local kustomize binary
		cmd = exec.Command("bash", "-c",
			fmt.Sprintf("%s/bin/kustomize build %s/config/test | kubectl delete --ignore-not-found -f -", projectDir, projectDir))
		if _, err := Run(cmd); err != nil {
			warnError(err)
		}
	}
	return nil
}
