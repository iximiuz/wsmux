package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// The base64-decoded {addr} path parameter is one of:
//
//	"host:port"                 -- legacy forward: dial host:port, pipe (kept for back-compat).
//	"dial:host:port"            -- explicit forward: same behavior as legacy.
//	"listen:host:port"          -- reverse listen: open a TCP listener on host:port.
//	                               This WS becomes the control channel; for each accepted
//	                               TCP conn, the server parks it and sends a JSON text
//	                               message {"type":"connect","cid":"..."} to the client.
//	"connect:<cid>"             -- reverse data: attach to the parked TCP conn keyed by cid
//	                               and pipe bytes bidirectionally.
const (
	kindDial    = "dial"
	kindListen  = "listen"
	kindConnect = "connect"

	// How long a parked reverse conn waits for its data WS before being dropped.
	pendingConnTTL = 15 * time.Second
)

type Server struct {
	ctx context.Context

	addr string

	errCh chan error

	// cid (hex string) -> *pendingConn, populated by reverse listeners and
	// consumed by `connect:<cid>` handlers.
	pending sync.Map
}

type pendingConn struct {
	conn     net.Conn
	bindAddr string
}

func NewServer(ctx context.Context, addr string, errCh chan error) *Server {
	return &Server{
		ctx:   ctx,
		addr:  addr,
		errCh: errCh,
	}
}

func (s *Server) ListenAndServe() error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("error listening on %s: %v", s.addr, err)
	}
	defer listener.Close()

	go func() {
		<-s.ctx.Done()
		listener.Close()
	}()

	return http.Serve(listener, s.Handler())
}

// Handler returns the HTTP handler that serves tunnel WebSocket endpoints.
// Useful for embedding in an existing HTTP server or for tests.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnels/{addr}", s.handle)
	return mux
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Accept connection from any origin
	},
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	payload, err := base64decode(r.PathValue("addr"))
	if err != nil {
		http.Error(w, "invalid destination address", http.StatusBadRequest)
		return
	}
	payload = strings.TrimSpace(payload)

	kind, arg := parseKind(payload)

	switch kind {
	case kindDial:
		s.handleDial(w, r, arg)
	case kindListen:
		s.handleListen(w, r, arg)
	case kindConnect:
		s.handleConnect(w, r, arg)
	default:
		http.Error(w, "unknown tunnel kind", http.StatusBadRequest)
	}
}

func (s *Server) handleDial(w http.ResponseWriter, r *http.Request, destAddr string) {
	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logErr(fmt.Errorf("WebSocket upgrade error: %v", err))
		return
	}
	defer wsConn.Close()

	var d net.Dialer
	tcpConn, err := d.DialContext(s.ctx, "tcp", destAddr)
	if err != nil {
		s.logErr(fmt.Errorf("error connecting to TCP address %s: %v", destAddr, err))
		return
	}
	defer tcpConn.Close()

	go s.runPingSender(wsConn)

	pipeWSBinaryTCP(wsConn, tcpConn, s.errCh)
}

