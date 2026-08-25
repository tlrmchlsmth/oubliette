# Oubliette

Oubliette is a design-stage project for disposable, agent-scoped Kubernetes
control planes that run real llm-d workloads through vCluster while keeping the
host API and host policy outside the agent trust boundary. Every model-driven
agent runs inside a sandbox owned by its consumer; OpenShell is the first-class
integration, while consumers such as Crucible may provide their own sandbox.

There is intentionally no controller or CLI implementation yet. The system
charter is the draft [[RFC-0001]], and architectural choices are being recorded
as ADRs before implementation begins. [[ADR-0001]] establishes the governance
process.

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
- ADR-0001, ADR-0003, ADR-0011, ADR-0012, and ADR-0013 are accepted. They
  establish govctl governance, the cluster-scoped API and expiry model,
  consumer-owned agent sandboxes, MCP lifecycle access, and a Go stack using
  controller-runtime plus the official MCP SDK.
- ADR-0002 is superseded; ADR-0004 through ADR-0010 remain proposed and
  alternatives-first.
- vCluster provisioning and the host synchronization, admission, resource,
  networking, scheduling, credential-delivery, and observability contracts
  remain to be accepted before their implementation work is authorized.
