package naming

import (
	"testing"
)

func TestCronJobName(t *testing.T) {
	tests := []struct {
		name     string
		pvcName  string
		expected string
		maxLen   int
	}{
		{
			name:     "short name",
			pvcName:  "my-pvc",
			expected: "snapshot-my-pvc",
			maxLen:   54,
		},
		{
			name:    "long name is truncated",
			pvcName: "this-is-a-very-long-pvc-name-that-exceeds-forty-two-characters-limit",
			maxLen:  54,
		},
		{
			name:     "empty name",
			pvcName:  "",
			expected: "snapshot-",
			maxLen:   54,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CronJobName(tt.pvcName)
			if tt.expected != "" && result != tt.expected {
				t.Errorf("CronJobName(%q) = %q, want %q", tt.pvcName, result, tt.expected)
			}
			if len(result) > tt.maxLen {
				t.Errorf("CronJobName(%q) length = %d, want <= %d", tt.pvcName, len(result), tt.maxLen)
			}
			if result[:9] != "snapshot-" {
				t.Errorf("CronJobName(%q) should start with 'snapshot-', got %q", tt.pvcName, result)
			}
		})
	}
}

func TestServerDeploymentName(t *testing.T) {
	if got := ServerDeploymentName("my-repo"); got != "kopia-server-my-repo" {
		t.Errorf("ServerDeploymentName = %q, want %q", got, "kopia-server-my-repo")
	}
}

func TestServerServiceName(t *testing.T) {
	if got := ServerServiceName("my-repo"); got != "kopia-server-my-repo" {
		t.Errorf("ServerServiceName = %q, want %q", got, "kopia-server-my-repo")
	}
}

func TestTLSSecretName(t *testing.T) {
	if got := TLSSecretName("my-repo"); got != "kopia-server-tls-my-repo" {
		t.Errorf("TLSSecretName = %q, want %q", got, "kopia-server-tls-my-repo")
	}
}

func TestUserSecretName(t *testing.T) {
	if got := UserSecretName("default", "pgdata"); got != "kopia-backup-user-default-pgdata" {
		t.Errorf("UserSecretName = %q, want %q", got, "kopia-backup-user-default-pgdata")
	}
}

func TestConfigMapName(t *testing.T) {
	if got := ConfigMapName("my-repo"); got != "kopia-config-my-repo" {
		t.Errorf("ConfigMapName = %q, want %q", got, "kopia-config-my-repo")
	}
}

func TestUsername(t *testing.T) {
	if got := Username("default", "pgdata", "myhost"); got != "default-pgdata@myhost" {
		t.Errorf("Username = %q, want %q", got, "default-pgdata@myhost")
	}
}

func TestServerLabels(t *testing.T) {
	labels := ServerLabels("my-repo")
	if labels["app"] != "kopia-server" {
		t.Errorf("ServerLabels missing app label")
	}
	if labels["kopia-repository"] != "my-repo" {
		t.Errorf("ServerLabels kopia-repository = %q, want %q", labels["kopia-repository"], "my-repo")
	}
}
