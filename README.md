# codemesh

A local web tool for Go, Julia, TypeScript, Python and Rust projects. It
does three jobs:

- **Map**: find code smells, packages first.
- **Review**: read a change as declarations, riskiest first, so review stops
  being the bottleneck.
- **Diagnose**: mark the places that need attention and say what to do.

It watches the repos you add and re-analyses their working trees within a
second of every save, so you can keep it open while you code. It also reads
the open pull requests of a repo through the `gh` CLI.

## Install

```
go install github.com/joaomdsg/codemesh/cmd/codemesh@latest
```

It needs Go 1.27 or newer and `git` on the PATH, `julia` 1.10 or newer to
analyse a Julia package, and `gh` to list pull requests.

## Run

```
codemesh [-addr localhost:7777] [-poll 1s] [path|url ...]
codemesh prognoses [dir]
```

Open http://localhost:7777. Each `path` is a local repo, each `url` a git
URL that codemesh clones into its cache; more can be added on the workspace
page. There is no flag for a base branch: see [Review](#review).

`codemesh prognoses` prints the Diagnose findings of the tree in `dir` (the
current directory by default), one per line, most urgent first. `dir` must
hold a `go.mod`, a `Project.toml`, a `package.json`, a `tsconfig.json`, a
`pyproject.toml`, a `setup.py` or a `Cargo.toml`. It registers nothing in
the workspace.

## Workspace

The workspace is the set of repos you added. The home page, `/`, adds a repo
from a path or a git URL, and lists:

- **Inbox**: every open pull request and every local working tree with
  changes, across repos. "Needs reading" holds those with Contract or Logic
  units not yet reviewed, "Skim" the rest. A row gives the repo, title,
  author, lines added and deleted, units, the highest risk, smells the change
  introduces and reviewing progress.
- **Repos**: a card each, with its languages and their precision (see
  [Languages](#languages)), the health census (fix first, fix soon, when
  convenient), the open pull request count and a Remove button.

A URL is cloned without file contents (`--filter=blob:none`; git fetches them
as needed) but with full history, which merge bases and churn need. Remove
forgets a repo and deletes its clone; a local checkout is never touched.

A repo can hold several roots: every directory with a project file of a
supported language, found below the repo's top and skipping hidden,
underscore, `testdata`, `vendor` and `node_modules` directories. The header
of a repo's page switches between repos and between roots.

Pages live under `/r/{repo}/`, or `/r/{repo}/in/{root}/` for a root other
than the repo's top:

| path | page |
|---|---|
| `/` | workspace |
| `/r/{repo}/` | map; `map/lens=churn&in=pkg` sets the lens and scope |
| `/r/{repo}/deps` | dependency matrix |
| `/r/{repo}/health[/{lens}]` | Diagnose |
| `/r/{repo}/prognosis/{key}` | one prognosis |
| `/r/{repo}/review[/{thread}]` | working-tree review |
| `/r/{repo}/prs` | open pull requests |
| `/r/{repo}/pr/{n}[/{thread}]` | pull request review |
| `/basket`, `/basket.txt` | handoff basket, and its prompt as plain text |

codemesh keeps its files in the XDG directories, under `codemesh/`:

| directory | holds |
|---|---|
| `$XDG_CONFIG_HOME` (`~/.config`) | `workspace.json`, the repo registry |
| `$XDG_STATE_HOME` (`~/.local/state`) | `basket.json`, and per repo `repos/{id}/` with `reviewed.json` and `notes.json` |
| `$XDG_CACHE_HOME` (`~/.cache`) | `clones/` and `worktrees/`, both rebuilt on demand |

Review marks and notes stay on disk when a repo is removed.

A local repo's working tree is watched and re-analysed on change. A clone has
no working tree: it follows its remote when its pull requests are refreshed.

## Pull requests

Open pull requests come from `gh pr list`, run in the repo, so the repo needs
a GitHub remote and `gh` needs to be logged in. When it is not, the repo's
card says what to do, in one line: install `gh`, run `gh auth login`, or that
pull requests are not available for it. The list refreshes after a minute,
or on the Refresh button of the Pull requests tab.

Opening a pull request fetches `pull/{n}/head` into `refs/codemesh/pr/{n}`,
which works for forks too, and takes the merge-base of that head with the
base branch's commit as the base. The pull request is read as the diff
between the two.

## Map

- **Atlas.** A whole root as one zoomable map: packages holding files
  holding declarations, area by lines of code. Scroll or drag to zoom and
  pan, double-click to reset, click a tile to open it.
- **Lenses** colour the tiles:
  - Smells: weighted smells per 100 lines, capped by the worst smell: info
    smells reach at most the second of five colours, warn the fourth, and
    only high smells the hottest.
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

A nested Go module, a directory below the root with its own `go.mod`, is a
root of its own: the map and the matrix of the outer root leave it out, and
a banner names it. Pick it in the header to map it.

The smell rules and their thresholds are in [DESIGN.md](DESIGN.md).

## Diagnose

The Diagnose tab marks the places that need attention and explains each in
plain words: what is wrong, why it matters, what to do and how to check.
Hover a marker for the short version, click it for the full one.
`codemesh prognoses [dir]` prints the same list, most urgent first. See
[DESIGN.md](DESIGN.md#diagnose).

## Review

Two changes are read the same way: a local repo's working tree, staged,
unstaged and untracked, at `/r/{repo}/review`, and an open pull request at
`/r/{repo}/pr/{n}`. The working tree is read against its merge-base with the
first of `origin/HEAD`, `main` and `master` that resolves, else `HEAD`; set
`defaultBase` on the repo's entry in `workspace.json` to use another ref. A
cloned repo has no working tree, so only its pull requests are read.

The change becomes one unit per declaration added, removed or modified. Each
unit lands in a lane, the chapters of the page:

| lane | holds |
|---|---|
| Contract | exported API of importable packages, added, removed or re-signed, and `go.mod` or `Project.toml` |
| Logic | behaviour changes |
| Tests | test code and fixtures |
| Other | non-code files and long string literals, such as an inlined script |
| Noise | comments, layout, moves, generated files, `go.sum` |

A nested root's files belong to that root's review, not the outer one's.

Inside a lane, units are ordered by risk. Chips give the reasons: production
callers, missing direct tests, complexity and its change, and smells the
change introduces.

**The page.** The header gives the title, the base and head commits, and
reading progress. A spine on the left lists each lane's units with their risk
and a reviewed tick; Noise is folded. The centre reads the units in order:
why read it first, its diff and its chips. Contract and Logic diffs render up
front, up to 3000 lines in total; the rest, and every Tests, Other and Noise
card, open on demand. Lanes list 60 units at a time, riskiest first. This
keeps a release-sized diff fast to load.

**Both snapshots.** A read analyses the base and the head, each in its own
worktree under the cache, and compares the two graphs. The header sums the
graph diff up: declarations added, removed and changed, reference edges added
and removed, and the smells the change introduces or fixes. A unit's chips
add its callers new in this change, and whether its signature changed. The
atlas pane on the right lights every touched declaration; its Head and Base
buttons switch between the head, where the change landed, and the base, where
the removed and rewritten code stood. Drag or scroll to zoom, double-click to
reset, and click a lit tile to open its card. Hide the pane with Hide atlas.

**Threads.** The Callers button under a unit lists who calls it, each with
its file and line, the call line in context, and its own callers count. A
caller that is itself a unit in this change links to it. Open a caller to list
its callers in turn: the open path, root to last hop, is a thread. It shows
as pills in a bar at the top, where a click trims it back, and in the
address, `/pr/{n}/{a~b~c}`, so a link reopens it. The head atlas draws the
thread as a line across the tiles of its hops. Keep thread puts it in the
handoff.

**Notes.** Click a line number of a diff, or a hop, to write a note. Notes
are saved per repo and shown inline, with an "outdated" tag once the code
under them changes.

**Marks.** Mark units reviewed as you go. A mark is tied to the unit's
source, so when the author pushes again only the units that changed come
back. Marks are stored with the other state, not in the repo, so nothing is
committed; a repo with no marks yet starts from those an older codemesh left in
`.git/codemesh/`.

| key | does |
|---|---|
| `j` / `k` | next / previous unit |
| `r` | mark the focused unit reviewed and move on |
| `c` | open the callers of the focused unit, or of the hop in focus |
| `n` | write a note on the unit's first change, or on the hop's call line |
| `t` | keep the open thread of callers in the handoff |
| `o` | show or hide the focused unit's diff |
| `b` | open the handoff basket |
| `?` | list the keys |

## Handoff

The basket at `/basket` collects what a coding agent should act on: findings
and prognoses from the map and Diagnose pages ("Add to handoff"), notes,
kept threads, and the smells a pull request introduces ("Add all" takes every
one). Each item keeps the text it had when added, so the prompt does not
shift under you. The page groups items by repo and kind, lets you move them
up and down within a group, remove them and edit the line that opens the
prompt, and previews the prompt. Copy puts it on the clipboard; `/basket.txt` serves it as plain text. A header
badge counts the items.

## Languages

| language | project file | reader | links |
|---|---|---|---|
| Go | `go.mod` | `go/packages`, with types | typed |
| Julia | `Project.toml` | Julia's `JuliaSyntax`, through `julia` | named |
| TypeScript, JavaScript | `package.json`, `tsconfig.json` | tree-sitter | named |
| Python | `pyproject.toml`, `setup.py` | tree-sitter | named |
| Rust | `Cargo.toml` | tree-sitter | named |

Go resolves every call with types. For the others a call is matched to
declarations by name, scoped by the imports. A call through a receiver,
`obj.f()`, links only when the receiver says whose `f` it is: `self` or
`this`, a local typed or built in place, or a type or module named in place.
Callers and
references there are estimates, which the workspace cards say. The
pass-through, same-argument and middle-man smells need types and apply to Go
only.

## Develop

```
./ci.sh
```

It runs gofmt, go vet, staticcheck, the build and `go test -race`.
