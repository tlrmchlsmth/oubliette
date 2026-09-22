package connector

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	"github.com/tlrmchlsmth/oubliette/internal/mcpauth"
	"github.com/tlrmchlsmth/oubliette/internal/vcluster"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/httpstream"
	streamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubernetesBackend belongs to a trusted consumer process. Its host credential
// needs TokenReview, Oubliette reads and scoped Secret/Service/Pod/portforward
// access. It is never delivered to the sandbox or used to execute agent commands.
type KubernetesBackend struct {
	Host     client.Client
	Config   *rest.Config
	Resolver mcpauth.Resolver
	Token    func() (string, error)
	Name     string
}

func (b *KubernetesBackend) Check(ctx context.Context) (Lease, error) {
	if b.Host == nil || b.Resolver == nil || b.Token == nil {
		return Lease{}, ErrDenied
	}
	if len(b.Name) > 59 || len(validation.IsDNS1123Label(b.Name)) != 0 {
		return Lease{}, ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	token, err := b.Token()
	if err != nil {
		return Lease{}, ErrDenied
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return Lease{}, ErrDenied
	}
	caller, err := b.Resolver.Resolve(ctx, token)
	if err != nil || caller == "" {
		return Lease{}, ErrDenied
	}
	var obj oubv1.Oubliette
	if err := b.Host.Get(ctx, types.NamespacedName{Name: b.Name}, &obj); err != nil {
		return Lease{}, ErrDenied
	}
	return authorize(&obj, caller, time.Now())
}

func authorize(obj *oubv1.Oubliette, caller string, now time.Time) (Lease, error) {
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(caller)))
	ready := apiMeta.FindStatusCondition(obj.Status.Conditions, oubv1.ConditionReady)
	namespace := "oub-" + obj.Name
	if caller == "" || obj.UID == "" || obj.Annotations[oubv1.CallerDigestAnnotation] != digest ||
		!obj.DeletionTimestamp.IsZero() || !now.Before(obj.Spec.ExpiresAt.Time) ||
		ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != obj.Generation ||
		obj.Status.ObservedGeneration != obj.Generation || obj.Status.HostNamespace != namespace ||
		apiMeta.IsStatusConditionTrue(obj.Status.Conditions, oubv1.ConditionExpiring) ||
		apiMeta.IsStatusConditionTrue(obj.Status.Conditions, oubv1.ConditionForgotten) {
		return Lease{}, ErrDenied
	}
	return Lease{Name: obj.Name, Namespace: namespace, Caller: caller, UID: obj.UID, ExpiresAt: obj.Spec.ExpiresAt.Time}, nil
}

func (b *KubernetesBackend) Open(ctx context.Context, lease Lease) (Connection, error) {
	// Independently re-check the pinned resource immediately before host access.
	current, err := b.Check(ctx)
	if err != nil || current != lease || b.Config == nil {
		return nil, ErrDenied
	}
	ctx, cancel := context.WithCancel(ctx)
	connection := &kubernetesConnection{cancel: cancel, done: make(chan struct{})}
	ok := false
	started := false
	defer func() {
		if !ok {
			cancel()
			if started {
				<-connection.done
			}
		}
	}()

	readCtx, readCancel := context.WithTimeout(ctx, 10*time.Second)
	defer readCancel()
	var secret corev1.Secret
	if err := b.Host.Get(readCtx, types.NamespacedName{Namespace: lease.Namespace, Name: "vc-" + lease.Name}, &secret); err != nil {
		return nil, ErrCredential
	}
	// Parse only embedded credentials. Ignore the bootstrap server, proxy and
	// contexts other than the selected one; never execute a credential plugin.
	bootstrap, err := bootstrapConfig(secret.Data["config"], lease.Name+"."+lease.Namespace)
	if err != nil {
		return nil, ErrCredential
	}
	var service corev1.Service
	if err := b.Host.Get(readCtx, types.NamespacedName{Namespace: lease.Namespace, Name: lease.Name}, &service); err != nil {
		return nil, ErrTransport
	}
	if service.Spec.Selector["app"] != "vcluster" || service.Spec.Selector["release"] != lease.Name {
		return nil, ErrTransport
	}
	var pods corev1.PodList
	if err := b.Host.List(readCtx, &pods, client.InNamespace(lease.Namespace), client.MatchingLabelsSelector{Selector: labels.SelectorFromSet(service.Spec.Selector)}); err != nil {
		return nil, ErrTransport
	}
	pod, port, err := controlPlaneTarget(lease, &service, pods.Items)
	if err != nil {
		return nil, err
	}
	url, err := portForwardURL(b.Config, lease.Namespace, pod)
	if err != nil {
		return nil, ErrTransport
	}
	// The tunnel lives for the session, not the ordinary host request timeout.
	forwardConfig := rest.CopyConfig(b.Config)
	forwardConfig.Timeout = 0
	transport, upgrader, err := forwardTransport(ctx, forwardConfig)
	if err != nil {
		return nil, ErrTransport
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url.String(), nil)
	if err != nil {
		return nil, ErrTransport
	}
	dialer := contextDialer{upgrader: upgrader, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, request: request}
	ready := make(chan struct{})
	forwarder, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{"0:" + strconv.Itoa(port)}, ctx.Done(), ready, io.Discard, io.Discard)
	if err != nil {
		return nil, ErrTransport
	}
	go func() { defer close(connection.done); _ = forwarder.ForwardPorts() }()
	started = true
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	select {
	case <-ready:
	case <-connection.done:
		return nil, ErrTransport
	case <-ctx.Done():
		return nil, ErrTransport
	case <-timer.C:
		return nil, ErrTransport
	}
	ports, err := forwarder.GetPorts()
	if err != nil || len(ports) != 1 {
		return nil, ErrTransport
	}
	bootstrap.Host = fmt.Sprintf("https://127.0.0.1:%d", ports[0].Local)
	connection.config = bootstrap
	ok = true
	return connection, nil
}

