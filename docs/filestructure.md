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
│       │   └── <FILTER>/      ← one of L R G B S H O, used verbatim
│       │       └── *.FIT      ← science light frames
│       └── 2026-09-16/
│           └── O/
│               └── *.FIT
│
├── Flats/                     ← note: NO filter level, unlike Lights
│   ├── 2026-09-10/
│   │   └── FLAT_S_67.02s_BIN1_-10C_GA2750_20260916_071758_808_PA310.FIT
│   └── 2026-09-16/            ← may be read to top up another session (--flat-count)
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

Directory names are exact: `Lights`, `Flats`, `Bias`, `Darks`, `Masters`.
These are case-sensitive, unlike file extensions.

`prepare` flags that shape this tree:

| Flag | Default | Effect |
|---|---|---|
| `--flat-count` | `0` | Flats per filter and angle. `0` keeps a session to its own date; `N` tops it up from neighbouring nights (§3.2). |

`conf/garfield.yaml` ships `flat-count: 0`, so borrowing is opt-in.

---

## 2. Lights

### 2.1 What makes a session

A session is a direct subdirectory of `Lights/<target>` whose name matches
exactly :

Anything else in `Lights/<target>/` is ignored — stray files, dotfiles, a
`masterFlat_*.fit` dropped there by mistake. Only directories are considered,
and only ones that match.

**Session numbering is positional, not derived from the date.** The matching
directories are sorted as strings and the index in that list, plus one, becomes
the session number :

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

Each subdirectory of a session directory is one filter, and its name must be
one of the seven known colour filters — **L**, **R**, **G**, **B**, **S**,
**H**, **O**. The name is used verbatim otherwise: no mapping, no normalisation.

A directory outside the set is **skipped with a warning** rather than carried
into the plan. Left alone it would become a filter with no flats, and the
session would then report a missing flat for it — naming a filter the
acquisition does not use.

Dotfile directories are skipped without comment.

The filter is a *directory* for lights and a *filename token* for flats.
The two must agree, and both are validated against the same set, for a flat
to be matched (§3).

### 2.3 Light filename grammar

A real frame:

```

target     filter   bin       gain         time        PA
   │         │       │   │     │       │     │          │
LBN527_LIGHT_R_300s_BIN1_-10C_GA0_20260910_021539_988_PA310_E.FIT
         │      │         │          │             │        │
       type    exp       temp       date           ms     flip
```

`<target>` is the `--target` value. `<flip>` is `E` or `W` side of pier.

**No regex validates the whole name.** Four independent regexes pluck tokens out
of it. A frame missing a token still gets analysed and copied — the token is
simply absent from the report.

| Token | Regex | Consequence if absent |
|---|---|---|---|
| frame date | `_(\d{8})_\d{6}_\d+_PA`  | `frameDateMin/Max` stay empty in `sessions.csv` |
| PA | `_PA(\d+)`  | Session PA is empty; **no flats will match** |
| gain | `_(GA\d+)_`  | Not counted in the session gains; no bias copied for it |
| exposure + gain | `_(\d+)s_BIN\d_-10C_(GA\d+)_`  | **No master dark** will be copied for this frame |

Frame date, time, milliseconds and flip are not used for matching at all. 
The `E`/`W` side of pier suffix is cosmetic here.

### 2.4 Position angles

A target is acquired at **one position angle, chosen before acquisition starts.**
Two faults contradict that and are both **fatal**:

| Fault | Means | Reported as |
|---|---|---|
| More than one angle **within** a session | instrument rotated mid-session | `multiple angles 240/310` |
| Sessions of one target at **different** angles | target rotated between nights | `is acquired at 2 different angles (240/310)` |

```
mixed angles: session Session_03 (2026-09-14): multiple angles 240/310;
session Session_07 (2026-09-18): multiple angles 180/310
```

Every offending session is named in one error, so a single run surfaces all of
them rather than one per fix-and-rerun. The failure happens during planning,
before anything is written, so it leaves no output tree behind.

Sessions carrying **no** angle are not part of this check. An empty `pa` can
never match a flat, so such a session already reports a missing flat (§3.2).

