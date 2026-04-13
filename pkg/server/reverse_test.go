package server_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iximiuz/wsmux/pkg/client"
	"github.com/iximiuz/wsmux/pkg/server"
)

func TestReverseTunnel_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wsmuxErrCh := make(chan error, 128)
	go drain(ctx, wsmuxErrCh)

	// Start the wsmux server behind an httptest server so we get a real ws:// URL.
	srv := server.NewServer(ctx, "", wsmuxErrCh)
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.Handler().ServeHTTP(w, r)
	}))
	defer httpSrv.Close()

	// Pick two free ports: one the wsmux server will bind (the reverse "remote"
	// side), and one for the fake target TCP service on the client side.
	remotePort := freePort(t)
	remoteAddr := "127.0.0.1:" + remotePort

	// Fake local target the reverse client dials on each accepted conn.
	// Echoes any bytes it reads, uppercased.
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("target listen: %v", err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					c.Write([]byte(strings.ToUpper(string(buf[:n]))))
				}
			}(c)
		}
	}()

	// Start the reverse client.
	wsURL := strings.Replace(httpSrv.URL, "http://", "ws://", 1)
	rc := client.NewReverseClient(ctx, remoteAddr, target.Addr().String(), wsURL, wsmuxErrCh)
	clientDone := make(chan error, 1)
	go func() { clientDone <- rc.ListenAndServe() }()

	// Wait for the listener to be up on the server side.
	waitForPortOpen(t, remoteAddr, 3*time.Second)

	// Dial the "remote" bound port — this triggers the reverse flow.
	conn, err := net.Dial("tcp", remoteAddr)
	if err != nil {
		t.Fatalf("dial remote: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "HELLO" {
		t.Fatalf("expected HELLO, got %q", got)
	}
}

func drain(ctx context.Context, ch <-chan error) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
		}
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	return port
}

func waitForPortOpen(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("port %s did not open within %s", addr, timeout)
}

