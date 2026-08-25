package vcluster

import "testing"

func TestValuesPinNumericNonRootIdentity(t *testing.T) {
	controlPlane := values("test", "oub-test")["controlPlane"].(map[string]any)
	statefulSet := controlPlane["statefulSet"].(map[string]any)
	security := statefulSet["security"].(map[string]any)
	for _, contextName := range []string{"podSecurityContext", "containerSecurityContext"} {
		context := security[contextName].(map[string]any)
		if context["runAsNonRoot"] != true || context["runAsUser"] != int64(1000) || context["runAsGroup"] != int64(1000) {
			t.Fatalf("%s does not pin the vCluster image's numeric non-root identity: %#v", contextName, context)
		}
	}
}