---

## 3. Flats

### 3.1 Layout

`Flats/<YYYY-MM-DD>/` — **one level only**. Unlike lights, there is no filter
subdirectory. The filter is a filename token instead. The scan is
non-recursive.

A real flat:

```

   filter     bin        gain            time       PA
     │         │          │                │         │  
FLAT_S_67.02s_BIN1_-10C_GA2750_20260916_071758_808_PA310.FIT
         │           │            │             │    
        exp        temp          date           ms   
```

Filter and PA must match or the file is skipped without comment:

The filter must be one of seven known colour filters, and the PA must be
present, or the file is dropped with a warning naming it.

`flatFilterRe` is **anchored on the `FLAT_` prefix** and its group is a
character class over `knownFilters`. `FLATL_41s_..._PA310.FIT` does not match,
because after `FLAT` comes `L` rather than `_`. Neither does a
`masterFlat_...` file or any light frame.

The recognised set is **exactly one character** from `LRGBSHO` — L, R, G, B,
S, H, O:

```
FLAT_S_67.02s_BIN1_-10C_GA2750_20260916_071758_808_PA310.FIT   → S   matched
FLAT_Ha_41.4s_BIN1_PA310.FIT                                 → nil no known filter
FLAT_OIII_41.4s_BIN1_PA310.FIT                               → nil no known filter
FLAT_L2_41.4s_BIN1_PA310.FIT                                 → nil no known filter
```

Two consequences:

- **Narrowband does not parse.** `Ha`, `OIII`, `SII`, `L2` and anything else
  outside the seven are dropped until their letters are added to `knownFilters`.

A file is *reported* rather than silently dropped when it carries the `FLAT_`
prefix but fails the filter test — that is how a newly added filter announces
itself. A name without the prefix (`FLATL_...`, a light frame, a master) is a
different kind of frame sharing the directory and is left alone. The prefix
test is deliberately looser than the token test, so a plainly malformed
`FLAT_-weird_name.FIT` is named rather than discarded.
- **Multi-character tokens cannot match by accident.** 

`knownFilters` is written in band order for readability. 

### 3.2 Matching

Flats are indexed by `filter@PA`, e.g. `S@310`,
and matched against the session's filter directories and session PA:

```
session 2026-09-16, filter S, PA310  →  Flats/2026-09-16/  file starting FLAT_S_ and containing _PA310
```

### 3.2 Matching, and borrowing across nights

Flats are indexed by `filter@PA`, e.g. `S@310`, and matched against the
session's filter directories and session PA.

How many a session collects is set by `--flat-count` (config key
`prepare.flat-count`):

| Value | Behaviour |
|---|---|
| **`0` (default)** | The session's own date only. No borrowing, no shortfall warning. |
| `N > 0` | Up to `N` per filter and angle, topped up from neighbouring nights. |

`conf/garfield.yaml` ships `flat-count: 0`, so borrowing is opt-in: a run that
does not ask for it behaves exactly as garfield did before borrowing existed.
A negative value is rejected rather than clamped, since a typo would otherwise
silently turn borrowing off.

With a positive target, the session's own date rarely holds enough, so it is
topped up from the target's neighbouring nights:

1. flats from the session's own date, up to `N`;
2. if still short, walk outward through the target's dates — **previous first,
   then next**, stepping back, forward, back — until `N` are held or the list
   runs out.

The first session has no previous date, so its walk begins at the **following**
one. That falls out of the same ordering rather than needing a special case.

Borrowing is bounded to the dates **this target acquired on**. A `Flats/`
directory belonging to another target is never reached into, so a session
cannot be completed from data the target never saw.

Every candidate is keyed on filter *and* angle, so flats shot at another angle
are never borrowed — a neighbour holding only PA240 contributes nothing to a
PA310 session, and the search moves past it rather than stopping there.

```
session Session_03 (2026-09-14): 6 flats for filter S (PA310), want 14;
searched 2026-09-10, 2026-09-11, 2026-09-15, 2026-09-16
```

