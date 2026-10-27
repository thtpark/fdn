#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
suite=${1:-all}
case "$suite" in
  static)
    test -z "$(gofmt -l guidance.go guidance_test.go cmd internal tests tools)"
    go mod verify
    go run ./tools/protocol --check
    go vet ./...
    go build ./cmd/...
    for target in linux/amd64 linux/arm64 darwin/arm64; do
      CGO_ENABLED=0 GOOS="${target%/*}" GOARCH="${target#*/}" go build ./cmd/...
    done
    bash -n scripts/test.sh
    sh -n scripts/install.sh
    for script in scripts/dev-agent.sh scripts/agent-cli.sh scripts/agent_service.sh scripts/agent-identity.sh; do sh -n "$script"; done
    ;;
  unit) go test -race -count=1 . ./internal/... ./tools/... ./tests/scenario ./tests/fixtureprovider ;;
  integration) go test -race -count=1 -tags=integration ./tests/integration ;;
  e2e) go test -count=1 -tags=e2e ./tests/e2e ;;
  stress) go test -race -count=1 -tags=stress ./tests/stress ;;
  all)
    for selected in static unit integration e2e stress; do bash scripts/test.sh "$selected"; done
    ;;
  *) printf '%s\n' 'Use static, unit, integration, e2e, stress, or all.' >&2; exit 2 ;;
esac
