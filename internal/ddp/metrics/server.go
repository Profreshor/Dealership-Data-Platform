package metrics

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// Run couples the private metrics listener to the API or scheduler lifetime.
// A bind failure prevents the workload from starting, and either exit stops both.
func Run(ctx context.Context, addr string, handler http.Handler, workload func(context.Context) error) error {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return errors.New("metrics address must be host:port")
	}
	if ctx.Err() != nil {
		return nil
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return nil
		}
		return errors.New("cannot listen for metrics; check --metrics-addr and port availability")
	}
	defer listener.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	served := make(chan error, 1)
	completed := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	go func() { completed <- workload(runCtx) }()
	var workloadErr, metricsErr error
	workDone, serverDone := false, false
	select {
	case workloadErr = <-completed:
		workDone = true
	case metricsErr = <-served:
		serverDone = true
	case <-ctx.Done():
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	shutdownErr := server.Shutdown(shutdown)
	if shutdownErr != nil {
		_ = server.Close()
	}
	if !serverDone {
		metricsErr = <-served
	}
	if !workDone {
		workloadErr = <-completed
	}
	if errors.Is(metricsErr, http.ErrServerClosed) {
		metricsErr = nil
	}
	if metricsErr != nil {
		metricsErr = errors.New("metrics listener stopped unexpectedly")
	}
	if ctx.Err() != nil && errors.Is(workloadErr, ctx.Err()) {
		workloadErr = nil
	}
	return errors.Join(workloadErr, metricsErr, shutdownErr)
}
