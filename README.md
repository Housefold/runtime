# Housefold Runtime

The stable Go supervisor and local control plane for Housefold. It connects to Home Assistant, manages the lifecycle and health of Housefold modules, and exposes the normalized Housefold API.

## Current baseline

- The first runtime is a Home Assistant OS add-on.
- It connects to HA through REST and WebSocket APIs; the custom HA integration bridge remains optional.
- Optional Go modules run as separate processes managed by the Runtime.
- Essential local behavior must not depend on the VPS, internet, cloud AI, or optional modules.

The exact module protocol, permissions, packaging details, and update trust model remain open. See [the Runtime specification](docs/runtime-spec.md) and the platform docs for current decisions.

## Development

Requires Go 1.26 or later. Run `go vet ./...` and `go build ./...` before committing. Add focused tests for behavior changes. CI checks formatting, vet, and build on pushes to `main` and on pull requests.

See [AGENTS.md](AGENTS.md) for repository instructions and [CONTRIBUTING.md](CONTRIBUTING.md) for change guidelines.
