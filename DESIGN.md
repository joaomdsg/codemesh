# codemesh design (v0.1)

codemesh is a local web tool for Go, Julia, TypeScript, Python and Rust
projects. It does three jobs:

1. **Map**: show where the smells are, packages first.
2. **Review**: make reading a change fast enough that review stops being the
   bottleneck.
3. **Diagnose**: mark the places that need attention and say what to do
   about each.

Run it with `codemesh [-addr localhost:7777] [-poll 1s] [path|url ...]`.
It serves a workspace of repos, each given as a local path or a git URL. It
analyses their working trees, so it reviews uncommitted work, and their open
pull requests, through `gh`. Its files live in the XDG directories, not in
your repos; in a local repo's `.git` it adds only `refs/codemesh/pr/*` and
the registrations of its worktrees. See Workspace below.

## What we kept from the Intent Canvas, and what we dropped

The canvas (`docs/intent-canvas-v10-packages-first.html`) is a prototype for
steering coding agents. We kept its two portable ideas:

- **Packages first.** Declared package boundaries are the map. Nothing is
  re-clustered.
- **Review the change as structure, not as files.** Group by what was touched
  and who calls it.

We dropped:

- **Leiden "districts".** They are non-deterministic and shift per commit,
  and they need call edges the prototype only text-matched. Packages and
  files are stable, and every Go developer already reads them.
- **The hex cartography and code-inside-hexes zoom.** We use a squarified
  treemap of packages, files and declarations, zoomed with D3, with source in
  a side panel. It is cheap, deterministic and stable while the code is
  edited.
- **Marks, fences, the dispatch gate and agent runs.** These are agent
  orchestration, which is out of scope for v0.1.
- **PR-number history.** It is GitHub-specific. Churn comes from plain
  `git log`. Pull requests are read for what they change, through `gh`.

## Truth over approximation

All Go code facts come from `go/packages` with full type information:
callers, uses, imports. No text matching. A smell the tool reports must be a
fact you can click through to. Go links are **typed**.

The other languages are **named**: a call is matched to declarations by name,
so callers and references are inferred, and the workspace cards say so.
Julia is the first case, because it picks a method when the code runs. Its
facts come from Julia's own parser (`Base.JuliaSyntax`), run by
`internal/lang/julia/julia.jl`, so spans, names, complexity and modules are
exact; the Diagnose legend says its callers are estimates. Each module that
owns files is a package, a submodule declared inside another module's file
stays in that file's package with its names qualified, and each method is a
declaration of its own, its ID carrying its argument types: `area(Square)`.
A name links
to the same package's declarations, to exported ones of packages it uses,
and `M.f` to M's, through `const M = ...` aliases and
`Base.get_extension`. A method of another module's function, such as
`Base.show`, is a method: called by dispatch, never dead.

TypeScript and JavaScript, Python and Rust are parsed with tree-sitter
(`internal/lang/treesitter`), which parses and runs no type checker. A name
reaches a declaration of its own package, or an exported one of a package it
imports. A name reached through a receiver links only where the receiver
names its owner: `self`/`this` the enclosing type, a local its annotated or
constructed type, `T.f` or `mod::f` the type or module. Any other receiver
links nothing, and neither does a Rust field read or an object key: a
same-named method elsewhere is a guess, and guessing invents callers. A file that is a quarter syntax errors or more is
reported on its package.

## Map

A repo holds one or more **roots**: directories with a project file of a
supported language (`go.mod`, `Project.toml`, `package.json`, `tsconfig.json`,
`pyproject.toml`, `setup.py`, `Cargo.toml`), found below the repo's top and
skipping hidden, underscore, `testdata`, `vendor` and `node_modules`
directories. Each root is one snapshot, and the pages below show one root at
a time. Go loads a nested module (a directory below a root with its own
`go.mod`) as a separate module, so the map, smells and matrix of the outer
root leave it out, and a banner points to it as a root of its own. Imports of
it are not module-internal edges.

- **Atlas.** One squarified treemap of the whole module: packages holding
  files holding declarations, area by lines of code. The server lays it out;
  D3 zooms and pans it, with labels that appear once their tile has room.
  Clicking a tile opens it, and the atlas flies to the selected package, file
  or declaration.
