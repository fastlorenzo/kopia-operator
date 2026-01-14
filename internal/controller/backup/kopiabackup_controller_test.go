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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

func setupTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	return scheme
}

func TestKopiaBackupReconciler_Reconcile_ResourceNotFound(t *testing.T) {
	scheme := setupTestScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaBackupReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existent",
			Namespace: "default",
		},
	})

	// Should not return error for not found
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestKopiaBackupReconciler_Reconcile_PVCNotFound(t *testing.T) {
	scheme := setupTestScheme()

	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "non-existent-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&backupv1alpha1.KopiaBackup{}).
		WithObjects(backup).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaBackupReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	_, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-backup",
			Namespace: "default",
		},
	})

	// Should return error when PVC not found (checked in getRelatedPVC)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PVC not found")
}

func TestKopiaBackupReconciler_Reconcile_RepositoryNotFound(t *testing.T) {
	scheme := setupTestScheme()

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}

	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "non-existent-repo",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pvc, backup).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaBackupReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	_, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-backup",
			Namespace: "default",
		},
	})

	// Should return error when repository not found
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestKopiaBackupReconciler_Reconcile_NoPodRunning(t *testing.T) {
	scheme := setupTestScheme()

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:           "test-host",
			Username:           "test-user",
			StorageType:        "filesystem",
			RepositoryPassword: "test-password",
			FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
				Path: "/tmp/test-repo",
			},
		},
	}

	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&backupv1alpha1.KopiaBackup{}).
		WithObjects(pvc, repo, backup).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaBackupReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-backup",
			Namespace: "default",
		},
	})

	// Should requeue when no pod is running
	require.NoError(t, err)
	assert.True(t, result.Requeue)
}

func TestKopiaBackupReconciler_Reconcile_SuccessfulCronJobCreation(t *testing.T) {
	scheme := setupTestScheme()

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:           "test-host",
			Username:           "test-user",
			StorageType:        "filesystem",
			RepositoryPassword: "test-password",
			FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
				Path:      "/backup",
				NFSServer: "nfs.example.com",
				NFSPath:   "/exports/backup",
			},
		},
	}

	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
		},
	}

	// Pod running with PVC mounted
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-pod",
			Namespace: "default",
			Labels: map[string]string{
				"app.kubernetes.io/name": "my-app",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "worker-1",
			Volumes: []corev1.Volume{
				{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: "test-pvc",
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&backupv1alpha1.KopiaBackup{}).
		WithObjects(pvc, repo, backup, pod).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaBackupReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-backup",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	// Verify CronJob was created
	cronJob := &batchv1.CronJob{}
	err = client.Get(ctx, types.NamespacedName{
		Name:      "snapshot-test-pvc",
		Namespace: "default",
	}, cronJob)
	require.NoError(t, err)
	assert.Equal(t, "0 2 * * *", cronJob.Spec.Schedule)
}

func TestShouldUpdateCronJob(t *testing.T) {
	tests := []struct {
		name     string
		found    *batchv1.CronJob
		new      *batchv1.CronJob
		expected bool
	}{
		{
			name: "same spec",
			found: &batchv1.CronJob{
				Spec: batchv1.CronJobSpec{
					Schedule: "0 2 * * *",
				},
			},
			new: &batchv1.CronJob{
				Spec: batchv1.CronJobSpec{
					Schedule: "0 2 * * *",
				},
			},
			expected: false,
		},
		{
			name: "different schedule",
			found: &batchv1.CronJob{
				Spec: batchv1.CronJobSpec{
					Schedule: "0 2 * * *",
				},
			},
			new: &batchv1.CronJob{
				Spec: batchv1.CronJobSpec{
					Schedule: "0 3 * * *",
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldUpdateCronJob(tt.found, tt.new)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldUpdateConfigMap(t *testing.T) {
	tests := []struct {
		name     string
		found    *corev1.ConfigMap
		new      *corev1.ConfigMap
		expected bool
	}{
		{
			name: "same data",
			found: &corev1.ConfigMap{
				Data: map[string]string{"key": "value"},
			},
			new: &corev1.ConfigMap{
				Data: map[string]string{"key": "value"},
			},
			expected: false,
		},
		{
			name: "different data",
			found: &corev1.ConfigMap{
				Data: map[string]string{"key": "old-value"},
			},
			new: &corev1.ConfigMap{
				Data: map[string]string{"key": "new-value"},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldUpdateConfigMap(tt.found, tt.new)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildBackupHistoryEntry(t *testing.T) {
	now := time.Now()
	startTime := metav1.NewTime(now.Add(-10 * time.Minute))
	completionTime := metav1.NewTime(now)

	tests := []struct {
		name           string
		job            *batchv1.Job
		expectedStatus backupv1alpha1.BackupStatus
	}{
		{
			name: "successful job",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-job",
				},
				Status: batchv1.JobStatus{
					StartTime:      &startTime,
					CompletionTime: &completionTime,
					Succeeded:      1,
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobComplete,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expectedStatus: backupv1alpha1.BackupStatusSuccessful,
		},
		{
			name: "failed job",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-job",
				},
				Status: batchv1.JobStatus{
					StartTime: &startTime,
					Failed:    1,
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobFailed,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expectedStatus: backupv1alpha1.BackupStatusFailed,
		},
		{
			name: "in progress job",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-job",
				},
				Status: batchv1.JobStatus{
					StartTime: &startTime,
					Active:    1,
				},
			},
			expectedStatus: backupv1alpha1.BackupStatusInProgress,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := buildBackupHistoryEntry(tt.job)
			assert.Equal(t, tt.expectedStatus, entry.Status)
			assert.Equal(t, "test-job", entry.JobName)
		})
	}
}

func TestGetJobBackupStatus(t *testing.T) {
	tests := []struct {
		name           string
		job            *batchv1.Job
		expectedStatus backupv1alpha1.BackupStatus
	}{
		{
			name: "complete condition",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobComplete,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expectedStatus: backupv1alpha1.BackupStatusSuccessful,
		},
		{
			name: "failed condition",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobFailed,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expectedStatus: backupv1alpha1.BackupStatusFailed,
		},
		{
			name: "active pods",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Active: 1,
				},
			},
			expectedStatus: backupv1alpha1.BackupStatusInProgress,
		},
		{
			name: "no activity (pending)",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Active:    0,
					Succeeded: 0,
					Failed:    0,
				},
			},
			expectedStatus: backupv1alpha1.BackupStatusInProgress,
		},
		{
			name: "fallback to succeeded count",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Succeeded: 1,
				},
			},
			expectedStatus: backupv1alpha1.BackupStatusSuccessful,
		},
		{
			name: "fallback to failed count",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Failed: 1,
				},
			},
			expectedStatus: backupv1alpha1.BackupStatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := getJobBackupStatus(tt.job)
			assert.Equal(t, tt.expectedStatus, status)
		})
	}
}

