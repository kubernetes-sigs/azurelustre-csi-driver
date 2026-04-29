/*
Copyright 2025 The Kubernetes Authors.

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
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"k8s.io/klog/v2"
)

const (
	// lustreSysVersionPath is the sysfs path for the loaded Lustre module version.
	lustreSysVersionPath = "/sys/module/lustre/version"

	// uniqueFsidMinMajor, uniqueFsidMinMinor, uniqueFsidMinPatch define the
	// minimum Lustre client version that supports the unique_fsid mount option.
	uniqueFsidMinMajor = 2
	uniqueFsidMinMinor = 15
	uniqueFsidMinPatch = 8
)

// lustreCapabilities caches detected Lustre module capabilities so that
// sysfs is read at most once per driver lifetime.
//
// Note: The cached result is permanent for the lifetime of the CSI DaemonSet
// pod. If the Lustre kernel module is upgraded on a node (e.g., from 2.15.7
// to 2.15.8), a DaemonSet pod restart is required for the driver to detect
// the new capabilities.
type lustreCapabilities struct {
	once               sync.Once
	supportsUniqueFsid bool
	version            string
}

// SupportsUniqueFsid returns true if the loaded Lustre module supports
// the unique_fsid mount option (>= 2.15.8). Detection is lazy — the
// first call reads /sys/module/lustre/version, and the result is cached.
// If the version cannot be determined, returns false (conservative).
func (c *lustreCapabilities) SupportsUniqueFsid() bool {
	c.once.Do(func() {
		c.version, c.supportsUniqueFsid = detectUniqueFsidSupport()
		if c.supportsUniqueFsid {
			klog.V(2).Infof("Lustre module version %s supports unique_fsid", c.version)
		} else {
			klog.V(2).Infof("Lustre module version %q does not support unique_fsid (requires >= %d.%d.%d)",
				c.version, uniqueFsidMinMajor, uniqueFsidMinMinor, uniqueFsidMinPatch)
		}
	})
	return c.supportsUniqueFsid
}

// detectUniqueFsidSupport reads the Lustre module version from sysfs and
// determines whether it meets the minimum version for unique_fsid support.
func detectUniqueFsidSupport() (string, bool) {
	version, err := readLustreVersion()
	if err != nil {
		klog.V(4).Infof("could not read Lustre version: %v", err)
		return "", false
	}

	major, minor, patch, err := parseLustreVersion(version)
	if err != nil {
		klog.V(4).Infof("could not parse Lustre version %q: %v", version, err)
		return version, false
	}

	supported := compareLustreVersion(major, minor, patch,
		uniqueFsidMinMajor, uniqueFsidMinMinor, uniqueFsidMinPatch) >= 0
	return version, supported
}

// readLustreVersion reads the Lustre module version from sysfs.
func readLustreVersion() (string, error) {
	data, err := os.ReadFile(lustreSysVersionPath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", lustreSysVersionPath, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// parseLustreVersion extracts major.minor.patch from a Lustre version string.
// Handles formats like "2.15.8", "2.15.8_34_gc0f2040", "2.15.8-ddn1", etc.
func parseLustreVersion(version string) (int, int, int, error) {
	// Strip everything after the first non-numeric/non-dot character
	// following the initial version prefix.
	cleaned := version
	for i, c := range version {
		if i > 0 && c != '.' && (c < '0' || c > '9') {
			cleaned = version[:i]
			break
		}
	}

	parts := strings.SplitN(cleaned, ".", 4)
	if len(parts) < 3 {
		return 0, 0, 0, fmt.Errorf("expected at least 3 version components in %q, got %d", version, len(parts))
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parsing major version %q: %w", parts[0], err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parsing minor version %q: %w", parts[1], err)
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parsing patch version %q: %w", parts[2], err)
	}

	return major, minor, patch, nil
}

// compareLustreVersion compares two version tuples. Returns:
//
//	-1 if a < b
//	 0 if a == b
//	 1 if a > b
func compareLustreVersion(aMajor, aMinor, aPatch, bMajor, bMinor, bPatch int) int {
	if aMajor != bMajor {
		if aMajor < bMajor {
			return -1
		}
		return 1
	}
	if aMinor != bMinor {
		if aMinor < bMinor {
			return -1
		}
		return 1
	}
	if aPatch != bPatch {
		if aPatch < bPatch {
			return -1
		}
		return 1
	}
	return 0
}
