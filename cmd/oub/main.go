package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tlrmchlsmth/oubliette/internal/lifecycle"
)

func main() {
	endpoint := os.Getenv("OUBLIETTE_MCP_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:18080/mcp"
	}
	token := os.Getenv("OUBLIETTE_MCP_TOKEN")
	if token == "" {
		fatalf("OUBLIETTE_MCP_TOKEN is required")
	}
	if len(os.Args) < 2 {
		fatalf("usage: oub create|get|list|renew|delete")
	}
	tool, arguments := parse(os.Args[1], os.Args[2:])
	httpClient := &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}}
	transport := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "oub", Version: "0.1.0"}, nil)
	session, err := mcpClient.Connect(context.Background(), transport, nil)
	if err != nil {
		fatalf("connect: %v", err)
	}
	defer session.Close()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: arguments})
	if err != nil {
		fatalf("call %s: %v", tool, err)
	}
	if result.IsError {
		fatalf("tool error: %v", result.Content)
	}
	b, err := json.MarshalIndent(result.StructuredContent, "", "  ")
	if err != nil {
		fatalf("marshal result: %v", err)
	}
	fmt.Println(string(b))
}

func parse(command string, args []string) (string, any) {
	switch command {
	case "create":
		fs := flag.NewFlagSet("create", flag.ExitOnError)
		ttl := fs.Int64("ttl", 600, "TTL in seconds")
		tier := fs.String("tier", "stub", "resource tier")
		_ = fs.Parse(args)
		if fs.NArg() != 1 {
			fatalf("usage: oub create [--ttl seconds] NAME")
		}
		return "oubliette_create", lifecycle.CreateInput{Name: fs.Arg(0), Tier: *tier, TTLSeconds: *ttl}
	case "get", "delete":
		if len(args) != 1 {
			fatalf("usage: oub %s NAME", command)
		}
		return "oubliette_" + command, lifecycle.NameInput{Name: args[0]}
	case "renew":
		fs := flag.NewFlagSet("renew", flag.ExitOnError)
		ttl := fs.Int64("ttl", 600, "new lifetime from now in seconds")
		_ = fs.Parse(args)
		if fs.NArg() != 1 {
			fatalf("usage: oub renew [--ttl seconds] NAME")
		}
		return "oubliette_renew", lifecycle.RenewInput{Name: fs.Arg(0), TTLSeconds: *ttl}
	case "list":
		if len(args) != 0 {
			fatalf("usage: oub list")
		}
		return "oubliette_list", lifecycle.ListInput{}
	default:
		fatalf("unknown command %q", command)
	}
	return "", nil
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func fatalf(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }
