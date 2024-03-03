# wsmux - a simple WebSocket tunnel server

Start an HTTP server that accepts incoming WebSocket connections and bidirectionally forwards data between the established connection and a TCP destination reachable from the server.

The primary purpose of this project is to provide a library.
The `wsmux` command is just a demo.

Usage:

```sh
# Server - listen for incoming WebSocket connections and tunnel ports
wsmux serve [-addr [host]:<port>]

# Client - forward local port to remote destination via wsmux server
wsmux proxy -server <addr> -local-port <port> [-local-host <host>] -remote-port <port> [-remote-host <host>]
```

Example:

```sh
# On the server, prepare the target (e.g., nginx)
$ docker run --rm -p 127.0.0.1:3000:80 nginx:alpine

# On the server again, start the wsmux server
$ wsmux serve -addr 0.0.0.0:8080
Starting wsmux server on 0.0.0.0:8080

# On the client, start the wsmux client
$ wsmux proxy -server ws://0.0.0.0:8080 -local-port 3001 -remote-port 3000
Starting wsmux client on localhost:3001 -> localhost:3000 via ws://0.0.0.0:8080

# Now, you can access the nginx server on http://localhost:3001
$ curl http://localhost:3001
<!DOCTYPE html>
<html>
<head>
<title>Welcome to nginx!</title>
...
```
