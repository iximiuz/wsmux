package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
)

// ReverseClient opens a control WS to a wsmux server asking it to listen on
// `remoteAddr`; for every TCP connection accepted there, the client dials
// `localAddr` on the local side and bridges the two via a per-connection data WS.
type ReverseClient struct {
	ctx context.Context

	remoteAddr string // bind address on the wsmux server
	localAddr  string // target address dialed on this (client) side

	viaAddr string

	headers http.Header

	errCh chan error
}

func NewReverseClient(
	ctx context.Context,
	remoteAddr string,
	localAddr string,
	viaAddr string,
	errCh chan error,
) *ReverseClient {
	return &ReverseClient{
		ctx:        ctx,
		remoteAddr: remoteAddr,
		localAddr:  localAddr,
		viaAddr:    viaAddr,
		headers:    http.Header{},
		errCh:      errCh,
	}
}

func (c *ReverseClient) SetHeader(key, value string) {
	c.headers.Set(key, value)
}

func (c *ReverseClient) ListenAndServe() error {
	controlURL := c.viaAddr + "/tunnels/" + base64encode("listen:"+c.remoteAddr)

	wsConn, resp, err := websocket.DefaultDialer.DialContext(c.ctx, controlURL, c.headers)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("error dialing reverse control WS (status %d): %w", resp.StatusCode, err)
		}
		return fmt.Errorf("error dialing reverse control WS: %w", err)
	}
	defer wsConn.Close()

	go func() {
		<-c.ctx.Done()
		wsConn.Close()
	}()

	for {
		messageType, data, err := wsConn.ReadMessage()
		if err != nil {
			if c.ctx.Err() != nil {
				return c.ctx.Err()
			}
			return fmt.Errorf("control WS read error: %w", err)
		}
		if messageType != websocket.TextMessage {
			continue
		}

		var msg struct {
			Type string `json:"type"`
			Cid  string `json:"cid"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			if c.errCh != nil {
				c.errCh <- fmt.Errorf("control WS decode error: %v", err)
			}
			continue
		}
		if msg.Type != "connect" || msg.Cid == "" {
			continue
		}

		go c.handleReverseConn(msg.Cid)
	}
}

func (c *ReverseClient) handleReverseConn(cid string) {
	localConn, err := (&net.Dialer{}).DialContext(c.ctx, "tcp", c.localAddr)
	if err != nil {
		if c.errCh != nil {
			c.errCh <- fmt.Errorf("error dialing local target %s: %v", c.localAddr, err)
		}
		return
	}
	defer localConn.Close()

	dataURL := c.viaAddr + "/tunnels/" + base64encode("connect:"+cid)
	wsConn, resp, err := websocket.DefaultDialer.DialContext(c.ctx, dataURL, c.headers)
	if err != nil {
		if c.errCh != nil {
			status := 0
			if resp != nil {
				status = resp.StatusCode
			}
			c.errCh <- fmt.Errorf("error dialing reverse data WS (status %d): %v", status, err)
		}
		return
	}
	defer wsConn.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			messageType, data, err := wsConn.ReadMessage()
			if err != nil {
				if c.errCh != nil {
					c.errCh <- fmt.Errorf("data WS read error: %v", err)
				}
				return
			}
			if messageType != websocket.BinaryMessage {
				if c.errCh != nil {
					c.errCh <- fmt.Errorf("invalid message type: %d", messageType)
				}
				return
			}
			if len(data) > 0 {
				if _, err = localConn.Write(data); err != nil {
					if c.errCh != nil {
						c.errCh <- fmt.Errorf("local TCP write error: %v", err)
					}
					return
				}
			}
		}
	}()

	go func() {
		buffer := make([]byte, 1024)
		for {
			n, err := localConn.Read(buffer)
			if err != nil {
				if err != io.EOF && c.errCh != nil {
					c.errCh <- fmt.Errorf("local TCP read error: %v", err)
				}
				return
			}
			if n > 0 {
				if err := wsConn.WriteMessage(websocket.BinaryMessage, buffer[:n]); err != nil {
					if c.errCh != nil {
						c.errCh <- fmt.Errorf("data WS write error: %v", err)
					}
					return
				}
			}
		}
	}()

	<-done
}
