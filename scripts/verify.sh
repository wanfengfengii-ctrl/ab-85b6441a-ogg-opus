#!/bin/sh
# One-shot verification entrypoint for the Compose "verify" service.
#
# Steps:
#   1. Go unit tests (the code tests)
#   2. Production build of both binaries
#   3. HTTP smoke suite against the healthy "api" service, including a
#      stream with an audio packet spanning a page boundary
#
# The exit status is the logical OR of every step, so any failure makes the
# one-shot service exit non-zero.
set -u

BASE_URL="${AUDIT_BASE_URL:-http://api:8080}"
rc=0

echo "==> [1/3] go test ./..."
if go test ./...; then
    echo "==> [1/3] tests passed"
else
    echo "==> [1/3] tests FAILED" >&2
    rc=1
fi

echo "==> [2/3] production build"
if CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/opus-audit-server ./cmd/server \
   && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/opus-audit-smoke ./cmd/smoke; then
    echo "==> [2/3] build passed"
else
    echo "==> [2/3] build FAILED" >&2
    rc=1
fi

echo "==> [3/3] HTTP smoke against ${BASE_URL}"
if /tmp/opus-audit-smoke -base "${BASE_URL}"; then
    echo "==> [3/3] smoke passed"
else
    echo "==> [3/3] smoke FAILED" >&2
    rc=1
fi

if [ "${rc}" -eq 0 ]; then
    echo "==> verification: ALL CHECKS PASSED"
else
    echo "==> verification: FAILURES DETECTED (exit ${rc})" >&2
fi
exit "${rc}"
