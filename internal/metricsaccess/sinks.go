package metricsaccess

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type KubernetesSecretSink struct {
	Client    client.Client
	Namespace string
	Name      string
}

// DeliverMetricsAccess writes a short-lived resident credential to a named,
// consumer-controlled Secret. The consumer projects only this Secret into the
// agent workload; lifecycle MCP never reads or returns it.
func (s KubernetesSecretSink) DeliverMetricsAccess(ctx context.Context, endpointIdentity string, credential []byte, expiresAt time.Time) error {
	if s.Client == nil || s.Namespace == "" || s.Name == "" || endpointIdentity == "" || len(credential) == 0 {
		return errors.New("resident metrics credential sink is incomplete")
	}
	key := client.ObjectKey{Namespace: s.Namespace, Name: s.Name}
	var secret corev1.Secret
	err := s.Client.Get(ctx, key, &secret)
	if apierrors.IsNotFound(err) {
		secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace}}
	} else if err != nil {
		return err
	}
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations["oubliette.tlrmchlsmth.github.io/metrics-endpoint"] = endpointIdentity
	secret.Annotations["oubliette.tlrmchlsmth.github.io/expires-at"] = expiresAt.UTC().Format(time.RFC3339)
	secret.Type = corev1.SecretTypeOpaque
	secret.Data = map[string][]byte{"credential": append([]byte(nil), credential...)}
	if secret.ResourceVersion == "" {
		return s.Client.Create(ctx, &secret)
	}
	return s.Client.Update(ctx, &secret)
}

type ConnectorSink func(context.Context, string, []byte, time.Time) error

func (s ConnectorSink) DeliverMetricsAccess(ctx context.Context, endpointIdentity string, credential []byte, expiresAt time.Time) error {
	if s == nil {
		return errors.New("external metrics connector is unavailable")
	}
	return s(ctx, endpointIdentity, credential, expiresAt)
}
