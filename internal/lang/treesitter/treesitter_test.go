package treesitter_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/lang/treesitter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func load(t *testing.T, f treesitter.Frontend, dir string) *code.Snapshot {
	t.Helper()
	s, err := f.Load(context.Background(), dir)
	require.NoError(t, err)
	return s
}

func decl(t *testing.T, s *code.Snapshot, id string) *code.Decl {
	t.Helper()
	d := s.Decl(id)
	require.NotNil(t, d, id)
	return d
}

func TestDetect_looksForTheLanguagesProjectFile(t *testing.T) {
	t.Parallel()

	assert.True(t, treesitter.TypeScript.Detect("testdata/ts"))
	assert.True(t, treesitter.Python.Detect("testdata/py"))
	assert.True(t, treesitter.Rust.Detect("testdata/rs"))
	assert.False(t, treesitter.TypeScript.Detect("testdata/py"))
	assert.False(t, treesitter.Python.Detect("testdata/rs"))
	assert.False(t, treesitter.Rust.Detect("testdata/ts"))
}

func TestLoadTypeScript_mapsDeclsMetricsAndCallersAcrossFiles(t *testing.T) {
	t.Parallel()
	s := load(t, treesitter.TypeScript, "testdata/ts")

	assert.Equal(t, code.Lang("typescript"), s.Lang)
	assert.Equal(t, "shop", s.Module)

	total := decl(t, s, "shop/src.total")
	assert.Equal(t, code.Func, total.Kind)
	assert.True(t, total.Exported)
	assert.Equal(t, 4, total.Start, "the doc comment is part of it")
	assert.Equal(t, 13, total.End)
	assert.Equal(t, 9, total.Lines)
	assert.Equal(t, 4, total.Complexity, "one for, one if and one &&")
	assert.Equal(t, 2, total.Nesting)
	assert.Equal(t, 1, total.Params)
	assert.Equal(t, "(items: Item[]): number", total.Signature)
	assert.Equal(t, []string{"shop/src.Cart.add"}, total.Callers)

	assert.False(t, decl(t, s, "shop/src.unused").Exported)
	assert.Empty(t, decl(t, s, "shop/src.unused").Callers, "util imports shop/src but cannot see a private function")
	grade := decl(t, s, "shop/src.grade")
	assert.Equal(t, 3, grade.Complexity)
	assert.Equal(t, 1, grade.Nesting, "an else-if continues its if")
	assert.Equal(t, code.Type, decl(t, s, "shop/src.Cart").Kind)
	assert.True(t, decl(t, s, "shop/src.Cart.add").Exported)
	assert.Equal(t, code.Method, decl(t, s, "shop/src.Cart.add").Kind)
	assert.False(t, decl(t, s, "shop/src.Cart.reset").Exported, "private")
	assert.Equal(t, code.Const, decl(t, s, "shop/src.LIMIT").Kind)

	price := decl(t, s, "shop/src/util.price")
	assert.Equal(t, code.Func, price.Kind, "an arrow function bound to a const")
	assert.True(t, price.Deprecated)
	assert.Equal(t, 3, price.Start)
	assert.Equal(t, 2, price.Complexity, "the ternary")
	assert.Equal(t, []string{"shop/src.total"}, price.Callers)
	assert.Contains(t, decl(t, s, "shop/src.Item").Users, "shop/src/util.price", "a parameter type")

	assert.Equal(t, []string{"shop/src/util"}, s.Package("shop/src").Imports)
	assert.Equal(t, []string{"shop/src"}, s.Package("shop/src/util").Imports)
	var tests []string
	for _, f := range s.Package("shop/src").Files {
		if f.Test {
			tests = append(tests, f.Path)
		}
	}
	assert.Equal(t, []string{"src/cart.test.ts"}, tests)
}

func TestLoadPython_mapsDeclsMetricsAndCallersAcrossFiles(t *testing.T) {
	t.Parallel()
	s := load(t, treesitter.Python, "testdata/py")

	total := decl(t, s, "app.total")
	assert.Equal(t, code.Func, total.Kind)
	assert.True(t, total.Exported)
	assert.Equal(t, 5, total.Start)
	assert.Equal(t, 11, total.End)
	assert.Equal(t, 7, total.Lines, "the docstring counts")
	assert.Equal(t, 4, total.Complexity)
	assert.Equal(t, 2, total.Nesting)
	assert.Equal(t, 1, total.Params)
	assert.Equal(t, []string{"app.Cart.add", "app.test_total"}, total.Callers, "a caller in another file, tests included")

	assert.False(t, decl(t, s, "app._unused").Exported, "leading underscore")
	add := decl(t, s, "app.Cart.add")
	assert.Equal(t, code.Method, add.Kind)
	assert.Equal(t, 1, add.Params, "self is not a parameter")
	assert.Equal(t, code.Const, decl(t, s, "app.LIMIT").Kind)

	price := decl(t, s, "app.core.price")
	assert.True(t, price.Deprecated, "the decorator")
	assert.Equal(t, 4, price.Start, "the decorator is part of it")
	assert.Equal(t, 2, price.Complexity, "the conditional expression")
	assert.Equal(t, []string{"app.total"}, price.Callers)
	assert.Contains(t, decl(t, s, "app.Item").Users, "app.Cart.add", "a parameter annotation")

	assert.Equal(t, []string{"app.core"}, s.Package("app").Imports)
	assert.True(t, s.Package("app").HasTests())
}

