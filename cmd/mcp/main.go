package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	"github.com/tlrmchlsmth/oubliette/internal/lifecycle"
	"github.com/tlrmchlsmth/oubliette/internal/mcpauth"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

func main() {
	var listen string
	var tokenAudience string
	flag.StringVar(&listen, "listen", ":8080", "HTTP listen address")
	flag.StringVar(&tokenAudience, "token-audience", mcpauth.DefaultAudience, "required audience for MCP bearer tokens")
	flag.Parse()
	scheme := runtime.NewScheme()
	utilruntime.Must(oubv1.AddToScheme(scheme))
	utilruntime.Must(authenticationv1.AddToScheme(scheme))
	kube, err := client.New(config.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		log.Fatal(err)
	}
	service := &lifecycle.Service{Client: kube}
	server := mcp.NewServer(&mcp.Implementation{Name: "oubliette", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "oubliette_create", Description: "Create an expiring Oubliette"}, func(ctx context.Context, _ *mcp.CallToolRequest, in lifecycle.CreateInput) (*mcp.CallToolResult, lifecycle.View, error) {
		out, err := service.Create(ctx, in)
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "oubliette_get", Description: "Get lifecycle state without credentials"}, func(ctx context.Context, _ *mcp.CallToolRequest, in lifecycle.NameInput) (*mcp.CallToolResult, lifecycle.View, error) {
		out, err := service.Get(ctx, in)
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "oubliette_list", Description: "List visible Oubliettes"}, func(ctx context.Context, _ *mcp.CallToolRequest, in lifecycle.ListInput) (*mcp.CallToolResult, lifecycle.ListOutput, error) {
		out, err := service.List(ctx, in)
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "oubliette_renew", Description: "Move a non-terminal expiry forward within policy"}, func(ctx context.Context, _ *mcp.CallToolRequest, in lifecycle.RenewInput) (*mcp.CallToolResult, lifecycle.View, error) {
		out, err := service.Renew(ctx, in)
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "oubliette_delete", Description: "Explicitly delete an Oubliette"}, func(ctx context.Context, _ *mcp.CallToolRequest, in lifecycle.NameInput) (*mcp.CallToolResult, lifecycle.DeleteOutput, error) {
		out, err := service.Delete(ctx, in)
		return nil, out, err
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20, PropagateRequestCancellation: true})
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpauth.Authenticate(mcpauth.KubernetesResolver{Client: kube, Audience: tokenAudience}, handler))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	httpServer := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("oubliette MCP listening on %s", listen)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