func TestHandlePVCRequest_NoPVC(t *testing.T) {
	scheme := setupTestScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaBackupReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := handlePVCRequest(log, ctx, reconciler, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existent-pvc",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestHandlePVCRequest_NoLabels(t *testing.T) {
	scheme := setupTestScheme()

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			// No labels
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pvc).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaBackupReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := handlePVCRequest(log, ctx, reconciler, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-pvc",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestGetRelatedPVC(t *testing.T) {
	scheme := setupTestScheme()
	log := zap.New(zap.UseDevMode(true))

	t.Run("PVC found", func(t *testing.T) {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pvc",
				Namespace: "default",
			},
		}

		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaBackupSpec{
				PVCName: "test-pvc",
			},
		}

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(pvc).
			Build()

		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		foundPVC, err := getRelatedPVC(log, ctx, reconciler, backup)

		require.NoError(t, err)
		require.NotNil(t, foundPVC)
		assert.Equal(t, "test-pvc", foundPVC.Name)
	})

	t.Run("PVC not found", func(t *testing.T) {
		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaBackupSpec{
				PVCName: "non-existent",
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		foundPVC, err := getRelatedPVC(log, ctx, reconciler, backup)

		require.Error(t, err)
		assert.Nil(t, foundPVC)
	})

	t.Run("No PVC name specified", func(t *testing.T) {
		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaBackupSpec{
				PVCName: "",
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		foundPVC, err := getRelatedPVC(log, ctx, reconciler, backup)

		require.Error(t, err)
		assert.Nil(t, foundPVC)
		assert.Contains(t, err.Error(), "no PVC specified")
	})
}

func TestGetRuntimeInfo(t *testing.T) {
	scheme := setupTestScheme()
	log := zap.New(zap.UseDevMode(true))

	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName: "test-pvc",
		},
	}

	t.Run("no pods running", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		nodeName, appName, podName, err := getRuntimeInfo(log, ctx, reconciler, backup)

		require.NoError(t, err)
		assert.Empty(t, nodeName)
		assert.Empty(t, appName)
		assert.Empty(t, podName)
	})

	t.Run("pod running with PVC", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "app-pod",
				Namespace: "default",
				Labels: map[string]string{
					"app.kubernetes.io/name": "my-app",
				},
			},
			Spec: corev1.PodSpec{
				NodeName: "worker-1",
				Volumes: []corev1.Volume{
					{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "test-pvc",
							},
						},
					},
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		}

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(pod).
			Build()

		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		nodeName, appName, podName, err := getRuntimeInfo(log, ctx, reconciler, backup)

		require.NoError(t, err)
		assert.Equal(t, "worker-1", nodeName)
		assert.Equal(t, "my-app", appName)
		assert.Equal(t, "app-pod", podName)
	})

	t.Run("skip backup pods", func(t *testing.T) {
		backupPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "snapshot-test-pvc-123",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				NodeName: "worker-1",
				Volumes: []corev1.Volume{
					{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "test-pvc",
							},
						},
					},
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		}

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(backupPod).
			Build()

		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		nodeName, _, _, err := getRuntimeInfo(log, ctx, reconciler, backup)

		require.NoError(t, err)
		assert.Empty(t, nodeName) // Should skip backup pods
	})

	t.Run("pod not running phase", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "app-pod",
				Namespace: "default",
				Labels: map[string]string{
					"app.kubernetes.io/name": "my-app",
				},
			},
			Spec: corev1.PodSpec{
				NodeName: "worker-1",
				Volumes: []corev1.Volume{
					{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "test-pvc",
							},
						},
					},
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending, // Not running
			},
		}

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(pod).
			Build()

		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		nodeName, _, _, err := getRuntimeInfo(log, ctx, reconciler, backup)

		require.NoError(t, err)
		assert.Empty(t, nodeName) // Should skip non-running pods
	})

	t.Run("pod with different PVC", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "app-pod",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				NodeName: "worker-1",
				Volumes: []corev1.Volume{
					{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "other-pvc", // Different PVC
							},
						},
					},
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		}

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(pod).
			Build()

		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		nodeName, _, _, err := getRuntimeInfo(log, ctx, reconciler, backup)

		require.NoError(t, err)
		assert.Empty(t, nodeName) // Should not match different PVC
	})
}

