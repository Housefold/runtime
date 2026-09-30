# Housefold Runtime

The stable Go supervisor and local control plane for Housefold. The Runtime starts as a foreground process, reports its own health, and maintains a bounded in-memory Home Assistant state view over WebSocket.

## Current baseline

- The first runtime is a Supervisor-managed Home Assistant OS app.
- It reads HA states through the Supervisor Core API proxy, reconciles snapshots and buffered events into atomic generations, and marks cached state stale when WebSocket continuity is lost; the custom HA integration Bridge remains optional.
- Optional Go modules run as separate processes managed by the Runtime.
- Essential local behavior must not depend on the VPS, internet, cloud AI, or optional modules.

State is retained in memory only and never exposed in the status page or logs. The status page reports only coarse synchronization metadata. The public Home Assistant WebSocket API does not promise an atomic snapshot/event barrier or silent-gap detection; see [ADR-003](docs/adr/003-ha-state-cache-and-reconnection.md) for the exact freshness boundary. The module protocol, module permissions, and update trust remain open. See [the Runtime specification](docs/runtime-spec.md) and its linked ADRs for current decisions.

## Development

Requires Go 1.26 or later. This single-app repository uses its root as the HAOS app directory and Go module, so local app builds can access the same source tree as Go checks. Run `go test ./...`, `go vet ./...`, and `go build ./...` from the repository root before committing. CI checks formatting, tests, vet, Go build, and both HAOS app image architectures on pushes to `main` and on pull requests.

## Home Assistant OS app

Add this repository as a local app repository in Home Assistant to build and run the development version. The repository root contains the app manifest and Dockerfile. The app provides a read-only status page through HA ingress and an internal health check for the Supervisor watchdog; it has no host port mapping. The status page shows Runtime health and coarse HA synchronization metadata without entity data. With the approved Core API proxy grant, Runtime uses the Supervisor-provided token and does not persist or log it. Use Supervisor app controls/logs for recovery, or the HAOS host console if Core UI is unavailable. A clean HAOS 18.3 generic AArch64 VM previously verified lifecycle and connectivity-probe recovery; it did not verify the state-synchronization path. Target appliance hardware and long-duration behavior remain unverified.

For a local image build, use the repository root as the app directory/build context and pass the HA architecture name expected by the app manifest:

```sh
docker buildx build --load --platform linux/amd64 --build-arg BUILD_ARCH=amd64 -f Dockerfile -t housefold-runtime:dev .
```

For ARM64, use `--platform linux/arm64 --build-arg BUILD_ARCH=aarch64` with the same context. A Docker build does not prove HAOS behavior; the reported VM smoke covers generic AArch64 only, not appliance hardware or long-duration operation.

See [AGENTS.md](AGENTS.md) for repository instructions and [CONTRIBUTING.md](CONTRIBUTING.md) for change guidelines.

The agent harness uses `bash scripts/agent_verify.sh` for uncached/race/static and
cross-build checks; add `--images` to build and load both HAOS architectures.
When a managed worker's HTTPS proxy needs its CA inside the builder, pass a
combined trusted CA bundle via `HOUSEFOLD_BUILD_CA=/path/to/bundle.pem`. The
optional BuildKit secret is used only by the Go build step and is absent from
the final scratch images; ordinary builds use their normal system trust.
