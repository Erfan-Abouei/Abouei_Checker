#!/bin/sh
# Enforce the Phase 0 coverage baseline without rewarding meaningless tests.
set -eu

profile="${1:-coverage.out}"
minimum="${COVERAGE_MIN:-60}"

if [ ! -f "$profile" ]; then
	echo "coverage profile not found: $profile" >&2
	exit 1
fi

actual="$(go tool cover -func="$profile" | awk '/^total:/ {gsub(/%/, "", $3); print $3}')"

if [ -z "$actual" ]; then
	echo "could not read total coverage from $profile" >&2
	exit 1
fi

printf 'Coverage: %s%% (minimum: %s%%)\n' "$actual" "$minimum"

if ! awk -v actual="$actual" -v minimum="$minimum" 'BEGIN { exit !(actual + 0 >= minimum + 0) }'; then
	echo "coverage $actual% is below the required $minimum%" >&2
	exit 1
fi
