package connector

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func readyObject() *oubv1.Oubliette {
	return &oubv1.Oubliette{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", UID: "original", Generation: 2, Annotations: map[string]string{oubv1.CallerDigestAnnotation: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("alice")))}},
		Spec:       oubv1.OublietteSpec{Tier: "stub", ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour))},
		Status:     oubv1.OublietteStatus{HostNamespace: "oub-alice", ObservedGeneration: 2, Conditions: []metav1.Condition{{Type: oubv1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 2}}},
	}
}

func TestAuthorizationBoundary(t *testing.T) {
	for name, mutate := range map[string]func(*oubv1.Oubliette){
		"foreign owner":   func(o *oubv1.Oubliette) { o.Annotations = nil },
		"expired":         func(o *oubv1.Oubliette) { o.Spec.ExpiresAt = metav1.NewTime(time.Now().Add(-time.Second)) },
		"deleting":        func(o *oubv1.Oubliette) { now := metav1.Now(); o.DeletionTimestamp = &now },
		"stale status":    func(o *oubv1.Oubliette) { o.Status.ObservedGeneration-- },
		"stale ready":     func(o *oubv1.Oubliette) { o.Status.Conditions[0].ObservedGeneration-- },
		"not ready":       func(o *oubv1.Oubliette) { o.Status.Conditions[0].Status = metav1.ConditionFalse },
		"missing ready":   func(o *oubv1.Oubliette) { o.Status.Conditions = nil },
		"wrong namespace": func(o *oubv1.Oubliette) { o.Status.HostNamespace = "host-system" },
		"missing uid":     func(o *oubv1.Oubliette) { o.UID = "" },
		"expiring": func(o *oubv1.Oubliette) {
			o.Status.Conditions = append(o.Status.Conditions, metav1.Condition{Type: oubv1.ConditionExpiring, Status: metav1.ConditionTrue})
		},
		"forgotten": func(o *oubv1.Oubliette) {
			o.Status.Conditions = append(o.Status.Conditions, metav1.Condition{Type: oubv1.ConditionForgotten, Status: metav1.ConditionTrue})
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := readyObject()
			mutate(o)
			if _, err := authorize(o, "alice", time.Now()); !errors.Is(err, ErrDenied) {
				t.Fatalf("got %v", err)
			}
		})
	}
	lease, err := authorize(readyObject(), "alice", time.Now())
	if err != nil || lease.Namespace != "oub-alice" || lease.UID != "original" {
		t.Fatalf("valid lease rejected: %v", err)
	}
}

type resolverFunc func(context.Context, string) (string, error)

func (f resolverFunc) Resolve(ctx context.Context, token string) (string, error) {
	return f(ctx, token)
}

func TestCheckAuthenticatesCaller(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = oubv1.AddToScheme(scheme)
	host := fake.NewClientBuilder().WithScheme(scheme).WithObjects(readyObject()).Build()
	b := &KubernetesBackend{Host: host, Name: "alice", Token: func() (string, error) { return "secret\n", nil },
		Resolver: resolverFunc(func(_ context.Context, token string) (string, error) {
			if token != "secret" {
				t.Fatal("token file was not trimmed")
			}
			return "alice", nil
		}),
	}
	if _, err := b.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.Resolver = resolverFunc(func(context.Context, string) (string, error) { return "bob", nil })
	if _, err := b.Check(context.Background()); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign identity allowed")
	}
	b.Resolver = resolverFunc(func(context.Context, string) (string, error) { return "", errors.New("sensitive upstream response") })
	if _, err := b.Check(context.Background()); err != ErrDenied {
		t.Fatalf("upstream error leaked: %v", err)
	}
}

func bootstrapFixture() *clientcmdapi.Config {
	cfg := clientcmdapi.NewConfig()
	cfg.CurrentContext = "bootstrap"
	cfg.Contexts["bootstrap"] = &clientcmdapi.Context{Cluster: "virtual", AuthInfo: "bootstrap"}
	cfg.Clusters["virtual"] = &clientcmdapi.Cluster{Server: "https://must-not-be-used.invalid", CertificateAuthorityData: []byte("virtual-ca")}
	cfg.AuthInfos["bootstrap"] = &clientcmdapi.AuthInfo{Token: "bootstrap-secret"}
	return cfg
}

