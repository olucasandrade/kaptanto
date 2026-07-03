#!/usr/bin/env bash
# coverage-gate.sh — single source of truth for the coverage gate.
#
# Both `make cover` (local) and .github/workflows/coverage.yml (CI) invoke
# this script against the same profile. Edit thresholds and exclusions HERE
# ONLY — do not duplicate them in the Makefile or the workflow, or local and
# CI will drift again.
#
# Usage: scripts/coverage-gate.sh [coverage.out]
#
# Two checks, both must pass:
#   1. Aggregate: filtered total coverage >= COVERAGE_THRESHOLD.
#   2. Per-package floor: every gated (non-excluded) package's own coverage
#      >= PER_PACKAGE_FLOOR. Catches a package collapsing toward zero while
#      packages already at 100% (event, logging, observability) keep the
#      aggregate passing.
set -euo pipefail

PROFILE="${1:-coverage.out}"

if [[ ! -f "$PROFILE" ]]; then
  echo "coverage-gate: profile not found: $PROFILE" >&2
  exit 1
fi

# ---- Config -----------------------------------------------------------

# Aggregate floor over the filtered (unit-testable) profile.
COVERAGE_THRESHOLD="${COVERAGE_THRESHOLD:-70.0}"

# ERE of profile lines excluded from BOTH the aggregate and the per-package
# floor below. These packages need live services or a running binary:
#   - ha (advisory-lock leader election), source/postgres, source/mongodb:
#     need a live Postgres/MongoDB instance to exercise meaningfully.
#   - cluster: has real unit tests (27.2% as of 2026-07), but its
#     Postgres-backed integration path is gated on TEST_CLUSTER_DSN, which
#     is unset in CI today. No compensating live-cluster run currently
#     exists, so it stays excluded rather than gated at a number nothing
#     backs up.
#   - output/rabbitmq: has real unit tests against a mocked broker (35.7%
#     as of 2026-07), but no integration/e2e job exercises a live RabbitMQ
#     instance today. Same reasoning as cluster.
#   - cmd/: the e2e job exercises the real compiled binary instead.
#   - generated *.pb.go and the main() entrypoint.
#
# Once the Integration fix plan wires TEST_CLUSTER_DSN and a RabbitMQ
# container in CI, revisit cluster and output/rabbitmq: either fold them
# into the gated set at an honest floor, or update this comment to point at
# the live job that now covers them. Until then, this comment should not
# claim compensating coverage that doesn't exist.
COVERAGE_EXCLUDE="${COVERAGE_EXCLUDE:-internal/(ha|source/postgres|source/mongodb|output/rabbitmq|cluster|cmd)/|cmd/kaptanto/main\.go|\.pb\.go:}"

# Per-package floor (percent), applied to every package NOT matched by
# COVERAGE_EXCLUDE above — i.e. the same gated set the aggregate covers.
# Deliberately conservative: as of 2026-07 the weakest gated packages are
# checkpoint (38.5%), backfill (40.9%) and output/grpc (45.5%). Set the
# floor here — not just in a comment — so raising it later is a visible
# diff. Ratchet upward as the Unit fix plan raises those packages.
PER_PACKAGE_FLOOR="${PER_PACKAGE_FLOOR:-35.0}"

# ---- Step 1: filter the profile ----------------------------------------

FILTERED="${PROFILE%.out}.filtered.out"
head -1 "$PROFILE" > "$FILTERED"
tail -n +2 "$PROFILE" | grep -vE "$COVERAGE_EXCLUDE" >> "$FILTERED" || true

# ---- Step 2: aggregate total --------------------------------------------

total=$(go tool cover -func="$FILTERED" | awk '/^total:/ {gsub(/%/,"",$3); print $3}')
echo "Unit-testable coverage: ${total}% (threshold: ${COVERAGE_THRESHOLD}%)"

gate_fail=0
awk -v t="$total" -v min="$COVERAGE_THRESHOLD" \
  'BEGIN { if (t+0 < min+0) { printf "FAIL: coverage %.1f%% is below threshold %.1f%%\n", t, min; exit 1 } else { printf "PASS: coverage %.1f%% meets threshold %.1f%%\n", t, min } }' \
  || gate_fail=1

# ---- Step 3: per-package floor ------------------------------------------

echo ""
echo "Per-package floor (>= ${PER_PACKAGE_FLOOR}%):"
tail -n +2 "$FILTERED" | awk -v floor="$PER_PACKAGE_FLOOR" '
{
  # profile line: file:startline.col,endline.col numstmt count
  split($1, a, ":")
  file = a[1]
  n = split(file, parts, "/")
  pkg = ""
  for (i = 1; i < n; i++) pkg = (pkg == "" ? parts[i] : pkg "/" parts[i])
  stmts = $2
  count = $3
  total_stmts[pkg] += stmts
  if (count + 0 > 0) covered_stmts[pkg] += stmts
}
END {
  fail = 0
  for (pkg in total_stmts) {
    pct = (total_stmts[pkg] > 0) ? (covered_stmts[pkg] * 100.0 / total_stmts[pkg]) : 0
    status = (pct + 0 < floor + 0) ? "FAIL" : "PASS"
    if (status == "FAIL") fail = 1
    printf "  %-6s %5.1f%%  %s\n", status, pct, pkg
  }
  exit fail
}' | sort -k3 || gate_fail=1

exit $gate_fail
