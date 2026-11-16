#!/bin/sh
# Run the command package's tests as parallel domain shards.
#
# The suite in ./cmd is ~36s of which prepare alone is 21s, so one test binary is
# slow no matter how many tests it holds. The domains below are disjoint name
# prefixes and run as separate processes; the last shard ("core") takes whatever
# the prefixes did not match, using -skip, so a new test always runs exactly once:
# it either matches a domain or lands in core.
#
# Each shard is formatted on its own and announced on a FIFO when it finishes, so
# the report streams: a domain appears the moment it completes, and a failure
# appears as it happens rather than at the end. GOTEST_MERGE=1 collects the shards
# and prints one consolidated report instead, which is easier to grep in CI.
#
# Patterns are RE2 (go test -run/-skip), so no lookahead. Keep them plain prefixes,
# and keep them mutually exclusive -- a name matching two shards runs twice, which
# the guard below refuses.
#
# Usage: scripts/test-cmd-domains.sh [go test flags, e.g. -short -race]
#   GOTEST=gotestsum            formatter to report through (default gotestsum)
#   GOTEST_FORMAT=testdox       its --format, e.g. testdox for make test-verbose
#   GOTEST_MERGE=1              one report at the end instead of streaming
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
merge=${GOTEST_MERGE:-0}
started=$(date +%s)

tmp=$(mktemp -d)
mkdir "$tmp/done"
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
awk '
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
: >"$tmp/expected"
for row in $domains; do
	idx=$((idx + 1))
	printf '%s %s\n' "${row%%|*}" "$(grep -c "^prepare-$idx$" "$tmp/expected.raw" || true)" >>"$tmp/expected"
done
printf 'core %s\n' "$(grep -c '^core$' "$tmp/expected.raw" || true)" >>"$tmp/expected"

has_runner=0
if command -v "$runner" >/dev/null 2>&1; then
	has_runner=1
fi

# shard_elapsed NAME prints the shard's own run time, which under a common start is
# its arrival time. Used to order the consolidated report.
shard_elapsed() {
	awk '
		/"Action":"(pass|fail)"/ && /"Package":/ && !/"Test":/ {
			if (match($0, /"Elapsed":[0-9.]+/)) {
				last = substr($0, RSTART + 10, RLENGTH - 10)
			}
		}
		END { printf "%.3f", last + 0 }
	' "$tmp/$1.raw"
}

# count_tests NAME prints the number of test events the shard produced, subtests
# included, which is what a single gotestsum run counts.
count_tests() {
	awk '
		/"Action":"pass"/ && /"Test":/ { n++ }
		END { print n + 0 }
	' "$tmp/$1.raw"
}

# print_shard NAME emits one shard's report. With a formatter it is the file that
# shard produced; without one it is a compact line, plus the output of anything
# that failed.
print_shard() {
	name=$1
	if [ -f "$tmp/$name.report" ]; then
		cat "$tmp/$name.report"
		return
	fi
	fails=$(grep -c '"Action":"fail","Test"' "$tmp/$name.raw" || true)
	printf '  %-18s %s tests' "$name" "$(count_tests "$name")"
	if [ "$fails" -gt 0 ]; then
		printf ', %s FAILED\n' "$fails"
		grep '"Action":"output"' "$tmp/$name.raw" |
			sed 's/.*"Output":"//; s/\\n"}$//' | grep -vE '^(ok |=== RUN)' || true
	else
		printf ' ok\n'
	fi
}

shards=0
pids=""
names=""
fail=0

