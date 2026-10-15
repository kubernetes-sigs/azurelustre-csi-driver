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
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

const (
	grpcShutdownTimeout = 10 * time.Second
)

var errGRPCShutdownTimeout = errors.New("timed out shutting down gRPC server")

// grpcRequestTracker coordinates request admission with shutdown. It counts
// running CSI handlers, not connections or responses still being written.
type grpcRequestTracker struct {
	stateMutex         sync.Mutex
	activeHandlerCount int
	shuttingDown       bool
	handlersFinished   chan struct{}
}

// RunGRPCServer serves CSI requests until cancellation or a serving failure.
func RunGRPCServer(ctx context.Context, endpoint string, identityService csi.IdentityServer, controllerService csi.ControllerServer, nodeService csi.NodeServer) error {
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	listener, err := listenGRPCEndpoint(ctx, endpoint)
	if err != nil {
		return err
	}
	return runGRPCServerOnListener(ctx, listener, identityService, controllerService, nodeService)
}

func runGRPCServerOnListener(ctx context.Context, listener net.Listener, identityService csi.IdentityServer, controllerService csi.ControllerServer, nodeService csi.NodeServer) error {
	serverContext, cancelRequests := context.WithCancel(ctx)
	defer cancelRequests()
	requestTracker := &grpcRequestTracker{handlersFinished: make(chan struct{})}
	server := grpc.NewServer(
		grpc.MaxConcurrentStreams(200),
		grpc.ChainUnaryInterceptor(requestTracker.trackAndCancelRequests(serverContext), logGRPC),
	)
	if identityService != nil {
		csi.RegisterIdentityServer(server, identityService)
	}
	if controllerService != nil {
		csi.RegisterControllerServer(server, controllerService)
	}
	if nodeService != nil {
		csi.RegisterNodeServer(server, nodeService)
	}
	return serveUntilShutdown(serverContext, cancelRequests, server, listener, requestTracker)
}

func serveUntilShutdown(serverContext context.Context, cancelRequests context.CancelFunc, server *grpc.Server, listener net.Listener, requestTracker *grpcRequestTracker) error {
	serveStopped := make(chan struct{})
	var serveErr error
	go func() {
		defer close(serveStopped)
		klog.Infof("Listening for connections on address: %#v", listener.Addr())
		serveErr = server.Serve(listener)
	}()
	// A shutdown request and a listener failure both require active RPC cleanup.
	select {
	case <-serverContext.Done():
	case <-serveStopped:
	}
	cancelRequests()
	shutdownErr := stopGRPCServer(server, requestTracker)
	// Join Serve before reading its error; cancellation must not hide a failure.
	<-serveStopped
	if serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
		return errors.Join(fmt.Errorf("failed to serve on %s: %w", listener.Addr(), serveErr), shutdownErr)
	}
	return shutdownErr
}

func stopGRPCServer(server *grpc.Server, requestTracker *grpcRequestTracker) error {
	requestTracker.beginShutdown()
	shutdownWorkerFinished := make(chan struct{})
	stopWaitingForHandlers := make(chan struct{})
	// GracefulStop can hold gRPC's internal lock while waiting for handlers,
	// blocking a concurrent Stop. Wait for our handlers first so Stop stays usable.
	go func() {
		defer close(shutdownWorkerFinished)
		select {
		case <-requestTracker.handlersFinished:
			server.GracefulStop()
		case <-stopWaitingForHandlers:
		}
	}()
	cleanupTimer := time.NewTimer(grpcShutdownTimeout)
	defer cleanupTimer.Stop()
	select {
	case <-shutdownWorkerFinished:
		return nil
	case <-cleanupTimer.C:
		close(stopWaitingForHandlers)
		// Close connections, but do not wait for code that ignores cancellation.
		server.Stop()
		<-shutdownWorkerFinished
		return errGRPCShutdownTimeout
	}
}

// trackAndCancelRequests returns a gRPC interceptor: a wrapper around each CSI
// handler that rejects new requests during shutdown and cancels active ones.
func (requests *grpcRequestTracker) trackAndCancelRequests(serverContext context.Context) grpc.UnaryServerInterceptor {
	return func(requestContext context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Admission and counting share a lock so shutdown cannot miss a new handler.
		requests.stateMutex.Lock()
		if requests.shuttingDown || serverContext.Err() != nil {
			requests.stateMutex.Unlock()
			return nil, status.Error(codes.Unavailable, "gRPC server is shutting down")
		}
		requests.activeHandlerCount++
		requests.stateMutex.Unlock()
		defer requests.handlerFinished()

		// Cancel the handler without closing its connection, so it can still reply.
		requestContext, cancelRequest := context.WithCancel(requestContext)
		defer cancelRequest()
		detachShutdownCancellation := context.AfterFunc(serverContext, cancelRequest)
		defer detachShutdownCancellation()
		return handler(requestContext, request)
	}
}

func (requests *grpcRequestTracker) handlerFinished() {
	requests.stateMutex.Lock()
	defer requests.stateMutex.Unlock()
	requests.activeHandlerCount--
	if requests.shuttingDown && requests.activeHandlerCount == 0 {
		close(requests.handlersFinished)
	}
}

// beginShutdown closes admission. handlersFinished closes once all admitted
// handlers return; gRPC may still need to finish writing their responses.
func (requests *grpcRequestTracker) beginShutdown() {
	requests.stateMutex.Lock()
	defer requests.stateMutex.Unlock()
	requests.shuttingDown = true
	if requests.activeHandlerCount == 0 {
		close(requests.handlersFinished)
	}
}

func listenGRPCEndpoint(ctx context.Context, endpoint string) (net.Listener, error) {
	network, address, err := ParseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}

	if network == "unix" {
		// Unix socket paths can outlive the process that created them.
		address = "/" + address
		if err := os.Remove(address); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to remove %s: %w", address, err)
		}
	}

	listenConfig := &net.ListenConfig{}
	listener, err := listenConfig.Listen(ctx, network, address)
	if err != nil {
		return nil, fmt.Errorf("failed to listen: %w", err)
	}
	return listener, nil
}
