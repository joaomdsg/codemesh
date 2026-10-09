# codemesh

A local web tool for Go modules and Julia packages. It does two jobs:

- **Map**: find code smells, packages first.
- **Review**: read a change as declarations, riskiest first, so review stops
  being the bottleneck.

A third, **Diagnose**, is a spike: see [Diagnose (spike)](#diagnose-spike).

It analyses the working tree and re-analyses within a second of every save,
so you can keep it open while you code.

## Install

```
go install github.com/joaomdsg/codemesh/cmd/codemesh@latest
```

It needs Go 1.27 or newer and `git` on the PATH, and `julia` 1.10 or newer
to analyse a Julia package. The Diagnose spike's
"Try a fix" also needs `claude` (Claude Code) on the PATH, and "Open a
draft PR" needs `gh`, logged in, and push access to `origin`.

## Run

```
codemesh [-addr localhost:7777] [-base REF] [-poll 1s] [-agent claude] [dir]
codemesh prognoses [dir]
```

Open http://localhost:7777. `codemesh prognoses` prints the Diagnose
findings, one per line, most urgent first. `dir` defaults to the current directory and must
hold a `go.mod`, or a Julia package's `Project.toml`.

The review is the working tree, staged, unstaged and untracked, against its
merge-base with `-base`. Without `-base` it is `origin/HEAD`, else `main`,
else `master`.

To review someone else's branch, check it out first, for example with
`gh pr checkout 42`, then run codemesh.

## Map

- **Atlas.** The whole module as one zoomable map: packages holding files
  holding declarations, area by lines of code. Scroll or drag to zoom and
  pan, double-click to reset, click a tile to open it.
- **Lenses** colour the tiles:
  - Smells: weighted smells per 100 lines; only warn and high smells
    reach the hottest colours.
  - Complexity: each declaration's cyclomatic complexity, a switch counting
    once.
  - Churn: commits in the 90 days up to the last commit.
  - Hotspot: churn × complexity.
- **Smells.** The side panel lists the smells in the current scope, 60 at
  a time, each with its file and line. Click a declaration to see its
  source, metrics and callers.
- **Dependencies.** A dependency structure matrix shows which package imports
  which, with reference counts and instability, and flags imports of less
  stable packages.

A nested module, a directory with its own `go.mod`, is left out of the map
and the matrix, and a banner names it. Run codemesh in it to map it.

The smell rules and their thresholds are in [DESIGN.md](DESIGN.md).

## Diagnose (spike)

The Diagnose tab marks the places that need attention and explains each in
plain words: what is wrong, why it matters, what to do and how to check.
Hover a marker for the short version, click it for the full one. "Try a
fix" asks Claude Code to treat it in a throwaway worktree, replays what it
did step by step on the map, and shows the before and after, checked with
the repository's own `ci.sh` or `make ci` when it has one. See
[DESIGN.md](DESIGN.md#diagnose-spike).

## Review

The change becomes one unit per declaration added, removed or modified. Each
unit lands in a lane:

| lane | holds |
|---|---|
| Contract | exported API of importable packages, added, removed or re-signed, and `go.mod` |
| Logic | behaviour changes |
| Tests | test code and fixtures |
| Other | non-Go files and long string literals, such as an inlined script |
| Noise | comments, layout, moves, generated files, `go.sum` |

Go files of a nested module have no type information in the outer one, so
they show as one unit per file. Run codemesh in that module to review them
by declaration.

Inside a lane, units are ordered by risk. Chips give the reasons: production
callers, missing direct tests, complexity and its change, and smells the
change introduces.

Contract and Logic diffs render up front, up to 3000 lines in total; the
rest, and every Tests, Other and Noise card, open on demand. Lanes list 60
units at a time, riskiest first. This keeps a release-sized diff fast to
load. Callers changed in the same diff link to their unit.

An atlas of the module sits above the outline, with every touched
declaration lit. It follows the focused unit; drag or scroll to zoom,
double-click to reset, and click a lit tile to open its card.

Mark units reviewed as you go. A mark is tied to the unit's source, so when
the author pushes again only the units that changed come back. Marks live in
`.git/codemesh/` and are never committed.

| key | does |
|---|---|
| `j` / `k` | next / previous unit |
| `r` | mark the focused unit reviewed and move on |
| `n` | next unreviewed unit |
| `o` | show or hide the focused unit's diff |

## Develop

```
./ci.sh
```

It runs gofmt, go vet, staticcheck, the build and `go test -race`.
