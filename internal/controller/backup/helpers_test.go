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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

func TestGetCronJobNameFromPVCName(t *testing.T) {
	tests := []struct {
		name     string
		pvcName  string
		expected string
	}{
		{
			name:     "short PVC name",
			pvcName:  "my-pvc",
			expected: "snapshot-my-pvc",
		},
		{
			name:     "exactly 42 characters",
			pvcName:  "123456789012345678901234567890123456789012",
			expected: "snapshot-123456789012345678901234567890123456789012",
		},
		{
			name:     "more than 42 characters",
			pvcName:  "1234567890123456789012345678901234567890123",
			expected: "snapshot-123456789012345678901234567890123456789012-3",
		},
		{
			name:     "long PVC name",
			pvcName:  "this-is-a-very-long-pvc-name-that-exceeds-42-characters-limit",
			expected: "snapshot-this-is-a-very-long-pvc-name-that-exceeds--t",
		},
		{
			name:     "empty PVC name",
			pvcName:  "",
			expected: "snapshot-",
		},
		{
			name:     "single character",
			pvcName:  "a",
			expected: "snapshot-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getCronJobNameFromPVCName(tt.pvcName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetKopiaRepositoryByName(t *testing.T) {
	// Create a scheme with the CRD types
	scheme := runtime.NewScheme()
	err := backupv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	log := zap.New(zap.UseDevMode(true))

	tests := []struct {
		name           string
		repositoryName string
		existingRepos  []backupv1alpha1.KopiaRepository
		expectError    bool
		expectNil      bool
		errorContains  string
	}{
		{
			name:           "repository exists in default namespace",
			repositoryName: "test-repo",
			existingRepos: []backupv1alpha1.KopiaRepository{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-repo",
						Namespace: "default",
					},
					Spec: backupv1alpha1.KopiaRepositorySpec{
						Hostname:    "test-host",
						Username:    "test-user",
						StorageType: "filesystem",
					},
				},
			},
			expectError: false,
			expectNil:   false,
		},
		{
			name:           "repository not found",
			repositoryName: "non-existent-repo",
			existingRepos:  []backupv1alpha1.KopiaRepository{},
			expectError:    true,
			expectNil:      true,
			errorContains:  "not found",
		},
		{
			name:           "multiple repositories with same name in different namespaces",
			repositoryName: "shared-repo",
			existingRepos: []backupv1alpha1.KopiaRepository{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "shared-repo",
						Namespace: "ns1",
					},
					Spec: backupv1alpha1.KopiaRepositorySpec{
						Hostname:    "test-host",
						Username:    "test-user",
						StorageType: "filesystem",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "shared-repo",
						Namespace: "ns2",
					},
					Spec: backupv1alpha1.KopiaRepositorySpec{
						Hostname:    "test-host",
						Username:    "test-user",
						StorageType: "filesystem",
					},
				},
			},
			expectError:   true,
			expectNil:     true,
			errorContains: "multiple KopiaRepositories",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build client with existing repositories
			clientBuilder := fake.NewClientBuilder().WithScheme(scheme)
			for i := range tt.existingRepos {
				clientBuilder = clientBuilder.WithObjects(&tt.existingRepos[i])
			}
			client := clientBuilder.Build()

			ctx := context.Background()
			result, err := getKopiaRepositoryByName(ctx, client, tt.repositoryName, log)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}

			if tt.expectNil {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
				assert.Equal(t, tt.repositoryName, result.Name)
			}
		})
	}
}

func TestGetCronJobNameFromPVCName_LengthConstraints(t *testing.T) {
	// Test that the generated name doesn't exceed Kubernetes name limits
	// Kubernetes resource names must be <= 253 characters
	longPVCName := ""
	for i := 0; i < 100; i++ {
		longPVCName += "a"
	}

	result := getCronJobNameFromPVCName(longPVCName)

	// The result should be: "snapshot-" (9 chars) + 42 chars + "-" (1 char) + 1 char = 53 chars max
	assert.LessOrEqual(t, len(result), 253)
	assert.LessOrEqual(t, len(result), 63) // DNS label limit
}
