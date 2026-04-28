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

package azurelustre

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLustreVersion(t *testing.T) {
	tests := []struct {
		name          string
		version       string
		expectMajor   int
		expectMinor   int
		expectPatch   int
		expectErr     bool
	}{
		{
			name:        "simple version",
			version:     "2.15.8",
			expectMajor: 2, expectMinor: 15, expectPatch: 8,
		},
		{
			name:        "version with SHA suffix",
			version:     "2.15.8_34_gc0f2040",
			expectMajor: 2, expectMinor: 15, expectPatch: 8,
		},
		{
			name:        "version with hyphen suffix",
			version:     "2.15.8-ddn1",
			expectMajor: 2, expectMinor: 15, expectPatch: 8,
		},
		{
			name:        "older version",
			version:     "2.15.7",
			expectMajor: 2, expectMinor: 15, expectPatch: 7,
		},
		{
			name:        "newer version",
			version:     "2.16.1",
			expectMajor: 2, expectMinor: 16, expectPatch: 1,
		},
		{
			name:        "2.17 version",
			version:     "2.17.0_42_gabcdef1",
			expectMajor: 2, expectMinor: 17, expectPatch: 0,
		},
		{
			name:      "too few components",
			version:   "2.15",
			expectErr: true,
		},
		{
			name:      "empty string",
			version:   "",
			expectErr: true,
		},
		{
			name:      "garbage",
			version:   "not-a-version",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, minor, patch, err := parseLustreVersion(tt.version)
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectMajor, major)
				assert.Equal(t, tt.expectMinor, minor)
				assert.Equal(t, tt.expectPatch, patch)
			}
		})
	}
}

func TestCompareLustreVersion(t *testing.T) {
	tests := []struct {
		name   string
		a      [3]int
		b      [3]int
		expect int
	}{
		{"equal", [3]int{2, 15, 8}, [3]int{2, 15, 8}, 0},
		{"major less", [3]int{1, 15, 8}, [3]int{2, 15, 8}, -1},
		{"major greater", [3]int{3, 15, 8}, [3]int{2, 15, 8}, 1},
		{"minor less", [3]int{2, 14, 8}, [3]int{2, 15, 8}, -1},
		{"minor greater", [3]int{2, 16, 0}, [3]int{2, 15, 8}, 1},
		{"patch less", [3]int{2, 15, 7}, [3]int{2, 15, 8}, -1},
		{"patch greater", [3]int{2, 15, 9}, [3]int{2, 15, 8}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := compareLustreVersion(
				tt.a[0], tt.a[1], tt.a[2],
				tt.b[0], tt.b[1], tt.b[2],
			)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestDetectUniqueFsidSupport(t *testing.T) {
	// This test validates the logic chain from version string to support bool.
	// It doesn't read sysfs — that's an integration concern.
	tests := []struct {
		name    string
		version string
		expect  bool
	}{
		{"exactly 2.15.8", "2.15.8", true},
		{"2.15.8 with SHA", "2.15.8_34_gc0f2040", true},
		{"2.15.7 too old", "2.15.7", false},
		{"2.15.6 too old", "2.15.6", false},
		{"2.16.1 newer", "2.16.1", true},
		{"2.17.0 newer", "2.17.0", true},
		{"2.14.0 too old", "2.14.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, minor, patch, err := parseLustreVersion(tt.version)
			require.NoError(t, err)
			result := compareLustreVersion(major, minor, patch,
				uniqueFsidMinMajor, uniqueFsidMinMinor, uniqueFsidMinPatch) >= 0
			assert.Equal(t, tt.expect, result)
		})
	}
}
