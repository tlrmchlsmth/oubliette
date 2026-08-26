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
- Use an isolated git worktree for changes when the primary worktree is dirty,
  belongs to another in-progress branch, or would otherwise mix unrelated user
  work. Never overwrite, stash, or carry unrelated primary-worktree changes
  into the task branch.
- Keep environment-specific operational scripts, cluster credentials, captured
  evidence, and one-off validation harnesses out of the repository. Run them
  from local or otherwise ephemeral workspace paths and record only portable
  product behavior and durable test coverage in the repository. Committing an
  environment-specific harness requires an accepted ADR that explains why it
  is a maintained product artifact.
- Sign every commit for DCO compliance with `git commit -s`.
