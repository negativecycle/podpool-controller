#!/usr/bin/env bash
#
# Checks out each point in the series and proves it builds and passes its own
# tests. This is what makes docs/tutorial-v2.html a tested document rather
# than a plausible one: a milestone that does not compile is a milestone a
# reader cannot follow.
#
# Usage, from a clean tree on the tutorial-v2 branch:
#
#     hack/verify-tutorial-steps.sh                      # tagged milestones
#     hack/verify-tutorial-steps.sh --all-commits        # every commit
#     hack/verify-tutorial-steps.sh t2- --all-commits    # a different series
#
# --all-commits starts at the first tag rather than at the repository root,
# and the reason is in the milestone it skips. The root commit is empty by
# design, so there is no module to build. The kubebuilder scaffold that
# follows cannot pass its own tests either: `kubebuilder create api` writes Go
# markers rather than YAML, so the generated suite points at a
# config/crd/bases that does not exist yet and sets ErrorIfCRDPathMissing.
# Milestone zero exists to settle exactly those debts, so verification begins
# where the series first claims to be green.
#
# Requires envtest assets. `make setup-envtest` puts them in bin/k8s/.
set -uo pipefail

cd "$(dirname "$0")/.."

PREFIX=milestone-
ALL_COMMITS=0

for arg in "$@"; do
    case "$arg" in
        --all-commits) ALL_COMMITS=1 ;;
        -h|--help) sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        -*) echo "verify-tutorial-steps: unknown option $arg" >&2; exit 2 ;;
        *) PREFIX="$arg" ;;
    esac
done

if ! git diff --quiet || ! git diff --cached --quiet; then
    echo "verify-tutorial-steps: working tree is dirty, refusing to check out anything" >&2
    exit 1
fi

ORIGINAL_REF=$(git symbolic-ref --quiet --short HEAD || git rev-parse HEAD)
restore() { git checkout --quiet "$ORIGINAL_REF"; }
trap restore EXIT

if [ -z "${KUBEBUILDER_ASSETS:-}" ]; then
    # -L so a symlinked bin/ is followed rather than skipped.
    ASSETS=$(find -L "$PWD/bin/k8s" -maxdepth 1 -mindepth 1 -type d 2>/dev/null | head -1)
    if [ -z "$ASSETS" ]; then
        echo "verify-tutorial-steps: no envtest assets; run 'make setup-envtest'" >&2
        exit 1
    fi
    export KUBEBUILDER_ASSETS="$ASSETS"
fi

TAGS=$(git tag --list "${PREFIX}*" | sort)
if [ -z "$TAGS" ]; then
    echo "verify-tutorial-steps: no ${PREFIX}* tags found" >&2
    exit 1
fi

FIRST=$(echo "$TAGS" | head -1)
LAST=$(echo "$TAGS" | tail -1)

if [ "$ALL_COMMITS" -eq 1 ]; then
    REFS=$(printf '%s\n%s\n' \
        "$(git rev-parse "${FIRST}^{commit}")" \
        "$(git rev-list --reverse "${FIRST}^{commit}..${LAST}^{commit}")")
    echo "verifying every commit from $FIRST through $LAST"
else
    REFS="$TAGS"
    echo "verifying tagged milestones ${FIRST} through ${LAST}"
fi

echo

failed=0
checked=0

for ref in $REFS; do
    label=$(git describe --tags --exact-match "$ref" 2>/dev/null || git rev-parse --short "$ref")
    printf '%-14s ' "$label"

    if ! git checkout --quiet "$ref"; then
        echo "CHECKOUT FAILED"
        failed=1
        continue
    fi

    # Nothing to prove about a commit that carries no module. The empty root
    # is the only one in practice, and --all-commits skips past it anyway;
    # this keeps the script honest if a series ever starts differently.
    if [ ! -f go.mod ]; then
        echo "skipped (no module)"
        continue
    fi

    checked=$((checked + 1))

    if ! out=$(go build ./... 2>&1); then
        echo "BUILD FAILED"
        echo "$out" | sed 's/^/    /'
        failed=1
        continue
    fi

    # The build-tagged suites (e2e, kwok) need a cluster and are excluded from
    # the default build, so this compiles and runs everything else.
    if ! out=$(go test ./... 2>&1); then
        echo "TEST FAILED"
        echo "$out" | tail -20 | sed 's/^/    /'
        failed=1
        continue
    fi

    count=$(echo "$out" | grep -c '^ok ')
    echo "ok (build + $count test packages)"
done

echo

if [ "$failed" -ne 0 ]; then
    echo "verify-tutorial-steps: at least one point in the series is broken"
    exit 1
fi

noun=points
[ "$checked" -eq 1 ] && noun=point

echo "verify-tutorial-steps: $checked $noun build and pass"
