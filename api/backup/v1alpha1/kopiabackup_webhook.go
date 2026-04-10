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
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/validate-backup-cloudinfra-be-v1alpha1-kopiabackup,mutating=false,failurePolicy=fail,sideEffects=None,groups=backup.cloudinfra.be,resources=kopiabackups,verbs=create;update,versions=v1alpha1,name=vkopiabackup-v1alpha1.kb.io,admissionReviewVersions=v1

// KopiaBackupCustomValidator validates KopiaBackup resources.
type KopiaBackupCustomValidator struct{}

var _ webhook.CustomValidator = &KopiaBackupCustomValidator{}

// ValidateCreate validates a KopiaBackup on creation.
func (v *KopiaBackupCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	backup, ok := obj.(*KopiaBackup)
	if !ok {
		return nil, fmt.Errorf("expected KopiaBackup, got %T", obj)
	}
	return validateKopiaBackup(backup)
}

// ValidateUpdate validates a KopiaBackup on update.
func (v *KopiaBackupCustomValidator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	backup, ok := newObj.(*KopiaBackup)
	if !ok {
		return nil, fmt.Errorf("expected KopiaBackup, got %T", newObj)
	}
	return validateKopiaBackup(backup)
}

// ValidateDelete is a no-op for KopiaBackup.
func (v *KopiaBackupCustomValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateKopiaBackup(backup *KopiaBackup) (admission.Warnings, error) {
	var allErrs []string

	if err := validateCronSchedule(backup.Spec.Schedule); err != nil {
		allErrs = append(allErrs, fmt.Sprintf("spec.schedule: %v", err))
	}

	if errs := validation.IsDNS1123Subdomain(backup.Spec.PVCName); len(errs) > 0 {
		allErrs = append(allErrs, fmt.Sprintf("spec.pvcName: %s", strings.Join(errs, "; ")))
	}

	if len(allErrs) > 0 {
		return nil, fmt.Errorf("validation failed: %s", strings.Join(allErrs, ", "))
	}
	return nil, nil
}

// validateCronSchedule checks that a cron expression has exactly 5 fields.
func validateCronSchedule(schedule string) error {
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return fmt.Errorf("must have exactly 5 fields (minute hour day-of-month month day-of-week), got %d", len(fields))
	}
	return nil
}
