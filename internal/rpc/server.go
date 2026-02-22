package rpc

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/channel"
	"github.com/creachadair/jrpc2/handler"
	jserver "github.com/creachadair/jrpc2/server"
	"github.com/steipete/wacli/internal/app"
)

// Server wraps an App and an EventHub and implements JSON-RPC 2.0 service.
type Server struct {
	app *app.App
	hub *Hub
}

// ServeOptions controls how the server listens for connections.
type ServeOptions struct {
	// Transport is "stdio" (default) or "tcp".
	Transport string
	// Listen is the address used when Transport=="tcp" (default 127.0.0.1:8686).
	Listen string
}

// NewServer creates an RPC server backed by the given App and EventHub.
func NewServer(a *app.App, hub *Hub) *Server {
	return &Server{app: a, hub: hub}
}

func (s *Server) buildAssigner() jrpc2.Assigner {
	return handler.Map{
		"send":           handler.New(s.rpcSend),
		"listChats":      handler.New(s.rpcListChats),
		"getMessages":    handler.New(s.rpcGetMessages),
		"subscribe":      handler.New(s.rpcSubscribe),
		"sendReaction":   handler.New(s.rpcSendReaction),
		"remoteDelete":   handler.New(s.rpcRemoteDelete),
		"sendFile":       handler.New(s.rpcSendFile),
		"searchMessages": handler.New(s.rpcSearchMessages),
	}
}

func (s *Server) serverOpts() *jrpc2.ServerOptions {
	return &jrpc2.ServerOptions{
		AllowPush: true,
	}
}

// Serve starts the JSON-RPC server using the transport specified in opts and
// blocks until ctx is cancelled or an unrecoverable error occurs.
func (s *Server) Serve(ctx context.Context, opts ServeOptions) error {
	switch opts.Transport {
	case "tcp":
		return s.serveTCP(ctx, opts.Listen)
	default: // "stdio" or empty
		return s.serveStdio(ctx)
	}
}

func (s *Server) serveStdio(ctx context.Context) error {
	ch := channel.Line(os.Stdin, os.Stdout)
	srv := jrpc2.NewServer(s.buildAssigner(), s.serverOpts()).Start(ch)
	go func() {
		<-ctx.Done()
		srv.Stop()
	}()
	return srv.Wait()
}

func (s *Server) serveTCP(ctx context.Context, addr string) error {
	if addr == "" {
		addr = "127.0.0.1:8686"
	}
	lst, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	fmt.Fprintf(os.Stderr, "wacli: JSON-RPC daemon listening on %s\n", addr)

	// Close listener when context is cancelled.
	go func() {
		<-ctx.Done()
		_ = lst.Close()
	}()

	acc := jserver.NetAccepter(lst, channel.Line)
	return jserver.Loop(ctx, acc, jserver.Static(s.buildAssigner()), &jserver.LoopOptions{
		ServerOptions: s.serverOpts(),
	})
}