func TestHandlePVCRequest_WithValidLabels(t *testing.T) {
	scheme := setupTestScheme()

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:        "test-host",
			Username:        "test-user",
			StorageType:     "filesystem",
			DefaultSchedule: "0 3 * * *",
		},
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			Labels: map[string]string{
				"backup.cloudinfra.be/repository": "test-repo",
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&backupv1alpha1.KopiaBackup{}).
		WithObjects(pvc, repo).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaBackupReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := handlePVCRequest(log, ctx, reconciler, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-pvc",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	// Verify KopiaBackup was created
	backup := &backupv1alpha1.KopiaBackup{}
	err = client.Get(ctx, types.NamespacedName{
		Name:      "test-pvc",
		Namespace: "default",
	}, backup)
	require.NoError(t, err)
	assert.Equal(t, "test-pvc", backup.Spec.PVCName)
	assert.Equal(t, "test-repo", backup.Spec.Repository)
	assert.Equal(t, "0 3 * * *", backup.Spec.Schedule)
}

func TestHandlePVCRequest_RepositoryNotFound(t *testing.T) {
	scheme := setupTestScheme()

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			Labels: map[string]string{
				"backup.cloudinfra.be/repository": "non-existent-repo",
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pvc).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaBackupReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	_, err := handlePVCRequest(log, ctx, reconciler, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-pvc",
			Namespace: "default",
		},
	})

	// Should return error because repository not found
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestHandlePVCRequest_LabelsWithoutRepositoryKey(t *testing.T) {
	scheme := setupTestScheme()

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			Labels: map[string]string{
				"app": "myapp", // Labels exist but not the repository key
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pvc).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaBackupReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := handlePVCRequest(log, ctx, reconciler, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-pvc",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestShouldDeleteKopiaBackup(t *testing.T) {
	scheme := setupTestScheme()
	log := zap.New(zap.UseDevMode(true))

	t.Run("not from annotation should not delete", func(t *testing.T) {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pvc",
				Namespace: "default",
			},
		}

		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
			Status: backupv1alpha1.KopiaBackupStatus{
				FromAnnotation: false, // Not created from annotation
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		shouldDelete, err := shouldDeleteKopiaBackup(log, ctx, reconciler, backup, pvc)

		require.NoError(t, err)
		assert.False(t, shouldDelete)
	})

	t.Run("from annotation with label should not delete", func(t *testing.T) {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pvc",
				Namespace: "default",
				Labels: map[string]string{
					"backup.cloudinfra.be/repository": "test-repo",
				},
			},
		}

		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaBackupSpec{
				Repository: "test-repo",
			},
			Status: backupv1alpha1.KopiaBackupStatus{
				FromAnnotation: true, // Created from annotation
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		shouldDelete, err := shouldDeleteKopiaBackup(log, ctx, reconciler, backup, pvc)

		require.NoError(t, err)
		assert.False(t, shouldDelete)
	})

	t.Run("from annotation without label should delete", func(t *testing.T) {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pvc",
				Namespace: "default",
				Labels:    map[string]string{}, // No backup label
			},
		}

		repo := &backupv1alpha1.KopiaRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-repo",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaRepositorySpec{
				Server: backupv1alpha1.KopiaServerSpec{
					Enabled: false, // Direct mode - no user cleanup
				},
			},
		}

		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaBackupSpec{
				Repository: "test-repo",
			},
			Status: backupv1alpha1.KopiaBackupStatus{
				FromAnnotation: true,
			},
		}

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(backup, repo).
			Build()

		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		shouldDelete, err := shouldDeleteKopiaBackup(log, ctx, reconciler, backup, pvc)

		require.NoError(t, err)
		assert.True(t, shouldDelete)
	})
}

