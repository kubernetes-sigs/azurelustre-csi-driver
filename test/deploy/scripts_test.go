/*
Copyright 2026 The Kubernetes Authors.

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

package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNamespaceGuards(t *testing.T) {
	tests := []struct {
		name         string
		driver       string
		nameNS       string
		partOfNS     string
		legacyNS     string
		discoveryErr string
		force        string
		defaultNS    bool
		installErr   string
		uninstallErr string
	}{
		{name: "fresh cluster"},
		{name: "default namespace upgrade", driver: "present", nameNS: "kube-system", defaultNS: true},
		{name: "same namespace upgrade", driver: "present", nameNS: "azurelustre-system"},
		{name: "partial same namespace install", partOfNS: "azurelustre-system"},
		{name: "legacy same namespace upgrade", driver: "present", legacyNS: "azurelustre-system"},
		{name: "node only other namespace", driver: "present", nameNS: "other", installErr: "already installed", uninstallErr: "workloads exist"},
		{name: "part of label only", partOfNS: "other", installErr: "already installed", uninstallErr: "workloads exist"},
		{name: "legacy labels only", legacyNS: "other", installErr: "already installed", uninstallErr: "workloads exist"},
		{name: "multiple namespaces", driver: "present", nameNS: "azurelustre-system\nother", installErr: "already installed", uninstallErr: "workloads exist"},
		{name: "mixed label generations", nameNS: "azurelustre-system", legacyNS: "other", installErr: "already installed", uninstallErr: "workloads exist"},
		{name: "orphan CSIDriver", driver: "present", installErr: "install namespace cannot be determined"},
		{name: "CSIDriver discovery failure", discoveryErr: "driver", installErr: "discovery failed"},
		{name: "name discovery failure", discoveryErr: "name", installErr: "discovery failed", uninstallErr: "discovery failed"},
		{name: "part of discovery failure", discoveryErr: "part-of", installErr: "discovery failed", uninstallErr: "discovery failed"},
		{name: "legacy discovery failure", discoveryErr: "legacy", installErr: "discovery failed", uninstallErr: "discovery failed"},
		{name: "explicit force cleanup", nameNS: "other", force: "1", installErr: "already installed"},
		{name: "force does not hide discovery errors", discoveryErr: "name", force: "1", installErr: "discovery failed", uninstallErr: "discovery failed"},
	}

	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, script := range []string{"install", "uninstall"} {
				t.Run(script, func(t *testing.T) {
					dir := t.TempDir()
					log := filepath.Join(dir, "mutations")
					require.NoError(t, os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeKubectl), 0o700)) // #nosec G306 -- executable test stub in t.TempDir
					t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
					t.Setenv("NAMESPACE", "azurelustre-system")
					namespace := "azurelustre-system"
					if tt.defaultNS {
						t.Setenv("NAMESPACE", "")
						namespace = "kube-system"
					}
					t.Setenv("FORCE", tt.force)
					t.Setenv("TEST_DRIVER", tt.driver)
					t.Setenv("TEST_NAME_NS", tt.nameNS)
					t.Setenv("TEST_PART_OF_NS", tt.partOfNS)
					t.Setenv("TEST_LEGACY_NS", tt.legacyNS)
					t.Setenv("TEST_DISCOVERY_ERROR", tt.discoveryErr)
					t.Setenv("TEST_MUTATIONS", log)

					args := []string{filepath.Join(root, "deploy", script+"-driver.sh")}
					if script == "install" {
						args = append(args, "local")
					}
					cmd := exec.CommandContext(t.Context(), "bash", args...) // #nosec G204 -- only runs the repository's deployment scripts
					cmd.Dir = root
					output, runErr := cmd.CombinedOutput()
					wantErr := tt.installErr
					if script == "uninstall" {
						wantErr = tt.uninstallErr
					}
					if wantErr != "" {
						require.Error(t, runErr, "%s", output)
						require.Contains(t, string(output), wantErr)
						_, statErr := os.Stat(log)
						require.True(t, os.IsNotExist(statErr), "guard must fail before mutations: %v", statErr)
						return
					}
					require.NoError(t, runErr, "%s", output)
					mutations, readErr := os.ReadFile(log) // #nosec G304 -- log path is inside t.TempDir
					require.NoError(t, readErr)
					require.Contains(t, string(mutations), "namespace: "+namespace)
					if !tt.defaultNS {
						require.NotContains(t, string(mutations), "namespace: kube-system")
					}
					if script == "install" {
						applies := 9
						if tt.defaultNS {
							applies = 8
						}
						require.Equal(t, applies, strings.Count(string(mutations), "apply -f -\n"))
					} else {
						require.Equal(t, 8, strings.Count(string(mutations), "delete -f - --ignore-not-found\n"))
					}
				})
			}
		})
	}
}

const (
	// Reject unexpected reads so tests cannot silently miss a discovery path.
	fakeKubectl = `#!/usr/bin/env bash
set -euo pipefail

if [[ "$1" == get ]]; then
  case "$2" in
    csidrivers.storage.k8s.io)
      [[ "$3" == azurelustre.csi.azure.com && "$4" == --ignore-not-found ]]
      if [[ "${TEST_DISCOVERY_ERROR}" == driver ]]; then
        echo "discovery failed" >&2
        exit 1
      fi
      if [[ -n "${TEST_DRIVER}" ]]; then
        echo csidriver.storage.k8s.io/azurelustre.csi.azure.com
      fi
      ;;
    deployments.apps,daemonsets.apps,pods)
      [[ "$3" == --all-namespaces && "$4" == -l ]]
      case "$5" in
        app.kubernetes.io/name=azurelustre-csi-driver)
          key=name
          namespaces="${TEST_NAME_NS}"
          ;;
        app.kubernetes.io/part-of=azurelustre-csi-driver)
          key=part-of
          namespaces="${TEST_PART_OF_NS}"
          ;;
        "app in (csi-azurelustre-controller,csi-azurelustre-node)")
          key=legacy
          namespaces="${TEST_LEGACY_NS}"
          ;;
        *) echo "unexpected selector: $5" >&2; exit 1 ;;
      esac
      if [[ "${TEST_DISCOVERY_ERROR}" == "${key}" ]]; then
        echo "discovery failed" >&2
        exit 1
      fi
      printf '%s\n' "${namespaces}"
      ;;
    configmap) exit 1 ;;
    daemonsets.apps|persistentvolumes) ;;
    *) echo "unexpected read: $*" >&2; exit 1 ;;
  esac
  exit 0
fi

printf '%s\n' "$*" >> "${TEST_MUTATIONS}"
case "$1" in
  create) printf 'kind: Namespace\nmetadata:\n  name: azurelustre-system\n' ;;
  apply|delete)
    if [[ "$2" == -f && "$3" == - ]]; then
      cat >> "${TEST_MUTATIONS}"
    fi
    ;;
  rollout) ;;
  *) echo "unexpected mutation: $*" >&2; exit 1 ;;
esac
`
)
