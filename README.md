# Oubliette

Oubliette is an early implementation of disposable, agent-scoped Kubernetes
control planes that run real llm-d workloads through vCluster while keeping the
host API and host policy outside the agent trust boundary. Every model-driven
agent runs inside a sandbox owned by its consumer; OpenShell is the first-class
integration, while consumers such as Crucible may provide their own sandbox.

The repository contains a Go controller, authenticated MCP lifecycle service,
scoped metrics gateway, evidence exporter, and `oub` client. The system charter remains the draft [[RFC-0001]], and
[[ADR-0001]] establishes the governance process.

## Lifecycle authentication

The MCP service authenticates caller-specific, audience-bound Kubernetes
credentials through TokenReview. Consumer-owned connectors request short-lived
tokens for the `oubliette-mcp` audience and pass them as bearer credentials;
the lifecycle service stores only a digest of the authenticated identity and
limits create, get, list, renew, and delete to that identity's Oubliettes.
Rotated tokens for the same identity retain access, while tokens issued for the
host API or another audience are rejected.

The controller reports `Ready=True` only after it has used the chart-generated
bootstrap kubeconfig in memory to reconcile the fixed `oubliette-agent`
ServiceAccount and its virtual `cluster-admin` binding. Consumer connectors can
then request short-lived virtual tokens without receiving that bootstrap
kubeconfig.

For example, a trusted connector can request a token for its own ServiceAccount
and supply it to `oub` without exposing it in a model transcript:

```console
export OUBLIETTE_MCP_TOKEN="$(kubectl -n consumer create token agent --audience=oubliette-mcp --duration=15m)"
oub list
```

## Kueue integration

Host-authoritative Kueue integration is opt-in through the controller's
`--kueue-cluster-queue` flag. The controller labels the host namespace and
creates a fixed `oubliette` LocalQueue; host admission requires synchronized
pods to use that queue and prevents the vCluster syncer from removing Kueue's
admission gate.

Host-cluster validation held a two-pod group, proved both pods remained gated
and unbound, admitted them together, and confirmed host drain was reflected
into the virtual API. Replacement-pod restart conformance remains blocked
because the stub tier uses an ephemeral SQLite `emptyDir`; durable control-plane
storage is part of the ADR-0017 decision.

## Recursive self-development

ADR-0019 selects one level of literal vCluster nesting for developing
Oubliette from a YOLO agent inside an outer Oubliette. The nested stack uses
only the outer virtual API and inherits the outer host namespace, trust domain,
quota, expiry, and teardown; it is not a second host isolation boundary.

Operators configure nested controllers with `--host-workload-queue` so both
the generated vCluster control-plane and CoreDNS pods retain the host queue
label across synchronization. The `--vcluster-run-as-user`,
`--vcluster-run-as-group`, and `--vcluster-fs-group` flags select an approved
numeric identity; setting all three to zero delegates assignment to platform
admission. Setting both `--vcluster-ephemeral-storage-request` and
`--vcluster-ephemeral-storage-limit` empty removes the chart defaults when the
authoritative ClusterQueue does not cover that resource. These are trusted
operator settings and are not lifecycle API inputs.

This manifest support does not yet make recursive self-development stable.
Restart-safe child control-plane state remains gated on ADR-0017, and complete
double-sync, scheduling-gate, status, log, and teardown conformance must pass on
a supported host before the topology is advertised as ready.

## Scoped metrics and evidence

ADR-0010 separates agent metrics access from operator-authoritative benchmark
evidence. Metrics are disabled by default. An operator enables the controller's
metrics profile and the `oubliette-metrics` Deployment together, supplies a
private Prometheus-compatible upstream, an allowlist, and an HMAC key, and
delivers the resulting short-lived credential through a resident projection or
authenticated external connector—not through MCP results.

The metrics Deployment hosts an authenticated issuer at
`/access/v1/credentials`. A consumer connector authenticates with the separate
issuer token and requests either `resident` or `external` placement; the
audience-bound response is delivered outside the lifecycle MCP transcript. The
installation Service is a LoadBalancer so an external connector can reach both
the issuer and gateway. Installation overlays must make that load balancer
private using the platform-specific annotation.

The gateway parses and scopes PromQL and metadata selectors, filters sensitive
labels, enforces time, sample, concurrency, and rate budgets, and revalidates
the Oubliette lifecycle on every request. `evidence-export` requires the
portable ADR-0010 provenance set and writes a content-addressed immutable bundle
outside the derived namespace.

Operator-authoritative benchmark collectors hand off each completed run in an
immutable ConfigMap labeled
`evidence.oubliette.tlrmchlsmth.github.io/pending-run=true`; its `bundle.json`
contains the run metadata and artifacts. The lifecycle controller exports every
pending bundle to its operator-owned evidence PVC during reconciliation and
again as a mandatory finalizer step. A failed export blocks vCluster and
namespace deletion.

Required JSON artifacts use provenance-specific top-level fields: `admitted`
or `workloads` for Kueue, `inputs`, `results`, `collector`, `objects` for
lineage, `queries`, `samples`, `pods`, `profiles`, `policy`, `spec`, `inventory`,
and `components`. Transport proof must contain both `configuration` and
`runtime`; rendered manifests must contain a Kubernetes `kind` and `metadata`.
Empty placeholders are rejected.

## Governance

[govctl](https://github.com/govctl-org/govctl) artifacts under `gov/` are the
source of truth. This repository bootstraps with govctl 0.19.1, recorded in
`.govctl-version`.

```console
govctl status
govctl check
govctl render rfc
```

RFCs define what must be true, ADRs record why choices are made, work items
authorize implementation, and verification guards decide when work is done.
Rendered RFC documentation is published under `docs/rfc/`.

## Current state

- RFC-0001 is a draft covering isolation, lifecycle, resource bounds,
  observability, a stub-tier bootstrap milestone, and the first GPU/SR-IOV
  milestone.
- ADR-0001, ADR-0003 through ADR-0015, ADR-0018, and ADR-0019 are accepted. They authorize
  the stub-tier implementation, its host boundaries, opt-in host-authoritative
  Kueue admission, provider-neutral RDMA profiles, and scoped observability.
- ADR-0002 is superseded. ADR-0016 and ADR-0017 remain proposed,
  alternatives-first, and include explicit decision criteria. Model and image
  access and persistent storage are not yet authorized for implementation.