func TestLoadRust_mapsDeclsMetricsAndCallersAcrossFiles(t *testing.T) {
	t.Parallel()
	s := load(t, treesitter.Rust, "testdata/rs")

	total := decl(t, s, "crate.total")
	assert.Equal(t, code.Func, total.Kind)
	assert.True(t, total.Exported)
	assert.Equal(t, 5, total.Start)
	assert.Equal(t, 14, total.End)
	assert.Equal(t, 4, total.Complexity)
	assert.Equal(t, 2, total.Nesting)
	assert.Equal(t, 1, total.Params)
	assert.Equal(t, []string{"crate.Cart.add", "crate.totals"}, total.Callers)

	assert.False(t, decl(t, s, "crate.unused").Exported)
	assert.Equal(t, code.Type, decl(t, s, "crate.Cart").Kind)
	assert.True(t, decl(t, s, "crate.Cart.add").Exported)
	assert.False(t, decl(t, s, "crate.Cart.reset").Exported)
	assert.True(t, decl(t, s, "crate.totals").Test, "inside #[cfg(test)]")
	assert.False(t, total.Test)

	cost := decl(t, s, "crate::price.cost")
	assert.True(t, cost.Deprecated)
	assert.Equal(t, 1, cost.Start, "the attribute is part of it")
	assert.Equal(t, 2, cost.Complexity, "a match counts once")
	assert.Equal(t, []string{"crate.total"}, cost.Callers)

	assert.Equal(t, []string{"crate::price"}, s.Package("crate").Imports)
	assert.Empty(t, s.Package("crate::price").Imports, "std is not in the crate")
}

func TestLoadTypeScript_namesTheLineOfASyntaxError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"bad"}`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	src := "export const a = 1;\n\nexport function ok() { return 1; }\nexport function broken( {\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "bad.ts"), []byte(src), 0o644))

	s := load(t, treesitter.TypeScript, dir)

	var errs []string
	for _, p := range s.Packages {
		errs = append(errs, p.Errors...)
	}
	assert.Equal(t, []string{"src/bad.ts:4: syntax errors"}, errs)
}

func packageErrors(s *code.Snapshot) []string {
	var errs []string
	for _, p := range s.Packages {
		errs = append(errs, p.Errors...)
	}
	return errs
}

func TestLoadTypeScript_keepsAFileWhoseParseRecoversLocally(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"app"}`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	src := `export function Head() {
  return (
    <head>
      <link href="https://x/css2?family=Inter&display=swap" rel="stylesheet" />
    </head>
  );
}

export function Page() {
  const a = 1;
  const b = 2;
  const c = 3;
  return <main>{a + b + c}</main>;
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "page.tsx"), []byte(src), 0o644))

	s := load(t, treesitter.TypeScript, dir)

	assert.Empty(t, packageErrors(s))
	decl(t, s, "app/src.Head")
	decl(t, s, "app/src.Page")
}

func TestLoadTypeScript_reportsAFileThatIsMostlyGarbage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"bad"}`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	src := ")))) }}}} @@@@ ;;;; ((((\n]]]] {{{{ ####\n))))) ((((( }}}}}\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "junk.ts"), []byte(src), 0o644))

	s := load(t, treesitter.TypeScript, dir)

	assert.Len(t, packageErrors(s), 1)
}

func TestLoadTypeScript_skipsLinksPipesAndUnreadableDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, text string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644))
	}
	write("package.json", `{"name":"app"}`)
	write("src/ok.ts", "export function ok() { return 1; }\n")
	require.NoError(t, os.Symlink("missing.ts", filepath.Join(dir, "src", "link.ts")))
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, "src", "pipe.ts"), 0o644))
	write("closed/x.ts", "export const x = 1;\n")
	require.NoError(t, os.Chmod(filepath.Join(dir, "closed"), 0))
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "closed"), 0o755) })

	s := load(t, treesitter.TypeScript, dir)

	decl(t, s, "app/src.ok")
	assert.Empty(t, packageErrors(s))
}

