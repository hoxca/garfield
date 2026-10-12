# File structure

How garfield expects to find data on disk. This document describes the
**input** contract in detail and the output layout as far as it is needed to
predict what a run will produce.

Two commands read files:

- `garfield prepare` — the strict one. It imposes a directory and filename
  convention and silently drops anything that does not match it. This document
  is mostly about that command.
- `garfield analyze` — the loose one. It takes any directory of FITS frames
  and has almost no structural expectations.

Claims here are traceable to a source line or a test. The source is the
authority; if this document and the code disagree, the code wins.

---

## 1. Overview

The acquisition tree is a capture volume. `prepare` reads it and writes a
session tree that can be imported into WBPP (PixInsight) in one step.

Defaults, from `cmd/prepare.go:23-26`:

| Flag | Default |
|---|---|
| `--input`, `-i` | `/Volumes/Dyno/Kosmodrom` |
| `--output`, `-o` | `/Volumes/T7/Workspace` |
| `--target`, `-t` | *(required)* |

```
<input>/
├── Lights/
│   └── <target>/              ← --target must match this directory name
│       ├── 2026-09-10/        ← one session per date directory
│       │   └── <FILTER>/      ← directory name used verbatim as the filter
│       │       └── *.FIT      ← science light frames
│       └── 2026-09-16/
│           └── O/
│               └── *.FIT
│
├── Flats/                     ← note: NO filter level, unlike Lights
│   ├── 2026-09-10/
│   │   └── FLAT_S_67.02s_BIN1_-10C_GA2750_20260916_071758_808_PA310.FIT
│   └── 2026-09-16/
│
├── Bias/
│   └── Masters/               ← flat, one file per gain
│       ├── masterBias_GA0_-10C_257f_20251117.xisf
│       └── masterBias_GA2750_-10C_129f_20251117.xisf
│
└── Darks/
    └── Masters/               ← one level of per-gain subdirectories
        ├── G0/
        │   └── masterDark_300s_GA0_-10C_20251118.xisf
        └── G2750/
            └── masterDark_600s_GA2750_-10C_20251120.xisf
```