func TestBootstrapCannotExecuteOrReadFiles(t *testing.T) {
	for name, mutate := range map[string]func(*clientcmdapi.Config){
		"exec": func(c *clientcmdapi.Config) {
			c.AuthInfos["bootstrap"].Exec = &clientcmdapi.ExecConfig{Command: "do-not-execute"}
		},
		"auth provider": func(c *clientcmdapi.Config) {
			c.AuthInfos["bootstrap"].AuthProvider = &clientcmdapi.AuthProviderConfig{Name: "untrusted"}
		},
		"token file":      func(c *clientcmdapi.Config) { c.AuthInfos["bootstrap"].TokenFile = "/private/host-token" },
		"client key":      func(c *clientcmdapi.Config) { c.AuthInfos["bootstrap"].ClientKey = "/private/host-key" },
		"client cert":     func(c *clientcmdapi.Config) { c.AuthInfos["bootstrap"].ClientCertificate = "/private/host-cert" },
		"ca file":         func(c *clientcmdapi.Config) { c.Clusters["virtual"].CertificateAuthority = "/private/host-ca" },
		"insecure tls":    func(c *clientcmdapi.Config) { c.Clusters["virtual"].InsecureSkipTLSVerify = true },
		"proxy":           func(c *clientcmdapi.Config) { c.Clusters["virtual"].ProxyURL = "https://untrusted.invalid" },
		"missing ca":      func(c *clientcmdapi.Config) { c.Clusters["virtual"].CertificateAuthorityData = nil },
		"missing context": func(c *clientcmdapi.Config) { c.CurrentContext = "missing" },
	} {
		t.Run(name, func(t *testing.T) {
			c := bootstrapFixture()
			mutate(c)
			data, _ := clientcmd.Write(*c)
			if _, err := bootstrapConfig(data, "alice.oub-alice"); err != ErrCredential {
				t.Fatalf("unsafe bootstrap accepted: %v", err)
			}
		})
	}
	data, _ := clientcmd.Write(*bootstrapFixture())
	c, err := bootstrapConfig(data, "alice.oub-alice")
	if err != nil || c.Host != "" || c.ServerName != "alice.oub-alice" || c.BearerToken != "bootstrap-secret" {
		t.Fatalf("unexpected bootstrap: %v", err)
	}
}

func TestVirtualCredentialHandoff(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/namespaces/default/serviceaccounts/oubliette-agent/token" || r.Header.Get("Authorization") != "Bearer bootstrap-secret" {
			t.Error("unexpected token request")
		}
		var request authenticationv1.TokenRequest
		scheme := runtime.NewScheme()
		_ = authenticationv1.AddToScheme(scheme)
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Error(readErr)
		}
		if _, _, err := serializer.NewCodecFactory(scheme).UniversalDeserializer().Decode(body, nil, &request); err != nil {
			t.Error(err)
		}
		if request.Spec.ExpirationSeconds == nil || *request.Spec.ExpirationSeconds != 600 {
			t.Error("not a ten-minute token request")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authenticationv1.TokenRequest{TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenRequest"}, Status: authenticationv1.TokenRequestStatus{Token: "virtual-agent-token", ExpirationTimestamp: metav1.NewTime(time.Now().Add(10 * time.Minute))}})
	}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	c := &kubernetesConnection{config: &rest.Config{Host: server.URL, BearerToken: "bootstrap-secret", TLSClientConfig: rest.TLSClientConfig{CAData: ca}}}
	credential, err := c.Issue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.config.BearerToken != "virtual-agent-token" || len(c.config.CertData) != 0 || len(c.config.KeyData) != 0 {
		t.Fatal("bootstrap authentication retained after handoff")
	}
	if strings.Contains(string(credential.Kubeconfig), "bootstrap-secret") {
		t.Fatal("bootstrap token leaked")
	}
	result, err := clientcmd.Load(credential.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Clusters) != 1 || len(result.Contexts) != 1 || len(result.AuthInfos) != 1 {
		t.Fatal("extra access in kubeconfig")
	}
	auth := result.AuthInfos["agent"]
	if auth.Token != "virtual-agent-token" || len(auth.ClientKeyData) != 0 || auth.Exec != nil {
		t.Fatal("wrong auth in kubeconfig")
	}
	if result.Clusters["oubliette"].Server != server.URL {
		t.Fatal("wrong API endpoint")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPortForwardHandshakeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://host.invalid/portforward", nil)
	started := make(chan struct{})
	dialer := contextDialer{request: request, client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}}
	finished := make(chan error, 1)
	go func() { _, _, err := dialer.Dial("portforward.k8s.io"); finished <- err }()
	<-started
	cancel()
	select {
	case err := <-finished:
		// client-go wraps the negotiation error without preserving its cause.
		if err == nil || ctx.Err() != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled handshake still running")
	}
}

