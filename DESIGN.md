# codemesh design (v0.1)

codemesh is a local web tool for Go modules and, in the Diagnose spike,
Julia packages. It does two jobs:

1. **Map**: show where the smells are, packages first.
2. **Review**: make reading a change fast enough that review stops being the
   bottleneck.

A third, **Diagnose**, is a spike on the `spike/diagnose` branch; see
[Diagnose (spike)](#diagnose-spike).

Run it in a module: `codemesh [-addr localhost:7777] [-base REF] [-poll 1s]
[-agent claude] [dir]`. It analyses the working tree, so it reviews uncommitted work as well
as branches.

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
  orchestration, which is out of scope for v0.1. The Diagnose spike tries
  one agent run per prognosis; it is not part of v0.1.
- **PR-number history.** It is GitHub-specific. Churn comes from plain
  `git log`.

## Truth over approximation

All Go code facts come from `go/packages` with full type information:
callers, uses, imports. No text matching. A smell the tool reports must be a
fact you can click through to.

Julia is the exception, because it picks a method when the code runs. Its
facts come from Julia's own parser (`Base.JuliaSyntax`), run by
`internal/code/julia.jl`, so spans, names, complexity and modules are exact;
but a call is matched to declarations by name, so callers and references are
inferred, and the Diagnose legend says so. Each module that owns files is a
package, a submodule declared inside another module's file stays in that
file's package with its names qualified, and each method is a declaration
of its own, its ID carrying its argument types: `area(Square)`. A name links
to the same package's declarations, to exported ones of packages it uses,
and `M.f` to M's, through `const M = ...` aliases and
`Base.get_extension`. A method of another module's function, such as
`Base.show`, is a method: called by dispatch, never dead.

## Map

Go loads a nested module (a directory below the root with its own
`go.mod`) as a separate module, so the map, smells and matrix leave it out,
and a banner names it. Imports of it are not module-internal edges.

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

Pass-through and same argument everywhere are Go only. Tests' call sites
do not count against same argument everywhere: a parameter only tests
vary is a test seam in the production interface.

Test code and generated files are exempt from every rule. Functions in a
main package are exempt from leans-on-another-package, since wiring other packages
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
   - **Other**: non-Go files, a nested module's `go.mod`, and long string
     literals: a package-level const or var whose value is only string
     literals over several lines, such as an inlined script. Go files of a nested module
     have no type information here and become one unit per file, in Logic
     or Tests, or Noise when only comments and layout changed.
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
   budget, show their header and open on demand. A lane lists 60 units at
   a time, riskiest first, and each "Show more" adds the next 60. Listing a
   release-sized lane at once (864 units on one repo) built a 2.8 MB page
   in 1.9 s; grouping by package was rejected because it breaks the risk
   order across the lane. An outline lists the listed units by lane with
   their marks.
7. **Where the change sits.** Above the outline, an atlas of the whole
   module lights every declaration the change touches: blue while open,
   green once reviewed. It flies to the focused unit's file, and clicking a
   lit tile opens its card.
8. **Keyboard first.** `j`/`k` move, `r` marks reviewed and moves to the
   next unreviewed unit, `n` skips to it, `o` shows or hides a diff.

## Diagnose (spike)

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
5. **Let Claude try** runs `claude -p` with `bypassPermissions` in a
   throwaway worktree of the last commit (`internal/fix`); `-agent` names
   another command called the same way. It may run any command there, so
   the only guards are that `git push` is denied, the worktree is removed on
   Discard or shutdown (or, after a crash, when codemesh next starts on the
   repository), and nothing is written back unless the reader opens
   a pull request (9). The starting point is analysed in a second worktree,
   kept for the run: the review reads each side's source when it is built,
   and Claude's copy has changed by then. Uncommitted edits are
   not in the worktree. Claude and each check run in a process group of
   their own, killed when they end, so a server one starts with `&` stops
   with it. On Linux, Discard and shutdown also kill what escaped its
   group: every process whose environment holds the run's `CODEMESH_RUN`
   token. A second Ctrl-C while the runs close waits for them; a third
   quits at once and leaves what they run behind.
   The prompt asks for the whole fix: restructuring, moving code to other
   or new files and changing unexported code are in scope; exported names,
   signatures and behaviour stay unless the problem is about them. It lists
   the other problems in the same file, and has Claude loop until
   `codemesh prognoses <worktree>` no longer lists the problem and the
   check passes: judged by its own reading, an agent stops at the first
   improvement.
   When Claude finishes, codemesh analyses and checks the worktree and
   resumes the same Claude session (`--resume`, with the flags passed
   again) with what the change left: smells it introduced, the problem if
   still there, a check it broke. It goes again until nothing is left, a
   round leaves as many as the one before, or five rounds ran. Each round
   is committed in the worktree; a round that leaves more than the one
   before is undone back to it, even when it failed or was stopped, so the
   result and a pull request never carry a worse round. A pull request
   folds these commits into one. A later round that fails, or a session
   with no id to resume, ends the loop with the change as it stands. The
   head shows the round; the result says what each round left and why the
   loop stopped, including an error's first line or a stop. Stop ends
   Claude's work and keeps what it changed, unless that left more than the
   round before; pressed during the checks, the starting point's or a
   round's, they finish, the head says it is stopping, and Claude does not
   start or go again. The replay marks where each round began; an undone round's steps stay, faded and labelled undone,
   and leave nothing on the map.
   The run's head names the model, the models of any helper agents, the
   API calls, the tokens and Claude Code's version, with a table per model
   in a fold. Its cost meter moves with each reply: an estimate, marked ≈,
   from the reply's tokens at list price (`internal/fix/usage.go`, which a
   price change must update), until Claude's own tally replaces it at the
   end of each round. A resumed session's tally covers every round from
   Claude Code 2.1.277 on; an older Claude Code's, or a smaller one from a
   round that did not exit cleanly, covers only its call and is added.