A shortfall warns and the run continues: the frames are usable, and being short
is not the same as having none. The warning only appears when a target is set —
at `0` there is nothing to fall short of. It names the count and every date
consulted, so a shortfall is distinguishable from a session that was never
properly searched.

A filter with lights but **no** flats at all is recorded in `missingFlats`,
warned about, and not copied, at any `--flat-count`. Flats for a filter with no
lights are never copied either.

Each planned flat records the date directory it was found in, not just its
name. `flats/<filter>/` in the output can therefore mix nights, and the copy
resolves its source from that recorded date rather than from the session's own
— otherwise a borrowed flat would be looked for in the wrong directory and
fail to open.

### 3.3 Why the session date, not the frame date

Flats are matched on the **session directory date**, never the date embedded in
the light filenames. Sessions run past midnight, so for most nights the two disagree by a day;
only the directory name lines up with `Flats/`. Borrowing (§3.2) reaches only
into other *session directory* dates, never into a frame-date.

This is why `frameDateMin` and `frameDateMax` are reported separately from `date` in `sessions.csv`.

### 3.3 Why the session date, not the frame date

Flats are matched on the **session directory date**, never the date embedded in
the light filenames. Sessions run past midnight, so for most nights the two disagree by a day;
only the directory name lines up with `Flats/`.

This is why `frameDateMin` and `frameDateMax` are reported separately from `date` in `sessions.csv`.

---

## 4. Master biases

```
<input>/Bias/Masters/*.xisf
```

The scan of master bias directory is **non-recursive** — subdirectories are skipped

```
           gain     frames 
            │         │
masterBias_GA0_-10C_257f_20251117.xisf
                 │           │
               temp         date
```

- Keyed by **gain**, taken from the filename via `_(GA\d+)_`.
- The frame count (`257f`) is **not read** — see §6.
- The date suffix must be eight digits directly before `.xisf`.
- A file with no gain token is skipped with a warning. 
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

The scan recurses **exactly one level**.
Files directly under `Masters/` are accepted alongside per-gain subdirectories.

### 5.1 The key is the filename, not the directory

The directory names and the filenames **deliberately disagree**:
`G2750/` holds `GA2750` files. The key comes from the filename alone.

Keying on the directory would yield `G2750`, which matches no light frame, and
**nothing would be copied at all**. This is the single sharpest edge in the
input contract. A dark in a directory whose name suggests gain `G2750` but
whose file says `GA2750` works; a dark in a directory named `GA2750` whose file
says `GA2750` also works, because the directory is never read.

The key renders as `GA2750/600s`.

### 5.2 The `-10C` hardcode

A light's needed dark is computed with `lightComboRe`:

`-10C` is **hardcoded**. The setpoint must appear literally, between the binning
token and the gain. A light taken at a different sensor temperature matches no
dark, and the only symptom is a warning listing the darks that *were* found.

Note this is a narrower pattern than `gainRe` (`_(GA\d+)_`), which has no
setpoint requirement. So a frame at the wrong temperature still contributes its
gain to the bias set, but contributes no dark.

### 5.3 Which darks get copied

Darks collection runs **after** the quality pass and considers **only approved
frames**. A dark for a rejected frame is never copied.

Missing masters are warnings, not errors — the frames are still usable data and
the dark can be supplied later:

```
no master dark for GA2750/600s in "..." (available: GA0/300s GA0/600s), affected sessions: Session_01
```

---

## 6. Duplicate masters

Two masters for one key is an anomaly worth surfacing whichever way it resolves

**Only the trailing date decides.** The frame count in a bias name (`257f`,
`129f`) is deliberately not read — more frames is not better than a more recent calibration.

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
| `paRe` | `_PA(\d+)` | 105 | session angle; flat index — a session must yield exactly one (§2.4) |
| `flatFilterRe` | `^FLAT_([LRGBSHO])_` | see §3.1 | flat index — **anchored at start**, set from `knownFilters` |
| `frameDateRe` | `_(\d{8})_\d{6}_\d+_PA` | 113 | report only |
| `gainRe` | `_(GA\d+)_` | 125 | session gains; bias index |
| `lightComboRe` | `_(\d+)s_BIN\d_-10C_(GA\d+)_` | 130 | light → dark — **`-10C` hardcoded** |
| `darkComboRe` | `masterDark_(\d+)s_(GA\d+)_` | 133 | dark index — **filename, not directory** |
| `masterDateRe` | `_(\d{8})\.xisf$` | 137 | duplicate-master tiebreak |

