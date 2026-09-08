/*
Copyright 2017 The Kubernetes Authors.

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
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestRunGRPCServerStartupErrors(t *testing.T) {
	blockedPath := t.TempDir()
	err := os.WriteFile(filepath.Join(blockedPath, "keep"), nil, 0o600)
	require.NoError(t, err)

	tests := []struct {
		name     string
		endpoint string
		message  string
	}{
		{name: "invalid endpoint", endpoint: "invalid", message: "invalid endpoint"},
		{name: "listener failure", endpoint: "tcp://127.0.0.1", message: "failed to listen"},
		{name: "socket removal failure", endpoint: "unix://" + blockedPath, message: "failed to remove"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := RunGRPCServer(t.Context(), test.endpoint, nil, nil, nil)
			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestRunGRPCServerServices(t *testing.T) {
	// A stale socket path must be removed before the server can bind it.
	socketPath := filepath.Join(t.TempDir(), "csi.sock")
	err := os.WriteFile(socketPath, nil, 0o600)
	require.NoError(t, err)

	driver := &Driver{CSIDriver: CSIDriver{Name: fakeDriverName, Version: "test", NodeID: "test-node"}}
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() {
		finished <- RunGRPCServer(ctx, "unix://"+socketPath, driver, driver, driver)
	}()
	t.Cleanup(func() {
		cancel()
		err := <-finished
		require.NoError(t, err)
	})

	connection := newLifecycleClient(t, "unix://"+socketPath)
	rpcCtx, rpcCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer rpcCancel()

	// The first RPC waits for startup; the others verify all three services are registered.
	info, err := csi.NewIdentityClient(connection).GetPluginInfo(rpcCtx, &csi.GetPluginInfoRequest{}, grpc.WaitForReady(true))
	require.NoError(t, err)
	assert.Equal(t, fakeDriverName, info.GetName())

	_, err = csi.NewControllerClient(connection).ControllerGetCapabilities(rpcCtx, &csi.ControllerGetCapabilitiesRequest{})
	require.NoError(t, err)

	node, err := csi.NewNodeClient(connection).NodeGetInfo(rpcCtx, &csi.NodeGetInfoRequest{})
	require.NoError(t, err)
	assert.Equal(t, "test-node", node.GetNodeId())
}

func TestRunGRPCServerServeError(t *testing.T) {
	listenerConfig := &net.ListenConfig{}
	listener, err := listenerConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	err = listener.Close()
	require.NoError(t, err)

	err = runGRPCServerOnListener(t.Context(), listener, nil, nil, nil)
	require.ErrorIs(t, err, net.ErrClosed)
	require.ErrorContains(t, err, "failed to serve")
}

func TestRunGRPCServerPreCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// Cancellation must win even over endpoint validation.
	err := RunGRPCServer(ctx, "invalid", nil, nil, nil)
	require.NoError(t, err)
}

func TestRunGRPCServerCancellationAfterServeStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener := &lifecycleListener{connections: make(chan net.Conn), closed: make(chan struct{})}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		finished := make(chan error, 1)
		go func() {
			finished <- runGRPCServerOnListener(ctx, listener, nil, nil, nil)
		}()

		// With no connections available, Serve must settle in the in-memory Accept.
		synctest.Wait()
		select {
		case err := <-finished:
			t.Fatalf("server exited before cancellation: %v", err)
		default:
		}

		cancel()
		err := <-finished
		require.NoError(t, err)
		select {
		case <-listener.closed:
		default:
			t.Fatal("shutdown did not close the listener")
		}
	})
}

func TestServeUntilShutdownAlreadyStopped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener := &lifecycleListener{connections: make(chan net.Conn), closed: make(chan struct{})}
		requests := &grpcRequestTracker{handlersFinished: make(chan struct{})}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		// Stop gRPC itself first: canceling the context alone would still race with Serve.
		server := grpc.NewServer()
		server.GracefulStop()

		err := serveUntilShutdown(ctx, cancel, server, listener, requests)
		require.NoError(t, err)
		select {
		case <-listener.closed:
		default:
			t.Fatal("Serve did not close the listener after finding the server stopped")
		}
	})
}

func TestDriverRunServerFailure(t *testing.T) {
	driver := &Driver{CSIDriver: CSIDriver{Name: fakeDriverName}, podRole: controllerPod}

	err := driver.Run(t.Context(), "invalid")
	require.ErrorContains(t, err, "invalid endpoint")
}

func TestDriverRunServerShutdown(t *testing.T) {
	driver := &Driver{CSIDriver: CSIDriver{Name: fakeDriverName}, podRole: controllerPod}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := driver.Run(ctx, "tcp://127.0.0.1:0")
	require.NoError(t, err)
}

type lifecycleIdentityServer struct {
	csi.UnimplementedIdentityServer
	probe func(context.Context) (*csi.ProbeResponse, error)
}

func (server *lifecycleIdentityServer) Probe(ctx context.Context, _ *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return server.probe(ctx)
}

func newLifecycleClient(t *testing.T, endpoint string) *grpc.ClientConn {
	t.Helper()
	connection, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() {
		err := connection.Close()
		require.NoError(t, err)
	})
	return connection
}

func TestRunGRPCServerCancelsActiveRPC(t *testing.T) {
	// Keep the handler active until server shutdown cancels its request context.
	entered := make(chan struct{})
	identity := &lifecycleIdentityServer{probe: func(ctx context.Context) (*csi.ProbeResponse, error) {
		close(entered)
		<-ctx.Done()
		return nil, convertHTTPResponseErrorToGrpcCodeError(ctx.Err())
	}}

	listener, err := listenGRPCEndpoint(t.Context(), "tcp://127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() {
		finished <- runGRPCServerOnListener(ctx, listener, identity, nil, nil)
	}()
	t.Cleanup(func() {
		cancel()
		err := <-finished
		require.NoError(t, err)
	})

	connection := newLifecycleClient(t, listener.Addr().String())
	rpcCtx, rpcCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer rpcCancel()
	result := make(chan error, 1)
	go func() {
		_, err := csi.NewIdentityClient(connection).Probe(rpcCtx, &csi.ProbeRequest{}, grpc.WaitForReady(true))
		result <- err
	}()

	// Cancellation after handler entry tests an active RPC, not startup or admission.
	select {
	case <-entered:
	case <-rpcCtx.Done():
		t.Fatal("RPC did not enter the handler")
	}

	cancel()
	err = <-result
	assert.Equal(t, codes.Canceled, status.Code(err))
}

func TestGRPCLifecycleCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shutdown, cancel := context.WithCancel(t.Context())
		defer cancel()
		requests := &grpcRequestTracker{handlersFinished: make(chan struct{})}
		wrapHandler := requests.trackAndCancelRequests(shutdown)
		canceled := make(chan struct{})
		release := make(chan struct{})
		finishCleanup := sync.OnceFunc(func() { close(release) })
		defer finishCleanup()
		result := make(chan error, 1)
		go func() {
			_, err := wrapHandler(t.Context(), nil, nil, func(ctx context.Context, _ any) (any, error) {
				<-ctx.Done()
				close(canceled)
				<-release
				return nil, ctx.Err()
			})
			result <- err
		}()
		synctest.Wait()

		// Cancellation reaches the handler, but its cleanup is still blocked.
		cancel()
		<-canceled
		requests.beginShutdown()
		select {
		case <-requests.handlersFinished:
			t.Fatal("drain completed before the handler returned")
		default:
		}

		// Draining must finish once the canceled handler completes its cleanup.
		finishCleanup()
		err := <-result
		require.ErrorIs(t, err, context.Canceled)
		<-requests.handlersFinished
	})
}

func TestGRPCLifecyclePreservesSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shutdown, cancel := context.WithCancel(t.Context())
		defer cancel()
		requests := &grpcRequestTracker{handlersFinished: make(chan struct{})}
		expected := &csi.ProbeResponse{}

		// A completed response must survive cancellation of the handler's context.
		response, err := requests.trackAndCancelRequests(shutdown)(t.Context(), nil, nil, func(ctx context.Context, _ any) (any, error) {
			cancel()
			<-ctx.Done()
			return expected, nil
		})
		require.NoError(t, err)
		assert.Same(t, expected, response)

		requests.beginShutdown()
		<-requests.handlersFinished
	})
}

func TestGRPCLifecycleRejectsNewRPC(t *testing.T) {
	tests := []struct {
		name   string
		cancel bool
	}{
		{name: "canceled context", cancel: true},
		{name: "draining server"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shutdown, cancel := context.WithCancel(t.Context())
			defer cancel()
			requests := &grpcRequestTracker{handlersFinished: make(chan struct{})}
			if test.cancel {
				cancel()
			} else {
				requests.beginShutdown()
			}

			called := false
			_, err := requests.trackAndCancelRequests(shutdown)(t.Context(), nil, nil, func(context.Context, any) (any, error) {
				called = true
				return nil, nil
			})
			assert.Equal(t, codes.Unavailable, status.Code(err))
			assert.False(t, called, "shutdown must reject new handlers")
		})
	}
}

func TestStopGRPCServerWaitsForCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// This handler finishes halfway through the deadline using the bubble's fake clock.
		requests := &grpcRequestTracker{handlersFinished: make(chan struct{})}
		result := make(chan error, 1)
		go func() {
			_, err := requests.trackAndCancelRequests(t.Context())(t.Context(), nil, nil, func(context.Context, any) (any, error) {
				time.Sleep(grpcShutdownTimeout / 2)
				return nil, nil
			})
			result <- err
		}()
		synctest.Wait()

		server := grpc.NewServer()
		t.Cleanup(server.Stop)
		start := time.Now()
		err := stopGRPCServer(server, requests)
		require.NoError(t, err)

		assert.Equal(t, grpcShutdownTimeout/2, time.Since(start))
		err = <-result
		require.NoError(t, err)
	})
}

func TestStopGRPCServerTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		requests := &grpcRequestTracker{handlersFinished: make(chan struct{})}
		release := make(chan struct{})
		finishCleanup := sync.OnceFunc(func() { close(release) })
		defer finishCleanup()
		handlerResult := make(chan error, 1)
		go func() {
			_, err := requests.trackAndCancelRequests(t.Context())(t.Context(), nil, nil, func(context.Context, any) (any, error) {
				<-release
				return nil, nil
			})
			handlerResult <- err
		}()
		synctest.Wait()

		server := grpc.NewServer()
		t.Cleanup(server.Stop)
		result := make(chan error, 1)
		start := time.Now()
		go func() {
			result <- stopGRPCServer(server, requests)
		}()
		synctest.Wait()

		// Cleanup is blocked: shutdown must wait for the full fake-time deadline.
		time.Sleep(grpcShutdownTimeout - time.Nanosecond)
		synctest.Wait()
		select {
		case <-result:
			t.Fatal("shutdown returned before the cleanup deadline")
		default:
		}
		err := <-result
		require.ErrorIs(t, err, errGRPCShutdownTimeout)
		assert.Equal(t, grpcShutdownTimeout, time.Since(start))

		finishCleanup()
		err = <-handlerResult
		require.NoError(t, err)
	})
}

type lifecycleListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func (listener *lifecycleListener) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.connections:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *lifecycleListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return nil
}

func (*lifecycleListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "in-memory", Net: "unix"}
}

func startInMemoryGRPCServer(ctx context.Context, t *testing.T, identity csi.IdentityServer) (*grpc.ClientConn, <-chan error) {
	t.Helper()
	// Both Accept and connection I/O must block inside the synctest bubble.
	serverConnection, clientConnection := net.Pipe()
	listener := &lifecycleListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
	listener.connections <- serverConnection

	ctx, cancel := context.WithCancel(ctx)
	finished := make(chan error, 1)
	go func() {
		finished <- runGRPCServerOnListener(ctx, listener, identity, nil, nil)
	}()

	connection, err := grpc.NewClient("passthrough:///in-memory",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return clientConnection, nil }),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		err := connection.Close()
		require.NoError(t, err)
	})
	return connection, finished
}

func TestRunGRPCServerForcedShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Keep the handler blocked past the shutdown deadline, even after cancellation.
		release := make(chan struct{})
		defer close(release)
		entered := make(chan struct{})
		identity := &lifecycleIdentityServer{probe: func(context.Context) (*csi.ProbeResponse, error) {
			close(entered)
			<-release
			return &csi.ProbeResponse{}, nil
		}}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		connection, finished := startInMemoryGRPCServer(ctx, t, identity)
		rpcCtx, rpcCancel := context.WithTimeout(t.Context(), 2*grpcShutdownTimeout)
		defer rpcCancel()
		result := make(chan error, 1)
		go func() {
			_, err := csi.NewIdentityClient(connection).Probe(rpcCtx, &csi.ProbeRequest{}, grpc.WaitForReady(true))
			result <- err
		}()
		<-entered

		// Shutdown must return on time without waiting for this handler to cooperate.
		start := time.Now()
		cancel()
		err := <-finished
		require.ErrorIs(t, err, errGRPCShutdownTimeout)
		assert.Equal(t, grpcShutdownTimeout, time.Since(start))

		err = <-result
		assert.Equal(t, codes.Unavailable, status.Code(err))
	})
}

func TestRunGRPCServerPreservesCompletedResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		// Trigger shutdown inside the handler, then return a successful response anyway.
		identity := &lifecycleIdentityServer{probe: func(ctx context.Context) (*csi.ProbeResponse, error) {
			cancel()
			<-ctx.Done()
			return &csi.ProbeResponse{}, nil
		}}

		connection, finished := startInMemoryGRPCServer(ctx, t, identity)
		rpcCtx, rpcCancel := context.WithTimeout(t.Context(), 2*grpcShutdownTimeout)
		defer rpcCancel()
		response, err := csi.NewIdentityClient(connection).Probe(rpcCtx, &csi.ProbeRequest{}, grpc.WaitForReady(true))
		require.NoError(t, err)
		assert.NotNil(t, response)

		err = <-finished
		require.NoError(t, err)
	})
}
