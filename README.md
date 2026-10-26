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
make test           # gofmt, golangci-lint, then the test suite
```

`make build` stamps the version from `git describe --tags`, so
`garfield version` reports something meaningful on a tagged build and `dev`
otherwise.

---

## Configuration

`conf/garfield.yaml` is optional — without it the compiled-in defaults apply.
Each key is `<section>.<flag-name>`, and **a flag on the command line always
wins over the file**.

```yaml
thresholds:
  min-snr: 11.0
  max-fwhm: 5.0
  max-ecc: 0.54
  min-score: 2.0
  min-stars: 680

concurrency:
  workers: 0        # 0 = NumCPU/2
  conv-workers: 0   # 0 = core count

prepare:
  input: /Volumes/Dyno/Kosmodrom
  output: /Volumes/T7/Workspace
  camera: c3        # c3 (rotator + gain) or g4 (fixed angle, no gain)
  flat-count: 0     # 0 = the session's own date only

analyze:
  dir: images
  recursive: false
```

The `thresholds` block is not a preference. It is the decision about which
frames get destroyed, and it applies to both commands at once. `--max-ecc` was
raised from 0.50 to 0.54 during development because frames just above 0.50 were
being discarded — treat any change here as a decision, not a tweak.

Point at a different file with `--config path/to/garfield.yaml`. A `--config`
path that does not exist is an error rather than a silent fallback.

---

## `garfield analyze`

Walks a directory, detects `.fit`/`.fits`/`.fts`, and computes FWHM,
eccentricity, SNR and score for each frame.

```
score = snr / fwhm × (1 − ecc)
```

**Shape comes from a 2D Gaussian PSF fit**, not from image moments. Each measured
star is fitted with an elliptical Gaussian whose background is a free parameter;
FWHM is the equivalent-circular width of the fitted profile and eccentricity is
`sqrt(1 − (b/a)²)` of its axes.

This matters below about 2 px FWHM. There the PSF spans only a few pixels,
background subtraction and noise clip the moments, and a round star can measure
0.6 while looking perfectly round. On an undersampled 1.69 ″/px field the fit
agrees with PixInsight's eccentricity to within 0.03 per frame, where moments
were 0.2 high.

Because it is a Gaussian fit, the FWHM it reports is a Gaussian-equivalent width
and will differ from a tool that fits a Moffat profile; it reads roughly 1.3×
PixInsight's figure on the same frames.

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
      --conv-workers int           inner convolution workers (0 = auto) [config: garfield.yaml]
  -d, --dir string                 directory containing the FITS files [config: garfield.yaml] (default "images")
      --format string              output format: console, csv or both (default "console")
  -h, --help                       help for analyze
      --limit-computed-stars int   limit the number of bright stars measured (default 500)
      --max-ecc float              maximum eccentricity for APPROVED [config: garfield.yaml] (default 0.54)
      --max-fwhm float             maximum FWHM for APPROVED [config: garfield.yaml] (default 5)
      --min-score float            minimum score for APPROVED [config: garfield.yaml] (default 2)
      --min-snr float              minimum SNR for APPROVED [config: garfield.yaml] (default 11)
      --min-stars int              minimum number of detected stars for APPROVED [config: garfield.yaml] (default 680)
  -o, --output string              output CSV file (required for csv/both to a file)
      --quiet                      suppress console output
  -r, --recursive                  also walk the subdirectories of --dir, 2 levels deep [config: garfield.yaml]
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
| `--format` | `console`, `csv`, or `both`. `csv`/`both` need `-o`. |
| `-w`, `--conv-workers` | both default to 0 = automatic. |

### Output

Console, one line per frame:

```
Processing 2 FITS file(s) in "images" (8 workers, conv=10)...

  [1/2] LBN527_LIGHT_B_..._PA310_E.FIT ... OK (478/2594 stars, FWHM=3.62, Ecc=0.303, SNR=32.27, Score=6.224, APPROVED)
  [2/2] LBN527_LIGHT_B_..._PA310_E.FIT ... OK (477/2920 stars, FWHM=3.31, Ecc=0.329, SNR=32.67, Score=6.619, APPROVED)

