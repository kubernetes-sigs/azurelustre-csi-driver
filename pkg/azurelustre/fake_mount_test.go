/*
Copyright 2020 The Kubernetes Authors.

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
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mount "k8s.io/mount-utils"
)

type fakeMounter struct {
	mount.FakeMounter
	notMountPoint   *bool
	mountCheckError error
	mountError      error
	unmountError    error
}

// Mount overrides mount.FakeMounter.Mount.
func (f *fakeMounter) Mount(source, target, fstype string, options []string) error {
	if f.mountError != nil {
		return f.mountError
	}
	return f.FakeMounter.Mount(source, target, fstype, options)
}

// MountSensitive overrides mount.FakeMounter.MountSensitive.
func (f *fakeMounter) MountSensitive(source, target, fstype string, options, sensitiveOptions []string) error {
	if f.mountError != nil {
		return f.mountError
	}
	return f.FakeMounter.MountSensitive(source, target, fstype, options, sensitiveOptions)
}

// MountSensitiveWithoutSystemdWithMountFlags overrides mount.FakeMounter.MountSensitiveWithoutSystemdWithMountFlags.
func (f *fakeMounter) MountSensitiveWithoutSystemdWithMountFlags(source, target, fstype string, options, sensitiveOptions, mountFlags []string) error {
	if f.mountError != nil {
		return f.mountError
	}
	return f.FakeMounter.MountSensitiveWithoutSystemdWithMountFlags(source, target, fstype, options, sensitiveOptions, mountFlags)
}

func (f *fakeMounter) IsLikelyNotMountPoint(file string) (bool, error) {
	if f.mountCheckError != nil {
		return true, f.mountCheckError
	}
	if f.notMountPoint != nil {
		return *f.notMountPoint, nil
	}
	return f.FakeMounter.IsLikelyNotMountPoint(file)
}

func (f *fakeMounter) Unmount(target string) error {
	if f.unmountError != nil {
		return f.unmountError
	}
	return f.FakeMounter.Unmount(target)
}

func (f *fakeMounter) UnmountWithForce(target string, _ time.Duration) error {
	return f.Unmount(target)
}

func preserveFakeUnmountedData(t *testing.T, mounter *fakeMounter) string {
	t.Helper()
	// Each fake unmount moves its visible tree to snapshot 0, 1, ...; no remote filesystem is involved.
	backingDirectory := t.TempDir()
	unmountCount := 0
	mounter.UnmountFunc = func(target string) error {
		// Hide the mounted filesystem's contents without deleting its data.
		backingPath := filepath.Join(backingDirectory, strconv.Itoa(unmountCount))
		if err := os.Rename(target, backingPath); err != nil {
			return err
		}
		unmountCount++
		return os.Mkdir(target, 0o700)
	}
	return backingDirectory
}

func newNodeMountTestDriver(t *testing.T) (*Driver, *fakeMounter, string) {
	t.Helper()
	driver := NewFakeDriver(t)
	driver.workingMountDir = t.TempDir()
	mounter := &fakeMounter{}
	backingDirectory := preserveFakeUnmountedData(t, mounter)
	driver.mounter = &mount.SafeFormatAndMount{Interface: mounter}
	forceMounter := mount.MounterForceUnmounter(mounter)
	driver.forceMounter = &forceMounter
	return driver, mounter, backingDirectory
}

func TestFakeMounterMountEntryPoints(t *testing.T) {
	const source = "127.0.0.1@tcp:/lustrefs"
	options := []string{"ro"}
	for _, operation := range []struct {
		name string
		run  func(*fakeMounter, string) error
	}{
		{name: "Mount", run: func(mounter *fakeMounter, target string) error {
			return mounter.Mount(source, target, "lustre", options)
		}},
		{name: "MountSensitive", run: func(mounter *fakeMounter, target string) error {
			return mounter.MountSensitive(source, target, "lustre", options, nil)
		}},
		{name: "MountSensitiveWithoutSystemdWithMountFlags", run: func(mounter *fakeMounter, target string) error {
			return mounter.MountSensitiveWithoutSystemdWithMountFlags(source, target, "lustre", options, nil, []string{"--no-mtab"})
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			for _, test := range []struct {
				name       string
				mountError error
			}{
				{name: "records a successful mount"},
				{name: "returns the injected error without recording a mount", mountError: errors.New("mount failed")},
			} {
				t.Run(test.name, func(t *testing.T) {
					target := t.TempDir()
					mounter := &fakeMounter{mountError: test.mountError}

					err := operation.run(mounter, target)

					if test.mountError != nil {
						require.ErrorIs(t, err, test.mountError)
						assert.Empty(t, mounter.MountPoints)
						assert.Empty(t, mounter.GetLog())
					} else {
						require.NoError(t, err)
						assert.Equal(t, []mount.MountPoint{{Device: source, Path: target, Type: "lustre", Opts: options}}, mounter.MountPoints)
						assert.Equal(t, []mount.FakeAction{{Action: "mount", Target: target, Source: source, FSType: "lustre"}}, mounter.GetLog())
					}
				})
			}
		})
	}
}

func TestFakeMounterMountPointResults(t *testing.T) {
	for _, test := range []struct {
		name                  string
		recordedMount         bool
		notMountPoint         *bool
		mountCheckError       error
		expectedNotMountPoint bool
	}{
		{name: "unmounted directory", expectedNotMountPoint: true},
		{name: "recorded mount", recordedMount: true},
		{name: "explicit unmounted result", recordedMount: true, notMountPoint: new(true), expectedNotMountPoint: true},
		{name: "explicit mounted result", notMountPoint: new(false)},
		{name: "lookup failure overrides a mount result", notMountPoint: new(false), mountCheckError: errors.New("mount lookup failed"), expectedNotMountPoint: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := t.TempDir()
			mounter := &fakeMounter{notMountPoint: test.notMountPoint, mountCheckError: test.mountCheckError}
			if test.recordedMount {
				mounter.MountPoints = []mount.MountPoint{{Path: target}}
			}

			actual, err := mounter.IsLikelyNotMountPoint(target)

			if test.mountCheckError != nil {
				require.ErrorIs(t, err, test.mountCheckError)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, test.expectedNotMountPoint, actual)
		})
	}
}
