package vcluster

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/storage/driver"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	ChartVersion              = "0.36.1"
	HandoffNamespace          = "default"
	HandoffServiceAccount     = "oubliette-agent"
	HandoffClusterRoleBinding = "oubliette-agent-cluster-admin"
)

type Manager interface {
	Ensure(context.Context, string, string) error
	Delete(context.Context, string, string) error
	Ready(context.Context, string, string) (bool, error)
}

type HelmManager struct {
	ChartPath            string
	Settings             *cli.EnvSettings
	Client               client.Client
	VirtualClientFactory func([]byte) (client.Client, error)
	Values               ValuesOptions
}

type RuntimeIdentity struct {
	User    int64
	Group   int64
	FSGroup int64
}

type ResourceBounds struct {
	Request string
	Limit   string
}

type ValuesOptions struct {
	HostWorkloadQueue string
	RuntimeIdentity   *RuntimeIdentity
	EphemeralStorage  *ResourceBounds
}

func DefaultValuesOptions() ValuesOptions {
	return ValuesOptions{
		RuntimeIdentity:  &RuntimeIdentity{User: 1000, Group: 1000, FSGroup: 1000},
		EphemeralStorage: &ResourceBounds{Request: "256Mi", Limit: "4Gi"},
	}
}

func (o ValuesOptions) Validate() error {
	if o.HostWorkloadQueue != "" {
		if problems := kvalidation.IsDNS1123Subdomain(o.HostWorkloadQueue); len(problems) > 0 {
			return fmt.Errorf("host workload queue must be a DNS-1123 subdomain: %s", problems[0])
		}
		if problems := kvalidation.IsValidLabelValue(o.HostWorkloadQueue); len(problems) > 0 {
			return fmt.Errorf("host workload queue must be a valid label value: %s", problems[0])
		}
	}
	if o.RuntimeIdentity != nil {
		identity := o.RuntimeIdentity
		allOmitted := identity.User == 0 && identity.Group == 0 && identity.FSGroup == 0
		allPositive := identity.User > 0 && identity.Group > 0 && identity.FSGroup > 0
		if !allOmitted && !allPositive {
			return fmt.Errorf("runtime user, group, and fsGroup must either all be positive or all be zero")
		}
	}
	if o.EphemeralStorage != nil {
		bounds := o.EphemeralStorage
		if (bounds.Request == "") != (bounds.Limit == "") {
			return fmt.Errorf("ephemeral-storage request and limit must both be set or both be empty")
		}
		if bounds.Request != "" {
			request, err := resource.ParseQuantity(bounds.Request)
			if err != nil || request.Sign() <= 0 {
				return fmt.Errorf("ephemeral-storage request must be a positive quantity")
			}
			limit, err := resource.ParseQuantity(bounds.Limit)
			if err != nil || limit.Sign() <= 0 {
				return fmt.Errorf("ephemeral-storage limit must be a positive quantity")
			}
			if limit.Cmp(request) < 0 {
				return fmt.Errorf("ephemeral-storage limit must be greater than or equal to request")
			}
		}
	}
	return nil
}

func (o ValuesOptions) withDefaults() ValuesOptions {
	defaults := DefaultValuesOptions()
	if o.RuntimeIdentity == nil {
		o.RuntimeIdentity = defaults.RuntimeIdentity
	}
	if o.EphemeralStorage == nil {
		o.EphemeralStorage = defaults.EphemeralStorage
	}
	return o
}

func (m *HelmManager) configuration(namespace string) (*action.Configuration, error) {
	settings := m.Settings
	if settings == nil {
		settings = cli.New()
	}
	cfg := new(action.Configuration)
	if err := cfg.Init(settings.RESTClientGetter(), namespace, "secret", func(format string, args ...any) {}); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (m *HelmManager) Ensure(ctx context.Context, name, namespace string) error {
	cfg, err := m.configuration(namespace)
	if err != nil {
		return fmt.Errorf("initialize helm: %w", err)
	}
	desired, err := values(name, namespace, m.Values)
	if err != nil {
		return fmt.Errorf("configure vcluster: %w", err)
	}
	release, err := action.NewStatus(cfg).Run(name)
	if err == nil {
		if release.Chart != nil && release.Chart.Metadata != nil && release.Chart.Metadata.Version == ChartVersion && reflect.DeepEqual(release.Config, desired) {
			return m.ensureHandoff(ctx, name, namespace)
		}
		chart, loadErr := loader.Load(m.ChartPath)
		if loadErr != nil {
			return fmt.Errorf("load vcluster chart: %w", loadErr)
		}
		upgrade := action.NewUpgrade(cfg)
		upgrade.Namespace = namespace
		upgrade.Wait = false
		upgrade.Timeout = 5 * time.Minute
		if _, upgradeErr := upgrade.RunWithContext(ctx, name, chart, desired); upgradeErr != nil {
			return fmt.Errorf("upgrade vcluster: %w", upgradeErr)
		}
		return nil
	}
	if !errors.Is(err, driver.ErrReleaseNotFound) {
		return fmt.Errorf("get helm release: %w", err)
	}

	chart, err := loader.Load(m.ChartPath)
	if err != nil {
		return fmt.Errorf("load vcluster chart: %w", err)
	}
	install := action.NewInstall(cfg)
	install.ReleaseName = name
	install.Namespace = namespace
	install.CreateNamespace = false
	install.Wait = false
	install.Timeout = 5 * time.Minute
	if _, err := install.RunWithContext(ctx, chart, desired); err != nil {
		return fmt.Errorf("install vcluster: %w", err)
	}
	return nil
}

func (m *HelmManager) Delete(ctx context.Context, name, namespace string) error {
	cfg, err := m.configuration(namespace)
	if err != nil {
		return fmt.Errorf("initialize helm: %w", err)
	}
	uninstall := action.NewUninstall(cfg)
	uninstall.KeepHistory = false
	uninstall.Wait = false
	_, err = uninstall.Run(name)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("uninstall vcluster: %w", err)
	}
	return nil
}

