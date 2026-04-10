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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

// getCronJobNameFromPVCName generates the CronJob name from a PVC name.
// Name format: snapshot-<first 42 chars>-<last char> if name > 42 chars.
func getCronJobNameFromPVCName(pvcName string) string {
	if len(pvcName) > 42 {
		return "snapshot-" + pvcName[:42] + "-" + string(pvcName[len(pvcName)-1])
	}
	return "snapshot-" + pvcName
}

// getKopiaRepositoryByName finds a KopiaRepository by name in the given namespace.
func getKopiaRepositoryByName(
	ctx context.Context,
	c client.Client,
	namespace string,
	repositoryName string,
) (*backupv1alpha1.KopiaRepository, error) {
	logger := log.FromContext(ctx)

	repository := &backupv1alpha1.KopiaRepository{}
	err := c.Get(ctx, types.NamespacedName{Name: repositoryName, Namespace: namespace}, repository)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("KopiaRepository not found", "repository", repositoryName, "namespace", namespace)
			return nil, fmt.Errorf("KopiaRepository %q not found in namespace %q", repositoryName, namespace)
		}
		return nil, fmt.Errorf("failed to get KopiaRepository %q: %w", repositoryName, err)
	}

	return repository, nil
}
