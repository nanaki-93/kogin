// Package toolserver serves one agent run's tools over the Model Context
// Protocol. A run gets its own loopback listener and a one-time bearer token;
// the CLI that runs the model reaches only these tools. The package knows
// nothing of projects or tasks: the caller supplies each tool's gates.
package toolserver

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Name is the server name the CLIs know the tools by: Claude Code calls them
// mcp__kasuri__<tool>, pi mcp__kasuri__<tool>.
const Name = "kasuri"

// Tool is one operation the model may call. Call receives the raw JSON
// arguments and returns the text the model reads; an error is returned to the
// model as a failed call, never as a protocol error.
type Tool struct {
	Name        string
	Description string
	// Schema is the JSON Schema of the arguments; its type must be object.
	Schema   json.RawMessage
	ReadOnly bool
	// Destructive marks tools that delete or overwrite.
	Destructive bool
	Call        func(ctx context.Context, arguments json.RawMessage) (string, error)
}

// Server is a running tool server.
type Server struct {
	// URL is the streamable HTTP endpoint on the loopback interface.
	URL string
	// Token is the bearer token every request must carry.
	Token string

	server *http.Server
	done   chan struct{}
	once   sync.Once
}

// Start listens on a free loopback port and serves the tools until Close or
// until ctx ends.
func Start(ctx context.Context, instructions string, tools []Tool) (*Server, error) {
	if len(tools) == 0 {
		return nil, errors.New("a tool server needs at least one tool")
	}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: Name, Title: "Kasuri", Version: "1"}, &mcp.ServerOptions{Instructions: instructions})
	for _, tool := range tools {
		if err := addTool(mcpServer, tool); err != nil {
			return nil, err
		}
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("tool server: listen on loopback: %w", err)
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{SessionTimeout: time.Hour})
	s := &Server{URL: "http://" + listener.Addr().String() + "/mcp", Token: token, done: make(chan struct{})}
	s.server = &http.Server{Handler: s.authorize(handler), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer close(s.done)
		_ = s.server.Serve(listener)
	}()
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-s.done:
		}
	}()
	return s, nil
}

// Close stops the server; calls in progress are canceled.
func (s *Server) Close() {
	s.once.Do(func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if s.server.Shutdown(shutdown) != nil {
			_ = s.server.Close()
		}
		<-s.done
	})
}

func (s *Server) authorize(next http.Handler) http.Handler {
	want := []byte("Bearer " + s.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func addTool(server *mcp.Server, tool Tool) error {
	var schema map[string]any
	if tool.Name == "" || tool.Call == nil || json.Unmarshal(tool.Schema, &schema) != nil || schema["type"] != "object" {
		return fmt.Errorf("tool server: tool %q needs a name, a handler and an object schema", tool.Name)
	}
	destructive := tool.Destructive
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: tool.ReadOnly, DestructiveHint: &destructive, OpenWorldHint: new(bool)}
	call := tool.Call
	server.AddTool(&mcp.Tool{Name: tool.Name, Description: tool.Description, InputSchema: schema, Annotations: annotations}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		arguments := request.Params.Arguments
		if len(arguments) == 0 {
			arguments = json.RawMessage("{}")
		}
		text, err := call(ctx, arguments)
		if err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
	})
	return nil
}

func newToken() (string, error) {
	var buffer [32]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", fmt.Errorf("tool server: token: %w", err)
	}
	return hex.EncodeToString(buffer[:]), nil
}
