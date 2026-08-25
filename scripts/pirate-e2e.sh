#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CONTEXT=${CONTEXT:-pirate}
CHART_VERSION=0.36.1
CHART_SHA256=84a6aa28ffd2504069ed987202238de85509c50050748fb2da4fd262a6861b35
PAUSE_IMAGE=registry.k8s.io/pause@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a
RUN_ID=$(date -u +%H%M%S)
EXPLICIT_NAME="e2e-explicit-${RUN_ID}"
TTL_NAME="e2e-ttl-${RUN_ID}"
TMP_DIR=$(mktemp -d)
EVIDENCE_DIR=${EVIDENCE_DIR:-"${ROOT}/artifacts/e2e/$(date -u +%Y%m%dT%H%M%SZ)"}
mkdir -p "$EVIDENCE_DIR"

CONTROLLER_PID=""
MCP_PID=""
FORWARD_PID=""

cleanup() {
  set +e
  if [[ -n "$FORWARD_PID" ]]; then kill "$FORWARD_PID" 2>/dev/null; wait "$FORWARD_PID" 2>/dev/null; fi
  for name in "$EXPLICIT_NAME" "$TTL_NAME"; do
    kubectl --context "$CONTEXT" delete oubliette "$name" --ignore-not-found --wait=false >/dev/null 2>&1
  done
  sleep 2
  if [[ -n "$MCP_PID" ]]; then kill "$MCP_PID" 2>/dev/null; wait "$MCP_PID" 2>/dev/null; fi
  if [[ -n "$CONTROLLER_PID" ]]; then kill "$CONTROLLER_PID" 2>/dev/null; wait "$CONTROLLER_PID" 2>/dev/null; fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

kubectl --context "$CONTEXT" --request-timeout=10s get --raw=/readyz >/dev/null
kubectl --context "$CONTEXT" auth can-i create customresourcedefinitions.apiextensions.k8s.io 2>/dev/null | grep -qx yes
kubectl --context "$CONTEXT" auth can-i create namespaces 2>/dev/null | grep -qx yes

kubectl config view --context="$CONTEXT" --minify --flatten --raw > "$TMP_DIR/host.kubeconfig"
helm pull vcluster --repo https://charts.loft.sh --version "$CHART_VERSION" --destination "$TMP_DIR" >/dev/null
echo "$CHART_SHA256  $TMP_DIR/vcluster-${CHART_VERSION}.tgz" | shasum -a 256 -c - >/dev/null

kubectl --context "$CONTEXT" apply -f "$ROOT/config/crd/bases/oubliette.tlrmchlsmth.github.io_oubliettes.yaml" >/dev/null
kubectl --context "$CONTEXT" apply -f "$ROOT/config/install/policy.yaml" >/dev/null

KUBECONFIG="$TMP_DIR/host.kubeconfig" go run "$ROOT/cmd/controller" --leader-elect=false --vcluster-chart="$TMP_DIR/vcluster-${CHART_VERSION}.tgz" >"$EVIDENCE_DIR/controller.log" 2>&1 &
CONTROLLER_PID=$!
openssl rand -hex 32 > "$TMP_DIR/mcp-token"
OUBLIETTE_MCP_TOKEN="$(<"$TMP_DIR/mcp-token")" KUBECONFIG="$TMP_DIR/host.kubeconfig" go run "$ROOT/cmd/mcp" --listen=127.0.0.1:18080 >"$EVIDENCE_DIR/mcp.log" 2>&1 &
MCP_PID=$!

for _ in {1..60}; do
  if curl -fsS http://127.0.0.1:18080/healthz >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsS http://127.0.0.1:18080/healthz >/dev/null
test "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/mcp)" = 401

oub() {
  OUBLIETTE_MCP_TOKEN="$(<"$TMP_DIR/mcp-token")" OUBLIETTE_MCP_ENDPOINT=http://127.0.0.1:18080/mcp go run "$ROOT/cmd/oub" "$@"
}

oub create --ttl 600 "$EXPLICIT_NAME" | tee "$EVIDENCE_DIR/create-explicit.json"
! rg -qi 'kubeconfig|client-certificate|client-key|bearer|token' "$EVIDENCE_DIR/create-explicit.json"
kubectl --context "$CONTEXT" wait --for=condition=Ready=True "oubliette/$EXPLICIT_NAME" --timeout=180s

HOST_NAMESPACE="oub-${EXPLICIT_NAME}"
kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" port-forward "service/$EXPLICIT_NAME" 18443:443 >"$EVIDENCE_DIR/port-forward.log" 2>&1 &
FORWARD_PID=$!
for _ in {1..30}; do
  if curl -ksS https://127.0.0.1:18443/readyz >/dev/null 2>&1; then break; fi
  sleep 1
done

kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get secret "vc-$EXPLICIT_NAME" -o jsonpath='{.data.config}' | base64 -d > "$TMP_DIR/admin.kubeconfig"
kubectl --kubeconfig="$TMP_DIR/admin.kubeconfig" config use-context "$EXPLICIT_NAME" >/dev/null
kubectl --kubeconfig="$TMP_DIR/admin.kubeconfig" config set-cluster "$EXPLICIT_NAME" --server=https://127.0.0.1:18443 --tls-server-name="$EXPLICIT_NAME.$HOST_NAMESPACE" >/dev/null
kubectl --kubeconfig="$TMP_DIR/admin.kubeconfig" -n default create serviceaccount oubliette-agent >/dev/null
kubectl --kubeconfig="$TMP_DIR/admin.kubeconfig" create clusterrolebinding oubliette-agent-cluster-admin --clusterrole=cluster-admin --serviceaccount=default:oubliette-agent >/dev/null
AGENT_TOKEN=$(kubectl --kubeconfig="$TMP_DIR/admin.kubeconfig" -n default create token oubliette-agent --duration=10m)
cp "$TMP_DIR/admin.kubeconfig" "$TMP_DIR/agent.kubeconfig"
kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" config set-credentials oubliette-agent --token="$AGENT_TOKEN" >/dev/null
kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" config set-context "$EXPLICIT_NAME" --user=oubliette-agent >/dev/null
kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" config delete-user "$EXPLICIT_NAME" >/dev/null
! rg -q 'client-certificate-data|client-key-data' "$TMP_DIR/agent.kubeconfig"

cp "$TMP_DIR/host.kubeconfig" "$TMP_DIR/host-denial.kubeconfig"
HOST_CONTEXT=$(kubectl --kubeconfig="$TMP_DIR/host-denial.kubeconfig" config current-context)
HOST_USER=$(kubectl --kubeconfig="$TMP_DIR/host-denial.kubeconfig" config view -o jsonpath='{.contexts[0].context.user}')
kubectl --kubeconfig="$TMP_DIR/host-denial.kubeconfig" config delete-user "$HOST_USER" >/dev/null
kubectl --kubeconfig="$TMP_DIR/host-denial.kubeconfig" config set-credentials virtual-agent --token="$AGENT_TOKEN" >/dev/null
kubectl --kubeconfig="$TMP_DIR/host-denial.kubeconfig" config set-context "$HOST_CONTEXT" --user=virtual-agent >/dev/null
if kubectl --kubeconfig="$TMP_DIR/host-denial.kubeconfig" get namespaces >/dev/null 2>&1; then
  echo "virtual token authenticated to the host API" >&2
  exit 1
fi

kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" run demo-workload --image="$PAUSE_IMAGE" --restart=Never --overrides="{\"spec\":{\"containers\":[{\"name\":\"demo-workload\",\"image\":\"$PAUSE_IMAGE\",\"resources\":{\"requests\":{\"cpu\":\"10m\",\"memory\":\"16Mi\",\"ephemeral-storage\":\"10Mi\"},\"limits\":{\"cpu\":\"100m\",\"memory\":\"64Mi\",\"ephemeral-storage\":\"100Mi\"}},\"securityContext\":{\"allowPrivilegeEscalation\":false,\"capabilities\":{\"drop\":[\"ALL\"]}}}]}}" >/dev/null
kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" wait --for=condition=Ready pod/demo-workload --timeout=180s
HOST_POD=$(kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get pods -l "vcluster.loft.sh/managed-by=$EXPLICIT_NAME" -o jsonpath='{.items[?(@.metadata.annotations.vcluster\.loft\.sh/namespace=="default")].metadata.name}')
test -n "$HOST_POD"

kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" run forbidden-privileged --image="$PAUSE_IMAGE" --restart=Never --privileged --overrides="{\"spec\":{\"containers\":[{\"name\":\"forbidden-privileged\",\"image\":\"$PAUSE_IMAGE\",\"securityContext\":{\"privileged\":true},\"resources\":{\"requests\":{\"cpu\":\"10m\",\"memory\":\"16Mi\",\"ephemeral-storage\":\"10Mi\"},\"limits\":{\"cpu\":\"100m\",\"memory\":\"64Mi\",\"ephemeral-storage\":\"100Mi\"}}}]}}" >/dev/null
sleep 8
! kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get pods -l "vcluster.loft.sh/managed-by=$EXPLICIT_NAME" -o name | rg -q forbidden-privileged
kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" logs "deployment/$EXPLICIT_NAME" --tail=1200 | rg 'forbidden-privileged.*forbidden' > "$EVIDENCE_DIR/admission.txt"

oub get "$EXPLICIT_NAME" > "$EVIDENCE_DIR/ready-explicit.json"
oub delete "$EXPLICIT_NAME" > "$EVIDENCE_DIR/delete-explicit.json"
kubectl --context "$CONTEXT" wait --for=delete "oubliette/$EXPLICIT_NAME" --timeout=180s
kubectl --context "$CONTEXT" wait --for=delete "namespace/$HOST_NAMESPACE" --timeout=180s
kill "$FORWARD_PID" 2>/dev/null || true
wait "$FORWARD_PID" 2>/dev/null || true
FORWARD_PID=""

oub create --ttl 120 "$TTL_NAME" > "$EVIDENCE_DIR/create-ttl.json"
kubectl --context "$CONTEXT" wait --for=condition=Ready=True "oubliette/$TTL_NAME" --timeout=120s
kubectl --context "$CONTEXT" wait --for=condition=Forgotten=True "oubliette/$TTL_NAME" --timeout=240s
! kubectl --context "$CONTEXT" get namespace "oub-$TTL_NAME" >/dev/null 2>&1
kubectl --context "$CONTEXT" get oubliette "$TTL_NAME" -o json | jq '{name:.metadata.name,expiresAt:.spec.expiresAt,status:.status}' > "$EVIDENCE_DIR/forgotten-ttl.json"

jq -n --arg explicit "$EXPLICIT_NAME" --arg ttl "$TTL_NAME" --arg hostPod "$HOST_POD" --arg chart "$CHART_VERSION" '{result:"pass",explicit:$explicit,ttl:$ttl,translatedHostPod:$hostPod,vclusterChart:$chart,credentials:"redacted"}' | tee "$EVIDENCE_DIR/summary.json"
