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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pborman/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

var lustreClientModules = []string{
	"lustre", "mgc", "mdc", "osc", "lov", "lmv", "fid", "fld",
	"obdclass", "ptlrpc", "ptlrpc_gss", "lnet", "libcfs",
	"ksocklnd", "ko2iblnd", "lnet_selftest",
}

// nodeTeardown is an opt-in experiment. Its directory must be a private volume
// shared only by the node driver and loader. Requests survive container restarts.
type nodeTeardown struct {
	mu          sync.RWMutex
	dir         string
	identity    string
	podsDir     string
	timeout     time.Duration
	interval    time.Duration
	modulesGone func() error
	unload      func(context.Context) error
}

func newNodeTeardown(dir string) (*nodeTeardown, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) == "/" {
		return nil, errors.New("node teardown requires an absolute private directory")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("node teardown path is not a directory")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	bootID, podUID := strings.TrimSpace(string(boot)), os.Getenv("POD_UID")
	if uuid.Parse(bootID) == nil || uuid.Parse(podUID) == nil {
		return nil, errors.New("node teardown requires valid boot and Pod identities")
	}
	g := &nodeTeardown{
		dir:      dir,
		identity: bootID + " " + podUID,
		podsDir:  "/var/lib/kubelet/pods",
		timeout:  60 * time.Second,
		interval: time.Second,
		modulesGone: func() error {
			return verifyLustreModulesGone("/sys/module", "/proc/modules")
		},
		unload: func(ctx context.Context) error {
			attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(attempt, "lustre_rmmod")
			cmd.WaitDelay = 2 * time.Second
			output, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("lustre_rmmod: %w: %s", err, output)
			}
			return nil
		},
	}
	if _, err := g.request(); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *nodeTeardown) read(name string) (string, error) {
	data, err := os.ReadFile(filepath.Clean(filepath.Join(g.dir, name)))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(strings.TrimSpace(string(data))) == 0 || len(data) > 1024 {
		return "", fmt.Errorf("invalid node teardown record %s", name)
	}
	return strings.TrimSpace(string(data)), nil
}

func (g *nodeTeardown) write(name, value string) error {
	file, err := os.CreateTemp(g.dir, "."+name+"-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			klog.ErrorS(err, "NodeTeardown: could not remove temporary record")
		}
	}()
	_, writeErr := file.WriteString(value + "\n")
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(g.dir, name))
}

func (g *nodeTeardown) request() (string, error) {
	request, err := g.read("request")
	if err != nil || request == "" {
		return request, err
	}
	fields := strings.Fields(request)
	if len(fields) != 3 || strings.Join(fields[:2], " ") != g.identity ||
		uuid.Parse(fields[2]) == nil {
		return "", errors.New("node teardown request has a stale or invalid identity")
	}
	return request, nil
}

func (g *nodeTeardown) admitPublish() error {
	request, err := g.request()
	if err != nil {
		return err
	}
	if request != "" {
		return errors.New("node teardown has started; new mounts are disabled")
	}
	return nil
}

func (d *Driver) finishNodeTeardown(ctx context.Context) error {
	g := d.teardown
	if g == nil {
		return nil
	}
	// Serialize the transition, not the wait. Other Pod unmounts must finish
	// without waiting for this node-wide operation.
	g.mu.Lock()
	request, err := d.prepareNodeTeardown(ctx)
	g.mu.Unlock()
	if err != nil || request == "" {
		return err
	}
	return g.waitForCompletion(ctx, request)
}

