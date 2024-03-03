package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/iximiuz/wsmux/pkg/client"
	"github.com/iximiuz/wsmux/pkg/server"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const usage = `Usage:
  # Server - listen for incoming WebSocket connections and tunnel ports
  wsmux serve -addr [host]:<port>

  # Client - forward local port to remote destination via wsmux server
  wsmux proxy -server <addr> -local-port <port> [-local-host <host>] -remote-port <port> [-remote-host <host>]
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(1)
	}

	ctx := context.Background()

	switch os.Args[1] {
	case "serve":
		serve(ctx)

	case "proxy":
		proxy(ctx)

	case "version":
		fmt.Printf("wsmux %s (built: %s commit: %s)\n", version, date, commit)

	default:
		fmt.Print(usage)
		os.Exit(1)
	}
}

func serve(ctx context.Context) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "0.0.0.0:8080", "Server address")
	fs.Parse(os.Args[2:])

	fmt.Printf("Starting wsmux server on %s\n", *addr)

	errCh := make(chan error, 100)
	go handleErrors(ctx, errCh)

	srv := server.NewServer(ctx, *addr, errCh)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func proxy(ctx context.Context) {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	serverAddr := fs.String("server", "", "Server address")
	localPort := fs.Int("local-port", 0, "Local port")
	localHost := fs.String("local-host", "localhost", "Local host")
	remotePort := fs.Int("remote-port", 0, "Remote port")
	remoteHost := fs.String("remote-host", "localhost", "Remote host")
	fs.Parse(os.Args[2:])

	if *serverAddr == "" {
		fmt.Println("-server flag is required")
		os.Exit(1)
	}

	if *localPort == 0 {
		fmt.Println("-local-port flag is required")
		os.Exit(1)
	}

	if *remotePort == 0 {
		fmt.Println("-remote-port flag is required")
		os.Exit(1)
	}

	fmt.Printf(
		"Starting wsmux client on %s:%d -> %s:%d via %s\n",
		*localHost, *localPort, *remoteHost, *remotePort, *serverAddr,
	)

	errCh := make(chan error, 100)
	go handleErrors(ctx, errCh)

	c := client.NewClient(
		ctx,
		fmt.Sprintf("%s:%d", *localHost, *localPort),
		fmt.Sprintf("%s:%d", *remoteHost, *remotePort),
		*serverAddr,
		errCh,
	)
	if err := c.ListenAndServe(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func handleErrors(ctx context.Context, errCh <-chan error) {
	for {
		select {
		case <-ctx.Done():
			return

		case err := <-errCh:
			fmt.Println(err)
		}
	}
}