---

## 8. Extensions and dotfiles

Two distinct extension sets, kept apart because masters are in PixInsight's format : 

| Set | Extensions | Used for |
|---|---|---|
| `fitsExts` | `.FIT` `.FITS` `.FTS` | lights, flats |
| `masterExts` | `.XISF` | master biases and darks |

Extensions are upper-cased before lookup, so `.fit` and `.xisf` work. But the
**sets do not overlap**: a master with a `.fit` extension is rejected, and a
light with a `.xisf` extension is not collected.

Dotfiles are excluded everywhere, two sources produce them and both are traps:

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

**With `-r`, the walk stops at depth 2** 
This is sized for the acquisition layout, where a frame
sits two levels below `Lights/<target>`:

```
Lights/<target>/2026-09-10/S/frame.FIT
                    ←1→   ←2→
```

The cap cuts on **directory** depth, not file depth, so a directory at depth 3
is skipped whole while its parent's depth-2 frames are still collected. When
the cap stops the walk, `analyze` says the search was bounded rather than
complete — on stderr.

Also excluded: directories named like a frame (covers a directory called `trap.FIT`),
symlinked directories (cycle risk; the acquisition volumes hold none).
A symlinked *file* is still analysed. Duplicate basenames across
subdirectories are warned about.

### 9.1 Known limitation: the filter and date columns

`parseFilename` is separate from `prepare`'s regexes and
much dumber — it splits on `_` and takes positional fields:

So the third `_`-separated field is the filter and the eighth is the date. This
holds for light and flat names but **not** for a generated master name:

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

Both CSVs use `;` as separator, matching
the reference PixInsight tool's export format.

`metrics/sessions.csv`:

```
session;date;pa;filters;lightCount;approvedCount;rejectedCount;flatCount;missingFlats;frameDateMin;frameDateMax;gains;darks
Session_01;2026-09-16;PA310;S;2;2;0;2;;2026-09-16;2026-09-17;GA0;GA0/300s
```

`flatCount` counts every flat copied for the session, **including any borrowed
from a neighbouring date** (§3.2), so it does not imply the flats came from
that night. The source dates are recorded per flat in the plan but are not
written to the CSV.

`metrics/frames.csv` — one row per analysed frame, approved or not:

```
session;sessionDate;pa;filter;filename;detectedStars;starCount;avgFWHM;avgSignal;avgEccentricity;snr;score;decision;error
```

The `decision` column holds one of six values, which are **data, not messages** 
Anything parsing this file must match on them:

| Value | Meaning |
|---|---|
| `APPROVED` | passed every threshold |
| `REJECTED NO STARS` | no star met the detection floor |
| `REJECTED ECCENTRICITY` | failed `--max-ecc` |
| `REJECTED FWHM` | failed `--max-fwhm` |
| `REJECTED SNR` | failed `--min-snr` |
| `REJECTED SCORING` | failed `--min-score` |

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
| `--flat-count` negative | `--flat-count cannot be negative, got %d` |
| `Lights/<target>` absent | `target %q not found in %q` |
| No date directories | `no session (YYYY-MM-DD directory) found in %q` |
| More than one angle in a session | `mixed angles: session ... multiple angles ...` (§2.4) |
| Sessions of one target at different angles | `... is acquired at N different angles (...)` (§2.4) |
| Malformed config YAML | (deliberate: the config decides which frames get discarded) |
| `--config` path given but missing | no fallback to the search path |

### Warnings — the run continues

| Condition |
|---|
| `Bias/Masters` or `Darks/Masters` directory absent |
| No flats for a filter that has lights, after borrowing (§3.2) |
| Fewer than `--flat-count` flats for a filter, after borrowing (§3.2) — only when that flag is positive |
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

