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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const ChartVersion = "0.36.1"

type Manager interface {
	Ensure(context.Context, string, string) error
	Delete(context.Context, string, string) error
	Ready(context.Context, string, string) (bool, error)
}

type HelmManager struct {
	ChartPath string
	Settings  *cli.EnvSettings
	Client    client.Client
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
	desired := values(name, namespace)
	release, err := action.NewStatus(cfg).Run(name)
	if err == nil {
		if release.Chart != nil && release.Chart.Metadata != nil && release.Chart.Metadata.Version == ChartVersion && reflect.DeepEqual(release.Config, desired) {
			return nil
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
	var secret corev1.Secret
	if err := m.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "vc-" + name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	var deployment appsv1.Deployment
	if err := m.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &deployment); err == nil {
		return deployment.Status.ReadyReplicas == 1 && len(secret.Data["config"]) > 0, nil
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
	return sts.Status.ReadyReplicas == 1 && len(secret.Data["config"]) > 0, nil
}

func values(name, namespace string) map[string]any {
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
		"controlPlane": map[string]any{
			"backingStore": map[string]any{"database": map[string]any{"embedded": map[string]any{"enabled": true}}},
			"service":      map[string]any{"spec": map[string]any{"type": "ClusterIP"}},
			"statefulSet": map[string]any{
				"image": map[string]any{"repository": "loft-sh/vcluster-oss"},
				"resources": map[string]any{
					"requests": map[string]any{"cpu": "100m", "memory": "256Mi", "ephemeral-storage": "256Mi"},
					"limits":   map[string]any{"cpu": "2", "memory": "2Gi", "ephemeral-storage": "4Gi"},
				},
				"security": map[string]any{
					"podSecurityContext":       map[string]any{"runAsNonRoot": true, "runAsUser": int64(1000), "runAsGroup": int64(1000), "fsGroup": int64(1000)},
					"containerSecurityContext": map[string]any{"allowPrivilegeEscalation": false, "runAsNonRoot": true, "runAsUser": int64(1000), "runAsGroup": int64(1000), "capabilities": map[string]any{"drop": []any{"ALL"}}},
				},
				"persistence": map[string]any{
					"volumeClaim": map[string]any{"enabled": false},
					"dataVolume":  []any{map[string]any{"name": "data", "emptyDir": map[string]any{}}},
				},
			},
		},
		"policies": map[string]any{
			"resourceQuota": map[string]any{"enabled": false},
			"limitRange":    map[string]any{"enabled": false},
			"networkPolicy": map[string]any{"enabled": false},
		},
		"exportKubeConfig": map[string]any{
			"context": name,
			"server":  fmt.Sprintf("https://%s.%s:443", name, namespace),
		},
	}
}