Only `Lights/<target>` is required. A missing `Flats`, `Bias/Masters` or
`Darks/Masters` produces warnings, not errors — see
[§11 Failure modes](#11-failure-modes).

Directory names are exact: `Lights`, `Flats`, `Bias`, `Darks`, `Masters`
(`cmd/prepare.go:30-61`). These are case-sensitive, unlike file extensions.

---

## 2. Lights

### 2.1 What makes a session

A session is a direct subdirectory of `Lights/<target>` whose name matches
exactly (`cmd/prepare.go:102`):

```go
var sessionDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
```

Anything else in `Lights/<target>/` is ignored — stray files, dotfiles, a
`masterFlat_*.fit` dropped there by mistake. Only directories are considered,
and only ones that match.

**Session numbering is positional, not derived from the date.** The matching
directories are sorted as strings and the index in that list, plus one, becomes
the session number (`cmd/prepare.go:634-637`):

```
Lights/M51/2026-01-15/   →  sorted first   →  Session_01
Lights/M51/2026-03-02/   →  sorted second  →  Session_02
Lights/M51/2026-12-31/   →  sorted third   →  Session_03
```

Renaming or removing a date directory renumbers everything after it. The
`session` column in `metrics/sessions.csv` and the `date` column travel
together, so join on the session name and read the date from there rather than
assuming a mapping.

A target with no date directories is a fatal error.

### 2.2 The filter directory

Every subdirectory of a session directory is a filter (`filterSubdirs`,
`cmd/prepare.go:750`). The name is **used verbatim** — no validation, no
mapping, no check that it is a real filter. `B`, `G`, `L`, `R`, `O`, `S` are
what the acquisition happens to use.

The filter is a *directory* for lights and a *filename token* for flats. The two
must agree for a flat to be matched (§3).

Dotfile directories are skipped.

### 2.3 Light filename grammar

A real frame:

```
LBN527_LIGHT_R_300s_BIN1_-10C_GA0_20260910_021539_988_PA310_E.FIT
        │      │    │   │   │   │        │       │    │    │   │
     target  type filter exp bin  temp  gain    date   time  ms  PA flip
```

`<target>` is the `--target` value. `<flip>` is `E` or `W`.

**No regex validates the whole name.** Four independent regexes pluck tokens out
of it. A frame missing a token still gets analysed and copied — the token is
simply absent from the report.

| Token | Regex | Source | Consequence if absent |
|---|---|---|---|
| frame date | `_(\d{8})_\d{6}_\d+_PA` | `prepare.go:113` | `frameDateMin/Max` stay empty in `sessions.csv` |
| PA | `_PA(\d+)` | `prepare.go:105` | Session PA is empty; **no flats will match** |
| gain | `_(GA\d+)_` | `prepare.go:125` | Not counted in the session gains; no bias copied for it |
| exposure + gain | `_(\d+)s_BIN\d_-10C_(GA\d+)_` | `prepare.go:130` | **No master dark** will be copied for this frame |

Frame date, time, milliseconds and flip are not used for matching at all. The
`E`/`W` suffix is cosmetic here.

### 2.4 Multi-angle sessions

If the frames of one session carry more than one PA value, the **lowest sorted
angle wins** and a warning lists every angle found (`cmd/prepare.go:695-707`):

```
session Session_01 (2026-09-16) : angles multiples 0/90, flats pris pour PA0
```

This is a silent quality decision, not an error. A session that was rotated
mid-night gets only one angle's flats, and the report will not tell you the
other frames exist.

---

## 3. Flats

### 3.1 Layout

`Flats/<YYYY-MM-DD>/` — **one level only**. Unlike lights, there is no filter
subdirectory. The filter is a filename token instead. The scan is
non-recursive (`indexFlats`, `cmd/prepare.go:772`).

A real flat:

```
FLAT_S_67.02s_BIN1_-10C_GA2750_20260916_071758_808_PA310.FIT
     │   │      │   │   │        │       │   │    │   │
  filter  dur  bin  temp  gain   date   time  ms   PA
```

Both of these must match or the file is skipped without comment:

```go
var flatFilterRe = regexp.MustCompile(`^FLAT_([A-Za-z0-9]+)_`)  // prepare.go:109
var paRe          = regexp.MustCompile(`_PA(\d+)`)              // prepare.go:105
```

`flatFilterRe` is **anchored at the start of the name**. `FLATL_H_41s_PA310.FIT`
does not match, because after `FLAT` comes `L` rather than `_`. Neither does a
`masterFlat_...` file or any light frame.

The filter group is `[A-Za-z0-9]+` — no underscores, no hyphens.

### 3.2 Matching

Flats are indexed by `filter@PA` (`flatKey`, `cmd/prepare.go:767`), e.g. `S@310`,
and matched against the session's filter directories and session PA:

```
session 2026-09-16, filter S, PA310  →  Flats/2026-09-16/  file starting FLAT_S_ and containing _PA310
```

A filter with lights but no matching flats is recorded in `missingFlats`,
warned about, and **not** copied. Flats for a filter with no lights are never
copied either.

### 3.3 Why the session date, not the frame date

Flats are matched on the **session directory date**, never the date embedded in
the light filenames (`cmd/prepare.go:709-711`). Sessions run past midnight, so
for most nights the two disagree by a day; only the directory name lines up with
`Flats/`.

This is why `frameDateMin` and `frameDateMax` are reported separately from
`date` in `sessions.csv` — the discrepancy is real and worth seeing.

---

## 4. Master biases

```
<input>/Bias/Masters/*.xisf
```

Flat directory. The scan is **non-recursive** — subdirectories are skipped
(`indexBiasMasters`, `cmd/prepare.go:909`).

```
masterBias_GA0_-10C_257f_20251117.xisf
          │      │    │       │
        gain   temp frames  date
```

- Keyed by **gain**, taken from the filename via `_(GA\d+)_`.
- The frame count (`257f`) is **not read** — see §6.
- The date suffix must be eight digits directly before `.xisf`.
- A file with no gain token is skipped with a warning. The legacy `G0` spelling
  does not match, so a bias named `masterBias_G0_...` is ignored:

  ```
  /input/Bias/Masters: "masterBias_G0_...xisf" ignored, no gain setting in the name
  ```

- Only **one** master bias is copied per gain, for the whole target.

---

## 5. Master darks

```
<input>/Darks/Masters/
├── G0/
│   └── masterDark_300s_GA0_-10C_20251118.xisf
├── G2750/
│   └── masterDark_600s_GA2750_-10C_20251120.xisf
└── masterDark_120s_GA0_-10C_20251119.xisf     ← also accepted
```

The scan recurses **exactly one level** (`indexDarkMasters`, `cmd/prepare.go:1080`).
Files directly under `Masters/` are accepted alongside per-gain subdirectories.

### 5.1 The key is the filename, not the directory

The directory names and the filenames **deliberately disagree**:
`G2750/` holds `GA2750` files. The key comes from the filename alone, via
`darkComboRe` (`cmd/prepare.go:133`):

```go
var darkComboRe = regexp.MustCompile(`masterDark_(\d+)s_(GA\d+)_`)
```

Keying on the directory would yield `G2750`, which matches no light frame, and
**nothing would be copied at all**. This is the single sharpest edge in the
input contract. A dark in a directory whose name suggests gain `G2750` but
whose file says `GA2750` works; a dark in a directory named `GA2750` whose file
says `GA2750` also works, because the directory is never read.

The key renders as `GA2750/600s` (`darkKey.String`, `cmd/prepare.go:1063`).

### 5.2 The `-10C` hardcode

A light's needed dark is computed with `lightComboRe` (`cmd/prepare.go:130`):

```go
var lightComboRe = regexp.MustCompile(`_(\d+)s_BIN\d_-10C_(GA\d+)_`)
```

`-10C` is **hardcoded**. The setpoint must appear literally, between the binning
token and the gain. A light taken at a different sensor temperature matches no
dark, and the only symptom is a warning listing the darks that *were* found.

Note this is a narrower pattern than `gainRe` (`_(GA\d+)_`), which has no
setpoint requirement. So a frame at the wrong temperature still contributes its
gain to the bias set, but contributes no dark.

### 5.3 Which darks get copied

`collectDarkKeys` runs **after** the quality pass and considers **only approved
frames** (`cmd/prepare.go:1227`). A dark for a rejected frame is never copied.

Missing masters are warnings, not errors — the frames are still usable data and
the dark can be supplied later:

```
no master dark for GA2750/600s in "..." (available: GA0/300s GA0/600s), affected sessions: Session_01
```

---

## 6. Duplicate masters

Two masters for one key is an anomaly worth surfacing whichever way it resolves
(`preferLater`, `cmd/prepare.go:1186`).

**Only the trailing date decides.** The frame count in a bias name (`257f`,
`129f`) is deliberately not read — more frames is not better evidence of a
current calibration.

| Situation | Outcome | Warning |
|---|---|---|
| Candidate date newer | Candidate wins | collision — both named, kept one named |
| Candidate date older | Held master kept | collision — both named, kept one named |
| Same date | **First in sorted order** kept | tie — explicitly says the date could not decide |

An undecided tie is deterministic only because candidates are visited in sorted
order, which is why it is reported as a tie rather than a choice.

Biases and darks are indexed independently, so both can live in the same
directory without colliding.

---

## 7. Regex reference

All in `cmd/prepare.go` unless noted.

| Var | Pattern | Line | Keys |
|---|---|---|---|
| `sessionDateRe` | `^\d{4}-\d{2}-\d{2}$` | 102 | which directories are sessions |
| `paRe` | `_PA(\d+)` | 105 | session angle; flat index |
| `flatFilterRe` | `^FLAT_([A-Za-z0-9]+)_` | 109 | flat index — **anchored at start** |
| `frameDateRe` | `_(\d{8})_\d{6}_\d+_PA` | 113 | report only |
| `gainRe` | `_(GA\d+)_` | 125 | session gains; bias index |
| `lightComboRe` | `_(\d+)s_BIN\d_-10C_(GA\d+)_` | 130 | light → dark — **`-10C` hardcoded** |
| `darkComboRe` | `masterDark_(\d+)s_(GA\d+)_` | 133 | dark index — **filename, not directory** |
| `masterDateRe` | `_(\d{8})\.xisf$` | 137 | duplicate-master tiebreak |

---

## 8. Extensions and dotfiles

Two distinct extension sets, kept apart because masters are in PixInsight's
format (`cmd/prepare.go:116`, `120`):

| Set | Extensions | Used for |
|---|---|---|
| `fitsExts` | `.FIT` `.FITS` `.FTS` | lights, flats |
| `masterExts` | `.XISF` | master biases and darks |

Extensions are upper-cased before lookup, so `.fit` and `.xisf` work. But the
**sets do not overlap**: a master with a `.fit` extension is rejected, and a
light with a `.xisf` extension is not collected.

Dotfiles are excluded everywhere (`isFITSName`, `cmd/prepare.go:805`; and
explicitly in `isMasterFile`, `cmd/prepare.go:1157`). Two sources produce them
and both are traps:

- macOS writes AppleDouble sidecars `._<frame>.FIT` on exFAT volumes. They carry
  a FITS extension, so without the dotfile rule they would all be analysed and
  each would fail as unreadable, filling the report with error rows.
- The syncthing client leaves `.syncthing.masterDark_....tmp` partials in the
  masters directories. Copying a half-transferred master is worse than having
  none.

---

## 9. `analyze` input

Much looser. Any directory of FITS frames works.

| Flag | Default |
|---|---|
| `--dir`, `-d` | `images` |
| `--recursive`, `-r` | `false` |
| `--limit-computed-stars` | 500 |
| `--format` | `console` (or `csv`, `both`) |
| `--output`, `-o` | *(CSV file; required for csv/both to a file)* |

**Flat by default.** Only files directly in `--dir`. Descending is opt-in so a
stray frame below the chosen directory cannot quietly join the report.

**With `-r`, the walk stops at depth 2** (`maxRecursiveDepth`,
`cmd/analyze.go:310`). This is sized for the acquisition layout, where a frame
sits two levels below `Lights/<target>`:

```
Lights/<target>/2026-09-10/S/frame.FIT
                 ←1→    ←2→
```

The cap cuts on **directory** depth, not file depth, so a directory at depth 3
is skipped whole while its parent's depth-2 frames are still collected. When
the cap stops the walk, `analyze` says the search was bounded rather than
complete — on stderr.

Also excluded: directories named like a frame (covers a directory called
`trap.FIT`), symlinked directories (cycle risk; the acquisition volumes hold
none). A symlinked *file* is still analysed. Duplicate basenames across
subdirectories are warned about.

### 9.1 Known limitation: the filter and date columns

`parseFilename` (`cmd/analyze.go:102`) is separate from `prepare`'s regexes and
much dumber — it splits on `_` and takes positional fields:

```go
parts := strings.Split(base, "_")
if len(parts) >= 3 { filter = parts[2] }
if len(parts) >= 8 { date = parts[7][:8] }
```

So the third `_`-separated field is the filter and the eighth is the date. This
holds for light and flat names but **not** for a generated master name:

```
masterFlat_BIN-1_9576x6388_FILTER-S_mono_SESSION-1.fit
                  ^^^^^^^^^^^ reported as filter
```

A master dropped into an `analyze` directory reports its dimensions as a filter.
This is a known limitation, pinned by `TestParseFilenameMasterFlat`.

---

## 10. Output layout

Written under `<output>/<target>/`. Exactly three directories at the target
root — pinned by `TestPrepareRealTreeShape` (`cmd/prepare_test.go:349`).

```
<output>/<target>/
├── metrics/
│   ├── sessions.csv
│   └── frames.csv
│
├── Sessions/                          ← the only valid WBPP import directory
│   ├── Session_01/
│   │   ├── lights/<FILTER>/*.FIT      ← approved frames only
│   │   └── flats/<FILTER>/*.FIT       ← matched flats
│   ├── Session_02/
│   │   └── ...
│   ├── bias/                          ← shared, one file per gain
│   │   └── masterBias_GA0_-10C_257f_20251117.xisf
│   └── darks/                         ← shared, one file per gain+exposure
│       └── masterDark_300s_GA0_-10C_20251118.xisf
│
└── rejected/                          ← NOT under Sessions/
    └── Session_01/<FILTER>/*.FIT
```

Four things worth knowing:

- **Filenames are never changed.** `copyFITS` is a plain buffered stream copy.
- **`bias/` and `darks/` are siblings of `Session_NN`, not inside it.** A master
  is a property of the sensor setting, not of a night — one file serves every
  session at that gain, and nesting it per session would duplicate
  hundreds-of-megabyte files once per night (20 nights of two masters is 466 MB
  versus 18 GB). **Anything globbing `Sessions/*/` will pick these up**; filter
  on the `Session_` prefix.
- **Rejected frames are hoisted out of the session tree** so a session directory
  holds only usable data, and everything discarded is quarantined in one place.
- **A frame with no recorded verdict is treated as not approved** rather than
  silently promoted into `lights/`.

### 10.1 Reports

Both CSVs use `;` as separator (`csvSeparator`, `cmd/analyze.go:246`), matching
the reference PixInsight tool's export format.

`metrics/sessions.csv`:

```
session;date;pa;filters;lightCount;approvedCount;rejectedCount;flatCount;missingFlats;frameDateMin;frameDateMax;gains;darks
Session_01;2026-09-16;PA310;S;2;2;0;2;;2026-09-16;2026-09-17;GA0;GA0/300s
```

`metrics/frames.csv` — one row per analysed frame, approved or not:

```
session;sessionDate;pa;filter;filename;detectedStars;starCount;avgFWHM;avgSignal;avgEccentricity;snr;score;decision;error
```

The `decision` column holds one of six values, which are **data, not
messages** — anything parsing this file must match on them:

| Value | Meaning |
|---|---|
| `APPROVED` | passed every threshold |
| `REJECTED NO STARS` | no star met the detection floor |
| `REJECTED ECCENTRICITY` | failed `--max-ecc` |
| `REJECTED FWHM` | failed `--max-fwhm` |
| `REJECTED SNR` | failed `--min-snr` |
| `REJECTED SCORING` | failed `--min-score` |

These were `APPROUVEE` and `REJETEE *` before the interface was translated to
English, so a CSV written by an older build mixes both vocabularies.

Both reports record the **bare** session name, never a path, so consumers join
on the session column. Metric columns come from the same `imageResultRow` used
by `analyze`, so the two commands' CSVs cannot disagree.

---

## 11. Failure modes

### Fatal

| Condition | Message |
|---|---|
| `--target` missing | `--target is required` |
| `--input` or `--output` empty | `... cannot be empty` |
| `Lights/<target>` absent | `target %q not found in %q` |
| No date directories | `no session (YYYY-MM-DD directory) found in %q` |
| Malformed config YAML | (deliberate: the config decides which frames get discarded) |
| `--config` path given but missing | no fallback to the search path |

### Warnings — the run continues

| Condition |
|---|
| `Bias/Masters` or `Darks/Masters` directory absent |
| No flats for a filter that has lights |
| No master bias for a gain the lights use |
| No master dark for a `(gain, exposure)` an approved frame needs |
| Two masters for one key (collision or tie) |
| Master filename carries no gain token |
| Multiple PA values in one session |
| `analyze -r` stopped at the depth cap |

A session missing its masters is still usable data; calibration can be supplied
later in PixInsight.

---

## 12. Sharp edges

Things that will bite, ranked by how quietly they fail:

1. **Dark directory names are ignored.** `Darks/Masters/G2750/` holding
   `masterDark_600s_GA2750_...` works. Nothing about the directory is read, so
   a misnamed directory costs you nothing — but a *misnamed file* (`G2750`
   instead of `GA2750`) means no dark is ever copied. Symmetrically, `Bias/`
   files with the legacy `G0` spelling are skipped with a warning.

2. **`-10C` is hardcoded** in `lightComboRe`. A different sensor setpoint
   silently matches no dark. The gain regex has no such requirement, so a frame
   at the wrong temperature still gets a bias but no dark.

3. **Multi-PA sessions take the lowest angle** and copy only that angle's flats.
   The report records one PA; the other frames' existence is visible only in the
   warning.

4. **Session numbers are positional.** Inserting a date directory renumbers
   every later session. Never treat `Session_03` as a stable identifier — join
   on the `date` column.

5. **Flats must live under the session's date directory**, matching the
   *directory* date. Because sessions cross midnight, the frame date inside the
   filenames will usually differ. Both are reported; use `date`, not
   `frameDateMin`.

6. **`FLAT_` is anchored.** `FLATL_H_41s_PA310.FIT` is not a flat. The filter
   token is `[A-Za-z0-9]+` — a filter containing `_` or `-` breaks extraction.

7. **Masters must be `.xisf`.** A `.fit` master is rejected by the extension
   check *and* excluded by the directory structure — the raw calibration frames
   under `Darks/-10C/G0/30s/*.FIT` are doubly out of scope.

8. **`analyze`'s filter and date columns are positional.** A
   `masterFlat_BIN-1_9576x6388_...fit` reports `9576x6388` as its filter
   (§9.1).

9. **Quality defaults are stricter than the PixInsight reference.** On the
   82-frame `data/all/` set, garfield rejects 15 where the reference export
   rejects 0 — `data/correlation_test/correlation.txt` records the comparison.
   The rejects are all low-end tail in both systems, so this is stricter rather
   than contradictory, but a first run can look alarming.

---

## Appendix: where the contract is pinned

Layout claims in this document are enforced by tests, not just documented:

| Behaviour | Test |
|---|---|
| only `YYYY-MM-DD` directories are sessions | `TestPrepareOnlyAcceptsDateDirs` |
| full output tree shape, 3 dirs at root | `TestPrepareRealTreeShape` |
| `Session_%02d` numbering and padding | `TestPrepareSessionDirPadding` |
| session dirs live under `Sessions/` | `TestPrepareSessionsLiveUnderSessionsDir` |
| darks keyed on filename not directory | `TestIndexDarkMastersKeysOnFilenameNotDirectory` |
| `.fit` masters rejected | `TestIndexDarkMastersRejectsNonMasters` |
| duplicate dark resolved by date; tie reported | `TestIndexDarkMastersPrefersTheLatestDate`, `TestIndexDarkMastersReportsAnUndecidedTie` |
| gain token shape, legacy `G0` must **not** match | `TestGainReMatchesAcquisitionNames` |
| light→dark exposure/gain extraction | `TestLightDarkKeyRe`, `TestDarkComboRe` |
| non-master files and dotfiles skipped in bias dir | `TestPrepareSkipsNonMasterFilesInBiasDir` |
| biases shared across sessions | `TestPrepareBiasIsSharedAcrossSessions` |
| darks shared across sessions | `TestPrepareDarksSharedAcrossSessions` |
| only needed biases copied | `TestPrepareCopiesOnlyTheBiasesNeeded` |
| only approved frames draw a dark | `TestPrepareDarksOnlyForApprovedFrames` |
| flats keyed on the **session** date | `TestPrepareUsesSessionDateForFlats`, `TestFramesCSVUsesSessionDateNotFrameDate` |
| multi-PA warns and takes the lowest angle | `TestPrepareMixedAnglesWarns`, `TestPrepareFiltersFlatsByAngle` |
| missing bias/dark/flats are warnings, not errors | `TestPrepareWarnsOnMissing{Bias,Dark,Flats}`, `TestPrepareWithout{Bias,Darks}MastersIsNotAnError` |
| no verdict means not approved | `TestSessionPlanApproved` |
| analyze master-name filter limitation | `TestParseFilenameMasterFlat` |
| documented output paths in help | `TestPrepareLongHelpDocumentsLayout` |

Note that `TestPrepareLongHelpDocumentsLayout` asserts only the three session
paths. `bias/`, `darks/` and `metrics/` are implemented and tested but are not
mentioned in `prepare --help`.