3. **Rotated targets cannot be prepared.** One target means one angle, chosen
   upfront (§2.4). A session with two angles, or sessions of one target at
   different angles, is a hard failure. If you ever *do* need to combine
   targets acquired at different angles, that is a change to the acquisition
   layout, not something garfield can reconcile — the flats are matched per
   session date, so there is no single angle to calibrate against.

4. **Session numbers are positional.** Inserting a date directory renumbers
   every later session. Never treat `Session_03` as a stable identifier — join
   on the `date` column.

5. **Flats must live under a session date directory of the same target**,
   matching the *directory* date. Because sessions cross midnight, the frame
   date inside the filenames will usually differ. Both are reported; use `date`,
   not `frameDateMin`. With `--flat-count 0`, the default, a session keeps
   only what its own date held — so a flats directory with a few frames, or
   none, is entirely normal and never warned about beyond `missingFlats`. With
   a positive count a session short of its target borrows from its neighbours
   (§3.2), and borrowing never crosses into another target's dates.

6. **Only seven filters are known**, on both sides: L, R, G, B, S, H, O. A flat
   with any other filter is dropped with a warning, and a light subdirectory
   that is not one of the seven is skipped with a warning rather than becoming
   a phantom filter that then reports a missing flat. **Narrowband needs
   `knownFilters` extended** — `FLAT_Ha_` and `FLAT_OIII_` do not parse until
   then (§3.1).

7. **Masters must be `.xisf`.** A `.fit` master is rejected by the extension
   check *and* excluded by the directory structure — the raw calibration frames
   under `Darks/-10C/G0/30s/*.FIT` are doubly out of scope.

8. **`analyze`'s filter and date columns are positional.** A
   `masterFlat_BIN-1_9576x6388_...fit` reports `9576x6388` as its filter
   (§9.1).

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
| flats topped up to 14 from the previous night | `TestCollectFlatsBorrowsFromPreviousDate` |
| first session borrows from the following night | `TestCollectFlatsFirstSessionBorrowsFromNextDate` |
| search walks past a neighbour with no match | `TestCollectFlatsWalksPastAnEmptyNeighbour` |
| borrowing never crosses to another target | `TestCollectFlatsNeverBorrowsAForeignDate` |
| shortfall reported with count and dates | `TestCollectFlatsReportsAShortfall`, `TestPrepareWarnsWhenShortOfFlats` |
| `--flat-count 0` keeps the session's own date | `TestCollectFlatsZeroTargetKeepsOnlyTheSessionDate`, `TestPrepareZeroFlatCountWarnsNothing` |
| negative `--flat-count` rejected | `TestPrepareRejectsNegativeFlatCount` |
| `prepare.flat-count` is a config key | `TestFlatCountIsAConfigKey` |
| a borrowed flat is copied from its own date | `TestPrepareBorrowsFlatsAndCopiesThem` |
| consultation order, first session included | `TestFlatSearchOrder` |
| mixed angles within a session are fatal | `TestPrepareMixedAnglesIsFatal`, `TestPlanSessionLeavesAngleUnsetOnMixedAngles` |
| sessions at different angles are fatal | `TestPrepareMixedAnglesAcrossSessionsIsFatal`, `TestMixedAnglesAcrossSessions` |
| every offending session reported at once | `TestPrepareReportsEveryMixedAngleAtOnce` |
| a single-angle target still prepares | `TestPrepareSingleAngleTargetIsUnaffected` |
| flats matched on PA, multi-angle flats fine | `TestPrepareFiltersFlatsByAngle` |
| missing bias/dark/flats are warnings, not errors | `TestPrepareWarnsOnMissing{Bias,Dark,Flats}`, `TestPrepareWithout{Bias,Darks}MastersIsNotAnError` |
| no verdict means not approved | `TestSessionPlanApproved` |
| analyze master-name filter limitation | `TestParseFilenameMasterFlat` |
| documented output paths in help | `TestPrepareLongHelpDocumentsLayout` |

Note that `TestPrepareLongHelpDocumentsLayout` asserts only the three session
paths. `bias/`, `darks/` and `metrics/` are implemented and tested but are not
mentioned in `prepare --help`.
