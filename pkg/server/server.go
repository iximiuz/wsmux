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
		if s.errCh != nil {
			s.errCh <- fmt.Errorf("WebSocket upgrade error: %v", err)
		}
		return
	}
	defer wsConn.Close()

	var d net.Dialer
	tcpConn, err := d.DialContext(s.ctx, "tcp", destAddr)
	if err != nil {
		if s.errCh != nil {
			s.errCh <- fmt.Errorf("error connecting to TCP address %s: %v", destAddr, err)
		}
		return
	}
	defer tcpConn.Close()

	// Start ping sender
	go s.runPingSender(wsConn)

	// Channel to signal when one side of the connection closes
	done := make(chan struct{})

	// Copy data from WebSocket to TCP
	go func() {
		defer close(done)
		for {
			messageType, data, err := wsConn.ReadMessage()
			if err != nil {
				if s.errCh != nil {
					s.errCh <- fmt.Errorf("WebSocket read error: %v", err)
				}
				return
			}
			if messageType != websocket.BinaryMessage {
				if s.errCh != nil {
					s.errCh <- fmt.Errorf("invalid message type: %d", messageType)
				}
				return
			}

			if len(data) > 0 {
				if _, err = tcpConn.Write(data); err != nil {
					if s.errCh != nil {
						s.errCh <- fmt.Errorf("TCP write error: %v", err)
					}
					return
				}
			}
		}
	}()

	// Copy data from TCP to WebSocket
	go func() {
		buffer := make([]byte, 1024)
		for {
			n, err := tcpConn.Read(buffer)
			if err != nil {
				if err != io.EOF {
					if s.errCh != nil {
						s.errCh <- fmt.Errorf("TCP read error: %v", err)
					}
				}
				return
			}
			if n > 0 {
				if err := wsConn.WriteMessage(websocket.BinaryMessage, buffer[:n]); err != nil {
					if s.errCh != nil {
						s.errCh <- fmt.Errorf("WebSocket write error: %v", err)
					}
					return
				}
			}
		}
	}()

	<-done // Wait for either side to close
}

func (s *Server) runPingSender(wsConn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := wsConn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
				if s.errCh != nil {
					s.errCh <- fmt.Errorf("ping error: %v", err)
				}
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
