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

A real frame:

```
target   type  filter  exp    bin    temp  gain   date      time    ms   PA     flip
  │       │      │      │      │      │     │      │         │      │    │      │
LBN527_LIGHT_R_300s_BIN1_-10C_GA0_20260910_021539_988_PA310_E.FIT
```

`<target>` is the `--target` value. `<flip>` is `E` or `W` side of pier.

Note the two different conventions this one name carries: the **target** comes
first in a light but a flat leads with `FLAT_`, and the **date** is eighth here
and sixth in a flat (§3.1). The grammars are independent — no position is
cross-checked against the other — so a token can sit at a different index in each.

There is a **fifth** token here that garfield never reads: the `filter` field. It
is transcribed from the same wheel setting as the filter directory, which is the
authoritative one (§2.2), so it carries no information the run does not already
have.

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

Never read at all: **filter**, exposure, binning, setpoint, and the `LIGHT`
type marker. The filter is the wheel (§2.2); the rest either feed a report or
are decoration.

Only two of the four read tokens change what is copied. A missing **frame date**
costs a report column and nothing else. A missing **PA**, **gain**, or
**exposure + gain** pair costs data: the frame is still copied into `lights/`,
but it never reaches a flat, a bias, or a dark.

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

A real flat, annotated token by token:

```
FLAT_S_67.02s_BIN1_-10C_GA2750_20260916_071758_808_PA310.FIT
     │     │     │     │     │      │      │    │     │
     │     │     │     │     │      │      │    │     └── PA310   _PA(\d+)
     │     │     │     │     │      │      │    └──────── ms      808
     │     │     │     │     │      │      └───────────── time    071758
     │     │     │     │     │      └──────────────────── date    20260916
     │     │     │     │     └─────────────────────────── gain    GA2750
     │     │     │     └──────────────────────────────── temp    -10C
     │     │     └────────────────────────────────────── bin     BIN1
     │     └──────────────────────────────────────────── exp     67.02s
     └────────────────────────────────────────────────── filter  S
```

Two tokens are load-bearing and three of the rest are not:

| Token | Regex | If absent |
|---|---|---|
| filter | `^FLAT_([LRGBSHO])_` | file dropped, with a warning naming it (§3.1.1) |
| PA | `_PA(\d+)` | file dropped, with a warning naming it |

Exposure, binning, setpoint, gain, date, time and milliseconds are **never read**
on the flat side. `67.02s` and `GA2750` in the name above are decoration: no flat
is ever matched to a light by exposure or gain. That is the opposite of the dark
side, where both are part of the key (§5).

### 3.1.1 The seven known filters

`flatFilterRe` is **anchored on the `FLAT_` prefix**, and its group is a
character class spliced from `knownFilters`:

```
^FLAT_([LRGBSHO])_
```

`FLATL_41s_..._PA310.FIT` does not match, because after `FLAT` comes `L` rather
than `_`. Neither does a `masterFlat_...` file or any light frame.

The recognised set is **exactly one character** from `LRGBSHO` — L, R, G, B,
S, H, O:

```
FLAT_S_67.02s_BIN1_-10C_GA2750_20260916_071758_808_PA310.FIT   → S     indexed at S@310
FLAT_Ha_41.4s_BIN1_PA310.FIT                                  → nil   no known filter
FLAT_OIII_41.4s_BIN1_PA310.FIT                                → nil   no known filter
FLAT_L2_41.4s_BIN1_PA310.FIT                                  → nil   no known filter
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
session 2026-09-16, filter S, PA310
  →  Flats/2026-09-16/  file starting FLAT_S_ and containing _PA310
```

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

- Keyed by **gain**, taken from the filename via `_(GA\d+)_`.
- The frame count (`257f`) is **not read** — see §6.
- The setpoint (`-10C`) is **not read** here either: a bias matches a gain,
  not a gain and a temperature. Only `lightComboRe` on the *light* side has the
  setpoint in its pattern (§5.2).
- The date suffix must be eight digits directly before `.xisf`. A master with
  no parseable date cannot be tiebroken, so it loses to any dated rival (§6).
- A file with no gain token is skipped with a warning.
- Only **one** master bias is copied per gain, for the whole target.

The asymmetry with darks is deliberate and worth knowing before it surprises
you: `Bias/Masters/` is flat, while `Darks/Masters/` accepts one level of
per-gain subdirectories (§5). Dark masters are organised by gain on disk;
biases are not.

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

The scan recurses **exactly one level**: files directly under `Masters/` are
accepted alongside per-gain subdirectories, and a directory two levels down
(`Darks/Masters/G0/30s/`) is not visited at all. That cap is what keeps the raw
calibration frames out of the index.

### 5.1 The key is the filename, not the directory

The directory names and the filenames **deliberately disagree**:
`G2750/` holds `GA2750` files. The key comes from the filename alone.

Keying on the directory would yield `G2750`, which matches no light frame, and
**nothing would be copied at all**. This is the single sharpest edge in the
input contract. A dark in a directory whose name suggests gain `G2750` but
whose file says `GA2750` works; a dark in a directory named `GA2750` whose file
says `GA2750` also works, because the directory is never read.

The key renders as `GA2750/600s`.

### 5.2 The setpoint is part of the dark key

A light's needed dark is computed with `lightComboRe`:

```
_(\d+)s_BIN\d_([-+]?\d+C)_(GA\d+)_
```

