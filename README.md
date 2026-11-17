<div align="center">

# garfield

Quality analysis and session preparation for FITS acquisition trees.

Garfield measures what is actually on your sensor, decides which frames are
worth keeping, and reorganises a night's acquisition into a tree that
PixInsight's **WBPP** imports in one step.

</div>

---

## What garfield does

Two commands, one shared quality gate.

**`analyze`** takes any directory of FITS frames and reports, per frame: stars
detected, FWHM, eccentricity, SNR and a score. Its job is measurement — you run
it to see what your thresholds would do before committing a whole night's data
to them.

**`prepare`** takes one target from an acquisition tree and builds a working
tree from it. It runs the *same* analysis on every light frame, routes each one
by verdict, attaches the flats and master calibration frames that belong to that
session, and writes two CSV reports. The output is shaped for WBPP.

The shared thresholds are what make the two agree. Both commands call the same
code with the same `qualityThresholds`, so a frame `analyze` calls `APPROVED` is
the frame `prepare` copies into `lights/`. That is enforced by construction, not
by convention.

```
analyze  →  tune thresholds against a sample  →  prepare  →  WBPP
```

`prepare` is the strict one. It imposes a directory and filename convention and
drops anything that does not match it, sometimes without comment. The full
contract is in [`docs/filestructure.md`](docs/filestructure.md); this document
is the orientation.

---

## Build

Go 1.23 or later.

```sh
make build          # → bin/garfield
go build -o bin/garfield .   # equivalent, without version stamping
make test           # gofmt, golangci-lint, then the test suite (sharded, see below)
```

`make build` stamps the version from `git describe --tags`, so
`garfield version` reports something meaningful on a tagged build and `dev`
otherwise.

`make test` runs the command package's tests as parallel domain shards
(`scripts/test-cmd-domains.sh`): prepare alone is two thirds of the suite, so one
binary is slow however many tests it holds. The domains are the name prefixes in
that script's table, and the last shard takes whatever they did not match, so a new
test always runs exactly once. Two guards make that safe: a test matching two
prefixes is refused (it would run twice), and a shard that runs fewer tests than it
owns fails the run (a malformed invocation otherwise reports success with zero
tests). Wall time is ~14 s against ~36 s single-threaded, and `make test-race` drops
from 6 minutes to 1.5.

The report **streams**: each domain is formatted on its own and printed the moment
it finishes, so a failure appears as it happens rather than at the end. Shards are
printed as contiguous lines, with the per-shard summary and its blank line stripped. The domain
is in the package label — `✓ cmd (cloud) (1.2s)` first, `✓ cmd (core) (13.5s)` last
— which is the order the shards actually completed in, and the internal packages
usually lead because they are the quickest.

`GOTEST_MERGE=1 make test` waits and prints one consolidated report instead, with
the internal packages first and the command domains in completion order; that is
the shape to prefer in a CI log, where one summary block greps better than
interleaved arrivals. `make test-verbose` streams `testdox`, grouping each domain's
tests under its own header.

---

## Configuration

`conf/garfield.yaml` is optional — without it the compiled-in defaults apply.
Each key is `<section>.<flag-name>`, and **a flag on the command line always
wins over the file**.

```yaml
thresholds:
  min-snr: 11.0
  max-fwhm: 3.2
  max-ecc: 0.64
  min-score: 2.0
  min-stars: 380
  max-fwhm-sigma: 4.0  # FWHM outliers above the filter's median; 0 disables
  max-halo-ratio: 2.0  # session-relative bright-star halo; 0 disables
  max-structure: 1.12  # session-relative sky structure (bad seeing); 0 disables
  min-transparency: 0.80  # contrast against frames at a similar airmass; 0 disables
  sun-below: 15.0      # reject frames taken with the Sun less than this far below the horizon; 0 disables
  moffat: true         # Moffat PSF fit (slower, FWHM matches PixInsight); false = Gaussian

concurrency:
  workers: 0        # 0 = NumCPU/2
  conv-workers: 0   # 0 = core count

prepare:
  input: /Volumes/Dyno/Kosmodrom
  output: /Volumes/T7/Workspace
  camera: auto      # auto (INSTRUME), c3 (rotator + gain), g4 (fixed angle, no gain) or asi2600
  flat-count: 0     # 0 = the session's own date only

analyze:
  dir: images
  recursive: false
```

The `thresholds` block is not a preference. It is the decision about which
frames get destroyed, and it applies to both commands at once. `--max-ecc` was
raised to 0.64 during development because frames just above the previous bound
were being discarded — treat any change here as a decision, not a tweak.

Point at a different file with `--config path/to/garfield.yaml`. A `--config`
path that does not exist is an error rather than a silent fallback.

---

## `garfield analyze`

Walks a directory, detects `.fit`/`.fits`/`.fts`, and computes FWHM,
eccentricity, SNR and score for each frame.

```
score = snr / fwhm × (1 − ecc)
```

**Shape comes from a PSF fit**, not from image moments. Each measured star is
fitted with an elliptical profile whose background is a free parameter; FWHM is
the width of the fitted half-maximum contour and eccentricity is
`sqrt(1 − (b/a)²)` of its axes.

Two profiles are available, selected by `--moffat`, which is **on by default**:

| mode | profile | cost | FWHM on `data/6_files.csv` |
|---|---|---|---|
| `--moffat` (default) | Moffat, exponent fitted | ~0.42 ms/star | **1.21–1.45** (PixInsight 1.18–1.31) |
| `--moffat=false` | Gaussian | ~0.05 ms/star | 1.52–1.64 |

A real PSF has broader wings than a Gaussian can express, so a Gaussian fit
returns a width larger than the half-maximum contour actually has — about
0.3 px on these undersampled frames. That is the whole reason the option
exists; it is not a different label on the same number. Eccentricity barely
moves between modes, because it is a ratio of axes and so insensitive to the
wing shape.

The fit matters below about 2 px FWHM. There the PSF spans only a few pixels,
background subtraction and noise clip the moments, and a round star can measure
0.6 while looking perfectly round. Both modes fix that: on an undersampled
1.69 ″/px field they agree with PixInsight to within 0.03 on eccentricity, where
moments were 0.2 high.

Metrics from the two modes are **not comparable**, and neither are scores from
runs made before the fit existed: FWHM reads lower, so
`score = snr/fwhm × (1 − ecc)` reads higher.

### `garfield analyze --help`

