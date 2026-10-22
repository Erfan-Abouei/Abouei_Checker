#!/bin/sh
# Enforce a per-package statement coverage floor from a Go coverage profile.
# Usage: check-package-coverage.sh <profile> <minimum> <pkg-substring>...
# A package matches when its import path contains one of the substrings.
set -eu

profile="${1:-coverage.out}"
minimum="${2:-80}"
shift 2 || true

if [ ! -f "$profile" ]; then
	echo "coverage profile not found: $profile" >&2
	exit 1
fi

# shellcheck disable=SC2068
for want in $@; do
	result="$(awk -v want="$want" '
		NR == 1 { next }
		{
			file = $1
			sub(/:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/, "", file)
			if (index(file, want) == 0) { next }
			statements = $2
			covered = ($3 > 0) ? statements : 0
			total += statements
			hit += covered
		}
		END {
			if (total == 0) { printf "MISSING"; exit 0 }
			printf "%.1f", (hit * 100.0) / total
		}' "$profile")"
	if [ "$result" = "MISSING" ]; then
		echo "no coverage data for package matching: $want" >&2
		exit 1
	fi
	printf 'Coverage %s: %s%% (minimum: %s%%)\n' "$want" "$result" "$minimum"
	if ! awk -v actual="$result" -v minimum="$minimum" 'BEGIN { exit !(actual + 0 >= minimum + 0) }'; then
		echo "coverage $result% for $want is below the required $minimum%" >&2
		exit 1
	fi
done