Done: 2 file(s) processed.
```

`478/2594` is `starCount/detectedStars`: measured over found.

CSV, `;`-separated to match the reference PixInsight tool's export format:

```
filename;filter;date;detectedStars;starCount;avgFWHM;avgSignal;avgEccentricity;snr;score;decision;error
```

`decision` is one of six values, which are **data, not messages** —
`APPROVED`, `REJECTED NO STARS`, `REJECTED ECCENTRICITY`, `REJECTED FWHM`,
`REJECTED SNR`, `REJECTED SCORING`. Match on them exactly.

### Known limitation

`analyze`'s `filter` and `date` columns come from a positional parser, not
`prepare`'s regexes. It splits on `_` and reads field 3 as the filter and field
8 as the date, which is right for lights and wrong for everything else —
pointed at `Flats/`, every flat reports its exposure as its filter. It does not
affect `prepare`, which never calls that parser. See
[§9.1](docs/filestructure.md#91-known-limitation-the-filter-and-date-columns).

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
      --camera string      camera the filenames come from: c3 (rotator and gain) or g4 (fixed angle, no gain) [config: garfield.yaml] (default "c3")
      --conv-workers int   inner convolution workers (0 = auto) [config: garfield.yaml]
      --dry-run            show the plan without copying
      --flat-count int     flats to collect per filter and angle; 0 = the session's own date only [config: garfield.yaml]
  -h, --help               help for prepare
  -i, --input string       root of the acquisition data [config: garfield.yaml] (default "/Volumes/Dyno/Kosmodrom")
      --max-ecc float      maximum eccentricity for APPROVED [config: garfield.yaml] (default 0.54)
      --max-fwhm float     maximum FWHM for APPROVED [config: garfield.yaml] (default 5)
      --min-score float    minimum score for APPROVED [config: garfield.yaml] (default 2)
      --min-snr float      minimum SNR for APPROVED [config: garfield.yaml] (default 11)
      --min-stars int      minimum number of detected stars for APPROVED [config: garfield.yaml] (default 680)
  -o, --output string      destination root [config: garfield.yaml] (default "/Volumes/T7/Workspace")
      --quiet              suppress console output
      --skip-existing      do not overwrite existing files
  -t, --target string      astronomical target (directory under Lights/)
  -w, --workers int        processing workers (0 = auto: NumCPU/2) [config: garfield.yaml]

Global Flags:
      --config string   YAML configuration file (default: conf/garfield.yaml if it exists)
```

### What a run does

1. **Index** the target's date directories as sessions, numbered in
   chronological order.
2. **Check the angle.** One target must be acquired at one position angle; more
   than one is a fatal error, before anything is written.
3. **Analyse** every light frame and record a verdict.
4. **Route** each frame: `APPROVED` to `lights/<filter>/`, everything else to
   the target-level `rejected/` tree.
5. **Match flats** by filter *and* angle, on the session's date.
6. **Copy masters**: biases by gain (C3) or setpoint (G4), darks by gain,
   exposure *and* setpoint (C3) or exposure and setpoint (G4), only those the
   approved frames actually need.
7. **Write** `metrics/sessions.csv` and `metrics/frames.csv`.

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

### Two cameras

`--camera` selects the filename convention. Both write into the same tree.

| | `c3` (default) | `g4` |
|---|---|---|
| Camera | Moravian C3, CMOS | Moravian G4, CCD |
| Light name | `<target>_LIGHT_<F>_<exp>_BIN_<temp>_GA0_<date>_<time>_<ms>_PA310_E` | `<prefix>_LIGHT_<F>_<exp>_BIN_<temp>_<idx>_<date>_<time>_<ms>_W` |
| Position angle | present, matched | **absent** — no rotator, so ignored |
| Gain | present, keys the bias | **absent** — one gain setting |
| Bias key | gain | **sensor temperature setpoint** |
| Dark key | gain + exposure + setpoint | exposure + setpoint |
| Dark subdirectories | one level per gain | none |

The patterns are mutually exclusive, so a shared `Flats/` or `Darks/Masters/`
holding both cameras' files is safe: the inactive camera's files are left alone
rather than reported as malformed. An unknown `--camera` is rejected instead of
defaulting, and a switch that contradicts every light frame warns once and names
the likely fix.

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
came from that night.

`metrics/frames.csv` — one row per analysed frame, approved or not, with the
same six `decision` values as `analyze`. Metric columns come from the same row
type `analyze` writes, so the two commands' CSVs cannot disagree.

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
5. **A rotated target cannot be prepared at all** — on a C3. The G4 has no
   rotator; see the Two cameras table.
6. **`--camera` defaults to `c3`.** A G4 run that omits it finds no angles, no
   flats and no masters, then warns once about the likely cause. It fails safely,
   but it does fail — the switch is not inferred.
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