6. **The check** is the repository's own gate: `./ci.sh`, else a `ci`
   target in a Makefile or justfile, else `go build ./... && go test ./...`
   in the module, or `Pkg.test()` in a Julia package, with one precompile
   task at a time: parallel precompilation of package extensions can hang
   it. `go test ./...` alone stops at nested modules and skips whatever else
   a gate runs. Claude is told to leave it passing, and
   codemesh runs it before and after.
7. **The replay** shows what Claude did over time. Each thought, read,
   search, edit and command is a step, timed as the stream arrives. Claude
   reads and edits through the shell as often as through its own tools, so
   after each step codemesh compares the worktree with git: a step that
   changed files is an edit, shown as its diffs (one per separate change),
   not its command. A shell command reads the module files it names as
   arguments; a path built at run time is missed. A read knows its lines
   when it says them: the Read tool's offset and limit, `sed -n`, `head`,
   `tail`, and the matches `grep -n` or `rg -n` printed; anything else reads
   the whole file. Line numbers are taken back through the run's earlier
   edits to the starting tree. The map colours what was touched up to the
   playhead: the declarations read in blue, a file read whole or searched
   in pale blue; in an edited file each declaration's green deepens with
   the lines changed in it, and the rest of the file keeps a faint tint;
   older touches fade, and the file edited by the current step is
   outlined, with an edit count. A legend names each colour. The map is the
   tree the run started from, then the tree it left once that is analysed,
   so files Claude created get tiles at the end; until then their edits are
   listed beside the map. Swimlanes, one per kind of step, have a brush to zoom into a
   stretch. A list shows each step's text, diff or output. Scrubbing either
   one, scrolling the list or pressing ← and → moves all three. While a run
   is live the playhead follows the newest step until the reader moves it.
8. **The result** shows whether the check passed before and after, the map
   before and after with the changed code outlined, the prognoses fixed and
   new, and the code changes ordered by review risk. The two maps share one
   coordinate space, so panning or zooming either moves both; where the
   change added or removed declarations, the tiles there shift. An exported
   declaration the change re-signed (after) or removed (before) is outlined
   in red: its callers may break.
9. **Open a draft PR** is offered once Claude finished, the check passes
   on the result and the repository has an origin remote. codemesh folds the change since the starting commit into
   one commit titled after the prognosis, pushes it to origin as
   `codemesh/<rule>-<place>-<time>` and runs `gh pr create --draft` against the
   branch the repository was on when the run started. That branch must hold
   the starting commit so the PR holds only the change: when origin lacks
   the branch or is behind, codemesh pushes the starting commit to it
   first, never forced, and stops if origin's branch moved on elsewhere. It
   refuses a run started on a detached HEAD. Git never prompts; the commit
   carries the repository's own identity, or codemesh's when it has none. The local branch goes with the
   worktree. Its summary is Claude's last one; when a later round failed
   before summing up, the heading names the round the summary is from.

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
  them to `atlas.js`, which only draws, zooms and links. The Diagnose
  spike's island, `diagnose.js`, follows the same pattern.
- Dependencies:
  - `golang.org/x/tools/go/packages` loads Go code; Julia's own parser,
    through the `julia` on the PATH, loads Julia code. A pure-Go
    tree-sitter grammar was tried first and mis-parsed 4 of HyperSignal.jl's
    10 files.
  - `github.com/bluekeyes/go-gitdiff` parses diffs. It handles renames,
    binary files and mode changes correctly, where the alternatives do not.
  - Complexity is hand-rolled, about 40 lines, to avoid an
    untagged dependency.

## Layout

```
cmd/codemesh        flags, wiring, serve
internal/code       load a module: packages, files, decls, metrics, refs
internal/smell      detectors over a code.Snapshot
internal/gitx       git: merge-base, diff, show, churn, worktree
internal/review     diff + base/head snapshots → ranked change units
internal/prognosis  snapshot + smells → plain-language prognoses (spike)
internal/fix        Claude Code in a throwaway worktree, before/after (spike)
internal/live       re-analyse on change, keep the base worktree, publish
internal/treemap    squarified layout, pure
internal/ui         via pages
internal/testrepo   throwaway git repos and the calc fixture, for tests
```
