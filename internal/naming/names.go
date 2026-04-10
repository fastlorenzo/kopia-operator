package naming

import "fmt"

// CronJobName generates the CronJob name from a PVC name.
// Name format: snapshot-<first 42 chars>-<last char> if name > 42 chars.
func CronJobName(pvcName string) string {
	if len(pvcName) > 42 {
		return "snapshot-" + pvcName[:42] + "-" + string(pvcName[len(pvcName)-1])
	}
	return "snapshot-" + pvcName
}

// ServerDeploymentName returns the Deployment name for a Kopia Server.
func ServerDeploymentName(repoName string) string {
	return fmt.Sprintf("kopia-server-%s", repoName)
}

// ServerServiceName returns the Service name for a Kopia Server.
func ServerServiceName(repoName string) string {
	return fmt.Sprintf("kopia-server-%s", repoName)
}

// TLSSecretName returns the default TLS secret name for a repository.
func TLSSecretName(repoName string) string {
	return fmt.Sprintf("kopia-server-tls-%s", repoName)
}

// UserSecretName returns the user credentials secret name.
func UserSecretName(namespace, pvcName string) string {
	return fmt.Sprintf("kopia-backup-user-%s-%s", namespace, pvcName)
}

// ConfigMapName returns the Kopia config ConfigMap name.
func ConfigMapName(repoName string) string {
	return fmt.Sprintf("kopia-config-%s", repoName)
}

// Username returns the Kopia user identifier for a backup.
func Username(namespace, pvcName, hostname string) string {
	return fmt.Sprintf("%s-%s@%s", namespace, pvcName, hostname)
}

// ServerLabels returns the standard labels for Kopia Server resources.
func ServerLabels(repoName string) map[string]string {
	return map[string]string{
		"app":                          "kopia-server",
		"kopia-repository":             repoName,
		"app.kubernetes.io/name":       "kopia-server",
		"app.kubernetes.io/instance":   repoName,
		"app.kubernetes.io/managed-by": "kopia-operator",
	}
}