func portForwardURL(config *rest.Config, namespace, pod string) (*url.URL, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	// REST requests inherit the HTTP client's timeout as a URL query parameter.
	// Ordinary host reads stay bounded, but this tunnel lives for the lease.
	return clientset.CoreV1().RESTClient().Post().Namespace(namespace).Resource("pods").Name(pod).SubResource("portforward").Timeout(0).URL(), nil
}

// SPDY reads the HTTP upgrade response directly from its socket. A request
// context alone does not interrupt that read. Tie the underlying socket to the
// session too, covering stalled upgrade responses and established streams.
// This connector deliberately requires a direct private route to the host API.
func forwardTransport(ctx context.Context, config *rest.Config) (http.RoundTripper, spdy.Upgrader, error) {
	if config.Proxy != nil || config.Insecure || !strings.HasPrefix(config.Host, "https://") {
		return nil, nil, ErrTransport
	}
	tlsConfig, err := rest.TLSConfigFor(config)
	if err != nil {
		return nil, nil, ErrTransport
	}
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	base := &http.Transport{TLSClientConfig: tlsConfig, DialContext: func(dialCtx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 20 * time.Second}).DialContext(dialCtx, network, address)
		if err != nil {
			return nil, err
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		return &sessionConn{Conn: conn, stop: stop}, nil
	}}
	upgrader, err := streamspdy.NewRoundTripperWithConfig(streamspdy.RoundTripperConfig{UpgradeTransport: base, PingPeriod: 5 * time.Second})
	if err != nil {
		return nil, nil, ErrTransport
	}
	wrapped, err := rest.HTTPWrappersForConfig(config, upgrader)
	return wrapped, upgrader, err
}

type sessionConn struct {
	net.Conn
	stop func() bool
}

func (c *sessionConn) Close() error { c.stop(); return c.Conn.Close() }

// client-go's default SPDY dialer creates a request without a context. Carry the
// session context through the upgrade so a stalled handshake cannot outlive it.
type contextDialer struct {
	upgrader spdy.Upgrader
	client   *http.Client
	request  *http.Request
}

func (d contextDialer) Dial(protocols ...string) (httpstream.Connection, string, error) {
	connection, protocol, err := spdy.Negotiate(d.upgrader, d.client, d.request, protocols...)
	if err != nil {
		return nil, protocol, err
	}
	return ownStreamConnection(d.request.Context(), connection), protocol, nil
}

// client-go v0.35 closes its shared SPDY connection after a per-request error
// (including a broken pipe when kubectl exec finishes). The session owns this
// connection instead: local stream errors remain local, while cancellation and
// genuine peer/transport closure still end the tunnel through CloseChan.
func ownStreamConnection(ctx context.Context, connection httpstream.Connection) httpstream.Connection {
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-connection.CloseChan():
		}
	}()
	return sessionStreamConnection{Connection: connection}
}

type sessionStreamConnection struct{ httpstream.Connection }

func (sessionStreamConnection) Close() error { return nil }

