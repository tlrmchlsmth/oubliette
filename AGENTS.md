# Agent instructions

Oubliette uses govctl as its governance control plane.

- Run `govctl status` and inspect referenced RFCs, ADRs, and active work items
  before changing the repository.
- Treat TOML artifacts under `gov/` as authoritative. Use govctl resource
  commands for lifecycle-managed edits, then run `govctl check`.
- Do not begin product implementation without an active work item grounded in
  applicable normative clauses and accepted ADRs.
- Keep proposed ADRs alternatives-first. Do not silently encode a proposed
  choice in code, manifests, examples, or prose as if it were accepted.
- Render RFC projections with `govctl render rfc`; do not hand-edit generated
  files under `docs/rfc/`.
- Add executable guards and conformance cases as implementation work is
  authorized.
- Sign every commit for DCO compliance with `git commit -s`.