func TestGetOrDeleteCronJob(t *testing.T) {
	scheme := setupTestScheme()
	log := zap.New(zap.UseDevMode(true))

	t.Run("cronjob not found", func(t *testing.T) {
		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
		}

		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pvc",
				Namespace: "default",
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		cronJob, shouldDelete, err := getOrDeleteCronJob(log, ctx, reconciler, "snapshot-test-pvc", backup, pvc)

		require.NoError(t, err)
		assert.Nil(t, cronJob)
		assert.False(t, shouldDelete)
	})

	t.Run("cronjob found with pvc", func(t *testing.T) {
		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
		}

		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pvc",
				Namespace: "default",
			},
		}

		cronJob := &batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "snapshot-test-pvc",
				Namespace: "default",
			},
		}

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(cronJob).
			Build()

		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		foundCronJob, shouldDelete, err := getOrDeleteCronJob(log, ctx, reconciler, "snapshot-test-pvc", backup, pvc)

		require.NoError(t, err)
		require.NotNil(t, foundCronJob)
		assert.Equal(t, "snapshot-test-pvc", foundCronJob.Name)
		assert.False(t, shouldDelete)
	})

	t.Run("cronjob found without pvc should delete", func(t *testing.T) {
		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
		}

		cronJob := &batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "snapshot-test-pvc",
				Namespace: "default",
			},
		}

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(cronJob).
			Build()

		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		foundCronJob, shouldDelete, err := getOrDeleteCronJob(log, ctx, reconciler, "snapshot-test-pvc", backup, nil)

		require.NoError(t, err)
		require.NotNil(t, foundCronJob)
		assert.True(t, shouldDelete)
	})
}

func TestBuildBackupHistoryEntry_WithFailedConditionMessage(t *testing.T) {
	now := time.Now()
	startTime := metav1.NewTime(now.Add(-10 * time.Minute))

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-job",
		},
		Status: batchv1.JobStatus{
			StartTime: &startTime,
			Failed:    1,
			Conditions: []batchv1.JobCondition{
				{
					Type:    batchv1.JobFailed,
					Status:  corev1.ConditionTrue,
					Message: "BackoffLimitExceeded: Job has reached the specified backoff limit",
				},
			},
		},
	}

	entry := buildBackupHistoryEntry(job)

	assert.Equal(t, backupv1alpha1.BackupStatusFailed, entry.Status)
	assert.Equal(t, "BackoffLimitExceeded: Job has reached the specified backoff limit", entry.Message)
}

func TestBuildBackupHistoryEntry_WithNoStartTime(t *testing.T) {
	creationTime := metav1.NewTime(time.Now())

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-job",
			CreationTimestamp: creationTime,
		},
		Status: batchv1.JobStatus{
			// No StartTime set
			Active: 1,
		},
	}

	entry := buildBackupHistoryEntry(job)

	assert.Equal(t, backupv1alpha1.BackupStatusInProgress, entry.Status)
	assert.Equal(t, creationTime, entry.StartTime)
	assert.Equal(t, "Backup is currently running", entry.Message)
}

