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
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
)

// fakeConn is a net.Conn stub for tests; probe only calls Close on the connection it dials, so
// closeErr lets a test simulate a close failure.
type fakeConn struct {
	net.Conn
	closeErr error
}

func (c fakeConn) Close() error { return c.closeErr }

func TestClusterPingCacheProbe(t *testing.T) {
	dialOpenPort := func(context.Context, string, string) (net.Conn, error) { return fakeConn{}, nil }
	dialCloseErr := func(context.Context, string, string) (net.Conn, error) {
		return fakeConn{closeErr: errors.New("close failed")}, nil
	}
	dialUnreachable := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}

	tests := []struct {
		desc                  string
		dial                  func(context.Context, string, string) (net.Conn, error)
		pingFails             bool
		expectedReachable     bool
		expectedPingAttempted bool
	}{
		{
			desc:                  "port open and lnetctl ping succeeds",
			dial:                  dialOpenPort,
			pingFails:             false,
			expectedReachable:     true,
			expectedPingAttempted: true,
		},
		{
			desc:                  "port open but conn close errors, ping still succeeds",
			dial:                  dialCloseErr,
			pingFails:             false,
			expectedReachable:     true,
			expectedPingAttempted: true,
		},
		{
			desc:                  "port open but lnetctl ping fails",
			dial:                  dialOpenPort,
			pingFails:             true,
			expectedReachable:     false,
			expectedPingAttempted: true,
		},
		{
			desc:                  "port unreachable so lnetctl ping is skipped",
			dial:                  dialUnreachable,
			pingFails:             false,
			expectedReachable:     false,
			expectedPingAttempted: false,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			fakeRunner := NewFakeCommandRunner(test.pingFails)
			c := &clusterPingCache{commandRunner: fakeRunner, dial: test.dial}

			result, err := c.probe(t.Context(), "1.1.1.1")
			require.NoError(t, err)

			reachable, ok := result.(bool)
			require.True(t, ok, "probe result should be a bool")
			assert.Equal(t, test.expectedReachable, reachable)

			if test.expectedPingAttempted {
				assert.Equal(t, []string{"lnetctl ping 1.1.1.1@tcp"}, fakeRunner.CalledCommands, "probe must invoke lnetctl with the <ip>@tcp NID once the port is open")
			} else {
				assert.Empty(t, fakeRunner.CalledCommands, "probe must not run lnetctl when the port is unreachable")
			}
		})
	}
}

func TestClusterPingCacheUsesTheNewProbeResultAtTTLExpiry(t *testing.T) {
	for _, testCase := range []struct {
		name                            string
		initialReachable, nextReachable bool
		initialCode, nextCode           codes.Code
	}{
		{name: "cached success becomes unreachable", initialReachable: true, nextReachable: false, initialCode: codes.OK, nextCode: codes.FailedPrecondition},
		{name: "cached failure becomes reachable", initialReachable: false, nextReachable: true, initialCode: codes.FailedPrecondition, nextCode: codes.OK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				probes := 0
				reachable := testCase.initialReachable
				cache, err := azcache.NewTimedCache(clusterPingCacheTTL, func(context.Context, string) (any, error) {
					probes++
					return reachable, nil
				}, false)
				require.NoError(t, err)
				checker := &clusterPingCache{cache: cache}
				assert.Equal(t, testCase.initialCode, status.Code(checker.EnsureReachable("10.0.0.4")))
				reachable = testCase.nextReachable
				time.Sleep(clusterPingCacheTTL - time.Nanosecond)
				assert.Equal(t, testCase.initialCode, status.Code(checker.EnsureReachable("10.0.0.4")), "the cached result must remain in use before expiry")
				assert.Equal(t, 1, probes)
				time.Sleep(time.Nanosecond)
				assert.Equal(t, testCase.nextCode, status.Code(checker.EnsureReachable("10.0.0.4")), "the result at expiry must come from the new probe, not the old cached value")
				assert.Equal(t, 2, probes)
			})
		})
	}
}

func TestClusterPingCacheDoesNotSerializeDifferentMGS(t *testing.T) {
	var probes atomic.Int32
	release := make(chan struct{})
	releaseProbes := sync.OnceFunc(func() { close(release) })
	entered := make(chan string, 2)
	cache, err := azcache.NewTimedCache(time.Hour, func(_ context.Context, address string) (any, error) {
		probes.Add(1)
		entered <- address
		<-release
		return true, nil
	}, false)
	require.NoError(t, err)
	checker := &clusterPingCache{cache: cache}
	results := make(chan error, 2)
	t.Cleanup(func() {
		releaseProbes()
		for range 2 {
			select {
			case result := <-results:
				assert.NoError(t, result)
			case <-time.After(30 * time.Second):
				t.Error("probe caller did not finish after its release channel was closed")
				return
			}
		}
	})
	go func() { results <- checker.EnsureReachable("10.0.0.4") }()
	go func() { results <- checker.EnsureReachable("10.0.0.5") }()
	var started []string
	for range 2 {
		select {
		case address := <-entered:
			started = append(started, address)
		case <-time.After(30 * time.Second):
			t.Fatalf("both different-MGS probes must start while neither is released; started: %v", started)
		}
	}
	assert.ElementsMatch(t, []string{"10.0.0.4", "10.0.0.5"}, started)
	assert.Equal(t, int32(2), probes.Load(), "an unrelated MGS must not wait behind a slow probe")
	releaseProbes()
}

func TestClusterPingCacheAcceptorPort(t *testing.T) {
	missing := &clusterPingCache{acceptorPortPath: filepath.Join(t.TempDir(), "missing")}
	assert.Equal(t, defaultAcceptorPort, missing.acceptorPort(), "should fall back to the default when the sysfs file is missing")

	portFile := filepath.Join(t.TempDir(), "accept_port")
	require.NoError(t, os.WriteFile(portFile, []byte("1988\n"), 0o600))
	configured := &clusterPingCache{acceptorPortPath: portFile}
	assert.Equal(t, "1988", configured.acceptorPort(), "should read and trim the node's configured acceptor port")
}

func TestClusterPingCacheEnsureReachable(t *testing.T) {
	tests := []struct {
		desc         string
		getter       func(context.Context, string) (any, error)
		expectedCode codes.Code
		expectedMsg  string
	}{
		{
			desc: "cache contains reachable ping result",
			getter: func(context.Context, string) (any, error) {
				return true, nil
			},
			expectedCode: codes.OK,
		},
		{
			desc: "cache contains unreachable ping result",
			getter: func(context.Context, string) (any, error) {
				return false, nil
			},
			expectedCode: codes.FailedPrecondition,
			expectedMsg:  "is not reachable",
		},
		{
			desc: "cache value type assertion fails",
			getter: func(context.Context, string) (any, error) {
				return "not-a-bool", nil
			},
			expectedCode: codes.Internal,
			expectedMsg:  "type assertion failed for pingCache value",
		},
		{
			desc: "cache getter returns error",
			getter: func(context.Context, string) (any, error) {
				return nil, errors.New("cache getter failure")
			},
			expectedCode: codes.Internal,
			expectedMsg:  "error getting pingCache value",
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			cache, err := azcache.NewTimedCache(1*time.Second, test.getter, false)
			require.NoError(t, err)
			c := &clusterPingCache{cache: cache}

			err = c.EnsureReachable("1.1.1.1")
			if test.expectedCode == codes.OK {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Equal(t, test.expectedCode, status.Code(err))
			assert.Contains(t, err.Error(), test.expectedMsg)
		})
	}
}
