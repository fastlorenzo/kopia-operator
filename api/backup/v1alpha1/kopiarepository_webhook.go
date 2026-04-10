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
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/validate-backup-cloudinfra-be-v1alpha1-kopiarepository,mutating=false,failurePolicy=fail,sideEffects=None,groups=backup.cloudinfra.be,resources=kopiarepositories,verbs=create;update,versions=v1alpha1,name=vkopiarepository-v1alpha1.kb.io,admissionReviewVersions=v1

// KopiaRepositoryCustomValidator validates KopiaRepository resources.
type KopiaRepositoryCustomValidator struct{}

var _ webhook.CustomValidator = &KopiaRepositoryCustomValidator{}

// ValidateCreate validates a KopiaRepository on creation.
func (v *KopiaRepositoryCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	repo, ok := obj.(*KopiaRepository)
	if !ok {
		return nil, fmt.Errorf("expected KopiaRepository, got %T", obj)
	}
	return validateKopiaRepository(repo)
}

// ValidateUpdate validates a KopiaRepository on update.
func (v *KopiaRepositoryCustomValidator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	repo, ok := newObj.(*KopiaRepository)
	if !ok {
		return nil, fmt.Errorf("expected KopiaRepository, got %T", newObj)
	}
	return validateKopiaRepository(repo)
}

// ValidateDelete is a no-op for KopiaRepository.
func (v *KopiaRepositoryCustomValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateKopiaRepository(repo *KopiaRepository) (admission.Warnings, error) {
	var allErrs []string
	var warnings admission.Warnings

	switch repo.Spec.StorageType {
	case StorageTypeFilesystem:
		if repo.Spec.FileSystemOptions.Path == "" {
			allErrs = append(allErrs, "spec.fileSystemOptions.path is required when storageType is \"filesystem\"")
		}
	case StorageTypeSFTP:
		if repo.Spec.SFTPOptions.Path == "" {
			allErrs = append(allErrs, "spec.sftpOptions.path is required when storageType is \"sftp\"")
		}
		if repo.Spec.SFTPOptions.Host == "" {
			allErrs = append(allErrs, "spec.sftpOptions.host is required when storageType is \"sftp\"")
		}
		if repo.Spec.SFTPOptions.CredentialsSecret == "" {
			allErrs = append(allErrs, "spec.sftpOptions.credentialsSecret is required when storageType is \"sftp\"")
		}
	default:
		allErrs = append(allErrs, fmt.Sprintf("unsupported storageType %q, must be one of: %q, %q",
			repo.Spec.StorageType, StorageTypeFilesystem, StorageTypeSFTP))
	}

	if repo.Spec.Server.Enabled && repo.Spec.Server.AdminPasswordSecretName == "" {
		warnings = append(warnings, "server.adminPasswordSecretName not set; repository password secret will be used for the server admin password")
	}

	if len(allErrs) > 0 {
		return warnings, fmt.Errorf("validation failed: %s", strings.Join(allErrs, ", "))
	}
	return warnings, nil
}