func TestUpdateBackupStatusFromJobs(t *testing.T) {
	scheme := setupTestScheme()
	log := zap.New(zap.UseDevMode(true))

	t.Run("no jobs - status should be pending", func(t *testing.T) {
		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaBackupSpec{
				PVCName: "test-pvc",
			},
		}

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&backupv1alpha1.KopiaBackup{}).
			WithObjects(backup).
			Build()

		reconciler := &KopiaBackupReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		err := reconciler.updateBackupStatusFromJobs(ctx, log, backup)

		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.BackupStatusPending, backup.Status.LastBackupStatus)
	})

	t.Run("with successful job", func(t *testing.T) {
		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaBackupSpec{
				PVCName: "test-pvc",
			},
		}

		now := time.Now()
		startTime := metav1.NewTime(now.Add(-10 * time.Minute))
		completionTime := metav1.NewTime(now)

		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "snapshot-test-pvc-12345",
				Namespace: "default",
			},
			Status: batchv1.JobStatus{
				StartTime:      &startTime,
				CompletionTime: &completionTime,
				Succeeded:      1,
				Conditions: []batchv1.JobCondition{
					{
						Type:   batchv1.JobComplete,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&backupv1alpha1.KopiaBackup{}).
			WithObjects(backup, job).
			Build()

		reconciler := &KopiaBackupReconciler{
			Client:   client,
			Scheme:   scheme,
			Recorder: record.NewFakeRecorder(10),
		}

		ctx := context.Background()
		err := reconciler.updateBackupStatusFromJobs(ctx, log, backup)

		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.BackupStatusSuccessful, backup.Status.LastBackupStatus)
		assert.NotNil(t, backup.Status.LastBackupTime)
		assert.NotNil(t, backup.Status.LastSuccessfulBackupTime)
		assert.Len(t, backup.Status.BackupHistory, 1)
	})

	t.Run("with failed job", func(t *testing.T) {
		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaBackupSpec{
				PVCName: "test-pvc",
			},
		}

		now := time.Now()
		startTime := metav1.NewTime(now.Add(-10 * time.Minute))

		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "snapshot-test-pvc-12345",
				Namespace: "default",
			},
			Status: batchv1.JobStatus{
				StartTime: &startTime,
				Failed:    1,
				Conditions: []batchv1.JobCondition{
					{
						Type:   batchv1.JobFailed,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&backupv1alpha1.KopiaBackup{}).
			WithObjects(backup, job).
			Build()

		reconciler := &KopiaBackupReconciler{
			Client:   client,
			Scheme:   scheme,
			Recorder: record.NewFakeRecorder(10),
		}

		ctx := context.Background()
		err := reconciler.updateBackupStatusFromJobs(ctx, log, backup)

		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.BackupStatusFailed, backup.Status.LastBackupStatus)
		assert.NotNil(t, backup.Status.LastBackupTime)
		assert.Nil(t, backup.Status.LastSuccessfulBackupTime) // No successful backup
	})

	t.Run("with multiple jobs", func(t *testing.T) {
		backup := &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaBackupSpec{
				PVCName: "test-pvc",
			},
		}

		now := time.Now()
		startTime1 := metav1.NewTime(now.Add(-30 * time.Minute))
		completionTime1 := metav1.NewTime(now.Add(-20 * time.Minute))
		startTime2 := metav1.NewTime(now.Add(-10 * time.Minute))

		job1 := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "snapshot-test-pvc-11111",
				Namespace: "default",
			},
			Status: batchv1.JobStatus{
				StartTime:      &startTime1,
				CompletionTime: &completionTime1,
				Succeeded:      1,
				Conditions: []batchv1.JobCondition{
					{
						Type:   batchv1.JobComplete,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}

		job2 := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "snapshot-test-pvc-22222",
				Namespace: "default",
			},
			Status: batchv1.JobStatus{
				StartTime: &startTime2,
				Failed:    1,
				Conditions: []batchv1.JobCondition{
					{
						Type:   batchv1.JobFailed,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&backupv1alpha1.KopiaBackup{}).
			WithObjects(backup, job1, job2).
			Build()

		reconciler := &KopiaBackupReconciler{
			Client:   client,
			Scheme:   scheme,
			Recorder: record.NewFakeRecorder(10),
		}

		ctx := context.Background()
		err := reconciler.updateBackupStatusFromJobs(ctx, log, backup)

		require.NoError(t, err)
		// Most recent job (job2) failed
		assert.Equal(t, backupv1alpha1.BackupStatusFailed, backup.Status.LastBackupStatus)
		// But we had a previous successful backup
		assert.NotNil(t, backup.Status.LastSuccessfulBackupTime)
		assert.Len(t, backup.Status.BackupHistory, 2)
	})
}