- **Lenses** colour the tiles:
  - Smells: weighted smells per 100 lines, in fixed bands, capped by the
    worst smell: info smells alone reach band 2, warn band 4. Without the
    cap, one info smell on a three-line const is the hottest tile.
  - Complexity: modified cyclomatic complexity per declaration, in fixed
    bands.
  - Churn: commits to the file in the 90 days up to the last commit, so a
    quiet repo still shows where its work went; relative to the hottest
    file.
  - Hotspot: churn × complexity, relative. Code that is both hard and often
    touched is where bugs live.
- **Smells list** for the current scope, ranked by severity, 60 at a time.
  Each smell gives its measure, its threshold, its file and line, and one
  line on why it matters. A declaration opens with its source, metrics and
  callers. The scope, `in=` in the map's path segment
  (`/r/{repo}/map/lens=churn&in=pkg`), takes an import path, a package
  directory or a file path.
- **Dependencies** as a dependency structure matrix (DSM), with packages
  ordered by layer. A node-link graph turns into a hairball past 20 packages.
  Go forbids import cycles, so every mark sits right of the diagonal; the
  matrix earns its place with reference counts per edge, instability per
  package, and the imports that break the Stable Dependencies Principle
  marked in red.

### Smells v0.1

Each smell is a rule with a documented threshold:

| smell | rule | why |
|---|---|---|
| long function | > 60 code lines (high > 120) | hard to hold in your head |
| complex function | modified cyclomatic > 10 (high > 20) | decisions you must read and test |
| deep nesting | nesting depth > 4 | control flow hides the main path |
| many parameters | > 5 parameters | the function does several jobs |
| large file | > 600 code lines (high > 1200) | the file has several concerns |
| unused export | exported from a package other modules cannot import (`internal/`), no use outside its package; a type counts as used through its constants, fields or methods, and a constant through its type | API surface that nobody uses |
| dead code | no use anywhere; unexported names, or any name in a main package; not `main`, `init` or methods | weight with no value |
| leans on another package | most of its references go to one other package | it may live in the wrong package |
| imports a less stable package | depends on a package more unstable than itself | breaks the Stable Dependencies Principle: its changes ripple back |
| untested package | no test files, and no test in another package refers to its declarations; not a main package | changes land unguarded |
| pass-through | the body is one call whose arguments are the function's own parameters, in order; not `main`, `init` or a `Deprecated:` wrapper | a layer that adds an interface and no behaviour |
| same argument everywhere | a func (not a method, which interfaces may call unseen) with ≥ 3 production call sites, none of them as a value, all passing one constant for a parameter; not an export of an importable package | the parameter should be a default |
| middle man | a type with ≥ 3 methods outside tests and generated files, at least half of which only call the same-named method of one field, passing their own parameters and returning its results; not a `Deprecated:` type, nor one that implements its field's interface type (a decorator, proxy or bridge stands in for what it wraps) | callers could use the field directly; a candidate, since an adapter to another interface also delegates on purpose |

A finding past the high limit is high severity and names that limit
("121 lines, high limit 120").

