package client

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
)

type Client struct {
	ctx context.Context

	localAddr  string
	remoteAddr string
	viaAddr    string

	headers http.Header

	errCh chan error
}

func NewClient(
	ctx context.Context,
	localAddr string,
	remoteAddr string,
	viaAddr string,
	errCh chan error,
) *Client {
	return &Client{
		ctx:        ctx,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		viaAddr:    viaAddr,
		headers:    http.Header{},
		errCh:      errCh,
	}
}

func (c *Client) SetHeader(key, value string) {
	c.headers.Set(key, value)
}

func (c *Client) ListenAndServe() error {
	listener, err := net.Listen("tcp", c.localAddr)
	if err != nil {
		return fmt.Errorf("error listening on %s: %w", c.localAddr, err)
	}
	defer listener.Close()

	go func() {
		<-c.ctx.Done()
		listener.Close()
	}()

	for c.ctx.Err() == nil {
		localConn, err := listener.Accept()
		if err != nil {
			if c.errCh != nil {
				c.errCh <- fmt.Errorf("error accepting local connection: %w", err)
			}
			continue
		}

		go func() {
			defer localConn.Close()

			wsConn, _, err := websocket.DefaultDialer.DialContext(
				c.ctx,
				c.viaAddr+"/tunnels/"+base64encode(c.remoteAddr),
				c.headers,
			)
			if err != nil {
				if c.errCh != nil {
					c.errCh <- fmt.Errorf("error dialing WebSocket server: %w", err)
				}
				return
			}
			defer wsConn.Close()

			go func() { io.Copy(wsConn.UnderlyingConn(), localConn) }()
			io.Copy(localConn, wsConn.UnderlyingConn())
		}()
	}

	return c.ctx.Err()
}

func base64encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
