package scheduler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type shutdownConn struct {
	net.Conn
	cancel       context.CancelFunc
	armed        chan struct{}
	deadline     chan struct{}
	writeResult  chan error
	armOnce      *sync.Once
	deadlineOnce *sync.Once
}

func (c *shutdownConn) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("UPDATE ops.executions e SET status")) {
		c.armOnce.Do(func() { close(c.armed) })
		c.cancel()
		select {
		case <-c.deadline:
		case <-time.After(5 * time.Second):
			return 0, fmt.Errorf("pgx did not apply cancellation deadline")
		}
	}
	n, err := c.Conn.Write(p)
	if bytes.Contains(p, []byte("UPDATE ops.executions e SET status")) {
		c.writeResult <- err
	}
	return n, err
}

func (c *shutdownConn) SetDeadline(deadline time.Time) error {
	err := c.Conn.SetDeadline(deadline)
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		c.deadlineOnce.Do(func() { close(c.deadline) })
	}
	return err
}

func TestRunNormalizesPlannerWriteTimeoutAfterCancellation(t *testing.T) {
	base := schedulerDB(t)
	cfg := base.Config()
	cancelReady := make(chan struct{})
	deadlineReady := make(chan struct{})
	writeResult := make(chan error, 1)
	var armOnce, deadlineOnce sync.Once
	ctx, cancel := context.WithCancel(t.Context())
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &shutdownConn{Conn: conn, cancel: cancel, armed: cancelReady, deadline: deadlineReady, writeResult: writeResult, armOnce: &armOnce, deadlineOnce: &deadlineOnce}, nil
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	runDone := make(chan error, 1)
	go func() { runDone <- Run(ctx, pool, schedulerConfig(), t.TempDir(), nil) }()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("scheduler shutdown: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("scheduler did not shut down")
	}
	select {
	case <-cancelReady:
	case <-time.After(2 * time.Second):
		t.Fatal("planner write was not reached")
	}
	select {
	case <-deadlineReady:
	case <-time.After(2 * time.Second):
		t.Fatal("planner write did not observe cancellation deadline")
	}
	select {
	case err := <-writeResult:
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("planner write error = %v, want network timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("planner write did not return")
	}
}
