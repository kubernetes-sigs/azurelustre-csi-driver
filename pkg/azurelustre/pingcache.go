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
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/util"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
)

const (
	clusterPingCacheTTL = 20 * time.Second
	dialTimeout         = 5 * time.Second
	// A timeout can't actually bound the ping: lnetctl's LNet handshake is an uninterruptible
	// in-kernel wait (~lnet_lnd_timeout, ~50s) that no timeout shortens. This only catches a truly
	// stuck process.
	lnetctlPingTimeout = 60 * time.Second

	defaultAcceptorPort     = "988"
	defaultAcceptorPortPath = "/sys/module/lnet/parameters/accept_port"
)

// clusterPingChecker reports whether a Lustre MGS is reachable from this node.
type clusterPingChecker interface {
	EnsureReachable(mgsIPAddress string) error
}

// clusterPingCache wraps azcache.TimedCache to give callers a typed EnsureReachable instead of the
// untyped getter, cache-read-type argument, and context plumbing.
type clusterPingCache struct {
	cache         azcache.Resource
	commandRunner util.CommandRunnerInterface
	// dial and acceptorPortPath are fields only so tests can substitute a fake dialer and a sysfs
	// fixture without real network or node access.
	dial             func(ctx context.Context, network, address string) (net.Conn, error)
	acceptorPortPath string
}

func newClusterPingCache(commandRunner util.CommandRunnerInterface) (*clusterPingCache, error) {
	c := &clusterPingCache{
		commandRunner:    commandRunner,
		dial:             (&net.Dialer{Timeout: dialTimeout}).DialContext,
		acceptorPortPath: defaultAcceptorPortPath,
	}

	cache, err := azcache.NewTimedCache(clusterPingCacheTTL, c.probe, false)
	if err != nil {
		return nil, err
	}
	c.cache = cache

	return c, nil
}

// LNet uses one acceptor port fabric-wide, so this node's own configured port is also the port the
// MGS listens on.
func (c *clusterPingCache) acceptorPort() string {
	if b, err := os.ReadFile(c.acceptorPortPath); err == nil {
		if p := strings.TrimSpace(string(b)); p != "" {
			return p
		}
	}
	return defaultAcceptorPort
}

// probe returns false (not an error) when a stage fails, so a transient failure is cached as
// "unreachable" instead of surfacing to callers as an internal cache error.
func (c *clusterPingCache) probe(ctx context.Context, mgsIPAddress string) (any, error) {
	dialAddr := net.JoinHostPort(mgsIPAddress, c.acceptorPort())
	conn, err := c.dial(ctx, "tcp", dialAddr)
	if err != nil {
		klog.Warningf("LNet reachability gate: dial to %s failed: %v", dialAddr, err)
		return false, nil
	}
	// The dial succeeding is what proves reachability; a Close error is incidental, so only log it.
	if cerr := conn.Close(); cerr != nil {
		klog.V(4).Infof("closing reachability dial to %s: %v", dialAddr, cerr)
	}

	pingAddr := mgsIPAddress + "@tcp"
	if output, err := c.commandRunner.RunWithTimeout(ctx, lnetctlPingTimeout, "lnetctl", "ping", pingAddr); err != nil {
		klog.Warningf("lnetctl ping to %s failed with error: %v, output: %s", pingAddr, err, output)
		return false, nil
	}
	return true, nil
}

// EnsureReachable uses a background context, not the caller's: the cached result is shared by every
// volume on this MGS, so one request's cancellation must not poison the shared probe.
func (c *clusterPingCache) EnsureReachable(mgsIPAddress string) error {
	cacheData, err := c.cache.Get(context.Background(), mgsIPAddress, azcache.CacheReadTypeDefault)
	if err != nil {
		return status.Errorf(codes.Internal, "error getting pingCache value for MGS IP %q: %v", mgsIPAddress, err)
	}

	pinged, ok := cacheData.(bool)
	if !ok {
		return status.Error(codes.Internal, "type assertion failed for pingCache value")
	}
	if !pinged {
		klog.Warningf("MGS IP address %s is not reachable (cached result)", mgsIPAddress)
		return status.Errorf(codes.FailedPrecondition,
			"MGS IP address %q is not reachable; verify the cluster IP address and node network configuration are correct (if the cluster was only briefly unreachable, the operation will recover on retry)", mgsIPAddress)
	}

	return nil
}
