package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
)

type Server struct {
	ctx context.Context

	addr string

	errCh chan error
}

func NewServer(ctx context.Context, addr string, errCh chan error) *Server {
	return &Server{
		ctx:   ctx,
		addr:  addr,
		errCh: errCh,
	}
}

func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnels/{addr}", s.handle)

	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("error listening on %s: %v", s.addr, err)
	}
	defer listener.Close()

	go func() {
		<-s.ctx.Done()
		listener.Close()
	}()

	return http.Serve(listener, mux)
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Accept connection from any origin
	},
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	tcpAddr, err := base64decode(r.PathValue("addr"))
	if err != nil {
		http.Error(w, "invalid destination address", http.StatusBadRequest)
		return
	}

	port := string(tcpAddr)

	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("WebSocket upgrade error: %v\n", err)
		return
	}
	defer wsConn.Close()

	d := net.Dialer{}
	tcpConn, err := d.DialContext(s.ctx, "tcp", port)
	if err != nil {
		fmt.Printf("Error connecting to TCP address %s: %v\n", tcpAddr, err)
		return
	}
	defer tcpConn.Close()

	go func() { io.Copy(wsConn.UnderlyingConn(), tcpConn) }()
	io.Copy(tcpConn, wsConn.UnderlyingConn())
}

func base64decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
