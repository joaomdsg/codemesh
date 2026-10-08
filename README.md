# codemesh

A local web tool for Go modules. It does two jobs:

- **Map**: find code smells, packages first.
- **Review**: read a change as declarations, riskiest first, so review stops
  being the bottleneck.

It analyses the working tree and re-analyses on every save, so you can keep
it open while you code.

## Install

```
go install github.com/joaomdsg/codemesh/cmd/codemesh@latest
```

It needs Go 1.27 or newer and `git` on the PATH.

## Run

```
codemesh [-addr localhost:7777] [-base REF] [-poll 1s] [dir]
```

Open http://localhost:7777. `dir` defaults to the current directory and must
hold a `go.mod`.

The review is the working tree, staged, unstaged and untracked, against its
merge-base with `-base`. Without `-base` it is `origin/HEAD`, else `main`,
else `master`.

To review someone else's branch, check it out first, for example with
`gh pr checkout 42`, then run codemesh.

## Map

- **Treemap.** The module shows each package with its files inside. Click
  into a package to see its files with their declarations, then into a file.
  Area is lines of code.
- **Lenses** colour the tiles:
  - Smells: weighted smells per 100 lines.
  - Complexity: the highest cyclomatic complexity inside.
  - Churn: commits in the last 90 days.
  - Hotspot: churn × complexity.
- **Smells.** The side panel lists the smells in the current scope.
  Click a declaration to see its source, metrics and callers.
- **Dependencies.** A dependency structure matrix shows which package imports
  which, with reference counts and instability, and flags imports of less
  stable packages.

The smell rules and their thresholds are in [DESIGN.md](DESIGN.md).

## Review

The change becomes one unit per declaration added, removed or modified. Each
unit lands in a lane:

| lane | holds |
|---|---|
| Contract | exported API added, removed or re-signed |
| Logic | behaviour changes |
| Tests | test code and fixtures |
| Other | non-Go files |
| Noise | comments, layout, moves, generated files, `go.sum` |

Inside a lane, units are ordered by risk. Chips give the reasons: production
callers, missing direct tests, complexity and its change, and smells the
change introduces.

Contract and Logic diffs render up front. Tests, Other and Noise cards open
on demand, which keeps a release-sized diff fast to load. Callers changed in
the same diff link to their unit.

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
