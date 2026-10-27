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

**Contents**

| § | |
|---|---|
| [1](#1-overview) | Overview and the tree at a glance |
| [2](#2-lights) | Lights — sessions, filters, filename grammar, position angles |
| [3](#3-flats) | Flats — layout, the seven filters, matching, borrowing |
| [4](#4-master-biases) | Master biases |
| [5](#5-master-darks) | Master darks |
| [6](#6-duplicate-masters) | Duplicate masters |
| [7](#7-regex-reference) | Every regex in one table |
| [8](#8-extensions-and-dotfiles) | Extensions and dotfiles |
| [9](#9-analyze-input) | `analyze` input, and its one known limitation |
| [10](#10-output-layout) | Output layout and reports |
| [11](#11-failure-modes) | Failure modes — fatal vs warning |
| [12](#12-sharp-edges) | Sharp edges, ranked by how quietly they fail |
| [Appendix](#appendix-where-the-contract-is-pinned) | Which test pins which claim |

For a task-oriented summary rather than the full contract, see
[README.md](../README.md).

---

## 1. Overview

The acquisition tree is a capture volume. `prepare` reads it and writes a
session tree that can be imported into WBPP (PixInsight) in one step.

| Flag | Default |
|---|---|
| `--input`, `-i` | `/Volumes/Dyno/Kosmodrom` |
| `--output`, `-o` | `/Volumes/T7/Workspace` |
| `--target`, `-t` | *(required)* |
| `--camera` | `c3` |

Two cameras have written into this tree, and their filenames differ. `--camera`
selects which convention is read; the directory structure is the same for both.

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
│   │   ├── FLAT_S_67.02s_BIN1_-10C_GA2750_…_PA310.FIT        ← c3
│   │   └── IC1805_FLAT_S_BIN1_-20C_001_20201024_…_W.FIT      ← g4
│   └── 2026-09-16/            ← may be read to top up another session (--flat-count)
│
├── Bias/
│   └── Masters/               ← flat: no subdirectories
│       ├── masterBias_GA0_-10C_257f_20251117.xisf            ← c3, per gain
│       └── masterBias_-20C_257f_20201024.xisf                ← g4, per setpoint
│
└── Darks/
    └── Masters/
        ├── G0/                                               ← c3 only
        │   └── masterDark_300s_GA0_-10C_20251118.xisf
        ├── G2750/
        │   └── masterDark_600s_GA2750_-10C_20251120.xisf
        └── masterDark_900s_-20C_20201024.xisf                ← g4, no gain subdir
```

Only `Lights/<target>` is required. A missing `Flats`, `Bias/Masters` or
`Darks/Masters` produces warnings, not errors — see
[§11 Failure modes](#11-failure-modes).

**The two cameras' files coexist safely.** Every pattern is disjoint: a C3 flat
starts with `FLAT_` and so contains no `_FLAT_` substring for the G4 pattern to
find, and the gain and setpoint tokens at the corresponding position of a master
name cannot both be present. A file belonging to the other camera does not parse
under the active `--camera` and is left alone silently, which is correct — it is
valid data for the other instrument, not a malformed frame.

Directory names are exact: `Lights`, `Flats`, `Bias`, `Darks`, `Masters`.
These are case-sensitive, unlike file extensions. So `lights/` or `masters/`
is not found, and the run proceeds with whatever is missing — which is why a
typo in a directory name reads as "that data was never there".

Each subtree is scanned to a **fixed depth**, and nothing recurses further:

| Directory | Depth | Consequence |
|---|---|---|
| `Lights/<target>` | 3 (`<date>/<filter>/`) | deeper nesting is not seen |
| `Flats/` | 1 (`<date>/`) | flat filter is a filename token (§3.1) |
| `Bias/Masters` | 0 | a bias in a subdirectory is invisible (§4) |
| `Darks/Masters` | 1 | per-gain subdirs accepted, two levels are not (§5) |

`prepare` flags that shape this tree:

| Flag | Default | Effect |
|---|---|---|
| `--flat-count` | `0` | Flats per filter and angle. `0` keeps a session to its own date; `N` tops it up from neighbouring nights (§3.2). |
| `--camera` | `c3` | Filename convention and matching policy: `c3` (rotator and gain) or `g4` (fixed angle, no gain). An unknown value is rejected. |

`conf/garfield.yaml` ships `flat-count: 0`, so borrowing is opt-in.

---

## 2. Lights

### 2.1 What makes a session

A session is a direct subdirectory of `Lights/<target>` whose name matches
exactly `sessionDateRe`:

```
^\d{4}-\d{2}-\d{2}$
```

That is `YYYY-MM-DD` and nothing else. The check is lexical, so `2026-9-16` is
not a session and neither is `2026-09-16_old` or `2026-09-16-bias`.

Anything else in `Lights/<target>/` is ignored — stray files, dotfiles, a
`masterFlat_*.fit` dropped there by mistake. Only directories are considered,
and only ones that match.

**Session numbering is positional, not derived from the date.** The matching
directories are sorted as strings and the index in that list, plus one, becomes
the session number:

```
Lights/M51/2026-01-15/   →  sorted first   →  Session_01
Lights/M51/2026-03-02/   →  sorted second  →  Session_02
Lights/M51/2026-12-31/   →  sorted third   →  Session_03
```

So **adding, removing or renaming a date directory renumbers every later
session** — a new night inserted mid-run shifts `Session_05` to `Session_06`,
and everything after it. The `session` and `date` columns in
`metrics/sessions.csv` travel together, so read the date from the row rather
than assuming a mapping. `Session_03` is not a stable identifier (§12.4).

Padding is `%02d`: `Session_01` through `Session_99`, then `Session_100` — the
width is a minimum, not a maximum. A glob of `Session_[0-9][0-9]` therefore
misses a hundredth session, which matters only for a target acquired over a
hundred nights.

A target with no matching date directories at all is a fatal error:
`no session (YYYY-MM-DD directory) found in %q`.

### 2.2 The filter directory

Each subdirectory of a session directory is one filter, and its name must be
one of the seven known colour filters — **L**, **R**, **G**, **B**, **S**,
**H**, **O**. The name is used verbatim otherwise: no mapping, no normalisation.

A directory outside the set is **skipped with a warning** rather than carried
into the plan. Left alone it would become a filter with no flats, and the
session would then report a missing flat for it — naming a filter the
acquisition does not use.

Dotfile directories are skipped without comment.

The filter is a *directory* for lights and a *filename token* for flats. The two
are matched against each other, and each is validated against the same set
(`knownFilters`), for a flat to be picked up (§3). Neither the directory name nor
the flat's token is checked against **the other side's** spelling — a light in
`lights/S/` is matched to `FLAT_S_`, and the light's own filename token is never
read.

That is deliberate, and the acquisition makes it safe: the filter letter and the
directory name are two transcriptions of one filter-wheel setting, so they agree
by construction and there is nothing to compare. The wheel position does not
appear in a filename at all, so no cross-check is available even in principle.
The set is the wheel (§3.1.3).

### 2.3 Light filename grammar

The two cameras name their frames differently. `--camera` picks which grammar is
read; a frame from the other camera simply does not match, and is left alone.

**Moravian C3** — `.LIGHT.<filter>.<exp>.BIN.<temp>.GA<gain>.<date>.<time>.<ms>.PA<angle>.<flip>`

```
target  type  filter  exp    bin    temp  gain  date      time    ms   PA     flip
  │      │      │      │      │      │     │     │         │      │    │      │
LBN527_LIGHT_R_300s_BIN1_-10C_GA0_20260910_021539_988_PA310_E.FIT
```

**Moravian G4** — `.LIGHT.<filter>.<exp>.BIN.<temp>.<index>.<date>.<time>.<ms>.<flip>`

```
prefix  type  filter  exp    bin    temp   index  date      time    ms   flip
  │      │      │      │      │      │       │      │         │      │    │
IC1805HA_LIGHT_H_900s_BIN1_-20C_001_20201024_204428_998_W.FIT
```

`<flip>` is `E` or `W` side of pier, and is cosmetic in both grammars. On the G4
the `<index>` sits exactly where the C3 carries its gain — which is why `gainRe`
(`_(GA\d+)_`) cannot false-match a G4 frame: the token there is `001`, not `GA0`.

The G4 `<prefix>` is the target with the filter appended. It is **never read**:
the `Lights/<target>/<date>/<filter>/` directory is authoritative (§2.2), which
is why `Lights/IC1805/` holds frames whose names begin `IC1805HA_`.

Note the conventions a single name mixes: the **target** comes first in a light
but a flat leads with `FLAT_`, and the **date** is eighth in a light but sixth or
seventh in a flat depending on the camera (§3.1). The grammars are independent —
no position is cross-checked against another — so a token can sit at a different
index in each.

The `filter` token is likewise never read. It is transcribed from the same wheel
setting as the directory, which is the authoritative one (§2.2).

**No regex validates the whole name.** Independent regexes pluck tokens out of it.
A frame missing a token still gets analysed and copied — the token is simply
absent from the report.

| Token | C3 regex | G4 regex | Consequence if absent |
|---|---|---|---|
| frame date | `_(\d{8})_\d{6}_\d+` | *(same)* | `frameDateMin/Max` stay empty in `sessions.csv` |
| PA | `_PA(\d+)` | *(not present)* | C3 only: session PA is empty, so **no flats match** |
| gain | `_(GA\d+)_` | *(not present)* | C3 only: no bias copied for that gain |
| exposure + setpoint + gain | `_(\d+)s_BIN\d_([-+]?\d+C)_(GA\d+)_` | `_(\d+)s_BIN\d_([-+]?\d+C)_(\d+)_` | **No master dark** for this frame |

`frameDateRe` deliberately omits the `_PA` terminator the C3 name carries, so the
same pattern reads both grammars and needs no profile branch.

Frame date, time, milliseconds and flip are not used for matching at all.

Never read at all: **filter**, exposure, binning, the `LIGHT` type marker, and —
on the G4 — the prefix and frame index. The sensor temperature setpoint *is*
read, but only to pair a light with its dark (§5.2); it never affects the
frame's own routing.

What a missing token costs depends on the camera. A missing **frame date** costs
a report column and nothing else. On the C3, a missing **PA**, **gain**, or
**exposure + setpoint + gain** triple costs data: the frame is still copied into
`lights/`, but it never reaches a flat, a bias, or a dark. On the G4 only the
**exposure + setpoint** pair matters, because that is all the dark key holds.

### 2.4 Position angles (C3 only)

**The G4 has no rotator, so this whole section does not apply to it.** A G4 frame
carries no angle, sessions record none, and flats are matched on filter alone
(§3.2). The cross-session and within-session checks below are skipped entirely
under `--camera g4` rather than passing vacuously — there is no angle to have a
fault about.

On the C3, a target is acquired at **one position angle, chosen before
acquisition starts.** Two faults contradict that and are both **fatal**:

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

C3 sessions carrying **no** angle are not part of this check. An empty `pa` can
never match a flat, so such a session already reports a missing flat (§3.2) —
and, since a C3 frame always carries one, that is a sign the wrong `--camera` was
given (§2.3).

This is the strictest rule in the input contract, and it is strict on purpose:
a session's flats are matched against one angle (§3.2), so there is no
calibration to apply if the angle is ambiguous. Rejecting the target is more
honest than picking one angle and producing a session that looks fine.

---

## 3. Flats

### 3.1 Layout

`Flats/<YYYY-MM-DD>/` — **one level only**. Unlike lights, there is no filter
subdirectory. The filter is a filename token instead. The scan is
non-recursive.

**C3 flat**, annotated token by token:

```
FLAT_S_67.02s_BIN1_-10C_GA2750_20260916_071758_808_PA310.FIT
     │     │     │     │     │      │      │    │     │
     │     │     │     │     │      │      │    │     └── PA310   _PA(\d+)
     │     │     │     │     │      │      │    └──────── ms      808
     │     │     │     │     │      │      └───────────── time    071758
     │     │     │     │     │      └──────────────────── date    20260916
     │     │     │     │     └─────────────────────────── gain    GA2750
     │     │     │     └───────────────────────────────── temp    -10C
     │     │     └─────────────────────────────────────── bin     BIN1
     │     └───────────────────────────────────────────── exp     67.02s
     └─────────────────────────────────────────────────── filter  S
```

**G4 flat**: an unused prefix before `_FLAT_`, **no exposure**, an index after the
setpoint, and **no angle**:

```
prefix      FLAT  filter  bin    temp   index  date      time    ms   flip
  │          │      │      │      │       │      │         │      │    │
H_AutoFlat_FLAT_H_BIN1_-10C_001_20220822_071017_086_E.FIT
```

Which tokens are load-bearing differs by camera:

| Token | C3 | G4 |
|---|---|---|
| filter | `^FLAT_([LRGBSHO])_` — required | `^.+_FLAT_([LRGBSHO])_` — required |
| PA | `_PA(\d+)` — required, part of the match key | **not present**, not read |
| prefix | absent | required by the pattern, **never read** |
| everything else | never read | never read |

A flat whose filter token is not one of the seven is dropped with a warning
naming it (§3.1.1). A C3 flat with no PA is dropped with a warning. Exposure,
binning, setpoint, gain, date, time, milliseconds and flip are **never read** on
the flat side in either grammar: `67.02s` and `GA2750` in the C3 name are
decoration, and no flat is ever matched to a light by exposure or gain. That is
the opposite of the dark side, where both are part of the key (§5).

The two patterns are disjoint on purpose. A C3 flat begins with `FLAT_`, so it
contains no `_FLAT_` substring for the G4 pattern to find; a G4 flat has a prefix,
so it cannot match the C3 pattern anchored at the start. Consequently a shared
`Flats/<date>/` holding both cameras' flats is safe: the inactive camera's files
are left alone silently rather than reported as malformed, because they are valid
data for the other instrument.

### 3.1.1 The seven known filters

There is one pattern per camera, each **anchored** and each splicing
`knownFilters` in as a character class:

```
c3:  ^FLAT_([LRGBSHO])_
g4:  ^.+_FLAT_([LRGBSHO])_
```

`FLATL_41s_..._PA310.FIT` does not match the C3 pattern, because after `FLAT`
comes `L` rather than `_`. Neither does a `masterFlat_...` file or any light
frame. The G4 pattern requires a prefix before `_FLAT_`, which is what keeps it
off a C3 flat and keeps the C3 pattern off a G4 flat.

The recognised set is **exactly one character** from `LRGBSHO` — L, R, G, B,
S, H, O:

```
FLAT_S_67.02s_BIN1_-10C_GA2750_20260916_071758_808_PA310.FIT   → S     indexed at S@310
FLAT_Ha_41.4s_BIN1_PA310.FIT                                   → nil   no known filter
FLAT_OIII_41.4s_BIN1_PA310.FIT                                 → nil   no known filter
FLAT_L2_41.4s_BIN1_PA310.FIT                                   → nil   no known filter
```

Two consequences:

- **Narrowband does not parse.** `Ha`, `OIII`, `SII`, `L2` and anything else
  outside the seven are dropped. There is no pending change that would accept
  them — the set is the wheel (§3.1.3).
- **Multi-character tokens cannot match by accident.** Because the group is a
  character class rather than an alternation, `Ha` cannot be read as `H` — the
  `_` that must follow the group is missing. A file whose filter is `H` is
  indexed; one whose filter is `Ha` is not, and never half-matched.

### 3.1.2 Reported, not silently dropped

A file is **reported** rather than quietly discarded when it carries the `FLAT_`
prefix but fails the filter test:

```
Flats/2026-09-16: "FLAT_Ha_41.4s_BIN1_PA310.FIT" has no known filter (LRGBSHO), ignored
```

That is deliberate: it separates "a flat garfield cannot use" from "some other
kind of frame sharing the directory". A file *without* the prefix (`FLATL_...`,
a light frame, a master) is left alone, because calling it a broken flat would
be misleading. Given the wheel (§3.1.3) the case that fires in practice is an
operator error rather than a filter garfield has yet to learn.

The prefix test is deliberately **looser** than the token test, so a plainly
malformed `FLAT_-weird_name.FIT` is named rather than discarded.

### 3.1.3 The set is the wheel

`knownFilters` is **the filter wheel's inventory**, not a design choice. The
wheel has seven positions and all seven are occupied — L, R, G, B, S, H, O, in
that order — on a remote site whose wheel is full. `LRGBSHO` is therefore
complete, and it will not grow: narrowband would need a different wheel, not a
different constant. Extending `knownFilters` for its own sake would describe a
filter the instrument cannot take.

Two consequences for anyone reading the constant:

- **The order is the wheel.** `L R G B S H O` is positions 1–7, not ASCII order
  and not an arbitrary tidy. Nothing in the code depends on it — `flatFilterRe`
  splices it into a character class, which is order-independent — so the sequence
  carries information nothing else does. Do not sort it.
- **The filter check is a safety net, not routine filtering.** Since every
  position is in the set, a legitimate filter directory never trips it.
  `isKnownFilter` only fires on an operator error: a hand-made `calibration`
  directory, a typo, an `Ha` folder copied in by hand.

The same set governs the light side (§2.2), where the filter letter in the
filename is transcribed from the same wheel setting as the directory. That is why
the light filename's filter token is never read — there is nothing to check it
against, and the wheel position is not in the name.

### 3.2 Matching, and borrowing across nights

Flats are indexed by `filter@PA`, e.g. `S@310`, and matched against the
session's filter directories and session PA:

```
c3:  session 2026-09-16, filter S, PA310
       →  Flats/2026-09-16/  file starting FLAT_S_ and containing _PA310

g4:  session 2020-10-24, filter H
       →  Flats/2020-10-24/  file matching ^.+_FLAT_H_
```

On the G4 the angle component is empty, so the key is `H@` and every flat of that
filter matches. That is correct rather than sloppy: the imaging train has no
rotator, so there is only one angle and no flat can belong to a different one.

A session's flat requirement is therefore `filter@angle` on the C3 and `filter@`
on the G4, and borrowing below works on the same key either way.

Note the date is the **session's**, not the frame date (§12.5): sessions cross
midnight, so the `20260916` inside the filename usually names the next day.

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
the light filenames. Sessions run past midnight, so for most nights the two
disagree by a day; only the directory name lines up with `Flats/`. Borrowing
(§3.2) reaches only into other *session directory* dates, never into a
frame-date.

Concretely, a session directory named `2026-09-16` holding frames stamped
`20260917` looks for flats in `Flats/2026-09-16/`. Moving the session directory
to the frame date would break it.

This is why `frameDateMin` and `frameDateMax` are reported separately from `date`
in `sessions.csv` (§10.1) — `date` is the one that matched flats, and the frame
dates are a range for reference. Pinned by `TestFramesCSVUsesSessionDateNotFrameDate`.

---

## 4. Master biases

```
<input>/Bias/Masters/*.xisf
```

The scan of the master bias directory is **non-recursive** — subdirectories are
skipped. A bias held in `Bias/Masters/G0/` is invisible.

```
masterBias_GA0_-10C_257f_20251117.xisf
         │     │      │      │
         │     │      │      └──── date    20251117
         │     │      └─────────── frames  257f      (not read, see §6)
         │     └────────────────── temp    -10C      (not read)
         └──────────────────────── gain    GA0       (the key)
```

The key differs by camera, and so does what is read:

| | C3 | G4 |
|---|---|---|
| name | `masterBias_GA0_-10C_257f_20251117.xisf` | `masterBias_-20C_257f_20201024.xisf` |
| key | **gain** (`GA0`) | **sensor temperature setpoint** (`-20C`) |
| pattern | `_(GA\d+)_` | `masterBias_([-+]?\d+C)_` |

- The frame count (`257f`) is **not read** — see §6.
- On the C3 the setpoint (`-10C`) is **not read** here: a C3 bias matches a gain,
  not a gain and a temperature. Only the *light* side carries a setpoint, and
  only to pair a light with its dark (§5.2).
- On the G4 the gain does not exist, so the setpoint is the key instead. A CCD
  runs at one gain setting; the temperature is what varies between sessions.
- The date suffix must be eight digits directly before `.xisf`. A master with
  no parseable date cannot be tiebroken, so it loses to any dated rival (§6).
- A file whose key token is missing is skipped with a warning that names what it
  was looking for — a "gain setting" on the C3, a "sensor temperature setpoint"
  on the G4.
- Only **one** master bias is copied per key, for the whole target.
- The directory is flat for both cameras: `Bias/Masters/` has no subdirectories
  in either convention.

The asymmetry with darks is deliberate and worth knowing before it surprises
you: `Bias/Masters/` is flat for both cameras, while `Darks/Masters/` accepts one
level of per-gain subdirectories — but only on the C3 (§5).

---

## 5. Master darks

**C3** — one level of per-gain subdirectories, or flat:

```
<input>/Darks/Masters/
├── G0/
│   └── masterDark_300s_GA0_-10C_20251118.xisf
├── G2750/
│   └── masterDark_600s_GA2750_-10C_20251120.xisf
└── masterDark_120s_GA0_-10C_20251119.xisf     ← also accepted
```

**G4** — flat, with no gain subdirectory at all:

```
<input>/Darks/Masters/
├── masterDark_600s_-20C_20220901.xisf
├── masterDark_600s_-10C_20220901.xisf
└── masterDark_900s_-20C_20220901.xisf
```

The scan recurses **exactly one level**: files directly under `Masters/` are
accepted alongside subdirectories, and a directory two levels down
(`Darks/Masters/G0/30s/`) is not visited at all. That cap is what keeps the raw
calibration frames out of the index. It also means the G4 layout is read without
any special case — a flat directory is simply the one-level scan with no
subdirectories in it.

### 5.1 The key is the filename, not the directory

The directory names and the filenames **deliberately disagree** on the C3:
`G2750/` holds `GA2750` files. The key comes from the filename alone.

Keying on the directory would yield `G2750`, which matches no light frame, and
**nothing would be copied at all**. This is the single sharpest edge in the
input contract. A dark in a directory whose name suggests gain `G2750` but
whose file says `GA2750` works; a dark in a directory named `GA2750` whose file
says `GA2750` also works, because the directory is never read.

The G4 has no gain subdirectory to be misled by, which removes that edge
entirely for that camera.

The key renders as:

| | rendering | e.g. |
|---|---|---|
| C3 | `gain/exposure/setpoint` | `GA2750/600s/-10C` |
| G4 | `exposure/setpoint` | `600s/-20C` |

The G4 form omits the gain field rather than emitting an empty one: `/600s/-20C`
would read as a missing value instead of an absent property.

### 5.2 The sensor temperature setpoint is part of the dark key

A light's needed dark is computed with a pattern per camera:

```
c3:  _(\d+)s_BIN\d_([-+]?\d+C)_(GA\d+)_
g4:  _(\d+)s_BIN\d_([-+]?\d+C)_(\d+)_
```

On the C3 that is exposure, sensor temperature setpoint and gain — the three
properties that together make a dark necessary. On the G4 it is exposure and
setpoint: the third group is the frame index, captured only because it terminates
the setpoint token, and the gain does not exist. A dark is **thermal**, so a `-10C` dark does not
calibrate a `0C` light, and the setpoint is therefore part of the key rather than
an assumption about the instrument. The full term is used on first mention and
"setpoint" after that, since it is the only setpoint any of these patterns reads.

The setpoint used to be hardcoded as `-10C` in that pattern, which meant a target
shot at any other temperature matched no dark at all and the run said nothing
useful. A target may also mix setpoints across its sessions — a season can
straddle a change of cooling — so each session draws its own dark:

```
no master dark for GA0/300s/0C in ".../Darks/Masters"
  (available: GA0/300s/-10C), affected sessions: Session_02
```

The `available:` list is what makes a near miss diagnosable: the operator sees
that the right exposure and gain are on hand at the wrong temperature. On the G4
the same message reads `no master dark for 600s/-20C ... (available: 600s/-10C)`.

#### What the sensor temperature setpoint does *not* key

Only the dark side reads a setpoint. "Setpoint" here means sensor temperature
throughout — the filter wheel has positions (§3.1.3) and a rotator has angles, so
the unqualified word is genuinely ambiguous in this document. It is easy to
over-apply the rule, so explicitly:

| Pairing | C3 key | G4 key | Sensor temperature setpoint? |
|---|---|---|---|
| light → dark | exposure + gain + **setpoint** | exposure + **setpoint** | yes, both |
| light → bias | gain | **setpoint** | C3: no · **G4: yes** |
| light → flat | filter + angle | filter | no, either camera |

A **flat** is a shuttered frame of sky, so its dark current is negligible and the
setpoint does not change what it corrects. A flat shot at `0C` calibrates a
`-10C` light perfectly well, and matching them on temperature would reject good
flats for no physical reason. Pinned by
`TestFlatsAreNotFilteredByTemperature`.

A **C3 bias** is keyed on gain alone even though `masterBias_GA0_-10C_...` carries
a setpoint. Bias is a much weaker function of temperature than a dark, and
reading it would double the bias key and the copy count for no stated need.

A **G4 bias** is keyed on the setpoint, because there is no gain to key it on.
That is not an inconsistency but the consequence of a CCD having one gain
setting: the only thing that varies between its sessions is the temperature.

#### Group order differs between the two sides

The tokens appear in **opposite orders**, which is easy to get wrong when reading
one pattern and writing the other:

```
light:  ..._300s_BIN1_-10C_GA0_...      exp, setpoint, gain   → m[1] m[2] m[3]
dark:   masterDark_300s_GA0_-10C_...    exp, gain, setpoint   → m[1] m[2] m[3]
```

Copying a group index from the light side to the dark side reads the setpoint as
a gain and indexes every master under a key nothing can match — quietly, since
the only symptom is a missing dark. Pinned by
`TestDarkComboReGroupOrderDiffersFromLight`.

The G4 patterns avoid the problem differently: both sides are
`exposure, setpoint`, in that order, so the two are interchangeable. The cost is
that the light pattern carries an unused third group where the C3 has its gain.

#### `-0C` and `0C` are one key

`normaliseTemp` collapses a signed zero to `0C`, so a light at `-0C` finds a dark
named `0C`. Zero is the only value where the sign is ambiguous; `-10C` and `10C`
are twenty degrees apart and stay distinct keys, because collapsing them would
substitute a dark shot warm.

### 5.3 Which darks get copied

Darks collection runs **after** the quality pass and considers **only approved
frames**. A dark for a rejected frame is never copied.

Missing masters are warnings, not errors — the frames are still usable data and
the dark can be supplied later:

```
no master dark for GA2750/600s/-10C in "..." (available: GA0/300s/-10C GA0/600s/-10C), affected sessions: Session_01
```

---

## 6. Duplicate masters

Two masters for one key is an anomaly worth surfacing whichever way it resolves.

**Only the trailing date decides.** The frame count in a bias name (`257f`,
`129f`) is deliberately not read — more frames is not better than a more recent
calibration, and preferring a larger count would quietly reward whichever
directory was scanned last.

| Situation | Outcome | Warning |
|---|---|---|
| Candidate date newer | Candidate wins | `two masters for GA0 (a and b), retaining b` |
| Candidate date older | Held master kept | `two masters for GA0 (b and a), retaining b` |
| Same date | **First in sorted order** kept | `... at the same date (a and b), date insufficient to decide, kept a by name order` |

Both collisions and ties are reported, never resolved silently. The wording
differs on purpose: the collision message says "retaining", implying the date
chose; the tie message says "date insufficient to decide", because it did not.
A reader can tell from the message alone whether the choice was earned.

An undecided tie is deterministic only because candidates are visited in sorted
order, which is why it is reported as a tie rather than a choice. A tie is
therefore reproducible across machines and runs — it is arbitrary, but it does
not wander.

Biases and darks are indexed independently, so both can live in the same
directory without colliding.

---

## 7. Regex reference

All in `cmd/prepare.go` unless noted. The C3 patterns are plain globals; the G4
ones are reached through `c3Camera` / `g4Camera` (§7.1).

| Var | Pattern | Camera | Keys |
|---|---|---|---|
| `sessionDateRe` | `^\d{4}-\d{2}-\d{2}$` | both | which directories are sessions |
| `paRe` | `_PA(\d+)` | c3 | session angle; flat index — a session must yield exactly one (§2.4) |
| `flatFilterRe` | `^FLAT_([LRGBSHO])_` | c3 | flat index — **anchored at start**, spliced from `knownFilters` (§3.1.1) |
| `g4FlatFilterRe` | `^.+_FLAT_([LRGBSHO])_` | g4 | flat index — requires the unused prefix, which keeps it off a C3 flat |
| `frameDateRe` | `_(\d{8})_\d{6}_\d+` | both | report only; no `_PA` terminator so one pattern reads both grammars |
| `gainRe` | `_(GA\d+)_` | c3 | session gains; bias index |
| `lightComboRe` | `_(\d+)s_BIN\d_([-+]?\d+C)_(GA\d+)_` | c3 | light → dark — exp, **setpoint**, gain (§5.2) |
| `g4LightComboRe` | `_(\d+)s_BIN\d_([-+]?\d+C)_(\d+)_` | g4 | light → dark — exp, setpoint; group 3 is an unused frame index |
| `darkComboRe` | `masterDark_(\d+)s_(GA\d+)_([-+]?\d+C)_` | c3 | dark index — **filename, not directory**; group order reversed |
| `g4DarkComboRe` | `masterDark_(\d+)s_([-+]?\d+C)_` | g4 | dark index — no gain token |
| `g4BiasKeyRe` | `masterBias_([-+]?\d+C)_` | g4 | bias index — setpoint, since there is no gain |
| `masterDateRe` | `_(\d{8})\.xisf$` | both | duplicate-master tiebreak |

`flatFilterRe` is assembled from `flatNamePrefix` and `knownFilters` rather than
written out, so the pattern cannot drift from the constant the directory check
uses. The two sides read one source of truth, which matters because the set is
the wheel (§3.1.3) rather than something a run can vary. The group **must stay
capturing** — the filter is `mf[1]`, and an alternation or a non-capturing group
would return `nil` from every lookup while still compiling. `g4FlatFilterRe` has
the same requirement.

The line numbers are omitted deliberately: they drifted faster than the patterns
and reading the wrong one is worse than having none. Each pattern is pinned by a
test in the appendix.

### 7.1 `cameraProfile`

`cameraProfile` (in `cmd/prepare.go`) is the single place the two cameras differ:

| Field | Purpose |
|---|---|
| `hasPA` | whether a position angle is read and matched (§2.4) |
| `hasGain` | whether a gain key exists (§4, §5) |
| `flatFilterRe` | the flat filter pattern (§3.1) |
| `isFlat` | whether a rejected name is worth reporting (§3.1.2) |
| `lightDark` | light filename → master dark key |
| `darkMaster` | master dark filename → key |
| `biasKey` | master bias filename → key |

`cameraProfileFor` resolves `--camera` and **rejects an unknown value** rather
than falling back. Silently preparing a target with the other camera's rules
would match flats across angles that never shared them, or find no calibration
at all.

The patterns are mutually exclusive, which is what lets both cameras' files share
one acquisition tree: a name belonging to the inactive profile does not match and
is left alone, not reported as malformed.

---

## 8. Extensions and dotfiles

Two distinct extension sets, kept apart because the masters are in PixInsight's
XISF format rather than FITS:

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
| `--output`, `-o` | *(directory; the report is `analyse_report.csv` inside it)* |

**Flat by default.** Only files directly in `--dir`. Descending is opt-in so a
stray frame below the chosen directory cannot quietly join the report.

**With `-r`, the walk stops at depth 2.** The scan root counts as depth 0. This
is sized for the acquisition layout, where a frame sits two levels below
`Lights/<target>`:

```
Lights/<target>/2026-09-10/S/frame.FIT
            ←1→    ←2→    ←3→
   root   target  date  filter
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

`parseFilename` is separate from `prepare`'s regexes and much dumber — it splits
on `_` and takes two fixed positions, `parts[2]` and `parts[7]`:

```go
parts := strings.Split(strings.TrimSuffix(name, filepath.Ext(name)), "_")
if len(parts) >= 3 { filter = parts[2] }
if len(parts) >= 8 { date = raw[:4] + "-" + raw[4:6] + "-" + raw[6:8] }
```

Those indices are counted on the light grammar, where they are correct:

```
LBN527_LIGHT_R_300s_BIN1_-10C_GA0_20260910_021539_988_PA310_E.FIT
          parts[2]=R        parts[7]=20260910   →  filter "R", date "2026-09-10"
```

The flat grammar puts the target token in front, which shifts both by one, so
the same indices land on the wrong fields:

```
FLAT_S_67.02s_BIN1_-10C_GA2750_20260916_071758_808_PA310.FIT
      parts[2]=67.02s  parts[7]=071758          →  filter "67.02s", date ""
```

The date is empty because `071758` is six characters and the parser requires
eight before slicing. So `analyze` pointed at a `Flats/` directory reports every
flat's filter as its exposure and its date as blank.

This does not affect `prepare`, which never calls `parseFilename` and uses the
anchored regexes of §2.3 and §3.1 instead. It is visible only in the `filter`
and `date` columns of an `analyze` CSV, and only when that CSV is the point of
the run rather than a tuning pass.

A generated master name is worse, because the positions do not merely shift but
land on data:

```
masterFlat_BIN-1_9576x6388_FILTER-S_mono_SESSION-1.fit
                   parts[2] = 9576x6388      ← reported as the filter
```

A master dropped into an `analyze` directory reports its sensor dimensions as a
filter. This is a known limitation, pinned by `TestParseFilenameMasterFlat`
(`cmd/analyze_csv_test.go:128`), so that the current output stays visible and a
future fix reads as a deliberate change rather than a surprise.

---

## 10. Output layout

Written under `<output>/<target>/`. Three directories may appear at the target
root, and one of them is conditional:

| Directory | Created when |
|---|---|
| `metrics/` | always |
| `Sessions/` | always |
| `rejected/` | **only if at least one frame was rejected** |

So a clean run leaves two, not three. `rejected/` is created lazily by the
first quarantine copy — an empty `rejected/Session_01/` would imply a session
failed when it did not. Pinned by `TestPrepareRealTreeShape`
(`cmd/prepare_test.go:2603`) for the shape and by `TestPrepareSessionLayoutIsSymmetric`
(`cmd/prepare_test.go:1082`) for the absence.

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
│   ├── bias/                          ← shared: per gain (c3) or setpoint (g4)
│   │   └── masterBias_GA0_-10C_257f_20251117.xisf
│   └── darks/                         ← shared: per gain+exposure+setpoint (c3)
│       └── masterDark_300s_GA0_-10C_20251118.xisf    or exposure+setpoint (g4)
│
└── rejected/                          ← NOT under Sessions/; only if non-empty
    └── Session_01/<FILTER>/*.FIT
```

`bias/` and `darks/` are conditional for the same reason: they appear only when
a master the run actually needs was found. A target with no darks on hand gets
`Sessions/Session_01/` and nothing beside it, plus a warning (§11).

Five things worth knowing:

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

Both CSVs use `;` as separator, matching the reference PixInsight tool's export
format, so garfield output can be read alongside it directly.

`metrics/sessions.csv` — one row per session, plus a trailing summary row in
some runs:

```
session;date;pa;filters;lightCount;approvedCount;rejectedCount;flatCount;missingFlats;frameDateMin;frameDateMax;gains;darks
Session_01;2026-09-16;PA310;S;2;2;0;2;;2026-09-16;2026-09-17;GA0;GA0/300s/-10C
Session_02;2026-09-20;PA310;O;1;1;0;1;H;;2026-09-20;2026-09-20;GA0;GA0/300s/-10C
```

| Column | Meaning |
|---|---|
| `session` | **Bare** name (`Session_01`), never a path — join on this |
| `date` | the session **directory** date, the one flats matched on (§3.3) |
| `pa` | the session position angle; **empty** for a camera with no rotator |
| `filters` | filter directories the session actually holds |
| `lightCount` / `approvedCount` / `rejectedCount` | frames analysed, and how they split |
| `flatCount` | flats **copied**, including borrowed ones (§3.2) |
| `missingFlats` | filters with lights but no flats at all |
| `frameDateMin` / `frameDateMax` | range of the dates **inside** the light filenames |
| `gains` | distinct gains among the approved frames; **empty** on the G4, which has no gain |
| `darks` | keys a dark was copied for: `gain/exposure/setpoint` on the C3, `exposure/setpoint` on the G4 (§5.2) |

`date` and `frameDateMin` disagree on most nights, and that is correct: sessions
cross midnight. `date` is the one that matched flats.

`flatCount` counts every flat copied for the session, **including any borrowed
from a neighbouring date** (§3.2), so it does not imply the flats came from
that night. The source dates are recorded per flat in the plan but are not
written to the CSV — if you need to know where a flat came from, read the
filename's date field, since it is never rewritten (§10).

`metrics/frames.csv` — one row per analysed frame, approved or not:

```
session;sessionDate;pa;filter;filename;detectedStars;starCount;avgFWHM;avgSignal;avgEccentricity;snr;score;decision;error;background;noise;bgStructure;cloudIndex;cloudRatio;structRatio
```

`decision` holds one of **seven** values, which are **data, not messages** — anything
parsing this file must match on them, and the strings are part of the contract:

| Value | Meaning |
|---|---|
| `APPROVED` | passed every threshold |
| `REJECTED NO STARS` | no star met the detection floor |
| `REJECTED ECCENTRICITY` | failed `--max-ecc` |
| `REJECTED FWHM` | failed `--max-fwhm` |
| `REJECTED SNR` | failed `--min-snr` |
| `REJECTED SCORING` | failed `--min-score` |
| `REJECTED CLOUDS` | clouded relative to its own session (§10.1.1) |

Both reports record the **bare** session name, never a path, so consumers join
on the session column. Metric columns come from the same `imageResultRow` used
by `analyze`, so the two commands' CSVs cannot disagree.

The three columns after `error` are measurement, appended so the position of
every existing column is unchanged:

| Column | Meaning |
|---|---|
| `background` | median sky level in **physical ADU**. `readfits` applies `BSCALE`/`BZERO`, so a file storing unsigned 16-bit data with `BZERO=32768` reports its true sky, not the stored integer. |
| `noise` | per-pixel noise sigma from the background/noise estimator |
| `bgStructure` | MAD of 64×64 block medians divided by `noise`: sky variation at scales far larger than a star, dimensionless |

`background`, `noise` and `bgStructure` are measurements. `cloudIndex` and
`cloudRatio` are what the cloud rule acts on, and are described in §10.1.1.

### 10.1.1 The cloud rule

Cloud is the one decision that cannot be made from a frame alone: the same sky
brightness means different things on a moonlit night and a dark one. So a frame
is compared against its own session.

```
cloudIndex  = (1 / snr) / sqrt(background)
cloudRatio  = cloudIndex  / p25(session cloudIndex)
structRatio = bgStructure / p25(session bgStructure)
clouded     = cloudRatio > --max-cloud  ||  structRatio > --max-structure
```

**Sessions are found by clustering on acquisition time**, not by directory or by
the date in the filename. Frames are sorted by the `_YYYYMMDD_HHMMSS_` stamp and a
gap of more than two hours starts a new session. This matters because:

- a night routinely crosses midnight, so the filename date changes part-way
  through one session;
- one date string can appear in two different sessions (the tail of one night and
  the start of the next), and grouping by date merges them;
- a flat directory of prepared frames has no session directories to read at all.

`analyze` and `prepare` then group by **(session, filter)**. Filter is part of the
key because the index is built from SNR and sky background, which differ between
filters and exposures; a B frame and an L frame from the same night cannot share a
baseline. `prepare` uses its known session number instead of clustering.

**Why divide by `sqrt(background)`.** A naive "SNR dropped, so it was cloudy"
also fires on moonlight, which raises the sky and lowers SNR while leaving the
stars intact. Dividing out the part explained by a brighter sky leaves noise that
came from scattering instead, which is what cloud does.

**Why two signals.** The ratio catches cloud that dims stars. `bgStructure` —
the MAD of 64×64 block medians in units of noise — catches patchy cirrus that
leaves the ratio untouched but varies the sky across the field. On the reference
night each signal caught frames the other missed.

**Why structure is relative too.** An absolute structure threshold depends on the
instrument and the sky: one night measured 0.47–0.60 across all its frames and
another 0.61–0.67, with no cloud in either. A constant cut rejected the whole of
the second night. Dividing by the session's own 25th percentile leaves a night
with a uniformly more structured sky alone and flags only frames that stand out
within their night.

**Why the 25th percentile.** The median would sit inside the cloud once several
frames are affected, dragging the baseline up and masking the event. The 25th
percentile stays on the clear frames as long as at least a quarter of the session
is clear.

A session with fewer than four measured frames has no meaningful percentile, so
neither test runs and the session is left alone.

Only `APPROVED` frames are changed. A frame already rejected for eccentricity or
FWHM keeps that reason, so the report retains the more specific diagnosis.

Both thresholds default to the values calibrated on the reference night
(`1.08` and `1.12`) and both are configurable under `thresholds.max-cloud` and
`thresholds.max-structure`; setting either to `0` disables that half. They are
provisional: calibrated on one night's five clouded frames, with the margins
noted in that comparison. Expect to retune after another cloudy target.

`analyze` infers the session from acquisition time, which is exact except when a
night contains a pause longer than two hours: that is treated as two sessions, so
each half gets its own baseline. `prepare` uses the real session directory.

---

## 11. Failure modes

### Fatal

| Condition | Message |
|---|---|
| `--target` missing | `--target is required` |
| `--input` or `--output` empty | `... cannot be empty` |
| `--flat-count` negative | `--flat-count cannot be negative, got %d` |
| `--camera` not `c3`/`g4` | `unknown --camera %q, want c3 or g4` |
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
| No master bias for a key the lights use: a gain (c3) or a setpoint (g4) |
| No master dark for a `(gain, exposure, setpoint)` (c3) or `(exposure, setpoint)` (g4) an approved frame needs |
| Two masters for one key (collision or tie) |
| Master filename carries no key token: no gain (c3) or no setpoint (g4) |
| `--camera` disagrees with every light frame's shape (§11.1) |
| `analyze -r` stopped at the depth cap |

Multiple PA values in one session is **not** in this table: it is fatal (§2.4),
and an earlier revision of this document listed it in both places.

### 11.1 A wrong `--camera` is loud, and now says so

Pointing the wrong profile at a target still fails safely — nothing matches, and
the usual warnings fire — but they name filters and masters rather than the
cause. One extra warning names it:

```
warning: --camera c3 expects a position angle but none of the 15 light frame(s)
carry one; is this G4 data?
```

It fires only when **every** light frame contradicts the profile. A field mixing
both cameras' shapes is a contradiction no camera switch resolves, so it is left
to the ordinary warnings rather than guessing.

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

2. **A dark is thermal, so the setpoint is part of its key.** A `-10C` dark does
   not calibrate a `0C` light, and one is not silently substituted for the other —
   the mismatch is reported, naming the masters that *were* found so a near miss
   is visible (§5.2). The gain regex carries no such requirement, so a frame at
   the wrong temperature still gets a bias but no dark. This was the reverse
   before the setpoint entered the key: a light at `0C` matched no dark at all,
   whatever was on disk.

3. **Rotated targets cannot be prepared.** On the C3, one target means one
   angle, chosen upfront (§2.4). The G4 has no rotator at all, so for that
   camera this edge does not exist. A session with two angles, or sessions of one target at
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
   a phantom filter that then reports a missing flat. **Narrowband is not a
   pending code change** — the wheel holds seven filters and all seven positions
   are filled, so `LRGBSHO` is the whole inventory; `FLAT_Ha_` and `FLAT_OIII_`
   need a different wheel to parse at all, not an extended constant
   (§3.1.3).

7. **Masters must be `.xisf`.** A `.fit` master is rejected by the extension
   check *and* excluded by the directory structure — the raw calibration frames
   under `Darks/-10C/G0/30s/*.FIT` are doubly out of scope.

8. **`analyze`'s filter and date columns are positional.** A
   `masterFlat_BIN-1_9576x6388_...fit` reports `9576x6388` as its filter
   (§9.1).

9. **`--camera` defaults to `c3`.** A G4 run that omits it finds no position
   angles, no flats and no masters, and warns once about the likely cause
   (§11.1). It fails safely, but it does fail — the switch is not inferred.

---

## Appendix: where the contract is pinned

Layout claims in this document are enforced by tests, not just documented:

| Behaviour | Test |
|---|---|
| only `YYYY-MM-DD` directories are sessions | `TestPrepareOnlyAcceptsDateDirs` |
| full output tree shape at the target root | `TestPrepareRealTreeShape` |
| no `rejected/` tree when nothing was rejected | `TestPrepareSessionLayoutIsSymmetric` |
| `Session_%02d` numbering and padding | `TestPrepareSessionDirPadding` |
| session dirs live under `Sessions/` | `TestPrepareSessionsLiveUnderSessionsDir` |
| darks keyed on filename not directory | `TestIndexDarkMastersKeysOnFilenameNotDirectory` |
| `.fit` masters rejected | `TestIndexDarkMastersRejectsNonMasters` |
| duplicate dark resolved by date; tie reported | `TestIndexDarkMastersPrefersTheLatestDate`, `TestIndexDarkMastersReportsAnUndecidedTie` |
| gain token shape, legacy `G0` must **not** match | `TestGainReMatchesAcquisitionNames` |
| light→dark exposure/setpoint/gain extraction | `TestLightDarkKeyRe`, `TestDarkComboRe` |
| the two patterns' group order is reversed | `TestDarkComboReGroupOrderDiffersFromLight` |
| `-0C` and `0C` are one key; other signs stay distinct | `TestNormaliseTempCollapsesSignedZero`, `TestDarkKeysMatchAcrossSignedZero` |
| a target spanning two setpoints draws one dark each | `TestPrepareMixedTemperatureTargetNeedsTwoDarks` |
| a dark at the wrong setpoint is reported, not substituted | `TestPrepareDoesNotBorrowADarkAtTheWrongTemperature` |
| flats are not matched on setpoint | `TestFlatsAreNotFilteredByTemperature` |
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
| G4 patterns extract exposure and setpoint, not gain | `TestG4PatternsExtractKeys` |
| the two cameras' patterns never match each other | `TestCameraProfilesAreDisjoint` |
| G4 records no angle and matches flats on filter alone | `TestG4PlanHasNoAngleAndMatchesFlatsByFilter` |
| a C3 flat in a shared Flats tree is ignored by a G4 run | `TestG4IgnoresC3FlatsInTheSameDirectory` |
| G4 bias/dark copied under setpoint keys | `TestG4PrepareCopiesMastersKeyedOnSetpoint` |
| a G4 target spanning setpoints draws one master each | `TestG4MixedSetpointsDrawTwoMasters` |
| the C3 mixed-angle rules do not reach the G4 | `TestG4DoesNotFailOnMissingAngle` |
| unknown `--camera` rejected | `TestCameraFlagValidation` |
| a wrong `--camera` names the likely cause | `TestCameraMismatchWarning` |
| `prepare.camera` is a config key | `TestCameraIsAConfigKey` |
| `analyze` reads G4 names without a switch | `TestAnalyzeParsesG4Names` |

Note that `TestPrepareLongHelpDocumentsLayout` asserts only the three session
paths. `bias/`, `darks/` and `metrics/` are implemented and tested but are not
mentioned in `prepare --help`.