# start NAME LABEL PKG ARGS...
#   LABEL is "cmd" for a command shard, "internal" for the package group; it
#   decides how the report labels the shard.
start() {
	name=$1
	label=$2
	pkg=$3
	shift 3
	(
		# bash runs the parent's EXIT trap in a subshell too, which would delete
		# the working directory out from under the other shards.
		trap - 0
		set +e

		go test -json "$@" "$pkg" >"$tmp/$name.raw" 2>&1

		if [ "$has_runner" = 1 ]; then
			if [ "$label" = internal ]; then
				cp "$tmp/$name.raw" "$tmp/$name.json"
			else
				sed "s#\"Package\":\"garfield/cmd\"#\"Package\":\"garfield/cmd ($name)\"#g" \
					"$tmp/$name.raw" >"$tmp/$name.json"
			fi
			if [ -n "$format" ]; then
				set -- -f "$format"
			else
				set --
			fi
			"$runner" "$@" --raw-command -- cat "$tmp/$name.json" >"$tmp/$name.full" 2>&1
			grep -E '^DONE [0-9]+ tests' "$tmp/$name.full" |
				sed 's/^DONE \([0-9]*\) tests.*/\1/' >"$tmp/$name.count" || true
			grep -vE '^DONE [0-9]+ tests' "$tmp/$name.full" >"$tmp/$name.report"
		else
			count_tests "$name" >"$tmp/$name.count"
		fi
		# The marker is the last thing a shard does: the parent polls for it to
		# report in arrival order. A pipe was tried first and abandoned -- reading
		# from a FIFO returns EINTR when a child exits, which broke the loop.
		: >"$tmp/done/$name"
	) &
	pids="$pids $!"
	printf '%s %s\n' "$!" "$name" >>"$tmp/pids"
	names="$names $name"
	shards=$((shards + 1))
}

for row in $domains; do
	start "${row%%|*}" cmd ./cmd -run "${row#*|}" "$@"
done
start core cmd ./cmd -run '^Test' -skip "$skip" "$@"
# The internal packages are microseconds each; one process is enough.
start internal internal ./internal/... "$@"

# Report as each shard lands: poll the markers, printing each shard once. Reports
# are whole files, so two shards finishing together print back to back rather than
# interleaving mid-line.
arrivals=""
printed=" "
reported=0

report() {
	name=$1
	printed="$printed$name "
	arrivals="$arrivals $name"
	reported=$((reported + 1))
	if [ "$merge" = 0 ]; then
		print_shard "$name"
	fi
}

# pending NAME is false once the shard has been reported.
pending() {
	case "$printed" in
	*" $1 "*) return 1 ;;
	esac
	return 0
}

while [ "$reported" -lt "$shards" ]; do
	for name in $names; do
		pending "$name" || continue
		if [ -f "$tmp/done/$name" ]; then
			report "$name"
		fi
	done
	[ "$reported" -ge "$shards" ] && break

	# Anything still running? A shard's exit status is collected by `wait` below;
	# here we only need to know when to stop polling.
	alive=0
	while read -r pid name; do
		pending "$name" || continue
		if kill -0 "$pid" 2>/dev/null; then
			alive=1
		fi
	done <"$tmp/pids"
	if [ "$alive" = 1 ]; then
		sleep 0.05
		continue
	fi

	# Nothing is running and something never reported: say so rather than hang.
	for name in $names; do
		pending "$name" || continue
		if [ -f "$tmp/done/$name" ]; then
			report "$name"
			continue
		fi
		printf 'shard %s exited without reporting\n' "$name" >&2
		fail=1
		printed="$printed$name "
		reported=$((reported + 1))
	done
	break
done

# Did every shard run what it owns? Counts top-level tests only: subtests carry a
# slash in their name and belong to the parent.
for name in $arrivals; do
	[ "$name" = internal ] && continue
	want=$(awk -v n="$name" '$1 == n { print $2 }' "$tmp/expected")
	got=$(awk '
		/"Action":"(pass|skip|fail)"/ && /"Test":/ {
			if (match($0, /"Test":"[^"]*"/)) {
				t = substr($0, RSTART + 8, RLENGTH - 9)
				if (t !~ /\//) n++
			}
		}
		END { print n + 0 }
	' "$tmp/$name.raw")
	if [ "$want" != "$got" ]; then
		echo "shard $name ran $got test(s), expected $want" >&2
		fail=1
	fi
done

if [ "$merge" = 1 ]; then
	# Internal packages first, then the command domains by arrival.
	{
		for name in $arrivals; do
			[ "$name" = internal ] && continue
			printf '%s %s\n' "$(shard_elapsed "$name")" "$name"
		done
	} | sort -n >"$tmp/order"
	for name in internal $(awk '{ print $2 }' "$tmp/order"); do
		print_shard "$name"
	done
fi

total=0
for name in $arrivals; do
	if [ -f "$tmp/$name.count" ]; then
		total=$((total + $(cat "$tmp/$name.count")))
	fi
done
if [ "$total" -gt 0 ]; then
	echo
	echo "DONE $total tests in $(($(date +%s) - started))s"
fi

exit "$fail"