func TestStalledSPDYUpgradeClosesSocket(t *testing.T) {
	started, disconnected := make(chan struct{}), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(disconnected)
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
	dialer := contextDialer{request: request, client: &http.Client{Transport: transport}, upgrader: upgrader}
	finished := make(chan error, 1)
	go func() { _, _, err := dialer.Dial("portforward.k8s.io"); finished <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upgrade did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("stalled upgrade succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("stalled upgrade survived cancellation")
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("server socket remains connected")
	}
}

func TestControlPlaneTargetExcludesTenantPods(t *testing.T) {
	lease := Lease{Name: "alice", Namespace: "oub-alice"}
	service := &corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 443, TargetPort: intstr.FromString("https")}}}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "control", Namespace: lease.Namespace, Labels: map[string]string{"app": "vcluster", "release": "alice"}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Ports: []corev1.ContainerPort{{Name: "https", ContainerPort: 8443}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	name, port, err := controlPlaneTarget(lease, service, []corev1.Pod{pod})
	if err != nil || name != "control" || port != 8443 {
		t.Fatalf("unexpected target: %s:%d %v", name, port, err)
	}
	pod.Labels["vcluster.loft.sh/managed-by"] = "alice"
	if _, _, err := controlPlaneTarget(lease, service, []corev1.Pod{pod}); err != ErrTransport {
		t.Fatal("tenant pod allowed as API target")
	}
}

