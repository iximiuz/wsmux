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

			done := make(chan struct{})

			// Copy from WebSocket to local TCP connection
			go func() {
				defer close(done)
				for {
					messageType, reader, err := wsConn.NextReader()
					if err != nil {
						c.errCh <- fmt.Errorf("WebSocket read error: %v", err)
						return
					}
					if messageType != websocket.BinaryMessage {
						c.errCh <- fmt.Errorf("invalid message type: %d", messageType)
						continue
					}
					if _, err := io.Copy(localConn, reader); err != nil {
						c.errCh <- fmt.Errorf("copy to local connection error: %v", err)
						return
					}
				}
			}()

			// Copy from local TCP connection to WebSocket
			for {
				writer, err := wsConn.NextWriter(websocket.BinaryMessage)
				if err != nil {
					c.errCh <- fmt.Errorf("WebSocket write error: %v", err)
					break
				}
				if _, err := io.Copy(writer, localConn); err != nil {
					c.errCh <- fmt.Errorf("copy from local connection error: %v", err)
					writer.Close()
					break
				}
				writer.Close()
			}

			<-done // Wait for the copying goroutine to finish
		}()
	}

	return c.ctx.Err()
}

func base64encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
