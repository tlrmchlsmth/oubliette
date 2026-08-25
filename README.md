# Oubliette

Oubliette is an early implementation of disposable, agent-scoped Kubernetes
control planes that run real llm-d workloads through vCluster while keeping the
host API and host policy outside the agent trust boundary. Every model-driven
agent runs inside a sandbox owned by its consumer; OpenShell is the first-class
integration, while consumers such as Crucible may provide their own sandbox.

The repository contains a Go controller, authenticated MCP lifecycle service,
scoped metrics gateway, evidence exporter, and `oub` client. The system charter remains the draft [[RFC-0001]], and
[[ADR-0001]] establishes the governance process.

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

## Scoped metrics and evidence

ADR-0010 separates agent metrics access from operator-authoritative benchmark
evidence. Metrics are disabled by default. An operator enables the controller's
metrics profile and the `oubliette-metrics` Deployment together, supplies a
private Prometheus-compatible upstream, an allowlist, and an HMAC key, and
delivers the resulting short-lived credential through a resident projection or
authenticated external connector—not through MCP results.

The gateway parses and scopes PromQL and metadata selectors, filters sensitive
labels, enforces time, sample, concurrency, and rate budgets, and revalidates
the Oubliette lifecycle on every request. `evidence-export` requires the
portable ADR-0010 provenance set and writes a content-addressed immutable bundle
outside the derived namespace.

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
- ADR-0001, ADR-0003 through ADR-0015, and ADR-0018 are accepted. They authorize
  the stub-tier implementation, its host boundaries, opt-in host-authoritative
  Kueue admission, provider-neutral RDMA profiles, and scoped observability.
- ADR-0002 is superseded. ADR-0016 and ADR-0017 remain proposed,
  alternatives-first, and include explicit decision criteria. Model and image
  access and persistent storage are not yet authorized for implementation.
