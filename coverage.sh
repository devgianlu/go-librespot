#!/usr/bin/env bash
# Report test coverage over hand-written code.
#
# Generated code (protobuf, the OpenAPI server, mockery mocks) is excluded: it
# is thousands of untested statements that swamp the number and that nobody is
# going to write tests for. Counting it puts the project at ~7% when the code
# people actually maintain is far better covered.
#
#   ./coverage.sh          # per-package, least covered first, plus the totals
#   ./coverage.sh -func    # per-function detail
#   ./coverage.sh -zero    # only the functions with no coverage at all
#   ./coverage.sh -html    # browser report
set -euo pipefail

PROFILE="${COVERPROFILE:-coverage.out}"
FILTERED="$PROFILE.filtered"

go test -count=1 -tags "test_unit test_integration" \
    -covermode=atomic -coverprofile="$PROFILE" ./... >/dev/null

# Keep the mode line, drop every generated file.
head -n 1 "$PROFILE" > "$FILTERED"
grep -v -E '/proto/|\.pb\.go:|/mock_[A-Za-z]+\.go:|/api_gen\.go:' "$PROFILE" | tail -n +2 >> "$FILTERED"

# Per-package coverage, weighted by statement count. Averaging the per-function
# percentages instead would count a one-line helper the same as a 200 statement
# function, which is how you get a number nobody can act on.
#
# Profile lines are "<file>:<range> <numStmt> <count>".
per_package() {
    awk 'NR > 1 {
        split($1, part, ":")
        pkg = part[1]
        sub(/\/[^\/]*$/, "", pkg)
        sub(/^github.com\/devgianlu\/go-librespot\/?/, "", pkg)
        if (pkg == "") pkg = "(root)"

        stmts[pkg] += $2
        if ($3 > 0) covered[pkg] += $2
    } END {
        for (pkg in stmts)
            printf "%6.1f%%  %6d/%-6d  %s\n", \
                covered[pkg] * 100 / stmts[pkg], covered[pkg], stmts[pkg], pkg
    }' "$FILTERED" | sort -n
}

case "${1:-}" in
    -func) go tool cover -func="$FILTERED" ;;
    -html) go tool cover -html="$FILTERED" ;;
    -zero) go tool cover -func="$FILTERED" | awk '$NF == "0.0%"' ;;
    *)
        printf '%8s  %13s  %s\n' COVER STMTS PACKAGE
        per_package
        echo
        printf 'total (hand-written):        %s\n' \
            "$(go tool cover -func="$FILTERED" | tail -1 | awk '{print $3}')"
        printf 'total (including generated): %s\n' \
            "$(go tool cover -func="$PROFILE" | tail -1 | awk '{print $3}')"
        ;;
esac