func TestPrivateFileDelivery(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	sink, err := NewFileSink(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileSink(directory); err == nil {
		t.Fatal("existing output accepted")
	}
	for _, token := range []string{"first", "rotated"} {
		if err := sink.Write([]byte(token)); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "kubeconfig")
		data, err := os.ReadFile(path)
		if err != nil || string(data) != token {
			t.Fatal("credential not replaced")
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatal("credential is not private")
		}
		info, _ = os.Stat(directory)
		if info.Mode().Perm() != 0700 {
			t.Fatal("directory is not private")
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("output remains")
	}
	if err := os.Symlink(t.TempDir(), directory); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileSink(directory); err == nil {
		t.Fatal("symlink output accepted")
	}
}

func TestTokenFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if token, err := ReadTokenFile(path); err != nil || token != "secret" {
		t.Fatal("private token unreadable")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(path); err != ErrDenied {
		t.Fatal("public token file allowed")
	}
}

type fakeBackend struct {
	check func() (Lease, error)
	open  func(context.Context, Lease) (Connection, error)
}

func (b fakeBackend) Check(context.Context) (Lease, error)                { return b.check() }
func (b fakeBackend) Open(c context.Context, l Lease) (Connection, error) { return b.open(c, l) }

type fakeConnection struct {
	issue  func(context.Context) (Credential, error)
	done   chan struct{}
	closed bool
}

func (c *fakeConnection) Issue(ctx context.Context) (Credential, error) { return c.issue(ctx) }
func (c *fakeConnection) Done() <-chan struct{}                         { return c.done }
func (c *fakeConnection) Close()                                        { c.closed = true }

type fakeSink struct {
	write  func([]byte) error
	closed bool
}

func (s *fakeSink) Write(b []byte) error {
	if s.write != nil {
		return s.write(b)
	}
	return nil
}
func (s *fakeSink) Close() error { s.closed = true; return nil }

func TestSessionRejectsReplacementBeforeDelivery(t *testing.T) {
	lease, _ := authorize(readyObject(), "alice", time.Now())
	calls := 0
	issued, writes := 0, 0
	connection := &fakeConnection{done: make(chan struct{}), issue: func(context.Context) (Credential, error) {
		issued++
		return Credential{Kubeconfig: []byte("virtual"), ExpiresAt: time.Now().Add(10 * time.Minute)}, nil
	}}
	backend := fakeBackend{check: func() (Lease, error) {
		calls++
		l := lease
		if calls >= 3 {
			l.UID = "replacement"
		}
		return l, nil
	}, open: func(context.Context, Lease) (Connection, error) { return connection, nil }}
	sink := &fakeSink{write: func([]byte) error { writes++; return nil }}
	err := (Session{Backend: backend, Sink: sink}).Run(context.Background())
	if err != ErrDenied || issued != 1 || writes != 0 || !connection.closed || !sink.closed {
		t.Fatalf("replacement leaked access: %v", err)
	}
}

func TestSessionClosesOnFailure(t *testing.T) {
	for _, reason := range []string{"cancel", "expiry", "authorization", "transport", "delivery", "issuance", "long token"} {
		t.Run(reason, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			lease, _ := authorize(readyObject(), "alice", time.Now())
			if reason == "expiry" {
				lease.ExpiresAt = time.Now().Add(30 * time.Millisecond)
			}
			ready := false
			connection := &fakeConnection{done: make(chan struct{})}
			connection.issue = func(context.Context) (Credential, error) {
				if reason == "issuance" {
					return Credential{}, errors.New("secret upstream body")
				}
				expires := time.Now().Add(10 * time.Minute)
				if reason == "long token" {
					expires = expires.Add(time.Hour)
				}
				return Credential{Kubeconfig: []byte("virtual"), ExpiresAt: expires}, nil
			}
			backend := fakeBackend{check: func() (Lease, error) {
				if reason == "authorization" && ready {
					return Lease{}, errors.New("secret")
				}
				return lease, nil
			}, open: func(context.Context, Lease) (Connection, error) { return connection, nil }}
			sink := &fakeSink{write: func([]byte) error {
				if reason == "delivery" {
					return errors.New("secret")
				}
				return nil
			}}
			session := Session{Backend: backend, Sink: sink, PollInterval: time.Millisecond, Ready: func() {
				ready = true
				if reason == "cancel" {
					cancel()
				}
				if reason == "transport" {
					close(connection.done)
				}
			}}
			err := session.Run(ctx)
			if err == nil || strings.Contains(err.Error(), "secret") || !connection.closed || !sink.closed {
				t.Fatalf("unsafe failure: %v", err)
			}
		})
	}
}

func TestSessionRotatesVirtualCredentials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lease, _ := authorize(readyObject(), "alice", time.Now())
	now := time.Now()
	writes := 0
	issues := 0
	connection := &fakeConnection{done: make(chan struct{}), issue: func(context.Context) (Credential, error) {
		issues++
		return Credential{Kubeconfig: []byte(fmt.Sprint(issues)), ExpiresAt: now.Add(10 * time.Minute)}, nil
	}}
	backend := fakeBackend{check: func() (Lease, error) { return lease, nil }, open: func(context.Context, Lease) (Connection, error) { return connection, nil }}
	sink := &fakeSink{write: func(data []byte) error {
		writes++
		if string(data) != fmt.Sprint(writes) {
			t.Error("old credential reused")
		}
		if writes == 2 {
			cancel()
		}
		return nil
	}}
	session := Session{Backend: backend, Sink: sink, PollInterval: time.Millisecond, now: func() time.Time { return now }, Ready: func() { now = now.Add(6 * time.Minute) }}
	if err := session.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if writes != 2 || issues != 2 || !connection.closed || !sink.closed {
		t.Fatal("rotation or cleanup failed")
	}
}
