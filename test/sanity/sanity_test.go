/*
Copyright 2019 The Kubernetes Authors.

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

package sanity

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/azurelustre-csi-driver/test/utils/azure"
	"sigs.k8s.io/azurelustre-csi-driver/test/utils/credentials"
)

func TestSanity(t *testing.T) {
	creds, err := credentials.CreateAzureCredentialFile()
	defer func() {
		err := credentials.DeleteAzureCredentialFile()
		require.NoError(t, err)
	}()
	require.NoError(t, err)
	assert.NotNil(t, creds)

	t.Setenv("AZURE_CREDENTIAL_FILE", credentials.TempAzureCredentialFilePath)

	azureClient, err := azure.GetClient(creds.Cloud, creds.SubscriptionID, creds.AADClientID, creds.TenantID, creds.AADClientSecret)
	require.NoError(t, err)

	ctx := context.Background()
	// Create an empty resource group for sanity test
	log.Printf("Creating resource group %s in %s", creds.ResourceGroup, creds.Cloud)
	_, err = azureClient.EnsureResourceGroup(ctx, creds.ResourceGroup, creds.Location, nil)
	require.NoError(t, err)
	defer func() {
		// Only delete resource group the test created
		if strings.HasPrefix(creds.ResourceGroup, credentials.ResourceGroupPrefix) {
			log.Printf("Deleting resource group %s", creds.ResourceGroup)
			err := azureClient.DeleteResourceGroup(ctx, creds.ResourceGroup)
			require.NoError(t, err)
		}
	}()

	// Execute the script from project root
	err = os.Chdir("../..")
	require.NoError(t, err)
	// Change directory back to test/sanity
	defer func() {
		err := os.Chdir("test/sanity")
		require.NoError(t, err)
	}()

	projectRoot, err := os.Getwd()
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(projectRoot, "azurelustre-csi-driver"))

	cmd := exec.Command("./test/sanity/run-tests-all-clouds.sh", creds.Cloud)
	cmd.Dir = projectRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Sanity test failed %v", err)
	}
}