func (s *Server) handleListen(w http.ResponseWriter, r *http.Request, bindAddr string) {
	listener, err := net.Listen("tcp", bindAddr)
	if err != nil {
		http.Error(w, fmt.Sprintf("cannot bind %s: %v", bindAddr, err), http.StatusConflict)
		return
	}
	defer listener.Close()

	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logErr(fmt.Errorf("WebSocket upgrade error: %v", err))
		return
	}
	defer wsConn.Close()

	// Close the listener as soon as the control WS goes away so we release the
	// port and stop accepting connections that have nowhere to go.
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	go s.runPingSender(wsConn)

	// Drain any inbound messages so we notice the control WS closing (and so
	// the websocket library processes pongs).
	go func() {
		for {
			if _, _, err := wsConn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	// Track cids we parked so we can clean them up when the control WS dies.
	var (
		mu   sync.Mutex
		cids = make(map[string]struct{})
	)
	defer func() {
		mu.Lock()
		for cid := range cids {
			if v, ok := s.pending.LoadAndDelete(cid); ok {
				v.(*pendingConn).conn.Close()
			}
		}
		mu.Unlock()
	}()

	writeMu := &sync.Mutex{}

	for ctx.Err() == nil {
		tcpConn, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				s.logErr(fmt.Errorf("reverse listener accept error on %s: %v", bindAddr, err))
			}
			return
		}

		cid, err := newCID()
		if err != nil {
			tcpConn.Close()
			s.logErr(fmt.Errorf("cid generation: %v", err))
			return
		}

		s.pending.Store(cid, &pendingConn{conn: tcpConn, bindAddr: bindAddr})

		mu.Lock()
		cids[cid] = struct{}{}
		mu.Unlock()

		time.AfterFunc(pendingConnTTL, func() {
			if v, ok := s.pending.LoadAndDelete(cid); ok {
				v.(*pendingConn).conn.Close()
				mu.Lock()
				delete(cids, cid)
				mu.Unlock()
			}
		})

		msg, _ := json.Marshal(map[string]string{"type": "connect", "cid": cid})
		writeMu.Lock()
		err = wsConn.WriteMessage(websocket.TextMessage, msg)
		writeMu.Unlock()
		if err != nil {
			s.logErr(fmt.Errorf("control WS write error: %v", err))
			if v, ok := s.pending.LoadAndDelete(cid); ok {
				v.(*pendingConn).conn.Close()
			}
			return
		}
	}
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request, cid string) {
	v, ok := s.pending.LoadAndDelete(cid)
	if !ok {
		http.Error(w, "unknown or expired cid", http.StatusNotFound)
		return
	}
	pc := v.(*pendingConn)

	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		pc.conn.Close()
		s.logErr(fmt.Errorf("WebSocket upgrade error: %v", err))
		return
	}
	defer wsConn.Close()
	defer pc.conn.Close()

	go s.runPingSender(wsConn)

	pipeWSBinaryTCP(wsConn, pc.conn, s.errCh)
}

// pipeWSBinaryTCP copies bytes bidirectionally between a WebSocket (binary
// messages only) and a TCP conn, returning when either side closes.
func pipeWSBinaryTCP(wsConn *websocket.Conn, tcpConn net.Conn, errCh chan error) {
	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			messageType, data, err := wsConn.ReadMessage()
			if err != nil {
				if errCh != nil {
					errCh <- fmt.Errorf("WebSocket read error: %v", err)
				}
				return
			}
			if messageType != websocket.BinaryMessage {
				if errCh != nil {
					errCh <- fmt.Errorf("invalid message type: %d", messageType)
				}
				return
			}

			if len(data) > 0 {
				if _, err = tcpConn.Write(data); err != nil {
					if errCh != nil {
						errCh <- fmt.Errorf("TCP write error: %v", err)
					}
					return
				}
			}
		}
	}()

	go func() {
		buffer := make([]byte, 1024)
		for {
			n, err := tcpConn.Read(buffer)
			if err != nil {
				if err != io.EOF && errCh != nil {
					errCh <- fmt.Errorf("TCP read error: %v", err)
				}
				return
			}
			if n > 0 {
				if err := wsConn.WriteMessage(websocket.BinaryMessage, buffer[:n]); err != nil {
					if errCh != nil {
						errCh <- fmt.Errorf("WebSocket write error: %v", err)
					}
					return
				}
			}
		}
	}()

	<-done
}

func (s *Server) runPingSender(wsConn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := wsConn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
				s.logErr(fmt.Errorf("ping error: %v", err))
				return
			}
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Server) logErr(err error) {
	if s.errCh != nil {
		s.errCh <- err
	}
}

// parseKind splits the decoded payload into (kind, arg). A bare "host:port"
// with no recognized kind prefix is treated as kindDial for backward compat.
func parseKind(payload string) (kind, arg string) {
	prefix, rest, ok := strings.Cut(payload, ":")
	if !ok {
		return "", payload
	}
	switch prefix {
	case kindDial, kindListen, kindConnect:
		return prefix, rest
	}
	return kindDial, payload
}

func newCID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func base64decode(s string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}