func controlPlaneTarget(lease Lease, service *corev1.Service, pods []corev1.Pod) (string, int, error) {
	for _, pod := range pods {
		if pod.Labels["app"] != "vcluster" || pod.Labels["release"] != lease.Name ||
			pod.Labels["vcluster.loft.sh/managed-by"] != "" || pod.Namespace != lease.Namespace ||
			!pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		ready := false
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready {
			continue
		}
		for _, p := range service.Spec.Ports {
			if p.Port != 443 || (p.Protocol != "" && p.Protocol != corev1.ProtocolTCP) {
				continue
			}
			port := int(p.TargetPort.IntVal)
			if p.TargetPort.Type == intstr.String {
				port = 0
				for _, c := range pod.Spec.Containers {
					for _, cp := range c.Ports {
						if cp.Name == p.TargetPort.StrVal {
							port = int(cp.ContainerPort)
						}
					}
				}
			} else if port == 0 {
				port = 443
			}
			if port > 0 && port <= 65535 {
				return pod.Name, port, nil
			}
		}
	}
	return "", 0, ErrTransport
}

func bootstrapConfig(data []byte, tlsName string) (*rest.Config, error) {
	cfg, err := clientcmd.Load(data)
	if err != nil {
		return nil, ErrCredential
	}
	selected := cfg.Contexts[cfg.CurrentContext]
	if selected == nil {
		return nil, ErrCredential
	}
	cluster, auth := cfg.Clusters[selected.Cluster], cfg.AuthInfos[selected.AuthInfo]
	if cluster == nil || auth == nil || len(cluster.CertificateAuthorityData) == 0 ||
		cluster.InsecureSkipTLSVerify || cluster.CertificateAuthority != "" || cluster.ProxyURL != "" ||
		auth.Exec != nil || auth.AuthProvider != nil || auth.TokenFile != "" ||
		auth.ClientCertificate != "" || auth.ClientKey != "" || auth.Impersonate != "" ||
		auth.Username != "" || auth.Password != "" {
		return nil, ErrCredential
	}
	if auth.Token == "" && (len(auth.ClientCertificateData) == 0 || len(auth.ClientKeyData) == 0) {
		return nil, ErrCredential
	}
	return &rest.Config{
		Timeout:         10 * time.Second,
		BearerToken:     auth.Token,
		TLSClientConfig: rest.TLSClientConfig{ServerName: tlsName, CAData: cluster.CertificateAuthorityData, CertData: auth.ClientCertificateData, KeyData: auth.ClientKeyData},
	}, nil
}

type kubernetesConnection struct {
	config *rest.Config
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (c *kubernetesConnection) Done() <-chan struct{} { return c.done }
func (c *kubernetesConnection) Close() {
	c.once.Do(c.cancel)
	<-c.done
}

func (c *kubernetesConnection) Issue(ctx context.Context) (Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	kube, err := kubernetes.NewForConfig(c.config)
	if err != nil {
		return Credential{}, ErrCredential
	}
	seconds := int64(600)
	request, err := kube.CoreV1().ServiceAccounts(vcluster.HandoffNamespace).CreateToken(ctx, vcluster.HandoffServiceAccount,
		&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &seconds}}, metav1.CreateOptions{})
	if err != nil || request.Status.Token == "" {
		return Credential{}, ErrCredential
	}
	// Subsequent rotation uses only the virtual ServiceAccount token. Drop the
	// bootstrap auth after the initial handoff; do not retain an admin certificate
	// or long-lived bootstrap token throughout the consumer session.
	c.config = &rest.Config{Host: c.config.Host, Timeout: 10 * time.Second, BearerToken: request.Status.Token,
		TLSClientConfig: rest.TLSClientConfig{CAData: c.config.CAData, ServerName: c.config.ServerName}}
	// Construct from scratch: the host and bootstrap auth never reach the sink.
	cfg := clientcmdapi.NewConfig()
	cfg.CurrentContext = "oubliette"
	cfg.Clusters["oubliette"] = &clientcmdapi.Cluster{Server: c.config.Host, TLSServerName: c.config.ServerName, CertificateAuthorityData: c.config.CAData}
	cfg.AuthInfos["agent"] = &clientcmdapi.AuthInfo{Token: request.Status.Token}
	cfg.Contexts["oubliette"] = &clientcmdapi.Context{Cluster: "oubliette", AuthInfo: "agent", Namespace: "default"}
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return Credential{}, ErrCredential
	}
	return Credential{Kubeconfig: data, ExpiresAt: request.Status.ExpirationTimestamp.Time}, nil
}
