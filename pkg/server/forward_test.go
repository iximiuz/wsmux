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

// Verifies the legacy forward path ("dial host:port") still works after
// introducing the kind-prefixed payload. The client sends a bare "host:port"
// payload; the server must treat it as kindDial.
func TestForwardTunnel_LegacyDial_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 128)
	go drain(ctx, errCh)

	// Echo server that the tunnel will dial on the server side.
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
					c.Write(buf[:n])
				}
			}(c)
		}
	}()

	srv := server.NewServer(ctx, "", errCh)
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.Handler().ServeHTTP(w, r)
	}))
	defer httpSrv.Close()

	wsURL := strings.Replace(httpSrv.URL, "http://", "ws://", 1)

	localAddr := "127.0.0.1:" + freePort(t)
	c := client.NewClient(ctx, localAddr, target.Addr().String(), wsURL, errCh)
	go func() { _ = c.ListenAndServe() }()

	waitForPortOpen(t, localAddr, 3*time.Second)

	conn, err := net.Dial("tcp", localAddr)
	if err != nil {
		t.Fatalf("dial local: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "ping" {
		t.Fatalf("expected ping, got %q", got)
	}
}
