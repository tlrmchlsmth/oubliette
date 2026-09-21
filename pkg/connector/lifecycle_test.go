package connector

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTrustedLifecycleUsesAuthenticatedOwnership(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = oubv1.AddToScheme(scheme)
	host := fake.NewClientBuilder().WithScheme(scheme).Build()
	caller := "alice"
	authCalls := 0
	b := &KubernetesBackend{Host: host, Token: func() (string, error) { return "private-lifecycle-token", nil }, Resolver: resolverFunc(func(_ context.Context, token string) (string, error) {
		authCalls++
		if token != "private-lifecycle-token" {
			t.Fatal("wrong token")
		}
		return caller, nil
	})}
	call := func(tool, args string) (json.RawMessage, error) {
		return b.Lifecycle(context.Background(), tool, []byte(args))
	}
	out, err := call("oubliette_create", `{"name":"mine","ttlSeconds":600}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "token") || strings.Contains(string(out), "kubeconfig") || strings.Contains(string(out), "digest") {
		t.Fatal("credentials or ownership internals exposed")
	}
	caller = "bob"
	for _, tool := range []string{"oubliette_get", "oubliette_delete", "oubliette_renew"} {
		args := `{"name":"mine"}`
		if tool == "oubliette_renew" {
			args = `{"name":"mine","ttlSeconds":900}`
		}
		if _, err = call(tool, args); err == nil {
			t.Fatal("foreign access allowed", tool)
		}
	}
	out, err = call("oubliette_list", `{}`)
	if err != nil || strings.Contains(string(out), "mine") {
		t.Fatal("foreign list", string(out), err)
	}
	caller = "alice"
	if _, err = call("oubliette_renew", `{"name":"mine","ttlSeconds":900}`); err != nil {
		t.Fatal(err)
	}
	if _, err = call("oubliette_delete", `{"name":"mine"}`); err != nil {
		t.Fatal(err)
	}
	if authCalls != 7 {
		t.Fatal("each operation must authenticate", authCalls)
	}
	for _, args := range []string{`{"name":"mine","ttlSeconds":600,"caller":"bob"}`, `{"name":"mine","ttlSeconds":600,"hostContext":"other"}`, `null`, `{} {}`} {
		if _, err = call("oubliette_create", args); err == nil {
			t.Fatal("untyped input accepted", args)
		}
	}
	if _, err = call("kubectl", `{}`); err == nil {
		t.Fatal("arbitrary operation accepted")
	}
	caller = ""
	if _, err = call("oubliette_list", `{}`); err == nil {
		t.Fatal("anonymous accepted")
	}
}
