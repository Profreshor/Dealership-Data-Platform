package comms

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestProbeSMTPStallIsBounded(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(conn, "220 synthetic\r\n")
		_, _ = bufio.NewReader(conn).ReadString('\n')
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}()
	started := time.Now()
	err = Probe(context.Background(), probeSettings(listener.Addr().String(), "none"))
	if err == nil {
		t.Fatal("Probe unexpectedly succeeded after peer stalled")
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("Probe exceeded bound: %s", elapsed)
	}
}
