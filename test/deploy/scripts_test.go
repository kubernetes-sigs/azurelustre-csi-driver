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
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestInstallerWaitsForNodeRollout(t *testing.T) {
	tests := []struct {
		name   string
		status string
		polls  int
	}{
		{name: "ready old pods", status: "2 2 2 2 0 2 RollingUpdate", polls: 2},
		{name: "updated pods not yet available", status: "2 2 2 2 2 1 RollingUpdate", polls: 2},
		{name: "unobserved generation", status: "1 2 2 2 2 2 RollingUpdate", polls: 2},
		{name: "converged rolling update", status: "2 2 2 2 2 2 RollingUpdate", polls: 1},
		{name: "explicit OnDelete staging", status: "2 2 2 2 0 2 OnDelete", polls: 1},
		{name: "empty flavor", status: "2 2 0 0 0 0 RollingUpdate", polls: 1},
	}

	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			polls := filepath.Join(dir, "polls")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeKubectl), 0o700))                   // #nosec G306 -- executable test stub in t.TempDir
			require.NoError(t, os.WriteFile(filepath.Join(dir, "sleep"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o700)) // #nosec G306 -- avoids real polling delays
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("NAMESPACE", "azurelustre-system")
			for _, name := range []string{"TEST_DRIVER", "TEST_NAME_NS", "TEST_PART_OF_NS", "TEST_LEGACY_NS", "TEST_DISCOVERY_ERROR"} {
				t.Setenv(name, "")
			}
			t.Setenv("TEST_MUTATIONS", filepath.Join(dir, "mutations"))
			t.Setenv("TEST_NODE_POLLS", polls)
			t.Setenv("TEST_NODE_STATUS", tt.status)

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "deploy", "install-driver.sh"), "local") // #nosec G204 -- only runs the repository's deployment script
			cmd.Dir = root
			output, runErr := cmd.CombinedOutput()
			require.NoError(t, runErr, "%s", output)
			reads, readErr := os.ReadFile(polls) // #nosec G304 -- log path is inside t.TempDir
			require.NoError(t, readErr)
			require.Equal(t, tt.polls, strings.Count(string(reads), "csi-azurelustre-node-jammy\n"))
			for _, flavor := range []string{"noble", "azurelinux3"} {
				require.Equal(t, 1, strings.Count(string(reads), "csi-azurelustre-node-"+flavor+"\n"))
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
    daemonset)
      [[ "$4" == -n && "$5" == "${NAMESPACE:-kube-system}" ]]
      status='1 1 0 0 0 0 RollingUpdate'
      if [[ -n "${TEST_NODE_POLLS:-}" ]]; then
        status='2 2 2 2 2 2 RollingUpdate'
        if [[ "$3" == csi-azurelustre-node-jammy ]] && ! grep -qx "$3" "${TEST_NODE_POLLS}" 2>/dev/null; then
          status="${TEST_NODE_STATUS}"
        fi
        printf '%s\n' "$3" >> "${TEST_NODE_POLLS}"
      fi
      # Match the requested fields so the regression also exercises the old waiter.
      if [[ "$*" != *'.status.updatedNumberScheduled'* ]]; then
        read -r observed generation desired ready _rest <<<"${status}"
        status="${observed} ${generation} ${desired} ${ready}"
      fi
      printf '%s\n' "${status}"
      ;;
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
