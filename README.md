# Oubliette

Oubliette is a design-stage project for disposable, agent-scoped Kubernetes
control planes that run real LLM-serving workloads through vCluster while
keeping the host API and host policy outside the agent trust boundary.

There is intentionally no controller or CLI implementation yet. The normative
system charter is [[RFC-0001]], and architectural choices are being developed
as proposed ADRs before implementation begins. [[ADR-0001]] establishes the
governance process.

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
  observability, and the first GPU/SR-IOV milestone.
- ADR-0001 is accepted: Oubliette uses govctl from its first commit.
- ADR-0002 through ADR-0010 are proposed and alternatives-first.
- No product API, programming language, controller framework, or vCluster
  provisioning mechanism has been selected yet.
