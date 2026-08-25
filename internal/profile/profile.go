package profile

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const StubGeneration = "stub-v1"

type Profile struct {
	Generation string
	Quota      corev1.ResourceList
	Metrics    MetricsProfile
}

type MetricsProfile struct {
	Enabled              bool
	Generation           string
	EndpointPrefix       string
	IsolationScope       string
	TrustDomain          string
	AllowedMetrics       []string
	TrustedLabels        []string
	SensitiveLabels      []string
	ReuseGPUTelemetry    bool
	MaxLookbackSeconds   int64
	MinStepSeconds       int64
	MaxExecutionSeconds  int64
	MaxSamples           int
	MaxResponseBytes     int64
	MaxConcurrency       int
	MaxRequestsPerMinute int
	Retention            EvidenceRetentionPolicy
}

type EvidenceRetentionPolicy struct {
	Generation            string
	QueryableMetricsDays  int
	RawEvidenceDays       int
	ProvenanceSummaryDays int
}

func Resolve(tier string) (Profile, error) {
	if tier != "stub" {
		return Profile{}, fmt.Errorf("unsupported tier %q", tier)
	}
	return Profile{
		Generation: StubGeneration,
		Metrics:    MetricsProfile{Enabled: false},
		Quota: corev1.ResourceList{
			corev1.ResourcePods:                     resource.MustParse("8"),
			corev1.ResourceRequestsCPU:              resource.MustParse("4"),
			corev1.ResourceRequestsMemory:           resource.MustParse("8Gi"),
			corev1.ResourceLimitsCPU:                resource.MustParse("8"),
			corev1.ResourceLimitsMemory:             resource.MustParse("16Gi"),
			corev1.ResourceRequestsEphemeralStorage: resource.MustParse("10Gi"),
			corev1.ResourceLimitsEphemeralStorage:   resource.MustParse("20Gi"),
			corev1.ResourceConfigMaps:               resource.MustParse("40"),
			corev1.ResourceSecrets:                  resource.MustParse("80"),
			corev1.ResourceServices:                 resource.MustParse("20"),
			corev1.ResourceServicesNodePorts:        resource.MustParse("0"),
			corev1.ResourceServicesLoadBalancers:    resource.MustParse("0"),
			corev1.ResourcePersistentVolumeClaims:   resource.MustParse("0"),
		},
	}, nil
}