func TestLoadTypeScript_aFileItCannotReadIsAPackageErrorNotAFailedLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"app"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "ok.ts"), []byte("export function ok() { return 1; }\n"), 0o644))
	locked := filepath.Join(dir, "src", "locked.ts")
	require.NoError(t, os.WriteFile(locked, []byte("export const x = 1;\n"), 0o000))

	s := load(t, treesitter.TypeScript, dir)

	decl(t, s, "app/src.ok")
	assert.Equal(t, []string{"src/locked.ts: cannot be read"}, packageErrors(s))
}

func TestShape_tellsEditsApartAndIgnoresCommentsAndLayout(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name          string
		fe            treesitter.Frontend
		project, src  string
		body          string
		same, differs []string
	}{
		{"typescript", treesitter.TypeScript, "package.json", "src/a.ts",
			"export function f(a: number, b: number) {\n  return a + b; // sum\n}\n",
			[]string{"export function f(a: number, b: number) {\n\n  /* total */ return a   +   b;\n}\n"},
			[]string{"export function f(a: number, b: number) {\n  return a - b;\n}\n", "export function f(a: number, b: number) {\n  return a + 2;\n}\n", "export function f(a: number, b: number) {\n  return g(a, b);\n}\n"}},
		{"python", treesitter.Python, "pyproject.toml", "app/a.py",
			"def f(a, b):\n    return a + b  # sum\n",
			[]string{"def f(a, b):\n\n    # total\n    return a   +   b\n"},
			[]string{"def f(a, b):\n    return a - b\n", "def f(a, b):\n    return a + 2\n", "def f(a, b):\n    return g(a, b)\n"}},
		{"rust", treesitter.Rust, "Cargo.toml", "src/lib.rs",
			"pub fn f(a: i32, b: i32) -> i32 {\n    a + b // sum\n}\n",
			[]string{"pub fn f(a: i32, b: i32) -> i32 {\n\n    /* total */ a   +   b\n}\n"},
			[]string{"pub fn f(a: i32, b: i32) -> i32 {\n    a - b\n}\n", "pub fn f(a: i32, b: i32) -> i32 {\n    a + 2\n}\n", "pub fn f(a: i32, b: i32) -> i32 {\n    g(a, b)\n}\n"}},
	} {
		shape := func(src string) string {
			dir := t.TempDir()
			manifest := map[string]string{"package.json": `{"name":"app"}`, "pyproject.toml": "[project]\nname = \"app\"\n", "Cargo.toml": "[package]\nname = \"app\"\n"}[c.project]
			require.NoError(t, os.WriteFile(filepath.Join(dir, c.project), []byte(manifest), 0o644))
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, c.src)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, c.src), []byte(src), 0o644))
			s := load(t, c.fe, dir)
			for _, d := range s.Decls() {
				if d.Name == "f" {
					return d.Shape
				}
			}
			t.Fatalf("%s: no f in %s", c.name, src)
			return ""
		}
		base := shape(c.body)
		for _, s := range c.same {
			assert.Equal(t, base, shape(s), "%s: comments and layout", c.name)
		}
		for _, s := range c.differs {
			assert.NotEqual(t, base, shape(s), "%s: %q", c.name, s)
		}
	}
}

func tsProject(t *testing.T, src string) *code.Snapshot {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"app"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "a.ts"), []byte(src), 0o644))
	return load(t, treesitter.TypeScript, dir)
}

func TestLoadTypeScript_aDeclsOwnNameIsNotACallOfAnotherDeclWithThatName(t *testing.T) {
	t.Parallel()
	s := tsProject(t, "export class A { get(): number { return 1; } }\nexport class B { get(): number { return 2; } }\n")

	assert.Empty(t, decl(t, s, "app/src.A.get").Callers)
	assert.Empty(t, decl(t, s, "app/src.B.get").Callers)
}

func TestLoadTypeScript_aMemberCallThatTwoDeclsAnswerToLinksNeitherOfThem(t *testing.T) {
	t.Parallel()
	s := tsProject(t, "export class A { get(): number { return 1; } }\nexport class B { get(): number { return 2; } }\n"+
		"export class C { only(): number { return 3; } }\nexport function use(a: A, c: C) { return a.get() + c.only(); }\n")

	assert.Empty(t, decl(t, s, "app/src.A.get").Callers, "a.get() could be either get; it is not known which")
	assert.Empty(t, decl(t, s, "app/src.B.get").Callers)
	assert.Equal(t, []string{"app/src.use"}, decl(t, s, "app/src.C.only").Callers, "one candidate: linked")
}
