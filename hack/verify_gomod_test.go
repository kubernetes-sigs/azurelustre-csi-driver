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

package hack_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyGoMod(t *testing.T) {
	script, err := os.ReadFile("verify-gomod.sh")
	require.NoError(t, err)

	const fakeGo = `go() {
printf '%s\n' "$*" >> "${MOCK_COMMAND_LOG}"
case "$*" in
    'list -mod=readonly -m -f {{.Path}}{{if .Replace}} {{.Replace.Path}}{{end}} all')
        if [[ "${MOCK_FAILURE}" == list ]]; then
            echo "module resolution failed" >&2
            return 1
        fi
        printf '%s\n' "${MOCK_MODULE_PATHS}"
        ;;
    'mod tidy -diff')
        if [[ "${MOCK_FAILURE}" == tidy ]]; then
            echo "module metadata is not tidy" >&2
            return 1
        fi
        ;;
    'mod vendor')
        if [[ "${MOCK_FAILURE}" == vendor ]]; then
            echo "vendor generation failed" >&2
            return 1
        fi
        ;;
    *) echo "unexpected go command: $*" >&2; return 1 ;;
esac
}
`
	const fakeGit = `git() {
if [[ "$*" != 'diff vendor' ]]; then
    echo "unexpected diff command: $*" >&2
    return 1
fi
printf '%s' "${MOCK_VENDOR_DIFF}"
}
`
	tests := []struct {
		name        string
		modulePaths string
		failure     string
		vendorDiff  string
		expectedErr string
		expectedOps []string
	}{
		{
			name:        "published modules",
			modulePaths: "example.com/driver\nk8s.io/api\nk8s.io/client-go",
			expectedOps: []string{"mod tidy -diff", "mod vendor"},
		},
		{
			name:        "unrelated replacement and similar names remain allowed",
			modulePaths: "example.com/driver\nk8s.io/api ./local-api\nexample.com/k8s.io/kubernetes\nk8s.io/kubernetes-other",
			expectedOps: []string{"mod tidy -diff", "mod vendor"},
		},
		{
			name:        "direct monorepo dependency",
			modulePaths: "example.com/driver\nk8s.io/kubernetes",
			expectedErr: "k8s.io/kubernetes is in the dependency graph",
		},
		{
			name:        "transitive monorepo dependency",
			modulePaths: "example.com/driver\nexample.com/dependency\nk8s.io/kubernetes",
			expectedErr: "k8s.io/kubernetes is in the dependency graph",
		},
		{
			name:        "monorepo replaced with a fork",
			modulePaths: "example.com/driver\nk8s.io/kubernetes example.com/fork",
			expectedErr: "k8s.io/kubernetes is in the dependency graph",
		},
		{
			name:        "replacement target is the monorepo",
			modulePaths: "example.com/driver\nexample.com/alias k8s.io/kubernetes",
			expectedErr: "k8s.io/kubernetes is in the dependency graph",
		},
		{
			name:        "graph resolution failure stops verification",
			failure:     "list",
			expectedErr: "module resolution failed",
		},
		{
			name:        "tidy failure stops vendoring",
			modulePaths: "example.com/driver",
			failure:     "tidy",
			expectedErr: "Go module verification failed",
			expectedOps: []string{"mod tidy -diff"},
		},
		{
			name:        "vendor generation failure remains fatal",
			modulePaths: "example.com/driver",
			failure:     "vendor",
			expectedErr: "vendor generation failed",
			expectedOps: []string{"mod tidy -diff", "mod vendor"},
		},
		{
			name:        "vendor drift remains fatal",
			modulePaths: "example.com/driver",
			vendorDiff:  "diff --git a/vendor/example.go b/vendor/example.go",
			expectedErr: "go.mod/vendor is out of date",
			expectedOps: []string{"mod tidy -diff", "mod vendor"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			require.NoError(t, root.Mkdir("hack", 0o700))
			require.NoError(t, root.WriteFile("hack/verify-gomod.sh", script, 0o600))
			t.Setenv("MOCK_COMMAND_LOG", "commands")
			t.Setenv("MOCK_MODULE_PATHS", test.modulePaths)
			t.Setenv("MOCK_FAILURE", test.failure)
			t.Setenv("MOCK_VENDOR_DIFF", test.vendorDiff)
			command := exec.CommandContext(t.Context(), "bash", "-c", fakeGo+fakeGit+"source hack/verify-gomod.sh")
			command.Dir = root.Name()
			output, err := command.CombinedOutput()
			if test.expectedErr == "" {
				require.NoError(t, err, "%s", output)
				assert.Contains(t, string(output), "Done")
			} else {
				require.Error(t, err, "%s", output)
				assert.Contains(t, string(output), test.expectedErr)
				assert.NotContains(t, string(output), "Done")
			}
			commands, err := root.ReadFile("commands")
			require.NoError(t, err)
			actual := strings.Split(strings.TrimSpace(string(commands)), "\n")
			expected := append([]string{"list -mod=readonly -m -f {{.Path}}{{if .Replace}} {{.Replace.Path}}{{end}} all"}, test.expectedOps...)
			assert.Equal(t, expected, actual)
		})
	}
}