func (d *Driver) prepareNodeTeardown(ctx context.Context) (string, error) {
	g := d.teardown
	request, err := g.request()
	if err != nil || request != "" {
		return request, err
	}
	if d.kubeClient == nil {
		return "", errors.New("cannot establish node drain state without a Kubernetes client")
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	node, err := d.kubeClient.CoreV1().Nodes().Get(readCtx, d.NodeID, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read node drain state: %w", err)
	}
	if !node.Spec.Unschedulable {
		return "", nil
	}
	mounts, err := d.mounter.List()
	if err != nil {
		return "", fmt.Errorf("list remaining Lustre mounts: %w", err)
	}
	for _, mount := range mounts {
		if mount.Type == "lustre" && g.isPodMount(mount.Path) {
			return "", nil
		}
	}
	// Foreign or hidden mounts cannot authorize retirement: ordinary rmmod
	// must still succeed, and both processes check the actual kernel modules.
	request = g.identity + " " + uuid.New()
	if err := g.write("request", request); err != nil {
		return "", err
	}
	klog.InfoS("NodeTeardown: requested", "node", d.NodeID, "request", request)
	return request, nil
}

func (g *nodeTeardown) isPodMount(path string) bool {
	relative, err := filepath.Rel(g.podsDir, path)
	if err != nil {
		return false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	return len(parts) == 5 && uuid.Parse(parts[0]) != nil &&
		parts[1] == "volumes" && parts[2] == "kubernetes.io~csi" &&
		parts[3] != "" && parts[4] == "mount"
}

func (g *nodeTeardown) waitForCompletion(ctx context.Context, request string) error {
	klog.InfoS("NodeTeardown: awaiting completion", "request", request)
	err := wait.PollUntilContextTimeout(ctx, g.interval, g.timeout, true, func(context.Context) (bool, error) {
		current, err := g.request()
		if err != nil {
			return false, err
		}
		if current != request {
			return false, errors.New("node teardown request changed while waiting")
		}
		failed, err := g.read("failed")
		if err != nil {
			return false, err
		}
		if failed == request {
			return false, errors.New("loader could not complete node teardown")
		}
		completed, err := g.read("completed")
		if err != nil || completed != request {
			return false, err
		}
		if err := g.modulesGone(); err != nil {
			return false, fmt.Errorf("rejecting loader acknowledgement: %w", err)
		}
		return true, nil
	})
	if err == nil {
		klog.InfoS("NodeTeardown: verified completion", "request", request)
	} else {
		klog.InfoS("NodeTeardown: completion still pending", "request", request, "error", err)
	}
	return err
}

// ExperimentalNodeTeardownHealth recognizes a deliberately draining loader.
// It does not assert that teardown has completed.
func ExperimentalNodeTeardownHealth(dir string) error {
	g, err := newNodeTeardown(dir)
	if err != nil {
		return err
	}
	request, err := g.request()
	if err != nil {
		return err
	}
	if request == "" {
		return errors.New("no node teardown request")
	}
	return nil
}

// RunExperimentalNodeTeardown runs only in the loader container, before it
// enters a drained idle state. It never loads modules or forces their removal.
func RunExperimentalNodeTeardown(dir string) error {
	g, err := newNodeTeardown(dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return g.runWorker(ctx)
}

func (g *nodeTeardown) runWorker(ctx context.Context) error {
	request, err := g.request()
	if err != nil {
		return err
	}
	if request == "" {
		return errors.New("refusing module teardown without a request")
	}
	if err := g.completeRequest(ctx, request); err != nil {
		if recordErr := g.write("failed", request); recordErr != nil {
			klog.ErrorS(recordErr, "NodeTeardown: could not record failure")
		}
		return err
	}
	return nil
}

func (g *nodeTeardown) completeRequest(ctx context.Context, request string) error {
	failed, err := g.read("failed")
	if err != nil {
		return err
	}
	if failed == request {
		return errors.New("node teardown previously failed; explicit intervention required")
	}
	completed, err := g.read("completed")
	if err != nil {
		return err
	}
	if completed == request {
		return g.modulesGone()
	}
	if err := g.write("quiesced", request); err != nil {
		return err
	}
	klog.InfoS("NodeTeardown: loader quiesced", "request", request)
	hold, err := g.read("test-hold")
	if err != nil {
		return err
	}
	if hold != "" {
		if hold != g.identity {
			return errors.New("test hold belongs to a different node boot or Pod")
		}
		klog.InfoS("NodeTeardown: awaiting test release", "request", request)
		if err := wait.PollUntilContextCancel(ctx, g.interval, true, func(context.Context) (bool, error) {
			release, err := g.read("test-release")
			return release == request, err
		}); err != nil {
			return err
		}
	}
	var lastErr error
	err = wait.PollUntilContextTimeout(ctx, g.interval, g.timeout, true, func(attempt context.Context) (bool, error) {
		current, err := g.request()
		if err != nil {
			return false, err
		}
		if current != request {
			return false, errors.New("node teardown request changed during cleanup")
		}
		if lastErr = g.unload(attempt); lastErr != nil {
			if errors.Is(lastErr, exec.ErrNotFound) {
				return false, lastErr
			}
			klog.InfoS("NodeTeardown: unload not complete", "error", lastErr)
			return false, nil
		}
		lastErr = g.modulesGone()
		return lastErr == nil, nil
	})
	if err != nil {
		return errors.Join(err, lastErr)
	}
	if err := g.write("completed", request); err != nil {
		return err
	}
	klog.InfoS("NodeTeardown: loader completed", "request", request)
	return nil
}

func verifyLustreModulesGone(moduleDir, procModules string) error {
	entries, err := os.ReadDir(moduleDir)
	if err != nil {
		return err
	}
	loaded := make(map[string]bool)
	for _, entry := range entries {
		loaded[entry.Name()] = true
	}
	data, err := os.ReadFile(filepath.Clean(procModules))
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			loaded[fields[0]] = true
		}
	}
	for _, module := range lustreClientModules {
		if loaded[module] {
			return fmt.Errorf("lustre module %s remains loaded", module)
		}
	}
	return nil
}