```
Walks a directory, detects .fit/.fits/.fts files
and computes FWHM, eccentricity, SNR and score for each.

score = snr/fwhm*(1-ecc)

Example:
  garfield analyze images/
  garfield analyze --dir images/ --limit-computed-stars 500
  garfield analyze --dir images/ --format csv --output results.csv
  garfield analyze --dir images/ --format both --output results.csv --quiet

Usage:
  garfield analyze [dir] [flags]

Aliases:
  analyze, analyse

Flags:
      --cache                      reuse and record measurements in <output>/cache (requires --output)
      --conv-workers int           inner convolution workers (0 = auto) [config: garfield.yaml]
      --copy-rejected              copy rejected frames into <output>/rejected/<category>/
  -d, --dir string                 directory containing the FITS files [config: garfield.yaml] (default "images")
      --display                    show the stored suggestion for --filter, or every stored filter of a --target; reads no frames (requires --output)
      --filter string              only analyse frames of this filter (one of LRGBSHO)
      --format string              output format: console, csv or both (default "console")
  -h, --help                       help for analyze
      --keep float                 fraction of the good stack a --suggest run aims to keep; 0 = the default band
      --limit-computed-stars int   limit the number of bright stars measured (default 500)
      --max-ecc float              maximum eccentricity for APPROVED [config: garfield.yaml] (default 0.64)
      --max-fwhm float             maximum FWHM for APPROVED [config: garfield.yaml] (default 3.2)
      --max-fwhm-sigma float       reject a frame whose FWHM is more than this many robust sigmas above its filter's median; 0 disables [config: garfield.yaml] (default 4)
      --max-halo-ratio float       reject a frame whose bright-star halo exceeds this multiple of its session's; 0 disables [config: garfield.yaml] (default 2)
      --max-structure float        reject a frame that is both softer than its session and unusually structured above this multiple; 0 disables [config: garfield.yaml] (default 1.12)
      --min-score float            minimum score for APPROVED [config: garfield.yaml] (default 2)
      --min-snr float              minimum SNR for APPROVED [config: garfield.yaml] (default 11)
      --min-stars int              minimum number of detected stars for APPROVED [config: garfield.yaml] (default 380)
      --min-transparency float     reject a frame whose contrast is below this fraction of the best of the hour around it; 0 disables [config: garfield.yaml] (default 0.8)
      --moffat                     fit a Moffat PSF profile instead of a Gaussian (slower, FWHM agrees with PixInsight) [config: garfield.yaml] (default true)
  -o, --output string              output directory for the report and copied rejected frames
      --quiet                      suppress console output
  -r, --recursive                  also walk the subdirectories of --dir, 2 levels deep [config: garfield.yaml]
      --stack-gate                 reject frames from a night that is systematically worse than its filter's stack
      --suggest                    suggest thresholds from the measured stack: one filter with --filter, or every filter of a --target (requires --output)
      --sun-below float            reject a frame taken when the Sun was less than this many degrees below the horizon; 0 disables [config: garfield.yaml] (default 15)
  -t, --target string              analyse <input>/Lights/<target>, the same tree prepare reads; implies --recursive
  -w, --workers int                processing workers (0 = auto: NumCPU/2) [config: garfield.yaml]

Global Flags:
      --config string   YAML configuration file (default: conf/garfield.yaml if it exists)
```

### Reading the flags

`analyze [dir]` takes a positional directory, so `garfield analyze images/` and
`garfield analyze -d images/` are the same thing.

