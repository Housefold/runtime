#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ $# -gt 1 || ( $# -eq 1 && "$1" != "--images" ) ]]; then
  echo "usage: bash scripts/agent_verify.sh [--images]" >&2
  exit 2
fi
command -v go >/dev/null || { echo "BLOCKED: Go 1.26+ required" >&2; exit 2; }
command -v gofmt >/dev/null || { echo "BLOCKED: gofmt required" >&2; exit 2; }
python3 scripts/agent_tasks.py list >/dev/null
python3 -m unittest discover -s scripts -p "*_test.py"
git diff --check
go version
go env GOOS GOARCH CGO_ENABLED
bad=0
while IFS= read -r -d '' file; do
  result="$(gofmt -l "$file")"
  if [[ -n "$result" ]]; then echo "$result"; bad=1; fi
done < <(find cmd internal -type f -name '*.go' -print0)
[[ "$bad" == 0 ]] || { echo "FAIL: gofmt required" >&2; exit 1; }
go test -count=1 -timeout=5m ./...
go test -race -count=1 -timeout=5m ./...
go vet ./...
go build ./...
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$out/runtime-amd64" ./cmd/runtime
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o "$out/runtime-arm64" ./cmd/runtime
if [[ "${1:-}" == "--images" ]]; then
  command -v docker >/dev/null || { echo "BLOCKED: Docker/buildx required" >&2; exit 2; }
  docker buildx version
  image_flags=(--load)
  if [[ -n "${HOUSEFOLD_BUILD_CA:-}" ]]; then
    [[ -f "$HOUSEFOLD_BUILD_CA" ]] || { echo "BLOCKED: HOUSEFOLD_BUILD_CA file unavailable" >&2; exit 2; }
    image_flags+=(--secret "id=proxy_ca,src=$HOUSEFOLD_BUILD_CA")
  fi
  docker buildx build "${image_flags[@]}" --platform linux/amd64 --build-arg BUILD_ARCH=amd64 -f Dockerfile -t housefold-runtime:agent-amd64 .
  docker buildx build "${image_flags[@]}" --platform linux/arm64 --build-arg BUILD_ARCH=aarch64 -f Dockerfile -t housefold-runtime:agent-arm64 .
fi
echo "PASS: requested checks completed; HAOS/hardware validation is separate"
