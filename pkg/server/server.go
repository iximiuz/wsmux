package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

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
	destAddr, err := base64decode(r.PathValue("addr"))
	if err != nil {
		http.Error(w, "invalid destination address", http.StatusBadRequest)
		return
	}
	destAddr = strings.TrimSpace(destAddr)

	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.errCh <- fmt.Errorf("WebSocket upgrade error: %v", err)
		return
	}
	defer wsConn.Close()

	var d net.Dialer
	tcpConn, err := d.DialContext(s.ctx, "tcp", destAddr)
	if err != nil {
		s.errCh <- fmt.Errorf("error connecting to TCP address %s: %v", destAddr, err)
		return
	}
	defer tcpConn.Close()

	go s.runPingSender(wsConn)

	// Use channels to signal when copying is done
	done := make(chan struct{})

	// Copy from WebSocket to TCP
	go func() {
		defer close(done)
		for {
			messageType, reader, err := wsConn.NextReader()
			if err != nil {
				s.errCh <- fmt.Errorf("WebSocket read error: %v", err)
				return
			}
			if messageType != websocket.BinaryMessage {
				s.errCh <- fmt.Errorf("invalid message type: %d", messageType)
				continue
			}
			if _, err := io.Copy(tcpConn, reader); err != nil {
				s.errCh <- fmt.Errorf("copy to TCP error: %v", err)
				return
			}
		}
	}()

	// Copy from TCP to WebSocket
	for {
		writer, err := wsConn.NextWriter(websocket.BinaryMessage)
		if err != nil {
			s.errCh <- fmt.Errorf("WebSocket write error: %v", err)
			break
		}
		if _, err := io.Copy(writer, tcpConn); err != nil {
			s.errCh <- fmt.Errorf("copy from TCP error: %v", err)
			writer.Close()
			break
		}
		writer.Close()
	}

	<-done // Wait for the copying goroutine to finish
}

func (s *Server) runPingSender(wsConn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := wsConn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
				s.errCh <- fmt.Errorf("ping error: %v", err)
				return
			}
		case <-s.ctx.Done():
			return
		}
	}
}

func base64decode(s string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}
