#!/bin/sh
# Run the command package's tests as parallel domain shards.
#
# The suite in ./cmd is ~36s of which prepare alone is 21s, so one test binary is
# slow no matter how many tests it holds. The domains below are disjoint name
# prefixes and run as separate processes; the last shard ("core") takes whatever
# the prefixes did not match, using -skip, so a new test always runs exactly once:
# it either matches a domain or lands in core.
#
# Patterns are RE2 (go test -run/-skip), so no lookahead. Keep them plain prefixes,
# and keep them mutually exclusive -- a name matching two shards runs twice.
#
# Usage: scripts/test-cmd-domains.sh [go test flags, e.g. -short -race]
set -eu

cd "$(dirname "$0")/.."

domains='prepare-bias|^TestPrepareBias
prepare-dark|^TestPrepareDark
prepare-camera|^TestPrepareCamera
prepare-flats|^TestPrepareFlats
prepare-session|^TestPrepareSession
prepare-routing|^TestPrepareRouting
prepare-reports|^TestPrepareReports
prepare-run|^TestPrepare(DryRun|SkipExisting|Plan)
cache|^TestCache|^TestWriteSuggestion|^TestRenderSuggestion|^TestOpenMeasure|^TestPrepareCache
analyze|^TestAnalyze|^TestRunAnalyze|^TestRunAnalyse
camera|^TestCamera|^TestG4|^TestASI|^TestDetectCamera|^TestResolveCamera|^TestParseFilename|^TestFlatFilter|^TestIndexFlats
sessions|^TestSession|^TestProfile|^TestAttachSession|^TestWriteSessions|^TestStackGate|^TestStackRef|^TestPrintSession
suggest|^TestSuggest|^TestSimulate|^TestCountRejections|^TestGates|^TestOverride|^TestPinned|^TestExplicit
transparency|^TestTransparency
cloud|^TestCloud|^TestApplyRules|^TestApplyHalo|^TestApplyStructure|^TestPercentile
fwhm|^TestFWHM|^TestApplyFWHM
config|^TestConfig|^TestApplyConfig|^TestKnownConfig|^TestUnknownKey|^TestCompiledDefaults|^TestExcluded|^TestSections
workers|^TestWorker|^TestResolveWorkers|^TestPlan|^TestPhysical'

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# The union of every domain pattern, so core can skip them all.
skip=""
for row in $domains; do
	pattern=${row#*|}
	skip="${skip:+$skip|}$pattern"
done

# Guard: a test matching two domain patterns would run twice, once per shard.
# A test matching none is fine -- core picks it up by design.
printf '%s\n' $domains | sed 's/^[^|]*|//' >"$tmp/patterns"
go test ./cmd -list '^Test' | grep '^Test' >"$tmp/tests"
if ! awk '
	NR == FNR { pattern[++n] = $0; next }
	{
		hits = 0
		for (i = 1; i <= n; i++) {
			if ($0 ~ pattern[i]) {
				hits++
			}
		}
		if (hits > 1) {
			print "matched by " hits " domains: " $0
			bad = 1
		}
	}
	END { exit bad }
' "$tmp/patterns" "$tmp/tests"; then
	echo "shard patterns overlap; a test would run twice" >&2
	exit 1
fi

pids=""
names=""

# start shard NAME ARGS...
start() {
	name=$1
	shift
	go test "$@" "./cmd" >"$tmp/$name.log" 2>&1 &
	pids="$pids $!"
	names="$names $name"
}

for row in $domains; do
	start "${row%%|*}" -run "${row#*|}" "$@"
done
start core -run '^Test' -skip "$skip" "$@"

# The internal packages are microseconds each; one process is enough.
go test "$@" ./internal/... >"$tmp/internal.log" 2>&1 &
pids="$pids $!"
names="$names internal"

fail=0
for pid in $pids; do
	if ! wait "$pid" >/dev/null 2>&1; then
		fail=1
	fi
done

# Print in domain order, not completion order. A passing shard gets a line; a
# failing one gets its whole transcript, which is the only part worth reading.
for name in $names; do
	if grep -qE '^(FAIL|--- FAIL)' "$tmp/$name.log"; then
		echo "--- $name"
		cat "$tmp/$name.log"
		continue
	fi
	line=$(grep -hE '^(ok|FAIL)' "$tmp/$name.log")
	printf '  %-18s %s\n' "$name" "${line:-no result}"
done

exit "$fail"
