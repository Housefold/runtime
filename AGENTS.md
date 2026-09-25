# Agent guide

This file is a map and a set of guardrails. The product and architecture documents are the source for Housefold-wide intent; this repository's [Runtime specification](docs/runtime-spec.md) records the current Runtime scope and decisions.

## Read before changing behavior

1. [Project Vision](https://github.com/Housefold/docs/blob/main/Housefold_Project_Vision.md)
2. [System Architecture](https://github.com/Housefold/docs/blob/main/Housefold_System_Architecture.md)
3. [Domain Model](https://github.com/Housefold/docs/blob/main/Housefold_Domain_Model.md)
4. [Runtime specification](docs/runtime-spec.md)
5. Relevant records in [`docs/adr/`](docs/adr/)

## Guardrails

- Work only on the assigned slice. Do not add adjacent features, abstractions, or dependencies without a clear requirement.
- Follow the accepted baseline: HAOS add-on, HA REST/WebSocket first, optional Bridge, and separate-process Go modules.
- Keep module execution, updates, and the public module contract out of an initial bootstrap slice unless the assigned task explicitly includes an approved design.
- Do not invent public APIs, module protocols, permissions, credentials, update trust, or remote-access behavior. If the task depends on an unresolved decision, stop and write the question and options in a proposed ADR.
- Do not execute downloaded or discovered module code automatically. Module installation, trust, permissions, and updates are blocked until their security design is approved.
- Keep management and recovery local. Do not expose an unauthenticated control surface to the network or make local operation depend on the VPS or cloud.
- Preserve the last healthy runtime/module version during updates. Never permit overlapping active automation versions.
- Keep secrets and real household identities/activity out of source, fixtures, logs, and examples.
- Update the Runtime specification or an ADR when implementation changes an accepted contract.
- Make changes small and reviewable on `main`. Do not create a PR before the first release unless requested. Do not tag or release without explicit user direction.

## Completion

For behavior changes, add or update focused tests and report the exact checks run. The standard static checks are `gofmt`, `go vet ./...`, and `go build ./...`; run focused tests for behavior changes. Do not claim acceptance criteria are met by compilation alone.
