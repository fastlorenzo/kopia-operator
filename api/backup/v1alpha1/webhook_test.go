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

package v1alpha1

import (
	"testing"
)

func TestValidateKopiaBackup_ValidSchedule(t *testing.T) {
	backup := &KopiaBackup{
		Spec: KopiaBackupSpec{
			PVCName:    "my-pvc",
			Schedule:   "0 3 * * *",
			Repository: "my-repo",
		},
	}
	warnings, err := validateKopiaBackup(backup)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if len(warnings) > 0 {
		t.Errorf("expected no warnings, got: %v", warnings)
	}
}

func TestValidateKopiaBackup_InvalidSchedule(t *testing.T) {
	tests := []struct {
		name     string
		schedule string
	}{
		{"too few fields", "0 3 *"},
		{"too many fields", "0 3 * * * *"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backup := &KopiaBackup{
				Spec: KopiaBackupSpec{
					PVCName:    "my-pvc",
					Schedule:   tt.schedule,
					Repository: "my-repo",
				},
			}
			_, err := validateKopiaBackup(backup)
			if err == nil {
				t.Errorf("expected error for schedule %q, got nil", tt.schedule)
			}
		})
	}
}

func TestValidateKopiaBackup_InvalidPVCName(t *testing.T) {
	backup := &KopiaBackup{
		Spec: KopiaBackupSpec{
			PVCName:    "INVALID_PVC",
			Schedule:   "0 3 * * *",
			Repository: "my-repo",
		},
	}
	_, err := validateKopiaBackup(backup)
	if err == nil {
		t.Error("expected error for invalid PVC name, got nil")
	}
}

func TestValidateKopiaRepository_Filesystem_Valid(t *testing.T) {
	repo := &KopiaRepository{
		Spec: KopiaRepositorySpec{
			StorageType:        StorageTypeFilesystem,
			PasswordSecretName: "secret",
			Hostname:           "host",
			Username:           "user",
			FileSystemOptions: KopiaRepositoryStorageFileSystemSpec{
				Path: "/data",
			},
		},
	}
	_, err := validateKopiaRepository(repo)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateKopiaRepository_Filesystem_MissingPath(t *testing.T) {
	repo := &KopiaRepository{
		Spec: KopiaRepositorySpec{
			StorageType:        StorageTypeFilesystem,
			PasswordSecretName: "secret",
			Hostname:           "host",
			Username:           "user",
		},
	}
	_, err := validateKopiaRepository(repo)
	if err == nil {
		t.Error("expected error for missing filesystem path, got nil")
	}
}

func TestValidateKopiaRepository_SFTP_Valid(t *testing.T) {
	repo := &KopiaRepository{
		Spec: KopiaRepositorySpec{
			StorageType:        StorageTypeSFTP,
			PasswordSecretName: "secret",
			Hostname:           "host",
			Username:           "user",
			SFTPOptions: KopiaRepositoryStorageSFTPSpec{
				Path:              "/data",
				Host:              "sftp.example.com",
				CredentialsSecret: "sftp-creds",
			},
		},
	}
	_, err := validateKopiaRepository(repo)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateKopiaRepository_SFTP_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		opts KopiaRepositoryStorageSFTPSpec
	}{
		{"missing path", KopiaRepositoryStorageSFTPSpec{Host: "h", CredentialsSecret: "s"}},
		{"missing host", KopiaRepositoryStorageSFTPSpec{Path: "/p", CredentialsSecret: "s"}},
		{"missing creds", KopiaRepositoryStorageSFTPSpec{Path: "/p", Host: "h"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &KopiaRepository{
				Spec: KopiaRepositorySpec{
					StorageType:        StorageTypeSFTP,
					PasswordSecretName: "secret",
					Hostname:           "host",
					Username:           "user",
					SFTPOptions:        tt.opts,
				},
			}
			_, err := validateKopiaRepository(repo)
			if err == nil {
				t.Errorf("expected error for %s, got nil", tt.name)
			}
		})
	}
}

func TestValidateKopiaRepository_ServerMode_Warning(t *testing.T) {
	repo := &KopiaRepository{
		Spec: KopiaRepositorySpec{
			StorageType:        StorageTypeFilesystem,
			PasswordSecretName: "secret",
			Hostname:           "host",
			Username:           "user",
			FileSystemOptions:  KopiaRepositoryStorageFileSystemSpec{Path: "/data"},
			Server: KopiaServerSpec{
				Enabled: true,
			},
		},
	}
	warnings, err := validateKopiaRepository(repo)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("expected a warning about missing adminPasswordSecretName")
	}
}
