# codemesh design (v0.1)

codemesh is a local web tool for Go modules. It does two jobs:

1. **Map**: show where the smells are, packages first.
2. **Review**: make reading a change fast enough that review stops being the
   bottleneck.

Run it in a module: `codemesh [-addr :7777] [-base REF] [dir]`. It analyses
the working tree, so it reviews uncommitted work as well as branches.

## What we kept from the Intent Canvas, and what we dropped

The canvas (`docs/Intent Canvas v10 · packages first.html`) is a prototype for
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
  treemap, one level at a time, with source in a side pane. It is cheap,
  deterministic and stable while the code is edited.
- **Marks, fences, the dispatch gate and agent runs.** These are agent
  orchestration, which is out of scope for v0.1.
- **PR-number history.** It is GitHub-specific. Churn comes from plain
  `git log`.

## Truth over approximation

All code facts come from `go/packages` with full type information: callers,
uses, imports. No text matching. A smell the tool reports must be a fact you
can click through to.

## Map

- **Treemap.** Packages → files → declarations, one level per click, with a
  breadcrumb. Area is lines of code.
- **Lenses** colour the tiles:
  - Smells: weighted findings per line.
  - Complexity: max cyclomatic complexity.
  - Churn: commits in the last 90 days.
  - Hotspot: churn × complexity. Code that is both hard and often touched is
    where bugs live.
- **Findings list** for the current scope, ranked by severity. Each finding
  gives its measure, its threshold and one line on why it matters.
- **Dependencies** as a dependency structure matrix (DSM), with packages
  ordered by layer. A node-link graph turns into a hairball past 20 packages.
  In a DSM, a mark above the diagonal is an upward dependency, so layering
  violations jump out.

### Smells v0.1

Each smell is a rule with a documented threshold:

| smell | rule | why |
|---|---|---|
| long function | > 60 code lines | hard to hold in your head |
| complex function | cyclomatic > 10 (high > 20) | paths you must test and read |
| deep nesting | nesting depth > 4 | control flow hides the main path |
| many parameters | > 5 parameters | the function does several jobs |
| large file | > 600 code lines | the file has several concerns |
| unused export | exported, no use outside its package | API surface that nobody uses |
| dead code | declared, no use anywhere (not main/init/tests) | weight with no value |
| envious function | most of its references go to one other package | it may live in the wrong package |
| unstable dependency | depends on a package more unstable than itself | breaks the Stable Dependencies Principle |
| untested package | no test files | changes land unguarded |

## Review

A diff is a list of lines. A reviewer cares about declarations and their
consequences. codemesh turns `base...worktree` into a queue of **change
units**, one per declaration added, removed or modified.

1. **Triage into lanes.** Lanes are ordered by how much reading they need:
   - **Contract**: exported signatures added, removed or changed.
   - **Logic**: body changes.
   - **Tests**: test changes.
   - **Noise**: units whose syntax tree is unchanged apart from comments and
     formatting, pure moves (same body, other file), and generated or
     non-Go files.
   Noise is collapsed, and one key accepts it all.
2. **Risk order inside a lane.** The score combines callers (blast radius),
   whether the unit is exported, complexity after the change and its delta,
   lines changed, and whether any test references the unit. The reasons
   show as chips, so the order explains itself.
3. **Impact beside the hunk.** The real callers of a changed function are
   listed next to the hunk, from type information.
4. **Smell delta.** The base is analysed in a temporary worktree. The header
   says which smells the change introduces and which it fixes.
5. **Review state that survives new pushes.** Marking a unit reviewed stores
   its identity plus a hash of its new source in `.git/codemesh/`. When the
   author pushes again, only the units whose source changed come back.
   Re-review costs only the delta.
6. **Keyboard first.** `j`/`k` move, `r` marks reviewed, `n` skips to the
   next unreviewed unit.

## Live

codemesh watches the working tree. On change it re-analyses and pushes the
update to open pages over via's SSE stream. Keep it open while you code:
the map and the review queue follow your edits.

## Stack

- Go 1.27 and go-via. Server-rendered HTML with the `h` DSL, and SVG for the
  treemap.
- Hand-written CSS inlined through `PageMeta().Assets`, so via's CSP admits
  it by hash.
- No Node, no build step, no client framework.
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
internal/treemap    squarified layout, pure
internal/ui         via pages
```
