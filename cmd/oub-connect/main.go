// oub-connect runs in the trusted consumer environment, never in the agent.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	"github.com/tlrmchlsmth/oubliette/internal/mcpauth"
	"github.com/tlrmchlsmth/oubliette/pkg/connector"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("oub-connect", flag.ContinueOnError)
	hostConfig := fs.String("host-kubeconfig", "", "trusted consumer host kubeconfig (required)")
	hostContext := fs.String("host-context", "", "explicit host context (required)")
	tokenFile := fs.String("caller-token-file", "", "private file containing caller's oubliette-mcp audience token (required)")
	output := fs.String("output-dir", "", "new absolute directory on trusted consumer filesystem (required)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 || *hostConfig == "" || *hostContext == "" || *tokenFile == "" || *output == "" {
		return errors.New("usage: oub-connect --host-kubeconfig PATH --host-context CONTEXT --caller-token-file PATH --output-dir NEW_DIRECTORY NAME")
	}
	// ExplicitPath avoids merging an ambient KUBECONFIG into the host config.
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: *hostConfig}, &clientcmd.ConfigOverrides{CurrentContext: *hostContext}).ClientConfig()
	if err != nil {
		return errors.New("cannot load explicit host configuration")
	}
	config.Timeout = 10 * time.Second
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{oubv1.AddToScheme, corev1.AddToScheme, authenticationv1.AddToScheme} {
		if err := add(scheme); err != nil {
			return errors.New("cannot initialize Kubernetes scheme")
		}
	}
	host, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return errors.New("cannot initialize host client")
	}
	sink, err := connector.NewFileSink(*output)
	if err != nil {
		return err
	}
	backend := &connector.KubernetesBackend{Host: host, Config: config, Name: fs.Arg(0),
		Resolver: mcpauth.KubernetesResolver{Client: host, Audience: mcpauth.DefaultAudience},
		Token:    func() (string, error) { return connector.ReadTokenFile(*tokenFile) },
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	err = (connector.Session{Backend: backend, Sink: sink, Ready: func() { fmt.Fprintln(os.Stderr, "virtual access ready; private kubeconfig delivered") }}).Run(ctx)
	if err == context.Canceled && ctx.Err() != nil {
		return nil
	}
	return err
}
