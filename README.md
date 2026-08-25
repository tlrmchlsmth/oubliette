# Oubliette

Oubliette is an early implementation of disposable, agent-scoped Kubernetes
control planes that run real llm-d workloads through vCluster while keeping the
host API and host policy outside the agent trust boundary. Every model-driven
agent runs inside a sandbox owned by its consumer; OpenShell is the first-class
integration, while consumers such as Crucible may provide their own sandbox.

The repository contains a Go controller, authenticated MCP lifecycle service,
and `oub` client. The system charter remains the draft [[RFC-0001]], and
[[ADR-0001]] establishes the governance process.

## Pirate Milestone 0 demo

The demo runs the controller and MCP service locally against the VPN-only
Pirate cluster, while the vCluster control plane and synchronized workloads run
on Pirate. It requires `go`, `helm`, `kubectl`, `curl`, `jq`, `rg`, `openssl`,
and an authenticated `pirate` context with cluster-admin access.

```console
./scripts/pirate-e2e.sh
```

It proves authenticated lifecycle creation, private virtual API access,
short-lived virtual-only credentials, host admission and resource/network
boundaries, a real synchronized workload, explicit teardown, and TTL teardown.
Redacted evidence is written beneath `artifacts/e2e/`.

## Pirate Kueue conformance

Host-authoritative Kueue integration is opt-in through the controller's
`--kueue-cluster-queue` flag. The controller labels the host namespace and
creates a fixed `oubliette` LocalQueue; host admission requires synchronized
pods to use that queue and prevents the vCluster syncer from removing Kueue's
admission gate.

```console
./scripts/pirate-kueue-e2e.sh
```

The conformance run holds a two-pod group, proves both pods remain gated and
unbound, admits them together, and confirms host drain is reflected into the
virtual API. Replacement-pod restart conformance remains blocked because the
stub tier uses an ephemeral SQLite `emptyDir`; durable control-plane storage is
part of the ADR-0017 decision.

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
- ADR-0001, ADR-0003 through ADR-0007, ADR-0009, ADR-0011 through ADR-0015,
  and ADR-0018 are accepted. They authorize the stub-tier implementation, its
  host boundaries, and opt-in host-authoritative Kueue admission.
- ADR-0002 is superseded. ADR-0008, ADR-0010, ADR-0016, and ADR-0017 remain
  proposed, alternatives-first, and include explicit decision criteria.
- GPU/RDMA isolation, model and image access, persistent storage, and
  production observability remain proposed and are not authorized by the
  Milestone 0 work item.