Complexity is modified cyclomatic complexity: one plus each `if`, `for`,
`&&` and `||`, with a `switch` or `select` counting once however many cases
it has (lizard's `-m`). Plain McCabe counts every case, so a 14-case string
dispatch scored 29. A switch is one
decision a reader takes in at a glance; an `else if` ladder still counts
each branch, since each condition must be read. The cost: a long switch
no longer shows how many paths a test must cover.

Code lines are lines holding a Go token. A long string literal (see Other
below) counts as one line, so an inlined script does not make a large file.

Pass-through, same argument everywhere and middle man are Go only: the other
frontends have no types to tell a forward from a coincidence. Tests'
call sites do not count against same argument everywhere: a parameter only
tests vary is a test seam in the production interface.

Test code and generated files are exempt from every rule. Functions in a
main package are exempt from leans-on-another-package, since wiring other packages
together is a main package's job.

## Review

A diff is a list of lines. A reviewer cares about declarations and their
consequences. codemesh turns a change into a queue of **change units**, one
per declaration added, removed or modified. A change is either the working
tree of a local repo, read against its merge-base with the first of
`origin/HEAD`, `main` and `master` that resolves (else `HEAD`; a repo's
`defaultBase` in `workspace.json` overrides it), or an open pull request,
read as its head against the merge-base with its base branch. A clone has no
working tree, so only its pull requests are read.

1. **Triage into lanes.** Lanes are ordered by how much reading they need:
   - **Contract**: exported declarations of importable packages (not under
     `internal/`, not `main`) added, removed or re-signed, and the root's
     `go.mod` or `Project.toml`. A struct's signature is its exported
     fields, so a new private field is not a contract change.
   - **Logic**: other body and signature changes.
   - **Tests**: test code and `testdata` fixtures.
   - **Other**: non-code files, a nested module's `go.mod`, and long string
     literals: a package-level const or var whose value is only string
     literals over several lines, such as an inlined script. Each root has its
     own review: a file below a nested root is left out of the outer root's.
   - **Noise**: units whose syntax tree is unchanged apart from comments and
     formatting, pure moves (same source, other file or package), generated
     files, `go.sum`, and import lines. A declaration moved and edited is
     one modified unit, "moved from" its old file, not a removal plus an
     addition.
2. **Risk order inside a lane.** The score combines production callers
   (blast radius), whether the unit is exported (test functions aside),
   complexity after the change and its delta, lines changed, and whether
   any test calls the unit directly. Test callers are listed but add no
   risk. The reasons show as chips, so the order explains itself.
3. **Impact beside the hunk.** The callers of a changed function are
   listed next to its diff, typed for Go and by name otherwise. Callers
   changed in the same diff come first and link to their unit, so a contract
   change and its call sites are read together.
4. **Graph diff.** Both snapshots are analysed, each in a worktree under the
   cache, and `internal/graphdiff` compares them: declarations added,
   removed, moved (same name, kind and shape under another ID) and changed;
   reference edges and module-internal imports added and removed; smells
   introduced and fixed. The header sums it up. A unit's chips carry the
   smells it introduces, its callers new in this change and a changed
   signature.
5. **Review state that survives new pushes.** Marking a unit reviewed stores
   its identity plus a hash of its source in the repo's state directory, not
   in the repo. When the author pushes again, only the units whose source
   changed come back. Re-review costs only the delta. A repo with no state
   yet starts from the marks an older version left in `.git/codemesh/`.
6. **Read only what needs reading.** Contract and Logic diffs render up to a
   budget of 3000 lines; Tests, Other and Noise cards, and anything past the
   budget, show their header and open on demand. A lane lists 60 units at
   a time, riskiest first, and each "Show more" adds the next 60. Listing a
   release-sized lane at once (864 units on one repo) built a 2.8 MB page
   in 1.9 s; grouping by package was rejected because it breaks the risk
   order across the lane. An outline lists the listed units by lane with
   their marks.
7. **Where the change sits.** An atlas pane beside the reading lights every
   declaration the change touches: blue while open, green once reviewed. It
   flies to the focused unit's file, and clicking a lit tile opens its card.
   Its Head and Base buttons show the head, or the base where removed and
   rewritten code stood.
8. **Follow the blast radius.** Under a unit, "Callers" lists its callers
   and users: file and line, the call line in context, the caller's own
   caller count, and a link when the caller is a unit of this change. Opening
   a hop lists its callers in turn. The open path is a **thread**
   (`internal/thread`): root, then hops. It is the last path segment of the
   page's address, `/pr/{n}/{a~b~c}`, so a link reopens it, with `~` and `%`
   in a decl ID escaped; a bar above the reading shows it as pills to trim.
   Candidates are ordered production before tests, then by their own caller
   count. The head atlas draws the thread as a line across its tiles.
9. **Notes.** A click on a diff line, or on a hop, opens a note box.
   `internal/notes` stores the note per repo with its change (the working
   tree, or the pull request at any head), the unit's key when written and
   the line's text. When the unit's source changes the note shows as
   outdated instead of vanishing.
10. **Keyboard first.** `j`/`k` move, `r` marks reviewed and moves to the
    next unreviewed unit, `c` opens callers, `n` writes a note, `t` keeps the
    thread, `o` shows or hides a diff, `b` opens the basket, `?` lists the
    keys.

## Handoff

The basket (`internal/handoff`, `/basket`) is one list across repos of what
a coding agent should act on: findings and prognoses, notes, kept threads, and
the smells a pull request introduced. An item copies its text when added, so
the prompt does not change under the user, and keeps its source's key so a
stale one can be told. `Render` writes the prompt, grouped by repo then kind
in the order the user gave, under an editable opening line; the page previews
it, Copy puts it on the clipboard and `/basket.txt` serves it as text. It is
a prompt to paste, not a run: codemesh starts no agent.

## Diagnose

The diagnosis is for people who do not yet know what healthy code looks
like, as well as for those who do. It shows the map with a marker on each
place that needs attention, and says in plain words what is wrong, why it
matters, what to do and how to check it.

1. **Prognoses** come from rule templates over the snapshot, the smells and
   churn (`internal/prognosis`), so every number in a text is a fact the
   reader can click through to. There are four lenses:
   - Health: complex functions, more urgent when their file keeps changing.
   - Reach: declarations 50 or more places use.
   - Structure: a package holding over half the code, imports of less
     stable packages, and a package other packages import that exports
     over 60% of its declarations (from 8 up; a package nothing imports is a
     command or the module's public face, where exports serve outsiders).
   - Tests & smells: complex code no test calls, untested packages, long
     parameter lists (grouped when three or more share a package) and large
     files.
2. **Urgency** has three levels: fix first, fix soon, when convenient. Each
   level differs in size and fill as well as colour.
3. **The map** colours tiles by the lens, with a ramp under it naming what
   its coldest and hottest colours mean. Each package and file carries a
   tab with the hottest heat inside it, so a small hot function shows from
   the module view. Hovering a marker shows the one-sentence prognosis.
   Clicking a tile rings its callers up to three hops. Every map marks an
   exported declaration with a strip along its top edge and a bold label:
   it is a promise other packages may rely on.
4. **The prognosis** opens across the view. The map shrinks beside it,
   framed on the place, with the related places listed below it and why
   each matters.

## Workspace

`internal/workspace` is the registry of repos, `workspace.json` under
`$XDG_CONFIG_HOME/codemesh`. A repo has a stable ID, its name plus six hex
characters of a hash of its path or URL, which is what URLs carry:
`/r/{repo}/`, `/r/{repo}/in/{root}/` for a root other than the repo's top,
then `map/{view}`, `deps`, `health[/{lens}]`, `prognosis/{key}`,
`review[/{thread}]`, `prs` and `pr/{n}[/{thread}]`. A page's state is in its
path, not its query: via addresses a live page's stream and actions by the
mount pattern alone, which sees no query. `/` is the inbox and the repo cards,
`/basket` the handoff.

A local path must be inside a git repo; the repo's top directory is what is
registered. A git URL (`scheme://` or `user@host:owner/name`) is cloned into
`$XDG_CACHE_HOME/codemesh/clones/` with `--filter=blob:none`: blobs come on
demand, history stays complete, as merge bases and churn need it. A shallow
clone would break both. Removing a repo deletes its clone and never touches a
local checkout.

Files go by the XDG rules: the registry in the config directory; in the state
directory what the user produced, `basket.json` and per repo
`repos/{id}/reviewed.json` and `notes.json`; in the cache directory what can
be rebuilt, clones, the worktrees of analysed commits and the last snapshot
of each root. Worktrees a killed
run left behind are swept at start.

## Pull requests

`internal/ghx` runs `gh pr list --state open --json ...` in the repo and
nothing else of `gh`; it never touches git. Exit code 4 means not logged in,
a missing binary means no `gh`, and either shows on the repo's card as one
line with the fix. The list is cached for a minute, refreshed in the
background, and on the Pull requests tab by a button. A clone first fetches
its remote and moves to its branch head.

Opening pull request `n`:

1. `git fetch origin +pull/n/head:refs/codemesh/pr/n` gets the head. That
   ref lives on the base repository, so forks work.
2. If the base branch moved past what the repo has, fetch it.
3. The base is `git merge-base` of the head and `baseRefOid`.
4. `internal/analysis` loads both commits as snapshots, each from a
   worktree checked out under the cache and shared by roots of one commit,
   and compares them: the graph diff, the files, the findings of each side.
5. `internal/review` ranks the units from that.

The inbox lists every open pull request and every local tree with changes, in
"Needs reading" when a Contract or Logic unit is unreviewed and "Skim"
otherwise. Its rows come from the same review, built in the background; pull
request summaries one at a time, as each loads two snapshots.

## Live

codemesh watches the working tree of each local repo (`internal/live`): it
polls at `-poll`, hashes the sizes and modification times of source files and
project files, and on a change queues a reload and announces it on a topic. The
app turns that into an event, and open pages re-render over via's SSE stream.
Keep it open while you code: the map and the review follow your edits. A
clone has no working tree to watch.

Snapshots are cached by repo, root and commit (`internal/analysis`), with a
bounded number of loads running at once, as a load holds a process tree. A
snapshot's worktree lives as long as its cache entry.

Pages never wait for a load. A page with no snapshot yet shows a skeleton,
and one whose files changed shows the old snapshot marked "Analysing…"; the
load that lands re-renders it. A reload that fails leaves the old snapshot
under a "Load failed" banner until the files change again; a commit whose
load failed is tried again on the review page's Refresh. Opening a repo loads
its roots and the base of its working-tree review in the background, and a
root a page asks for goes ahead of that warm-up. Snapshots are kept on disk
per root and codemesh build: the working tree's, with a fingerprint of its
source files and HEAD, and the four commits read last. After a restart a
page shows them at once and only what changed loads again; the page stamp
says when the shown snapshot was analysed. The workspace cards count health
only from snapshots in memory or, for a working tree, on disk; the inbox
analyses each local repo's working tree, and a clone is analysed once it is
opened. Julia warm-up loads share one `julia` process, which exits after
five idle minutes; a warm-up load that finds it busy runs its own.

## Stack

- Go 1.27 and go-via. Server-rendered HTML with the `h` DSL.
- Hand-written CSS and a small keyboard script, inlined through the router's
  `Head.Assets`, so via's CSP admits them by hash.
- No Node, no build step, no client framework. The atlas is one island
  using D3 (v7.9.0, vendored, ISC), on the map and in the review: the server
  lays it out and renders it, with the lens heats or the reviewed cards and
  the selection, into client-only signals (`SignalCS`), so the layout is
  never posted back with an action. via v0.9.0 has no server-side setter for
  a `SignalCS`, so the values ride on a hidden element's `data-signals`
  attributes, which Datastar applies on every morph. A Datastar effect hands
  them to `atlas.js`, which only draws, zooms and links. The Diagnose
  island, `diagnose.js`, follows the same pattern.
- Dependencies:
  - `golang.org/x/tools/go/packages` loads Go code. Julia's own parser,
    through the `julia` on the PATH, loads Julia code: a pure-Go tree-sitter
    grammar was tried first and mis-parsed 4 of HyperSignal.jl's 10 files, so
    Julia keeps JuliaSyntax.
  - `github.com/odvcencio/gotreesitter` parses TypeScript, JavaScript,
    Python and Rust. It is pure Go, so `CGO_ENABLED=0` and a plain
    `go build` still work, with no C toolchain and no build tag; the cost is a
    bigger binary. These languages have no parser we could call without
    their toolchain on the PATH, and Node would break the no-Node rule. Only
    `internal/lang/treesitter/parse.go` imports it, so the runtime can be
    swapped.
  - The `git` and `gh` binaries on the PATH do the rest: no git library.
  - `github.com/bluekeyes/go-gitdiff` parses diffs. It handles renames,
    binary files and mode changes correctly, where the alternatives do not.
  - Complexity is hand-rolled, about 40 lines, to avoid an
    untagged dependency.

## Layout

```
cmd/codemesh        flags, wiring, serve, the prognoses command
internal/code       the model: Snapshot, packages, files, decls, refs; Builder
internal/lang       frontends: golang, julia, treesitter (TS/JS, Python, Rust); roots
internal/smell      detectors over a code.Snapshot, and the delta of two
internal/gitx       git: merge-base, diff, churn, clone, fetch, worktrees
internal/ghx        open pull requests through gh
internal/store      XDG directories, atomic JSON files
internal/workspace  the repo registry, clones
internal/analysis   cached snapshots per repo, root and commit; compare
internal/graphdiff  two snapshots → declarations, edges, imports, smells
internal/review     diff + base/head snapshots → ranked change units
internal/thread     callers of a declaration, followed hop by hop
internal/notes      review notes on units
internal/handoff    the basket and its prompt
internal/prognosis  snapshot + smells → plain-language prognoses
internal/live       watch local working trees, re-analyse, announce
internal/app        what the UI calls: workspace, engine, watchers, gh, stores
internal/treemap    squarified layout, pure
internal/ui         via pages
internal/testrepo   throwaway git repos and the calc fixture, for tests
```