func (m *HelmManager) Ready(ctx context.Context, name, namespace string) (bool, error) {
	config, found, err := m.bootstrapConfig(ctx, name, namespace)
	if err != nil || !found {
		return false, err
	}
	controlPlaneReady, err := m.controlPlaneReady(ctx, name, namespace)
	if err != nil || !controlPlaneReady {
		return false, err
	}
	virtual, err := m.virtualClient(config)
	if err != nil {
		return false, err
	}
	return handoffReady(ctx, virtual)
}

func (m *HelmManager) ensureHandoff(ctx context.Context, name, namespace string) error {
	config, found, err := m.bootstrapConfig(ctx, name, namespace)
	if err != nil || !found {
		return err
	}
	virtual, err := m.virtualClient(config)
	if err != nil {
		return err
	}
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: HandoffServiceAccount, Namespace: HandoffNamespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, virtual, serviceAccount, func() error { return nil }); err != nil {
		return fmt.Errorf("ensure virtual handoff ServiceAccount: %w", err)
	}
	return ensureHandoffBinding(ctx, virtual)
}

func (m *HelmManager) bootstrapConfig(ctx context.Context, name, namespace string) ([]byte, bool, error) {
	var secret corev1.Secret
	if err := m.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "vc-" + name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	config := secret.Data["config"]
	if len(config) == 0 {
		return nil, false, nil
	}
	return config, true, nil
}

func (m *HelmManager) controlPlaneReady(ctx context.Context, name, namespace string) (bool, error) {
	var deployment appsv1.Deployment
	if err := m.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &deployment); err == nil {
		return deployment.Status.ReadyReplicas == 1, nil
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	var sts appsv1.StatefulSet
	if err := m.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &sts); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return sts.Status.ReadyReplicas == 1, nil
}

func (m *HelmManager) virtualClient(kubeconfig []byte) (client.Client, error) {
	factory := m.VirtualClientFactory
	if factory == nil {
		factory = newVirtualClient
	}
	virtual, err := factory(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("build virtual API client: %w", err)
	}
	return virtual, nil
}

func newVirtualClient(kubeconfig []byte) (client.Client, error) {
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	config.Timeout = 10 * time.Second
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return client.New(config, client.Options{Scheme: scheme})
}

func ensureHandoffBinding(ctx context.Context, virtual client.Client) error {
	desired := desiredHandoffBinding()
	current := &rbacv1.ClusterRoleBinding{}
	if err := virtual.Get(ctx, types.NamespacedName{Name: desired.Name}, current); err != nil {
		if apierrors.IsNotFound(err) {
			if err := virtual.Create(ctx, desired); err != nil {
				return fmt.Errorf("create virtual handoff ClusterRoleBinding: %w", err)
			}
			return nil
		}
		return fmt.Errorf("get virtual handoff ClusterRoleBinding: %w", err)
	}
	if current.RoleRef != desired.RoleRef {
		if err := virtual.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("replace virtual handoff ClusterRoleBinding: %w", err)
		}
		return nil
	}
	if !reflect.DeepEqual(current.Subjects, desired.Subjects) {
		current.Subjects = desired.Subjects
		if err := virtual.Update(ctx, current); err != nil {
			return fmt.Errorf("repair virtual handoff ClusterRoleBinding: %w", err)
		}
	}
	return nil
}