Exposure, setpoint and gain — the three properties that together make a dark
necessary. A dark is **thermal**, so a `-10C` dark does not calibrate a `0C`
light, and the setpoint is therefore part of the key rather than an assumption
about the instrument.

The setpoint used to be hardcoded as `-10C` in that pattern, which meant a target
shot at any other temperature matched no dark at all and the run said nothing
useful. A target may also mix temperatures across its sessions — a season can
straddle a change of cooling — so each session draws its own dark:

```
no master dark for GA0/300s/0C in ".../Darks/Masters"
  (available: GA0/300s/-10C), affected sessions: Session_02
```

The `available:` list is what makes a near miss diagnosable: the operator sees
that the right exposure and gain are on hand at the wrong temperature.

#### What the setpoint does *not* key

Only the dark side reads a setpoint. It is easy to over-apply this, so explicitly:

| Pairing | Keyed on | Setpoint? |
|---|---|---|
| light → dark | exposure + gain + **setpoint** | yes |
| light → bias | gain | **no** — `indexBiasMasters` reads no setpoint |
| light → flat | filter + angle | **no** — never read on either side |

A **flat** is a shuttered frame of sky, so its dark current is negligible and the
setpoint does not change what it corrects. A flat shot at `0C` calibrates a
`-10C` light perfectly well, and matching them on temperature would reject good
flats for no physical reason. Pins: `TestFlatsAreNotFilteredByTemperature`.

A **bias** is keyed on gain alone even though `masterBias_GA0_-10C_...` carries a
setpoint. Bias is a much weaker function of temperature than a dark, and reading
it would double the bias key and the copy count for no stated need.

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
no master dark for GA2750/600s in "..." (available: GA0/300s GA0/600s), affected sessions: Session_01
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

All in `cmd/prepare.go` unless noted.

| Var | Pattern | Line | Keys |
|---|---|---|---|
| `sessionDateRe` | `^\d{4}-\d{2}-\d{2}$` | 103 | which directories are sessions |
| `paRe` | `_PA(\d+)` | 106 | session angle; flat index — a session must yield exactly one (§2.4) |
| `flatFilterRe` | `^FLAT_([LRGBSHO])_` | 139 | flat index — **anchored at start**, spliced from `knownFilters` (§3.1.1) |
| `frameDateRe` | `_(\d{8})_\d{6}_\d+_PA` | 143 | report only |
| `gainRe` | `_(GA\d+)_` | 155 | session gains; bias index |
| `lightComboRe` | `_(\d+)s_BIN\d_-10C_(GA\d+)_` | 160 | light → dark — **`-10C` hardcoded** |
| `darkComboRe` | `masterDark_(\d+)s_(GA\d+)_` | 163 | dark index — **filename, not directory** |
| `masterDateRe` | `_(\d{8})\.xisf$` | 167 | duplicate-master tiebreak |

`flatFilterRe` is assembled from `flatNamePrefix` and `knownFilters` rather than
written out, so the pattern cannot drift from the constant the directory check
uses. The two sides read one source of truth, which matters because the set is
the wheel (§3.1.3) rather than something a run can vary. The group **must stay
capturing** — the filter is `mf[1]`, and an alternation or a non-capturing group
would return `nil` from every lookup while still compiling.

The line numbers are accurate as of this revision and will drift; the patterns
are what matters and each is pinned by a test in the appendix.

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
| `--output`, `-o` | *(CSV file; required for csv/both to a file)* |

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
│   ├── bias/                          ← shared, one file per gain, if found
│   │   └── masterBias_GA0_-10C_257f_20251117.xisf
│   └── darks/                         ← shared, per gain+exposure, if found
│       └── masterDark_300s_GA0_-10C_20251118.xisf
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
Session_01;2026-09-16;PA310;S;2;2;0;2;;2026-09-16;2026-09-17;GA0;GA0/300s
Session_02;2026-09-20;PA310;O;1;1;0;1;H;;2026-09-20;2026-09-20;GA0;GA0/300s
```

| Column | Meaning |
|---|---|
| `session` | **Bare** name (`Session_01`), never a path — join on this |
| `date` | the session **directory** date, the one flats matched on (§3.3) |
| `pa` | the session position angle |
| `filters` | filter directories the session actually holds |
| `lightCount` / `approvedCount` / `rejectedCount` | frames analysed, and how they split |
| `flatCount` | flats **copied**, including borrowed ones (§3.2) |
| `missingFlats` | filters with lights but no flats at all |
| `frameDateMin` / `frameDateMax` | range of the dates **inside** the light filenames |
| `gains` | distinct gains among the approved frames |
| `darks` | `(gain/exposure)` keys a dark was copied for |

`date` and `frameDateMin` disagree on most nights, and that is correct: sessions
cross midnight. `date` is the one that matched flats.

`flatCount` counts every flat copied for the session, **including any borrowed
from a neighbouring date** (§3.2), so it does not imply the flats came from
that night. The source dates are recorded per flat in the plan but are not
written to the CSV — if you need to know where a flat came from, read the
filename's date field, since it is never rewritten (§10).

`metrics/frames.csv` — one row per analysed frame, approved or not:

```
session;sessionDate;pa;filter;filename;detectedStars;starCount;avgFWHM;avgSignal;avgEccentricity;snr;score;decision;error
```

`decision` holds one of six values, which are **data, not messages** — anything
parsing this file must match on them, and the strings are part of the contract:

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
| `analyze -r` stopped at the depth cap |

Multiple PA values in one session is **not** in this table: it is fatal (§2.4),
and an earlier revision of this document listed it in both places.

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
