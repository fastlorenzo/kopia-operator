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

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

// getCronJobNameFromPVCName generates the CronJob name from a PVC name
// Name format: snapshot-<first 42 chars>-<last char> if name > 42 chars
func getCronJobNameFromPVCName(pvcName string) string {
	if len(pvcName) > 42 {
		return "snapshot-" + pvcName[:42] + "-" + string(pvcName[len(pvcName)-1])
	}
	return "snapshot-" + pvcName
}

// getKopiaRepositoryByName finds a KopiaRepository by name, searching all namespaces if needed
func getKopiaRepositoryByName(
	ctx context.Context,
	c client.Client,
	repositoryName string,
	log logr.Logger,
) (*backupv1alpha1.KopiaRepository, error) {
	// Check if the repository exists in the current namespace
	repository := &backupv1alpha1.KopiaRepository{}
	err := c.Get(ctx, types.NamespacedName{Name: repositoryName}, repository)

	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "error getting KopiaRepository", "repositoryName", repositoryName)
			return nil, err
		}

		// Not found in current namespace, search all namespaces
		log.Info("KopiaRepository not found in the current namespace, checking all namespaces")
		var allRepositories backupv1alpha1.KopiaRepositoryList
		if err := c.List(ctx, &allRepositories); err != nil {
			log.Error(err, "Error listing KopiaRepositories")
			return nil, err
		}

		log.Info("found KopiaRepositories", "count", len(allRepositories.Items))
		var matchingRepositories []backupv1alpha1.KopiaRepository
		for _, repo := range allRepositories.Items {
			if repo.Name == repositoryName {
				matchingRepositories = append(matchingRepositories, repo)
			}
		}

		log.Info("found matching KopiaRepositories", "count", len(matchingRepositories))

		if len(matchingRepositories) == 0 {
			log.Info("KopiaRepository not found in all namespaces", "repositoryName", repositoryName)
			return nil, fmt.Errorf("KopiaRepository '%s' not found", repositoryName)
		}
		if len(matchingRepositories) > 1 {
			log.Error(nil, "multiple KopiaRepositories with the same name found", "repositoryName", repositoryName)
			return nil, fmt.Errorf("multiple KopiaRepositories with the same name found in multiple namespaces")
		}
		repository = &matchingRepositories[0]
	}

	log.Info("Found KopiaRepository", "repositoryName", repository.Name, "namespace", repository.Namespace)
	return repository, nil
}
