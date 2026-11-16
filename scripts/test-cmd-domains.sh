#!/bin/sh
# Run the command package's tests as parallel domain shards, and report them as one
# gotestsum run.
#
# The suite in ./cmd is ~36s of which prepare alone is 21s, so one test binary is
# slow no matter how many tests it holds. The domains below are disjoint name
# prefixes and run as separate processes; the last shard ("core") takes whatever
# the prefixes did not match, using -skip, so a new test always runs exactly once:
# it either matches a domain or lands in core.
#
# Patterns are RE2 (go test -run/-skip), so no lookahead. Keep them plain prefixes,
# and keep them mutually exclusive -- a name matching two shards runs twice, which
# the guard below refuses.
#
# Usage: scripts/test-cmd-domains.sh [go test flags, e.g. -short -race]
#   GOTEST=gotestsum            formatter to report through (default gotestsum)
#   GOTEST_FORMAT=testdox       its --format, e.g. testdox for make test-verbose
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

runner=${GOTEST:-gotestsum}
format=${GOTEST_FORMAT:-}

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

# Expected top-level count per shard, from the list the guard already built. A
# shard that runs fewer tests than it owns has silently skipped some -- or run
# none at all, which a malformed `go test` invocation does while still exiting 0.
printf '%s\n' $domains >"$tmp/rows"
awk -v skip="$skip" '
	NR == FNR { pattern[++n] = $0; next }
	{
		for (i = 1; i <= n; i++) {
			if ($0 ~ pattern[i]) {
				print "prepare-" i
				next
			}
		}
		print "core"
	}
' "$tmp/patterns" "$tmp/tests" >"$tmp/expected.raw"

idx=0
for row in $domains; do
	idx=$((idx + 1))
	name=${row%%|*}
	printf '%s %s\n' "$name" "$(grep -c "^prepare-$idx$" "$tmp/expected.raw" || true)" >>"$tmp/expected"
done
printf 'core %s\n' "$(grep -c '^core$' "$tmp/expected.raw" || true)" >>"$tmp/expected"

pids=""
names=""

# start NAME ARGS...
start() {
	name=$1
	shift
	go test -json "$@" ./cmd >"$tmp/$name.raw" 2>&1 &
	pids="$pids $!"
	names="$names $name"
}

for row in $domains; do
	start "${row%%|*}" -run "${row#*|}" "$@"
done
start core -run '^Test' -skip "$skip" "$@"

# The internal packages are microseconds each; one process is enough.
go test -json "$@" ./internal/... >"$tmp/internal.raw" 2>&1 &
pids="$pids $!"
names="$names internal"

fail=0
for pid in $pids; do
	# The exit status of the test binary, which is the only reliable verdict: a
	# formatter's status says nothing about the tests it was handed.
	if ! wait "$pid" >/dev/null 2>&1; then
		fail=1
	fi
done

# Did every shard run what it owns? Counts top-level tests only: subtests carry a
# slash in their name and belong to the parent.
short=""
for name in $names; do
	[ "$name" = internal ] && continue
	want=$(awk -v n="$name" '$1 == n { print $2 }' "$tmp/expected")
	got=$(awk '
		/"Action":"(pass|skip|fail)"/ && /"Test":"/ {
			if (match($0, /"Test":"[^"]*"/)) {
				t = substr($0, RSTART + 8, RLENGTH - 9)
				if (t !~ /\//) {
					n++
				}
			}
		}
		END { print n + 0 }
	' "$tmp/$name.raw")
	if [ "$want" != "$got" ]; then
		short="$short $name(want $want, saw $got)"
	fi
done
if [ -n "$short" ]; then
	echo "shards ran the wrong number of tests:$short" >&2
	fail=1
fi

# Label each shard's events with the domain, so one merged report names the shard
# instead of printing "garfield/cmd" nineteen times.
cp "$tmp/internal.raw" "$tmp/internal.json"
for name in $names; do
	[ "$name" = internal ] && continue
	sed "s#\"Package\":\"garfield/cmd\"#\"Package\":\"garfield/cmd ($name)\"#g" \
		"$tmp/$name.raw" >"$tmp/$name.json"
done

# Report order: the internal packages first, then the command domains in the order
# they actually finished. Every shard starts within milliseconds of the others, so
# its own elapsed time is its arrival time; sorting by it prints the quick domains
# before prepare and core, which is what a live report would have shown.
elapsed() {
	awk '
		/"Action":"(pass|fail|skip)"/ && /"Package":/ && !/"Test":/ {
			if (match($0, /"Elapsed":[0-9.]+/)) {
				last = substr($0, RSTART + 10, RLENGTH - 10)
			}
		}
		END { printf "%.3f", last + 0 }
	' "$1"
}

{
	for name in $names; do
		[ "$name" = internal ] && continue
		printf '%s %s\n' "$(elapsed "$tmp/$name.json")" "$name"
	done
} | sort -n >"$tmp/order"

files="$tmp/internal.json"
ordered="internal"
while read -r _ name; do
	files="$files $tmp/$name.json"
	ordered="$ordered $name"
done <"$tmp/order"

if command -v "$runner" >/dev/null 2>&1; then
	if [ -n "$format" ]; then
		"$runner" -f "$format" --raw-command -- cat $files || true
	else
		"$runner" --raw-command -- cat $files || true
	fi
else
	# No formatter: count the events ourselves. A passing shard gets a line; a
	# failing one gets every failing test and its output.
	for name in $ordered; do
		passes=$(awk '
			/"Action":"pass"/ && /"Test":"/ {
				if (match($0, /"Test":"[^"]*"/)) {
					t = substr($0, RSTART + 8, RLENGTH - 9)
					if (t !~ /\//) n++
				}
			}
			END { print n + 0 }
		' "$tmp/$name.json")
		fails=$(grep -c '"Action":"fail","Test"' "$tmp/$name.json" || true)
		printf '  %-18s %s tests' "$name" "$passes"
		if [ "$fails" -gt 0 ]; then
			printf ', %s FAILED\n' "$fails"
			grep '"Action":"output"' "$tmp/$name.json" |
				sed 's/.*"Output":"//; s/\\n"}$//' | grep -v '^ok ' || true
		else
			printf ' ok\n'
		fi
	done
fi

exit "$fail"
