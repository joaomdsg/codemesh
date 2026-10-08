# codemesh design (v0.1)

codemesh is a local web tool for Go modules. It does two jobs:

1. **Map**: show where the smells are, packages first.
2. **Review**: make reading a change fast enough that review stops being the
   bottleneck.

Run it in a module: `codemesh [-addr localhost:7777] [-base REF] [dir]`. It analyses
the working tree, so it reviews uncommitted work as well as branches.

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
  `git log`.

## Truth over approximation

All code facts come from `go/packages` with full type information: callers,
uses, imports. No text matching. A smell the tool reports must be a fact you
can click through to.

## Map

- **Atlas.** One squarified treemap of the whole module: packages holding
  files holding declarations, area by lines of code. The server lays it out;
  D3 zooms and pans it, with labels that appear once their tile has room.
  Clicking a tile opens it, and the atlas flies to the selected package, file
  or declaration.
- **Lenses** colour the tiles:
  - Smells: weighted smells per 100 lines, in fixed bands.
  - Complexity: cyclomatic complexity per declaration, in fixed bands.
  - Churn: commits in the last 90 days, relative to the hottest declaration.
  - Hotspot: churn × complexity, relative. Code that is both hard and often
    touched is where bugs live.
- **Smells list** for the current scope, ranked by severity, 60 at a time.
  Each smell gives its measure, its threshold, its file and line, and one
  line on why it matters. A declaration opens with its source, metrics and
  callers. The scope `?in=` takes an import path, a package directory or a
  file path.
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
| long function | > 60 code lines | hard to hold in your head |
| complex function | cyclomatic > 10 (high > 20) | paths you must test and read |
| deep nesting | nesting depth > 4 | control flow hides the main path |
| many parameters | > 5 parameters | the function does several jobs |
| large file | > 600 code lines | the file has several concerns |
| unused export | exported from a package other modules cannot import (`internal/`), no use outside its package | API surface that nobody uses |
| dead code | no use anywhere; unexported names, or any name in a main package; not `main`, `init` or methods | weight with no value |
| envious function | most of its references go to one other package | it may live in the wrong package |
| unstable dependency | depends on a package more unstable than itself | breaks the Stable Dependencies Principle |
| untested package | no test files | changes land unguarded |

Code lines are lines holding a Go token. String data (see Other below)
counts as one line, so an inlined script does not make a large file.

Test code and generated files are exempt from every rule. Functions in a
main package are exempt from envious-function, since wiring other packages
together is a main package's job.

## Review

A diff is a list of lines. A reviewer cares about declarations and their
consequences. codemesh turns `base...worktree` into a queue of **change
units**, one per declaration added, removed or modified.

1. **Triage into lanes.** Lanes are ordered by how much reading they need:
   - **Contract**: exported declarations of importable packages (not under
     `internal/`, not `main`) added, removed or re-signed, and the module's
     `go.mod`. A struct's signature is its exported fields, so a new private
     field is not a contract change.
   - **Logic**: other body and signature changes.
   - **Tests**: test code and `testdata` fixtures.
   - **Other**: non-Go files, a nested module's `go.mod`, and string data:
     a package-level const or var whose value is only string literals over
     several lines, such as an inlined script. Go files of a nested module have no type
     information here and become one unit per file, in Logic or Tests, or
     Noise when only comments and layout changed.
   - **Noise**: units whose syntax tree is unchanged apart from comments and
     formatting, pure moves (same source, other file or package), generated
     files, `go.sum`, and import lines. A declaration moved and edited is
     one modified unit, "moved from" its old file, not a removal plus an
     addition.
2. **Risk order inside a lane.** The score combines production callers
   (blast radius), whether the unit is exported, complexity after the change
   and its delta, lines changed, and whether any test calls the unit
   directly. Test callers are listed but add no risk. The reasons show as
   chips, so the order explains itself.
3. **Impact beside the hunk.** The real callers of a changed function are
   listed next to its diff, from type information. Callers changed in the
   same diff come first and link to their unit, so a contract change and its
   call sites are read together.
4. **Smell delta.** The base is analysed in a temporary worktree. Each smell
   the change introduces shows as a chip on its unit; the header counts new
   and fixed smells.
5. **Review state that survives new pushes.** Marking a unit reviewed stores
   its identity plus a hash of its source in `.git/codemesh/`. When the
   author pushes again, only the units whose source changed come back.
   Re-review costs only the delta.
6. **Read only what needs reading.** Contract and Logic diffs render up to a
   budget of 3000 lines; Tests, Other and Noise cards, and anything past the
   budget, show their header and open on demand. A lane lists its 60
   riskiest units until asked for the rest. An outline lists the listed
   units by lane with their marks.
7. **Where the change sits.** Above the outline, an atlas of the whole
   module lights every declaration the change touches: blue while open,
   green once reviewed. It flies to the focused unit's file, and clicking a
   lit tile opens its card.
8. **Keyboard first.** `j`/`k` move, `r` marks reviewed and moves to the
   next unreviewed unit, `n` skips to it, `o` shows or hides a diff.

## Live

codemesh watches the working tree. On change it re-analyses and pushes the
update to open pages over via's SSE stream. Keep it open while you code:
the map and the review queue follow your edits.

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
  them to `atlas.js`, which only draws, zooms and links.
- Dependencies:
  - `golang.org/x/tools/go/packages` loads the code.
  - `github.com/bluekeyes/go-gitdiff` parses diffs. It handles renames,
    binary files and mode changes correctly, where the alternatives do not.
  - Cyclomatic complexity is hand-rolled, about 30 lines, to avoid an
    untagged dependency.

## Layout

```
cmd/codemesh        flags, wiring, serve
internal/code       load a module: packages, files, decls, metrics, refs
internal/smell      detectors over a code.Snapshot
internal/gitx       git: merge-base, diff, show, churn, worktree
internal/review     diff + base/head snapshots → ranked change units
internal/live       re-analyse on change, keep the base worktree, publish
internal/treemap    squarified layout, pure
internal/ui         via pages
internal/testrepo   throwaway git repos and the calc fixture, for tests
```
