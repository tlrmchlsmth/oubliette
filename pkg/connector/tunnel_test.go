package connector

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	streamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
)

func TestClientStreamFailureDoesNotCloseSessionTransport(t *testing.T) {
	for _, mode := range []string{"peer-loss", "cancel"} {
		t.Run(mode, func(t *testing.T) { testClientStreamFailure(t, mode) })
	}
}

func TestPortForwardURLDoesNotInheritRequestTimeout(t *testing.T) {
	config := &rest.Config{Host: "https://host.invalid", Timeout: 10 * time.Second}
	u, err := portForwardURL(config, "oub-alice", "control-plane")
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Has("timeout") || config.Timeout != 10*time.Second {
		t.Fatal("session timeout leaked into URL or host request bounds changed")
	}
	if u.Path != "/api/v1/namespaces/oub-alice/pods/control-plane/portforward" {
		t.Fatal(u.Path)
	}
}

func testClientStreamFailure(t *testing.T, mode string) {
	peer := make(chan httpstream.Connection, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := httpstream.Handshake(r, w, []string{portforward.PortForwardProtocolV1Name}); err != nil {
			return
		}
		connection := streamspdy.NewResponseUpgrader().UpgradeResponse(w, r, func(s httpstream.Stream, replied <-chan struct{}) error {
			go func() {
				<-replied
				if s.Headers().Get(corev1.StreamType) == corev1.StreamTypeError {
					_, _ = io.WriteString(s, "simulated client broken pipe")
				} else {
					_, _ = io.WriteString(s, "ok")
				}
				_ = s.Close()
			}()
			return nil
		})
		if connection == nil {
			return
		}
		peer <- connection
		<-connection.CloseChan()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	transport, upgrader, err := forwardTransport(ctx, &rest.Config{Host: server.URL, TLSClientConfig: rest.TLSClientConfig{CAData: ca}})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
	ready := make(chan struct{})
	forwarder, err := portforward.NewOnAddresses(contextDialer{request: request, client: &http.Client{Transport: transport}, upgrader: upgrader}, []string{"127.0.0.1"}, []string{"0:8443"}, ctx.Done(), ready, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- forwarder.ForwardPorts() }()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("forwarder did not start")
	}
	ports, _ := forwarder.GetPorts()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(ports[0].Local)))
	for i := 0; i < 2; i++ {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		b, err := io.ReadAll(conn)
		_ = conn.Close()
		if err != nil || string(b) != "ok" {
			t.Fatalf("request %d: %q, %v", i, b, err)
		}
	}
	// A genuine host transport failure still closes the listener/session.
	if mode == "peer-loss" {
		_ = (<-peer).Close()
	} else {
		cancel()
	}
	select {
	case err := <-done:
		if mode == "peer-loss" && err == nil {
			t.Fatal("transport loss was successful")
		}
	case <-time.After(time.Second):
		t.Fatal("host failure did not end tunnel")
	}
}