| Flag | Note |
|---|---|
| `--limit-computed-stars` | how many of the brightest stars are *measured*. `detectedStars` counts everything found; the metrics come from the measured subset. Lower it for speed while tuning. |
| `-r`, `--recursive` | **off by default.** Only files directly in `--dir`. With `-r` the walk stops at directory depth 2, sized for `Lights/<target>/<date>/<filter>/`. When the cap truncates the walk, `analyze` says so on stderr — a bounded search is not a complete one. |
| `--filter` | one of `L R G B S H O`, case-insensitive. Only frames whose filename carries that filter are analysed, and with `-o` the report is named `analyse_report_<FILTER>.csv`. A `--target` run without it analyses every filter the target holds, one run each. Not a config key. |
| `--format` | `console`, `csv`, or `both`. `csv`/`both` need `-o`. |
| `-o`, `--output` | **a directory**, not a file. The report is written inside it as `analyse_report.csv`, or `analyse_report_<FILTER>.csv` when `--filter` is set or the run is scoped by `--target`. Without it, `--format csv` writes to stdout. |
| `--copy-rejected` | copy every rejected frame into `<output>/rejected/<category>/`, one folder per rejection reason. Requires `-o`. |
| `--display` | print the suggestion stored under `<output>/…/cache/suggestions.jsonl` instead of analysing: one filter with `--filter`, every stored one with `--target`. Reads no frames and needs only `-o`. |
| `-t, --target` | analyse `<input>/Lights/<target>`, the same tree `prepare` reads, and write into `<output>/<target>/metrics/`. Implies `-r`. Cannot be combined with `--dir`. Not a config key. |
| `-i, --input` | acquisition root used with `--target`. Config key `analyze.input`. |
| `--cache` | reuse the measurements stored in `<output>/cache/measurements.jsonl` (`<output>/<target>/metrics/cache/` for a target run), and record this run's. Requires `-o`: the store lives in the output directory, never in the acquisition tree. See [Reusing measurements](#reusing-measurements-between-runs). |
| `--stack-gate` | reject a frame from a night the stack condemns **when the frame itself sits outside the stack band**. Off by default. See [Session quality](#session-quality-and-suggested-thresholds). |
| `--suggest` | derive thresholds from the measured stack and write them to `<output>/suggested_<FILTER>.yaml` plus the store `prepare --suggested` reads. Needs `-o`; with `--filter` it suggests that filter, with `--target` every filter the target holds. Advisory: it never changes this run's verdicts. |
| `--sun-below` | reject a frame taken while the Sun's centre was less than this many degrees below the horizon, before any other gate. Default 15 (between nautical and astronomical twilight); `0` disables. Read from the frame's `SITELAT`/`SITELONG`/`DATE-OBS`, so a header without them is not judged on it. |
| `--keep` | the fraction of the good stack a `--suggest` run aims to keep; `0` uses the band the FWHM rule applies (4 robust sigmas by default). |
| `--quiet` | suppress console output. |
| `-w`, `--conv-workers` | both default to 0 = automatic. |

### Analysing a target

`-t/--target` names the tree the way `prepare` does, so the two commands share one
metrics directory per target:

```
garfield analyze -t LBN527 -i /Volumes/Dyno/Kosmodrom -o /Volumes/T7/Workspace \
                 --filter H --cache --suggest
garfield prepare -t LBN527 -o /Volumes/T7/Workspace --cache --suggested
```

**A target run is per filter.** Without `--filter`, the filters are discovered from
the frames' names and the whole analysis runs once per filter — its own report,
session profile and suggestion each — because every threshold and every population
rule belongs to one filter; a report mixing them would judge O against L's noise.
Filters run in wheel order, `L R G B S H O`, not alphabetically. `--filter`
narrows the run to one of them. A filter whose stack is too thin to suggest from
is a warning — the other filters are still analysed and suggested — and frames
carrying no known filter are skipped with one warning.

The scan walks `<input>/Lights/<target>/<date>/<filter>/`, which is the layout
`prepare` reads, so `-r` is implied. Reports, the session profile and the suggested
YAML land in `<output>/<target>/metrics/`, beside `prepare`'s `frames.csv`; rejected
frames go to `<output>/<target>/rejected/<category>/`. `--dir` remains the way to
analyse an arbitrary directory, and the two flags are refused together rather than
one silently winning.

### Output

Console, one line per frame:

```
Processing 2 FITS file(s) in "images" (8 workers, conv=10, max-fwhm-sigma=4, max-halo=2, max-structure=1.12)...

  [001/002] LBN527_B_20260910_025946_489 ... OK (478/2594 stars, FWHM=3.62, Ecc=0.303, SNR=32.27, Score=6.224, APPROVED)
  [002/002] LBN527_B_20260910_035155_591 ... OK (477/2920 stars, FWHM=3.31, Ecc=0.329, SNR=32.67, Score=6.619, APPROVED)

Done: 2 file(s) processed.
```

Each line carries a shortened label -- `<target>_<filter>[_<index>]_<date>_<time>_<ms>` -- and
a counter padded to the width of the run, so the columns do not shift as frames
run past nine or ninety-nine. The index is per camera because the filenames are:
the C3 writes its gain (`GA0`) where the G4 and the ASI2600 write a frame index
(`001`), so the C3 label has no index field. A name that follows neither grammar
(a flat, a hand-renamed file) prints in full. Only the console is shortened: the
CSVs always carry the full filename.

Every metric column is padded to the widest value in the run, so a four-digit
star count widens the column for every line instead of pushing the fields to its
right out of line.

Frames are ordered by their acquisition stamp, oldest first — the
`_YYYYMMDD_HHMMSS_` part of the name — so a night that crosses midnight reads in
the order it happened and a frame index never overrides the clock. The rows of
`analyse_report_<FILTER>.csv` follow the same order. Names with no stamp (flats,
hand-renamed files) come last, in path order.

`478/2594` is `starCount/detectedStars`: measured over found.

The header carries the session-relative thresholds in force, so the log says
which values produced the decisions. A threshold of `0` prints as `disabled`.

When the session had at least four frames to establish a baseline, each frame
line also carries the ratios those thresholds are compared against, and the Sun's
depth for the frame:

```
  [3/9] VDB149_..._035_....FIT ... OK (455/2710 stars, FWHM=1.69, Ecc=0.412, SNR=22.38, Score=5.100, fwhmRatio=1.05, halo=3.33, struct=1.020, sunBelow=22.40, REJECTED CLOUDS)
```

A ratio above the threshold is why the frame was rejected. `sunBelow` is how many
degrees the Sun sat below the horizon at `DATE-OBS` (`18` is astronomical night),
from the header's site; it is a dash when that frame's header has no site or
time. Each optional field is a column when at least one frame of the run measured
it, and a frame without a value prints a dash, so the fields after it stay
aligned. A field no frame measured is absent from every line — printing
`halo=0.00` would read as a clean frame rather than as an untested rule.

CSV, `;`-separated to match the reference PixInsight tool's export format, written
to `<output>/analyse_report.csv` when `-o` is given and to stdout otherwise. A
filtered or target-scoped run writes `<output>/analyse_report_<FILTER>.csv`, so
reports for different filters can share one output directory:

```
filename;filter;date;detectedStars;starCount;avgFWHM;avgSignal;avgEccentricity;snr;score;decision;error;background;noise;bgStructure;halo;haloRatio;structRatio;fwhmRatio;cached;sessionFlag;fwhmRatioStack;eccRatioStack;snrRatioStack;haloRatioStack;structRatioStack;suggestedDecision;haloStars;transparency;airmass;sunBelow
```

`cached` is `true` on a row whose numbers were read from the measurement cache
instead of measured in this run. It is the only column a `--cache` run changes.

`decision` holds one of **ten** values, which are **data, not messages** —
`APPROVED`, `REJECTED TWILIGHT`, `REJECTED NO STARS`, `REJECTED ECCENTRICITY`,
`REJECTED FWHM`, `REJECTED SNR`, `REJECTED SCORING`, `REJECTED CLOUDS`,
`REJECTED STRUCTURE`, `REJECTED TRANSPARENCY`. Match on them exactly.

`background`, `noise` and `bgStructure` are measurements, not decisions:

| Column | Meaning |
|---|---|
| `background` | median sky level in physical ADU. `readfits` applies `BSCALE`/`BZERO`, so this is the true sky, not the offset integer the file stores. |
| `noise` | per-pixel noise sigma from the background/noise estimator |
| `bgStructure` | MAD of 64×64 block medians, in units of `noise`: how much the sky varies across the frame at scales far larger than a star |

`halo`, `haloRatio`, `structRatio` and `fwhmRatio` are the session-relative
measurements:

| Column | Meaning |
|---|---|
| `halo` | median wing brightness of the brightest stars, in ADU above the local sky: the aureole thin cloud scatters around bright stars. **Empty when it could not be measured**, with `haloStars = 0` saying so: zero ADU is a measurement, a missing one is not |
| `haloRatio` | `halo` relative to the 25th percentile of its session |
| `structRatio` | `bgStructure` relative to the 25th percentile of its session |
| `fwhmRatio` | `avgFWHM` relative to the median of every frame of that filter in the run |

Both ratios are 0 when the session has fewer than four measured frames, or when
too few stars were bright enough to measure a halo. The halo itself is left
**empty** in that case, with `haloStars` recording how many stars contributed, so
a frame whose aureole could not be measured is not mistaken for a calm one.

`sunBelow` is how many degrees the Sun's centre sat below the horizon when the
frame was taken, computed from the header's `SITELAT`, `SITELONG` and `DATE-OBS`
(UTC), and kept to a hundredth of a degree. 18 is astronomical night; 0 is the
Sun on the horizon. The column is
**empty** when the header does not carry all three, because zero would read as a
measured horizon rather than a missing one. `DATE-OBS` is the start of the
exposure, so the depth is not corrected for its length. It is a property of where
and when the frame was taken, not of the pixels; it drives the twilight gate
below but otherwise changes no measurement.

A frame is `REJECTED TWILIGHT` when the Sun was less than `--sun-below`
degrees below the horizon at `DATE-OBS` (default 15°, between nautical and
astronomical twilight). It is the first gate, because no amount of good seeing
rescues a frame taken while the sky is still bright; it wins even when the frame
also fails a later threshold. The depth comes from the header's `SITELAT`,
`SITELONG` and `DATE-OBS`, and a frame whose header carries no site or time is
left alone rather than rejected on a guess. `0` disables the gate.

A frame is `REJECTED FWHM` when its `avgFWHM` lies more than
`--max-fwhm-sigma` robust sigmas above the median of its filter's whole
population:

```
median   = median(avgFWHM of every frame of that filter)
madSigma = 1.4826 * median(|avgFWHM - median|)
rejected = avgFWHM > median + max-fwhm-sigma * madSigma
```

Only the upper tail is rejected — a frame sharper than its filter's median is
better than average, not faulty. The gate needs at least eight frames of the
filter, and is skipped when the MAD is zero, because the bound would then sit
exactly on the median. Because the population is every frame passed to the
command, analyzing a single night and analyzing the whole target can produce
different bounds.

A frame is `REJECTED TRANSPARENCY` when its image contrast-to-noise is below
`--min-transparency` of the best frames taken through a similar airmass. That is
the thin-cloud signal: cloud dims the stars and flattens the contrast while the
sky barely moves. The default is deliberately conservative: on the night it was
calibrated against it rejected the whole of the obvious cloud passage and none of
the confirmed-clear frames (9% of the night), and left the milder dips to the
operator -- `--min-transparency 0.9` catches two thirds of those at about twice
the rejected frames.

Airmass rather than clock time is the grouping variable because it is the part of
the drift that is predictable from geometry: a frame at airmass 1.4 is dimmed by
where it was pointed, not by the weather, so it must be compared with other
frames at 1.4 rather than with a better hour. The reference is the 90th
percentile of those frames, widening the airmass tolerance from 0.05 to 0.10 to
0.20 until eight frames qualify, and falling back to the hour around the frame
when the header carries no `AIRMASS`.

A frame is `REJECTED CLOUDS` when `haloRatio > --max-halo-ratio`. It is
`REJECTED STRUCTURE` only when **three** things hold together: `structRatio`
above `--max-structure`, above `median + 4·MAD` of the session's own
`structRatio`, and the frame softer than its session's median FWHM. Set any
threshold to `0` to disable that rule.

The halo is the cloud signal because it is a **shape** measurement, not a sky
level: thin cloud scatters starlight into a wide aureole around the brightest
stars, growing the wings several-fold while the core and the PSF width barely
move. Moonlight, twilight and low altitude raise the sky without producing an
aureole, so they cannot trigger it — the false positives the old sky-level rule
suffered from. Structure is kept separately as a bad-seeing indicator: a sky that
varies strongly across the field produces frames that no amount of stacking
repairs.

**Both are relative to the frame's own session and filter.** A night can have a
uniformly more structured sky than another — moon, haze, light pollution — with
no cloud in it, and an absolute structure threshold rejects every frame of such a
night. Sessions are found by clustering frames on their acquisition time (a gap
of more than two hours starts a new one), so a night that crosses midnight stays
one session, and one attached to a flat directory of prepared frames works too.

A frame already rejected for eccentricity or FWHM keeps that reason — the rules
only override `APPROVED`. FWHM is tested first, then cloud, then bad seeing: a
soft frame is reported as soft rather than clouded.

### Copying rejected frames

`--copy-rejected` puts every rejected frame into `<output>/rejected/<category>/`:

```
<output>/                          ← or <output>/<target>/metrics/ with --target
├── analyse_report.csv          ← or analyse_report_<FILTER>.csv with --filter or --target
├── sessions_<FILTER>.csv        ← per-night profile, one file per filter
├── suggested_<FILTER>.yaml      ← only with --suggest
├── cache/
│   ├── measurements.jsonl      ← only with --cache
│   └── suggestions.jsonl       ← only with --suggest
└── rejected/
    ├── structure/
    ├── clouds/
    ├── eccentricity/
    ├── fwhm/
    ├── no-stars/
    ├── scoring/
    └── snr/
```

The category folders are a fixed mapping from the decision values, and a folder
appears only if a frame was rejected for that reason. Approved frames are never
copied, and neither are unreadable ones — they have no decision to categorise
them by.

The folders are **flat**. Since the scan can be recursive, two different frames
can share a basename; the first is kept and the second reported on stderr rather
than overwritten. On a tree that renumbers from `001` each session, that will
happen, so check the warnings before assuming every rejected frame was copied.

### Session quality and suggested thresholds

The gates are fixed values applied to every filter, which cannot be right for all
of them: S and O are weaker than L, sparse fields detect fewer stars, and what
counts as a soft frame depends on the sampling of the night. `analyze` already
has the measurements to do better, in two steps.

**Every run profiles the nights** (`sessions_<FILTER>.csv`, and the `sessionFlag`
column). Each session is compared with the whole filter population rather than
with itself, which is the one thing a session-relative rule cannot do:

```
stack reference   trimmed median and 1.4826*MAD over every measured frame
within-night      the same statistic over residuals from each night's own median
heterogeneity h   spread of the night medians / the within-night spread
shift_s           median of night s - stack median, with a sampling error of
                  1.2533*sigma/sqrt(n_s)
flagged when      the shift in the direction that hurts is larger than BOTH
                  3 sampling errors AND a tenth of the stack median
                  (a fifth for the dimensionless halo and structure)
scattered         more than half the night's frames outside the stack band,
                  when no single median moved far
```

`h` is reported but is deliberately *not* what decides: a robust spread of the
night medians cannot see one bad night among few, which is exactly the night
worth catching, and with many nights it starts to see sampling noise. The floor
of a tenth of the median and the three-sampling-error test bound both ends.

Tokens: `soft` (FWHM high), `cloudy` (halo high), `hazy` (structure high),
`thin` (SNR low), `trailed` (eccentricity high), `sparse` (few stars),
`scattered` (fat tail, no median shift). Profiling changes no verdict on its own.

**`--stack-gate` acts on it.** A frame from a flagged night is rejected *only if
the frame itself is outside the stack band on the metric that flagged the night*,
so the good frames of a bad night are kept and reported with the night's token.
This is why a wholly hazy night is now visible at all: its frames are compared
with the rest of the filter, not with their own haze.

**`--suggest` derives the thresholds.** The reference is the *good stack* —
frames from nights the profile cleared, plus the flagged nights' frames that
match the stack — and every gate but `min-stars` sits a band of robust sigmas
around its median:

```
garfield analyze -t SH2-142 -i /Volumes/Bolino/Lubhe_2026 -o out --filter H --suggest
```

```
suggested thresholds for filter H (80 frame(s), 7 night(s), 2026-08-12..2026-08-18)
  band 4.0 sigma around the good stack: keeps 71/80 good frames; 71/80 of the filter overall (89%)

  gate                current  suggested  rejects now → then
  sun-below                15         15            4 → 4
  min-stars               380        732            0 → 0
  max-ecc                0.64        0.7            0 → 0
  max-fwhm                3.2       1.88            0 → 1
  min-snr                  11       9.89           31 → 4
  min-score                 2          0            0 → 0
  max-halo-ratio            2       1.95            0 → 0
  max-structure          1.12       1.25            0 → 0
  min-transparency        0.8        0.8            0 → 0
  no metrics                -          -            0 → 0
  total                     -          -           35 → 9     (45/80 approved now, 71/80 under the suggestion)

  the suggestion would change 27 frame(s): 1 newly rejected, 26 newly approved
```

`min-stars` and `min-snr` are the exceptions, each with its own offset rather
than the band: **1.85 robust sigmas below** the good stack's median detected-star
count, and **2.5 below** its median SNR. Detected star counts have a long right
tail, so a four-sigma band falls under the usability floor and switches the gate
off; SNR is a tight measurement, so four sigmas sit below every frame of a clean
filter and reject nothing. Their fixed offsets keep each suggestion inside the
population the gate is meant to trim.

`sun-below` is copied, like `min-transparency`: it is a policy about when to
shoot, not something the stack can suggest. The table shows the same value in
both columns, and the rejects are the frames that policy costs.

The counts are **attributed**: each rejected frame is counted once, under the
first gate that rejected it in pipeline order, so the column sums to the total and
matches the `decision` column. A frame failing FWHM and score together is counted
under `max-fwhm`, which is also what the report says. Both FWHM rules share that
row because the report gives them one token.

**`--display` replays the stored suggestion.** Once `--suggest` has run, the
suggestion lives in `<output>/…/cache/suggestions.jsonl`, and `--display` prints
its table without measuring a frame or touching the acquisition volume:

```
garfield analyze -t SH2-142 -o /Volumes/T7/Workspace --filter O --display

suggested thresholds for filter O (75 frame(s), 5 night(s), 2026-08-12..2026-08-18)
  reference excludes 1 night(s): 2026-08-13 (trailed,sparse)
  band 4.0 sigma around the good stack: keeps 72/75 of the filter overall (96%)

  gate                current  suggested  rejects
  sun-below                15         15        1
  min-stars               380        293        0
  max-ecc                0.64       0.61        1
  ...
  min-transparency        0.8        0.8        1
  no metrics                -          -        0
  total                     -          -        3     (72/75 under the suggestion)
```

Without `--filter` it prints every filter the store holds, in wheel order. The
table is the derivation's snapshot: `current` is the configuration in force when
the suggestion was made, and `rejects` is what it would have rejected then — both
travel in the record, because a later run cannot recompute them without the
frames. A record written before `--display` existed has no counts; the command
says so and asks for one more `--suggest`.

The run's own verdicts are still the configured ones. Frames the suggestion would
judge differently carry a marker, so the what-if is per frame and not only a
total:

```
  [7/20] ..._02933_668.FIT ... OK (477/2920 stars, ..., REJECTED CLOUDS) [sugg: REJECTED FWHM]
```

and `suggestedDecision` in the CSV carries the same thing for a whole report
(empty when `--suggest` was not used, so the column never implies the suggestion
was applied).

`--keep` moves the band: `--keep 0.95` loosens it until 95% of the good stack
survives, `--keep 0.6` tightens it. It has nothing to search when
`--max-fwhm-sigma` is given explicitly, because that flag *is* the band; the
suggestion says so rather than silently ignoring one of the two. The band does not
move `min-stars` or `min-snr`: their 1.85- and 2.5-sigma offsets are fixed, so a
budget that needs 95% of the stack cannot buy back a frame below either.

**A gate you set on the command line is not suggested.** `--suggest --min-snr 15`
derives everything except `min-snr`, keeps your 15, and marks that row with `*` —
in the table, in the YAML provenance, and in the stored suggestion, so
`prepare --suggested` applies your value without being told again. Only flags
typed on the command line count; a value from `conf/garfield.yaml` is the baseline
the suggestion improves on, and treating it as fixed would make `--suggest` echo
the shipped config forever. The clamps are not negotiable — `min-snr`
never below 3, `max-ecc` never above 0.8, `min-stars` the good stack's median
minus 1.85 robust sigmas but never below 100 (the point below which no metric can
be computed at all), `min-score` never negative. A gate that clamps to 0 is a gate
the data says to disable, and the suggestion writes it as 0.

Two artifacts come out of `--suggest`: `suggested_<FILTER>.yaml` for pasting into
`conf/garfield.yaml`, and `cache/suggestions.jsonl` for `prepare --suggested`.

### Applying a suggestion

`prepare --suggested` judges frames with the stored thresholds **and the stored
reference**:

```
garfield analyze -d Lights -r --filter H -o out --suggest --cache   # measures + suggests
garfield prepare -t LBN527 -o out --suggested                       # judges with the suggestion
```

The reference travels with the thresholds because the relative rules compare a
frame with a population: the FWHM bound, the halo multiple and the structure
multiple are all properties of the stack, and recomputing them from the target's
own subset would move the bound, so the same frame would be judged differently
depending on what else was in the run. With those pinned, a verdict depends on the
frame rather than on its neighbours — which also fixes the documented limitation
that analyzing one night could disagree with analyzing the whole target.

Transparency is the exception, by construction: its ratio is "against the best of
the frames at a similar airmass", so no single denominator can be pinned. The
threshold travels; the neighbourhood is always taken from the frames being judged.
Freezing it once made a suggestion derived over five nights reject three of them,
because a night taken lower in the sky sits below a night-wide benchmark without
any cloud in it.

`--suggested` is explicit and off by default: the config still decides verdicts
unless you ask otherwise. A filter with no stored suggestion falls back to the
config with a warning naming it, and a store with nothing in it is an error
rather than a silent fallback. Stale suggestions are applied with a warning:
a differing schema, a differing measurement version, or a session date outside
the population the suggestion was derived from.

### Reusing measurements between runs

Measuring a frame is the expensive part of a run: decoding the pixels, estimating
the background, finding the stars and fitting a profile to hundreds of them. Only
the judging is cheap. `--cache` separates the two across runs:

```
garfield analyze -d Lights/LBN527 -r -o out --cache --format both   # measures, stores
garfield analyze -d Lights/LBN527 -r -o out --cache --format both   # reuses, re-judges
garfield prepare -t LBN527 -o out --cache                           # reuses the same store
```

The store is `<output>/cache/measurements.jsonl` — or
`<output>/<target>/metrics/cache/measurements.jsonl` when the run is target-scoped,
where it sits beside the reports it describes and where `prepare` looks for it. It
holds one JSON object per frame,
plain enough to read with `jq`. **Nothing is ever written to a frame**: the
cache lives in the output directory, so an acquisition tree stays read-only.
That is also why `--cache` requires `-o`.

What is stored is the measurement only — star counts, FWHM, eccentricity, signal,
SNR, sky, noise, structure, halo — never the verdict, the score or the session
ratios. The verdict depends on thresholds, and the ratios on the population the
frame belongs to, so both are derived again on every run:

- **Tuning a threshold does not invalidate anything.** Re-run with a different
  `--max-ecc` and every measurement is reused; only the judging changes.
- **`--moffat` and `--limit-computed-stars` do invalidate.** They change how the
  measurements are made, not how they are judged.

An entry is keyed on the frame's absolute path, size and modification time, so a
frame that was rewritten is measured again rather than trusted. The safe failure
mode is the price: a tree copied without preserving timestamps is measured once
more. The `cached` column says which rows came from the store, and the console
reports the split:

```
  cache: 12 measured, 96 reused from "out/cache/measurements.jsonl"
```

Frames that could not be read are not stored — that is an environment problem,
not a property of the pixels — but a frame with no usable star is, since
discovering that is the expensive part. Bump the store's schema when the
measurement code changes: a binary ignores entries it did not write, so the cost
of a stale store is always time, never a wrong number.

`--cache` is off by default: a run that only wants a report should not leave a
store behind. It is not a config key, for the same reason `--output` is not.

### Known limitation

`analyze`'s `filter` and `date` columns come from a positional parser, not
`prepare`'s regexes. It splits on `_` and reads field 3 as the filter and field
8 as the date, which is right for lights and wrong for everything else —
pointed at `Flats/`, every flat reports its exposure as its filter. It does not
affect `prepare`, which never calls that parser. See
[§9.1](docs/filestructure.md#91-known-limitation-the-filter-and-date-columns).
`--filter` matches on the same parsed field, so it inherits the limitation: it
is correct for lights and wrong on a `Flats/` tree.

---

## `garfield prepare`

Copies and reorganises one target's sessions from the acquisition tree into a
working tree.

### `garfield prepare --help`

```
Copies and reorganises the images of an astronomical target
from the acquisition tree into a working tree:

  <out>/<target>/Sessions/Session_NN/lights/<filter>/*.FIT
  <out>/<target>/Sessions/Session_NN/flats/<filter>/*.FIT
  <out>/<target>/rejected/Session_NN/<filter>/*.FIT

A session is a date directory under Lights/<target>.
Session numbers follow the chronological order of the dates.
Flats are selected by filter and by position angle
(PA), on the session date.

Every image is analysed before copying: only APPROVED images
go into lights/, the rest into rejected/. The quality
thresholds are those of the analyze command.

Example:
  garfield prepare --target LBN527
  garfield prepare -t LBN527 -i /Volumes/Dyno/Kosmodrom -o /Volumes/T7/Workspace
  garfield prepare -t M51 --dry-run

Usage:
  garfield prepare [flags]

Flags:
      --cache                  reuse and record measurements in <output>/cache
      --camera string          camera the filenames come from: auto (read INSTRUME from the first light), c3 (rotator and gain), g4 (fixed angle, no gain) or asi2600 (rotator and gain) [config: garfield.yaml] (default "auto")
      --conv-workers int       inner convolution workers (0 = auto) [config: garfield.yaml]
      --dry-run                show the plan without copying
      --flat-count int         flats to collect per filter and angle; 0 = the session's own date only [config: garfield.yaml]
  -h, --help                   help for prepare
  -i, --input string           root of the acquisition data [config: garfield.yaml] (default "/Volumes/Kosmodrom_2022/Kosmodrom")
      --max-ecc float          maximum eccentricity for APPROVED [config: garfield.yaml] (default 0.64)
      --max-fwhm float         maximum FWHM for APPROVED [config: garfield.yaml] (default 3.2)
      --max-fwhm-sigma float   reject a frame whose FWHM is more than this many robust sigmas above its filter's median; 0 disables [config: garfield.yaml] (default 4)
      --max-halo-ratio float   reject a frame whose bright-star halo exceeds this multiple of its session's; 0 disables [config: garfield.yaml] (default 2)
      --max-structure float    reject a frame that is both softer than its session and unusually structured above this multiple; 0 disables [config: garfield.yaml] (default 1.12)
      --min-score float        minimum score for APPROVED [config: garfield.yaml] (default 2)
      --min-snr float          minimum SNR for APPROVED [config: garfield.yaml] (default 11)
      --min-stars int          minimum number of detected stars for APPROVED [config: garfield.yaml] (default 380)
      --min-transparency float reject a frame whose contrast is below this fraction of the best of the hour around it; 0 disables [config: garfield.yaml] (default 0.8)
      --moffat                 fit a Moffat PSF profile instead of a Gaussian (slower, FWHM agrees with PixInsight) [config: garfield.yaml] (default true)
  -o, --output string          destination root [config: garfield.yaml] (default "/Volumes/T7/Workspace")
      --quiet                  suppress console output
      --skip-existing          do not overwrite existing files
      --stack-gate             reject frames from a night that is systematically worse than its filter's stack
      --suggested              judge frames with the thresholds stored by analyze --suggest
      --sun-below float        reject a frame taken when the Sun was less than this many degrees below the horizon; 0 disables [config: garfield.yaml] (default 15)
  -t, --target string          astronomical target (directory under Lights/)
  -w, --workers int            processing workers (0 = auto: NumCPU/2) [config: garfield.yaml]

Global Flags:
      --config string   YAML configuration file (default: conf/garfield.yaml if it exists)
```

### What a run does

1. **Detect the camera** from the first light's `INSTRUME` when `--camera` is
   `auto`; a missing or unrecognised value is fatal.
2. **Index** the target's date directories as sessions, numbered in
   chronological order.
3. **Check the angle.** One target must be acquired at one position angle; more
   than one is a fatal error, before anything is written.
4. **Analyse** every light frame and record a verdict. With `--cache` the
   measurements come from `<output>/cache/measurements.jsonl` when they are
   already there, which is what makes a second run over a prepared target fast.
   With `--suggested` the gates and their reference come from
   `<output>/cache/suggestions.jsonl`, written by `analyze --suggest`.
5. **Route** each frame: `APPROVED` to `lights/<filter>/`, everything else to
   the target-level `rejected/` tree.
6. **Match flats** by filter *and* angle, on the session's date.
7. **Copy masters**: biases by gain (C3) or setpoint (G4), darks by gain,
   exposure *and* setpoint (C3) or exposure and setpoint (G4), only those the
   approved frames actually need.
8. **Write** `metrics/sessions.csv` and `metrics/frames.csv`.

Start with `--dry-run`. It prints the whole plan without copying, and it costs
nothing:

```
$ garfield prepare -t TGT --dry-run
Dry run of "TGT" (1 session(s)) into "/out/TGT" [dry run]

  (dry run) Session_01 (2026-09-10) PA310: 2 approved, 0 rejected [S], no flat for [S]
  Dry run: no master bias (GA0)
  Dry run: no master dark ()

Dry run finished: 1 session(s), 2 approved image(s), 0 rejected image(s), 0 flat(s), 0 master bias, 0 master dark.
```

### `--flat-count`

How many flats a session collects, per filter and angle.

| Value | Behaviour |
|---|---|
| **`0` (default)** | the session's own date only — no borrowing, no shortfall warning |
| `N > 0` | up to `N`, topped up from the target's neighbouring nights, previous date first |

`conf/garfield.yaml` ships `flat-count: 0`, so borrowing is opt-in and a run
that does not ask for it behaves exactly as it did before borrowing existed. A
negative value is rejected rather than clamped — a typo would otherwise silently
turn borrowing off.

With a positive count, a session short of its target borrows from dates **this
target acquired on**, never from another target's. The first session has no
previous date, so its walk starts at the following one.

---

## Input structure

```
<input>/
├── Lights/                          ← required
│   └── <target>/                    ← --target must match this name exactly
│       ├── 2026-09-10/              ← one session per YYYY-MM-DD directory
│       │   └── <FILTER>/            ← one of L R G B S H O
│       │       └── *.FIT            ← science light frames
│       └── 2026-09-16/
│           └── O/
│               └── *.FIT
│
├── Flats/                           ← one level only, NO filter directory
│   ├── 2026-09-10/
│   │   ├── FLAT_S_67.02s_BIN1_-10C_GA2750_…_PA310.FIT       ← c3
│   │   └── H_AutoFlat_FLAT_H_BIN1_-10C_001_…_E.FIT          ← g4
│   └── 2026-09-16/
│
├── Bias/
│   └── Masters/                     ← flat: no subdirectories
│       ├── masterBias_GA0_-10C_257f_20251117.xisf           ← c3, per gain
│       └── masterBias_-20C_257f_20201024.xisf               ← g4, per setpoint
│
└── Darks/
    └── Masters/
        ├── G0/                                              ← c3 only
        │   └── masterDark_300s_GA0_-10C_20251118.xisf
        ├── G2750/
        │   └── masterDark_600s_GA2750_-10C_20251120.xisf
        └── masterDark_600s_-20C_20220901.xisf               ← g4, no gain subdir
```

Only `Lights/<target>` is required. Everything missing produces warnings, not
errors — the frames are still usable and calibration can be supplied later.

### Three cameras

`--camera` selects the filename convention. All three write into the same tree.

The default, `--camera auto`, reads `INSTRUME` from the target's first light
frame and picks the profile from it: `C3-61000` is the C3, `Gx/Cx USB` is the
G4, `ASI2600` is the ASI2600. Detection is **mandatory** in auto mode — a
missing, unreadable or unrecognised `INSTRUME` is fatal, and the error names the
explicit choices. Guessing the instrument would silently match the wrong flats
and masters. The result is logged:

```
camera: c3 detected from FITS header
```

`--camera c3`, `g4` or `asi2600` skips the header read and pins the profile.

| | `c3` | `g4` | `asi2600` |
|---|---|---|---|
| Camera | Moravian C3, CMOS | Moravian G4, CCD | ZWO ASI2600MM Pro, CMOS |
| Light name | `<target>_LIGHT_<F>_<exp>s_BIN<t>_<temp>_GA0_<date>_<time>_<ms>_PA310_E` | `<prefix>_LIGHT_<F>_<exp>s_BIN<t>_<temp>_<idx>_<date>_<time>_<ms>_W` | `<target>_LIGHT_<F>_<exp>s_BIN<t>_<temp>_<idx>_<date>_<time>_<ms>_GA_100_OF_50_PA97_W` |
| Flat name | `FLAT_<F>_<exp>s_BIN<t>_<temp>_GA2750_…_PA310.FIT` | `<prefix>_FLAT_<F>_BIN<t>_<temp>_<idx>_…` | `<prefix>_FLAT_<F>_BIN<t>_<temp>_<idx>_…_GA_100_OF_50_PA120.FIT` |
| Position angle | present, matched | **absent** — no rotator, ignored | present, matched |
| Gain | present (`GA0`), keys the bias | **absent** — one gain setting | present (`GA_100`), keys the bias |
| Offset | — | — | present (`OF_50`), fixed, **ignored** |
| Bias key | gain | sensor temperature setpoint | gain |
| Dark key | gain + exposure + setpoint | exposure + setpoint | gain + exposure + setpoint |
| Dark subdirectories | one level per gain | none | one level per gain |

Each run selects exactly one profile, so a shared `Flats/` or `Darks/Masters/`
holding several cameras' files is safe: the inactive camera's files are left
alone rather than reported as malformed. An unknown `--camera` is rejected
instead of defaulting, and a switch that contradicts every light frame warns
once and names the likely fix.

### The rules that bite

**Directory names are exact and case-sensitive**: `Lights`, `Flats`, `Bias`,
`Darks`, `Masters`. File extensions are not — `.fit` and `.xisf` both work. So a
lowercase `masters/` is not found, and the run proceeds with the data "missing"
rather than complaining about the typo.

**A session is a `YYYY-MM-DD` directory and nothing else.** `2026-9-16` is not
a session, nor is `2026-09-16_old`. Session numbers are positional: add a night
mid-run and every later session renumbers. `Session_03` is not an identifier —
join on `date` in the CSV.

**Filters are exactly `LRGBSHO`** — L, R, G, B, S, H, O — on both sides. That
set is the filter wheel: seven positions, all occupied, and it will not grow.
A directory outside the set is skipped with a warning; a flat outside the set is
dropped with a warning naming the file. In practice that only fires on operator
error — a hand-made directory, a typo — because every real position is in the
set. **Narrowband does not parse**: `FLAT_Ha_` and `FLAT_OIII_` need a different
wheel to be acquired at all, not a code change.

**The filter is a directory for lights and a filename token for flats.**
`Lights/<target>/<date>/S/` needs `FLAT_S_...`, not `FLAT_H_...`. The light's own
filename token is never read — both spellings come from the same wheel setting,
so they agree by construction.

**One position angle per target (C3).** More than one angle within a session, or
different angles across a target's sessions, is fatal. The failure happens
during planning, before any output is written, and every offending session is
named in one error. The G4 has no rotator, so none of this applies to it.

**Flats match on the session *directory* date**, never the date inside the
filenames. Sessions cross midnight, so the two disagree on most nights.

**Master darks key on the filename, never the directory.** `Darks/Masters/G2750/`
holding `masterDark_600s_GA2750_...` works. A file named `G2750` instead of
`GA2750` matches no light and **no dark is ever copied** — the sharpest edge in
the input contract, and it fails quietly.

**A dark must match the sensor temperature setpoint it was shot at.** On the C3,
exposure, gain and setpoint together identify the dark a light needs. On the G4
it is exposure and setpoint, because there is no gain. Either way a `-10C` dark
does not calibrate a `0C` light, so a target spanning two setpoints draws one dark
per setpoint. A C3 bias is keyed on gain alone; a G4 bias is keyed on the
setpoint, since that is the only setting that varies. Flats are not matched on
temperature at all.

**Masters must be `.xisf`.** A `.fit` master is rejected. The raw calibration
frames under `Darks/-10C/G0/30s/*.FIT` are doubly out of scope — two levels deep,
and not `.xisf`.

---

## Output structure, and feeding WBPP

```
<output>/<target>/
├── metrics/
│   ├── sessions.csv
│   └── frames.csv
│
├── Sessions/                          ← point WBPP here
│   ├── Session_01/
│   │   ├── lights/S/*.FIT            ← approved frames only
│   │   └── flats/S/*.FIT              ← matched flats
│   ├── Session_02/
│   │   └── ...
│   ├── bias/                          ← shared, one per gain
│   │   └── masterBias_GA0_-10C_257f_20251117.xisf
│   └── darks/                         ← shared, per gain+exposure+setpoint
│       └── masterDark_300s_GA0_-10C_20251118.xisf
│
└── rejected/                          ← NOT under Sessions/
    └── Session_01/H/*.FIT
```

**Point WBPP at `<output>/<target>/Sessions/`.** That is the only directory
built for import.

Four things to know about the shape:

- **Filenames are never changed.** A copy is a byte-for-byte stream copy, so
  the filename inside the output is the acquisition's filename. That is also how
  you trace a borrowed flat back to its source date.
- **`bias/` and `darks/` are siblings of `Session_NN`, not inside it.** A master
  belongs to a sensor setting, not to a night; nesting one per session would
  duplicate hundreds of megabytes nightly (20 nights of two masters is 466 MB
  versus 18 GB). **Anything globbing `Sessions/*/` picks these up** — filter on
  the `Session_` prefix.
- **Rejected frames are hoisted out** of the session tree, so an imported
  session holds only usable data and everything discarded is quarantined in one
  place.
- **`rejected/` only exists if something was rejected.** A clean run leaves
  `metrics/` and `Sessions/` and nothing else. Same for `bias/` and `darks/`:
  they appear only when a needed master was found.

### Reports

`metrics/sessions.csv`, `;`-separated:

```
session;date;pa;filters;lightCount;approvedCount;rejectedCount;flatCount;missingFlats;frameDateMin;frameDateMax;gains;darks
Session_01;2026-09-10;PA310;S;2;2;0;2;;2026-09-10;2026-09-10;GA0;GA0/300s/-10C
```

`date` is the session **directory** date — the one flats matched on.
`frameDateMin`/`frameDateMax` are the range of dates inside the light filenames,
which disagree with `date` on most nights. `flatCount` counts every flat copied,
**including any borrowed from a neighbouring date**, so it does not imply they
came from that night. `filters` and `missingFlats` list letters in wheel order,
`L R G B S H O`.

`metrics/frames.csv` — one row per analysed frame, approved or not, with the
same `decision` values as `analyze`. Metric columns come from the same row
type `analyze` writes, so the two commands' CSVs cannot disagree. Rows run oldest
first within each session/filter block, by the filename's acquisition stamp; the
sessions themselves are chronological, so each filter reads in the order it was
shot across the whole file.

Both reports record the **bare** session name, never a path, so consumers join on
the `session` column.

---

## Failure modes

Fatal — nothing is written, or the run stops:

| Condition |
|---|
| `--target` missing or `Lights/<target>` absent |
| `--flat-count` negative |
| No `YYYY-MM-DD` directories under the target |
| `--camera auto` (the default) and the first light has no usable `INSTRUME` |
| More than one position angle in a session, or across a target's sessions |
| Malformed config YAML (deliberate — the config decides what gets discarded) |

Warnings — the run continues, and the data is still usable:

| Condition |
|---|
| `Flats`, `Bias/Masters` or `Darks/Masters` missing |
| No flats for a filter that has lights |
| Fewer than `--flat-count` flats, when that flag is positive |
| No master bias for a gain in use; no master dark for a needed gain+exposure+setpoint |
| Two masters for one key — resolved by date, or reported as an undecided tie |
| `analyze -r` stopped at its depth cap, so the search was bounded |

---

## Sharp edges

Ranked by how quietly they fail. The full list is
[§12](docs/filestructure.md#12-sharp-edges).

1. **A misnamed dark file means no dark, ever.** The gain comes from the
   filename, never the directory. A misnamed *directory* costs nothing.
2. **A dark at the wrong setpoint is not silently substituted.** It is reported
   instead, with the masters that *were* found, so a near miss is visible.
3. **Session numbers are positional.** Insert a date directory and everything
   after it renumbers.
4. **Only seven filters are known** — the wheel's full inventory, so this is not
   a pending limitation. Narrowband needs different hardware.
5. **A rotated target cannot be prepared at all** — on a C3 or ASI2600. The G4
   has no rotator; see the Three cameras table.
6. **`--camera auto` needs a usable `INSTRUME`.** The header of the target's
   first light frame must name a known camera; a missing or unrecognised value
   stops the run before anything is written. Pass `--camera c3`, `g4` or
   `asi2600` to bypass detection.
7. **Masters must be `.xisf`.**

---

## Layout

```
cmd/            cobra commands: analyze, prepare, config, thresholds
internal/       FITS reading, star detection and metrics
conf/           garfield.yaml, the optional config file
docs/           filestructure.md — the full input/output contract
data/           sample frames
```

The contract is pinned by tests rather than by prose; the appendix of
[`docs/filestructure.md`](docs/filestructure.md#appendix-where-the-contract-is-pinned)
maps each claim to the test that enforces it. Where the two disagree, the code
wins.

## License

See [LICENSE](LICENSE).
