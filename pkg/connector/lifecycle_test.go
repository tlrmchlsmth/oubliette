package connector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPrivateTokenNormalizationAcrossConnectorOperations(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = oubv1.AddToScheme(scheme)
	host := fake.NewClientBuilder().WithScheme(scheme).WithObjects(readyObject()).Build()
	path := filepath.Join(t.TempDir(), "caller.token")
	calls := 0
	b := &KubernetesBackend{Host: host, Name: "alice", Token: func() (string, error) { return ReadTokenFile(path) },
		Resolver: resolverFunc(func(_ context.Context, token string) (string, error) {
			calls++
			if token != "private-lifecycle-token" {
				t.Fatal("resolver received an unnormalized or empty token")
			}
			return "alice", nil
		}),
	}
	for _, token := range []string{"private-lifecycle-token", "private-lifecycle-token\n", " \tprivate-lifecycle-token\r\n", "", " \t\r\n"} {
		if err := os.WriteFile(path, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
		before := calls
		_, checkErr := b.Check(context.Background())
		_, lifecycleErr := b.Lifecycle(context.Background(), "oubliette_list", []byte(`{}`))
		if strings.TrimSpace(token) == "" {
			if !errors.Is(checkErr, ErrDenied) || !errors.Is(lifecycleErr, ErrDenied) || calls != before {
				t.Fatal("empty token did not fail locally before authentication")
			}
		} else if checkErr != nil || lifecycleErr != nil || calls != before+2 {
			t.Fatalf("private token file rejected: check=%v lifecycle=%v", checkErr, lifecycleErr)
		}
	}
}

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
