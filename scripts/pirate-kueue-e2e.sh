#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CONTEXT=${CONTEXT:-pirate}
CHART_VERSION=0.36.1
CHART_SHA256=84a6aa28ffd2504069ed987202238de85509c50050748fb2da4fd262a6861b35
PAUSE_IMAGE=registry.k8s.io/pause@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a
RUN_ID=$(date -u +%H%M%S)
NAME="e2e-kueue-${RUN_ID}"
CLUSTER_QUEUE="oub-${NAME}"
HOST_NAMESPACE="oub-${NAME}"
TMP_DIR=$(mktemp -d)
EVIDENCE_DIR=${EVIDENCE_DIR:-"${ROOT}/artifacts/e2e-kueue/$(date -u +%Y%m%dT%H%M%SZ)"}
mkdir -p "$EVIDENCE_DIR"

CONTROLLER_PID=""
MCP_PID=""
FORWARD_PID=""

cleanup() {
  set +e
  if [[ -n "$FORWARD_PID" ]]; then kill "$FORWARD_PID" 2>/dev/null; wait "$FORWARD_PID" 2>/dev/null; fi
  kubectl --context "$CONTEXT" patch clusterqueue.kueue.x-k8s.io "$CLUSTER_QUEUE" --type=merge -p '{"spec":{"stopPolicy":"None"}}' >/dev/null 2>&1
  kubectl --context "$CONTEXT" delete oubliette "$NAME" --ignore-not-found --wait=false >/dev/null 2>&1
  if ! kubectl --context "$CONTEXT" wait --for=delete "namespace/$HOST_NAMESPACE" --timeout=90s >/dev/null 2>&1; then
    kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get pods -o name 2>/dev/null | while read -r pod; do
      kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" patch "$pod" --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1
    done
    kubectl --context "$CONTEXT" wait --for=delete "namespace/$HOST_NAMESPACE" --timeout=30s >/dev/null 2>&1
  fi
  if ! kubectl --context "$CONTEXT" wait --for=delete "oubliette/$NAME" --timeout=30s >/dev/null 2>&1; then
    kubectl --context "$CONTEXT" patch oubliette "$NAME" --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1
  fi
  kubectl --context "$CONTEXT" delete clusterqueue.kueue.x-k8s.io "$CLUSTER_QUEUE" --ignore-not-found >/dev/null 2>&1
  if [[ -n "$MCP_PID" ]]; then kill "$MCP_PID" 2>/dev/null; wait "$MCP_PID" 2>/dev/null; fi
  if [[ -n "$CONTROLLER_PID" ]]; then kill "$CONTROLLER_PID" 2>/dev/null; wait "$CONTROLLER_PID" 2>/dev/null; fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

start_forward() {
  kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" port-forward "service/$NAME" 18444:443 >"$EVIDENCE_DIR/port-forward.log" 2>&1 &
  FORWARD_PID=$!
  for _ in {1..30}; do
    if curl -ksS https://127.0.0.1:18444/readyz >/dev/null 2>&1; then return; fi
    sleep 1
  done
  return 1
}

oub() {
  OUBLIETTE_MCP_TOKEN="$(<"$TMP_DIR/mcp-token")" OUBLIETTE_MCP_ENDPOINT=http://127.0.0.1:18081/mcp go run "$ROOT/cmd/oub" "$@"
}

create_group() {
  local group=$1
  kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${group}-0
  labels:
    kueue.x-k8s.io/queue-name: oubliette
    kueue.x-k8s.io/pod-group-name: ${group}
  annotations:
    kueue.x-k8s.io/pod-group-total-count: "2"
spec:
  restartPolicy: Never
  containers:
    - name: workload
      image: ${PAUSE_IMAGE}
      resources:
        requests: {cpu: 10m, memory: 16Mi, ephemeral-storage: 10Mi}
        limits: {cpu: 100m, memory: 64Mi, ephemeral-storage: 100Mi}
      securityContext:
        allowPrivilegeEscalation: false
        capabilities: {drop: [ALL]}
---
apiVersion: v1
kind: Pod
metadata:
  name: ${group}-1
  labels:
    kueue.x-k8s.io/queue-name: oubliette
    kueue.x-k8s.io/pod-group-name: ${group}
  annotations:
    kueue.x-k8s.io/pod-group-total-count: "2"
spec:
  restartPolicy: Never
  containers:
    - name: workload
      image: ${PAUSE_IMAGE}
      resources:
        requests: {cpu: 10m, memory: 16Mi, ephemeral-storage: 10Mi}
        limits: {cpu: 100m, memory: 64Mi, ephemeral-storage: 100Mi}
      securityContext:
        allowPrivilegeEscalation: false
        capabilities: {drop: [ALL]}
EOF
}

wait_group_on_host() {
  local group=$1
  for _ in {1..60}; do
    count=$(kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get pods -l "kueue.x-k8s.io/pod-group-name=$group" -o name 2>/dev/null | wc -l | tr -d ' ')
    if [[ "$count" == "2" ]]; then return; fi
    sleep 1
  done
  return 1
}

kubectl --context "$CONTEXT" --request-timeout=10s get --raw=/readyz >/dev/null
kubectl --context "$CONTEXT" get crd clusterqueues.kueue.x-k8s.io localqueues.kueue.x-k8s.io workloads.kueue.x-k8s.io >/dev/null
kubectl --context "$CONTEXT" auth can-i create clusterqueues.kueue.x-k8s.io | grep -qx yes
kubectl --context "$CONTEXT" auth can-i create localqueues.kueue.x-k8s.io -n tms | grep -qx yes

kubectl config view --context="$CONTEXT" --minify --flatten --raw > "$TMP_DIR/host.kubeconfig"
helm pull vcluster --repo https://charts.loft.sh --version "$CHART_VERSION" --destination "$TMP_DIR" >/dev/null
echo "$CHART_SHA256  $TMP_DIR/vcluster-${CHART_VERSION}.tgz" | shasum -a 256 -c - >/dev/null

kubectl --context "$CONTEXT" apply -f "$ROOT/config/crd/bases/oubliette.tlrmchlsmth.github.io_oubliettes.yaml" >/dev/null
kubectl --context "$CONTEXT" apply -f "$ROOT/config/install/policy.yaml" >/dev/null
kubectl --context "$CONTEXT" apply --dry-run=server -f - >/dev/null <<EOF
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: ${CLUSTER_QUEUE}
spec:
  namespaceSelector:
    matchLabels:
      kueue.openshift.io/managed: "true"
  queueingStrategy: BestEffortFIFO
  stopPolicy: Hold
  resourceGroups:
    - coveredResources: [cpu, memory, ephemeral-storage]
      flavors:
        - name: default-flavor
          resources:
            - {name: cpu, nominalQuota: "2"}
            - {name: memory, nominalQuota: 2Gi}
            - {name: ephemeral-storage, nominalQuota: 10Gi}
EOF
kubectl --context "$CONTEXT" apply -f - >/dev/null <<EOF
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: ${CLUSTER_QUEUE}
spec:
  namespaceSelector:
    matchLabels:
      kueue.openshift.io/managed: "true"
  queueingStrategy: BestEffortFIFO
  stopPolicy: Hold
  resourceGroups:
    - coveredResources: [cpu, memory, ephemeral-storage]
      flavors:
        - name: default-flavor
          resources:
            - {name: cpu, nominalQuota: "2"}
            - {name: memory, nominalQuota: 2Gi}
            - {name: ephemeral-storage, nominalQuota: 10Gi}
EOF

KUBECONFIG="$TMP_DIR/host.kubeconfig" go run "$ROOT/cmd/controller" --leader-elect=false --vcluster-chart="$TMP_DIR/vcluster-${CHART_VERSION}.tgz" --kueue-cluster-queue="$CLUSTER_QUEUE" --kueue-managed-label=kueue.openshift.io/managed --kueue-managed-value=true >"$EVIDENCE_DIR/controller.log" 2>&1 &
CONTROLLER_PID=$!
openssl rand -hex 32 > "$TMP_DIR/mcp-token"
OUBLIETTE_MCP_TOKEN="$(<"$TMP_DIR/mcp-token")" KUBECONFIG="$TMP_DIR/host.kubeconfig" go run "$ROOT/cmd/mcp" --listen=127.0.0.1:18081 >"$EVIDENCE_DIR/mcp.log" 2>&1 &
MCP_PID=$!
for _ in {1..60}; do
  if curl -fsS http://127.0.0.1:18081/healthz >/dev/null 2>&1; then break; fi
  sleep 1
done

oub create --ttl 900 "$NAME" > "$EVIDENCE_DIR/create.json"
kubectl --context "$CONTEXT" wait --for=condition=Ready=True "oubliette/$NAME" --timeout=180s
test "$(kubectl --context "$CONTEXT" get namespace "$HOST_NAMESPACE" -o jsonpath='{.metadata.labels.kueue\.openshift\.io/managed}')" = true
test "$(kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get localqueue oubliette -o jsonpath='{.spec.clusterQueue}')" = "$CLUSTER_QUEUE"

start_forward
kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get secret "vc-$NAME" -o jsonpath='{.data.config}' | base64 -d > "$TMP_DIR/admin.kubeconfig"
kubectl --kubeconfig="$TMP_DIR/admin.kubeconfig" config use-context "$NAME" >/dev/null
kubectl --kubeconfig="$TMP_DIR/admin.kubeconfig" config set-cluster "$NAME" --server=https://127.0.0.1:18444 --tls-server-name="$NAME.$HOST_NAMESPACE" >/dev/null
kubectl --kubeconfig="$TMP_DIR/admin.kubeconfig" -n default create serviceaccount oubliette-agent >/dev/null
kubectl --kubeconfig="$TMP_DIR/admin.kubeconfig" create clusterrolebinding oubliette-agent-cluster-admin --clusterrole=cluster-admin --serviceaccount=default:oubliette-agent >/dev/null
AGENT_TOKEN=$(kubectl --kubeconfig="$TMP_DIR/admin.kubeconfig" -n default create token oubliette-agent --duration=15m)
cp "$TMP_DIR/admin.kubeconfig" "$TMP_DIR/agent.kubeconfig"
kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" config set-credentials oubliette-agent --token="$AGENT_TOKEN" >/dev/null
kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" config set-context "$NAME" --user=oubliette-agent >/dev/null
kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" config delete-user "$NAME" >/dev/null

kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" run forbidden-unqueued --image="$PAUSE_IMAGE" --restart=Never --overrides="{\"spec\":{\"containers\":[{\"name\":\"forbidden-unqueued\",\"image\":\"$PAUSE_IMAGE\",\"resources\":{\"requests\":{\"cpu\":\"10m\",\"memory\":\"16Mi\",\"ephemeral-storage\":\"10Mi\"},\"limits\":{\"cpu\":\"100m\",\"memory\":\"64Mi\",\"ephemeral-storage\":\"100Mi\"}},\"securityContext\":{\"allowPrivilegeEscalation\":false,\"capabilities\":{\"drop\":[\"ALL\"]}}}]}}" >/dev/null
sleep 8
! kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get pods -l "vcluster.loft.sh/managed-by=$NAME" -o name | rg -q forbidden-unqueued
kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" logs "deployment/$NAME" --tail=1200 | rg 'Oubliette workloads must use the fixed host Kueue LocalQueue' > "$EVIDENCE_DIR/queue-enforcement.txt"
kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" delete pod forbidden-unqueued --wait=false >/dev/null

GROUP_ONE="gang-a-${RUN_ID}"
create_group "$GROUP_ONE"
wait_group_on_host "$GROUP_ONE"
kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" wait --for=condition=QuotaReserved=False "workload/$GROUP_ONE" --timeout=120s
test "$(kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get pods -l "kueue.x-k8s.io/pod-group-name=$GROUP_ONE" -o json | jq '[.items[] | select(any(.spec.schedulingGates[]?; .name == "kueue.x-k8s.io/admission"))] | length')" = 2
test "$(kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get pods -l "kueue.x-k8s.io/pod-group-name=$GROUP_ONE" -o json | jq '[.items[] | select(.spec.nodeName != null)] | length')" = 0
sleep 10
test "$(kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get pods -l "kueue.x-k8s.io/pod-group-name=$GROUP_ONE" -o json | jq '[.items[] | select(any(.spec.schedulingGates[]?; .name == "kueue.x-k8s.io/admission"))] | length')" = 2
test "$(kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get pods -l "kueue.x-k8s.io/pod-group-name=$GROUP_ONE" -o json | jq '[.items[] | select(.spec.nodeName != null)] | length')" = 0
kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" logs "deployment/$NAME" --tail=1200 | rg 'Only the host Kueue controller may remove the admission scheduling gate' > "$EVIDENCE_DIR/gate-protection.txt"
kubectl --context "$CONTEXT" patch clusterqueue "$CLUSTER_QUEUE" --type=merge -p '{"spec":{"stopPolicy":"None"}}' >/dev/null
kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" wait --for=condition=Admitted=True "workload/$GROUP_ONE" --timeout=120s
kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" wait --for=condition=Ready pod -l "kueue.x-k8s.io/pod-group-name=$GROUP_ONE" --timeout=180s

kubectl --context "$CONTEXT" patch clusterqueue "$CLUSTER_QUEUE" --type=merge -p '{"spec":{"stopPolicy":"HoldAndDrain"}}' >/dev/null
kubectl --kubeconfig="$TMP_DIR/agent.kubeconfig" wait --for=delete pod -l "kueue.x-k8s.io/pod-group-name=$GROUP_ONE" --timeout=180s
kubectl --context "$CONTEXT" -n "$HOST_NAMESPACE" get workloads.kueue.x-k8s.io -o json | jq '{items: [.items[] | {name: .metadata.name, conditions: .status.conditions}]}' > "$EVIDENCE_DIR/workloads-after-drain.json"
jq -n '{tested:false, reason:"A Deployment rollout replaces the ephemeral vCluster control-plane pod and its SQLite emptyDir, invalidating virtual credentials and state; durable control-plane storage under ADR-0017 is required before replacement-pod restart conformance."}' > "$EVIDENCE_DIR/syncer-restart.json"

oub delete "$NAME" > "$EVIDENCE_DIR/delete.json"
kubectl --context "$CONTEXT" wait --for=delete "oubliette/$NAME" --timeout=180s
kubectl --context "$CONTEXT" wait --for=delete "namespace/$HOST_NAMESPACE" --timeout=180s
kubectl --context "$CONTEXT" delete clusterqueue "$CLUSTER_QUEUE" --wait=true >/dev/null

jq -n --arg oubliette "$NAME" --arg queue "$CLUSTER_QUEUE" --arg group "$GROUP_ONE" '{result:"pass", oubliette:$oubliette, clusterQueue:$queue, heldThenAdmittedGroup:$group, drainReflectedToVirtualPods:true, replacementPodRestart:"blocked-by-ephemeral-control-plane-storage", credentials:"redacted"}' | tee "$EVIDENCE_DIR/summary.json"