func handoffReady(ctx context.Context, virtual client.Client) (bool, error) {
	serviceAccount := &corev1.ServiceAccount{}
	if err := virtual.Get(ctx, types.NamespacedName{Name: HandoffServiceAccount, Namespace: HandoffNamespace}, serviceAccount); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	binding := &rbacv1.ClusterRoleBinding{}
	if err := virtual.Get(ctx, types.NamespacedName{Name: HandoffClusterRoleBinding}, binding); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	desired := desiredHandoffBinding()
	return binding.RoleRef == desired.RoleRef && reflect.DeepEqual(binding.Subjects, desired.Subjects), nil
}

func desiredHandoffBinding() *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: HandoffClusterRoleBinding},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: HandoffServiceAccount, Namespace: HandoffNamespace}},
	}
}

func values(name, namespace string, options ValuesOptions) (map[string]any, error) {
	options = options.withDefaults()
	if err := options.Validate(); err != nil {
		return nil, err
	}

	requests := map[string]any{"cpu": "100m", "memory": "256Mi"}
	limits := map[string]any{"cpu": "2", "memory": "2Gi"}
	if options.EphemeralStorage.Request == "" {
		requests["ephemeral-storage"] = nil
		limits["ephemeral-storage"] = nil
	} else {
		requests["ephemeral-storage"] = options.EphemeralStorage.Request
		limits["ephemeral-storage"] = options.EphemeralStorage.Limit
	}
	podSecurity := map[string]any{"runAsNonRoot": true, "seccompProfile": map[string]any{"type": "RuntimeDefault"}}
	containerSecurity := map[string]any{
		"allowPrivilegeEscalation": false,
		"runAsNonRoot":             true,
		"capabilities":             map[string]any{"drop": []any{"ALL"}},
	}
	if options.RuntimeIdentity.User > 0 {
		podSecurity["runAsUser"] = options.RuntimeIdentity.User
		podSecurity["runAsGroup"] = options.RuntimeIdentity.Group
		podSecurity["fsGroup"] = options.RuntimeIdentity.FSGroup
		containerSecurity["runAsUser"] = options.RuntimeIdentity.User
		containerSecurity["runAsGroup"] = options.RuntimeIdentity.Group
	} else {
		// Explicit nulls override chart-owned numeric defaults during Helm value
		// coalescing. Omitting these keys would restore runAsUser/runAsGroup 0.
		podSecurity["runAsUser"] = nil
		podSecurity["runAsGroup"] = nil
		podSecurity["fsGroup"] = nil
		containerSecurity["runAsUser"] = nil
		containerSecurity["runAsGroup"] = nil
	}
	statefulSet := map[string]any{
		"image": map[string]any{"repository": "loft-sh/vcluster-oss"},
		"resources": map[string]any{
			"requests": requests,
			"limits":   limits,
		},
		"security": map[string]any{
			"podSecurityContext":       podSecurity,
			"containerSecurityContext": containerSecurity,
		},
		"persistence": map[string]any{
			"volumeClaim": map[string]any{"enabled": false},
			"dataVolume":  []any{map[string]any{"name": "data", "emptyDir": map[string]any{}}},
		},
	}
	controlPlane := map[string]any{
		"backingStore": map[string]any{"database": map[string]any{"embedded": map[string]any{"enabled": true}}},
		"service":      map[string]any{"spec": map[string]any{"type": "ClusterIP"}},
		"statefulSet":  statefulSet,
	}
	if options.HostWorkloadQueue != "" {
		queueLabels := map[string]any{"kueue.x-k8s.io/queue-name": options.HostWorkloadQueue}
		statefulSet["pods"] = map[string]any{"labels": queueLabels}
		controlPlane["coredns"] = map[string]any{
			"deployment": map[string]any{"pods": map[string]any{"labels": queueLabels}},
		}
	}

	return map[string]any{
		"sync": map[string]any{
			"toHost": map[string]any{
				"pods":                   map[string]any{"enabled": true},
				"services":               map[string]any{"enabled": true},
				"endpoints":              map[string]any{"enabled": true},
				"endpointSlices":         map[string]any{"enabled": true},
				"configMaps":             map[string]any{"enabled": true, "all": false},
				"secrets":                map[string]any{"enabled": true, "all": false},
				"persistentVolumeClaims": map[string]any{"enabled": false},
				"networkPolicies":        map[string]any{"enabled": false},
				"ingresses":              map[string]any{"enabled": false},
			},
		},
		"controlPlane": controlPlane,
		"policies": map[string]any{
			"resourceQuota": map[string]any{"enabled": false},
			"limitRange":    map[string]any{"enabled": false},
			"networkPolicy": map[string]any{"enabled": false},
		},
		"exportKubeConfig": map[string]any{
			"context": name,
			"server":  fmt.Sprintf("https://%s.%s:443", name, namespace),
		},
	}, nil
}
