package code

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
)

// forwards returns the function fn's body only calls, with fn's own
// parameters as the arguments in order, or nil. A deprecated wrapper is
// left alone: keeping an old name working is its job.
func forwards(fn *ast.FuncDecl, info *types.Info) *types.Func {
	if deprecated(fn.Doc) {
		return nil
	}
	call := onlyCall(fn.Body)
	if call == nil {
		return nil
	}
	callee, _ := info.Uses[calleeIdent(call.Fun)].(*types.Func)
	if callee == nil || !passesParams(fn, call, info) {
		return nil
	}
	return callee
}

// delegates returns the receiver field whose same-named method fn only
// calls, passing its parameters and handing back the results, or "". A
// call that drops results the callee returns does more than delegate.
func delegates(fn *ast.FuncDecl, info *types.Info) string {
	if fn.Recv == nil || len(fn.Recv.List[0].Names) == 0 || forwards(fn, info) == nil {
		return ""
	}
	call := onlyCall(fn.Body)
	method, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || method.Sel.Name != fn.Name.Name {
		return ""
	}
	if _, dropped := fn.Body.List[0].(*ast.ExprStmt); dropped {
		if sig, _ := info.TypeOf(call.Fun).(*types.Signature); sig == nil || sig.Results().Len() > 0 {
			return ""
		}
	}
	field, ok := ast.Unparen(method.X).(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	recv, ok := ast.Unparen(field.X).(*ast.Ident)
	if !ok || info.Uses[recv] == nil || info.Uses[recv] != info.Defs[fn.Recv.List[0].Names[0]] {
		return ""
	}
	sel := info.Selections[field]
	if sel == nil || sel.Kind() != types.FieldVal || len(sel.Index()) != 1 {
		return ""
	}
	if decorates(info.Defs[fn.Recv.List[0].Names[0]], sel.Type()) {
		return ""
	}
	return field.Sel.Name
}

// decorates reports whether recv's type implements its field's interface
// type: a decorator, proxy or bridge stands in for what it wraps, so its
// forwarding is the point of the design.
func decorates(recv types.Object, field types.Type) bool {
	iface, ok := field.Underlying().(*types.Interface)
	if recv == nil || !ok {
		return false
	}
	t := recv.Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	return types.Implements(t, iface) || types.Implements(types.NewPointer(t), iface)
}

func deprecated(doc *ast.CommentGroup) bool {
	return doc != nil && strings.Contains(doc.Text(), "Deprecated:")
}

// onlyCall returns the call that is body's one statement, returned or not.
func onlyCall(body *ast.BlockStmt) *ast.CallExpr {
	if body == nil || len(body.List) != 1 {
		return nil
	}
	var call *ast.CallExpr
	switch s := body.List[0].(type) {
	case *ast.ReturnStmt:
		if len(s.Results) == 1 {
			call, _ = ast.Unparen(s.Results[0]).(*ast.CallExpr)
		}
	case *ast.ExprStmt:
		call, _ = ast.Unparen(s.X).(*ast.CallExpr)
	}
	return call
}

// passesParams reports whether call's arguments are fn's parameters, in order.
func passesParams(fn *ast.FuncDecl, call *ast.CallExpr, info *types.Info) bool {
	var params []*ast.Ident
	variadic := false
	for _, f := range fn.Type.Params.List {
		if len(f.Names) == 0 {
			return false
		}
		params = append(params, f.Names...)
		_, variadic = f.Type.(*ast.Ellipsis)
	}
	if len(params) != len(call.Args) || variadic != call.Ellipsis.IsValid() {
		return false
	}
	for i, a := range call.Args {
		id, ok := ast.Unparen(a).(*ast.Ident)
		if !ok || info.Uses[id] == nil || info.Uses[id] != info.Defs[params[i]] {
			return false
		}
	}
	return true
}

// calleeIdent is the name a call's function expression resolves through:
// f, pkg.f or x.m. An explicit instantiation such as f[T](x), or a
// closure literal, returns nil.
func calleeIdent(fun ast.Expr) *ast.Ident {
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		return f
	case *ast.SelectorExpr:
		return f.Sel
	}
	return nil
}

// callName names fn as code in package from would spell it, with a
// method's receiver type in front.
func callName(fn *types.Func, from string) string {
	name := fn.Name()
	if recv := fn.Signature().Recv(); recv != nil {
		if t := typeName(recv.Type()); t != "" {
			name = t + "." + name
		}
	}
	if fn.Pkg() != nil && fn.Pkg().Path() != from {
		name = fn.Pkg().Name() + "." + name
	}
	return name
}

// callees maps each name a call in n goes through to that call.
func callees(n ast.Node) map[*ast.Ident]*ast.CallExpr {
	out := map[*ast.Ident]*ast.CallExpr{}
	ast.Inspect(n, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id := calleeIdent(c.Fun); id != nil {
				out[id] = c
			}
		}
		return true
	})
	return out
}

// callSites gathers, for one func, what its production call sites pass.
type callSites struct {
	params  []string
	args    []siteArg // per parameter; the variadic one stays varied
	n       int
	escapes bool
}

type siteArg struct {
	value, text string
	varied      bool
}

// site records one reference to fn. A reference that is not a call makes
// fn escape; a test's call is not counted.
func (l *loader) site(u unit, to *Decl, fn *types.Func, call *ast.CallExpr) {
	cs := l.calls[to]
	if cs == nil {
		cs = newCallSites(fn.Signature())
		l.calls[to] = cs
	}
	if call == nil {
		cs.escapes = true
		return
	}
	if !u.d.Test {
		cs.add(call, u.info)
	}
}

func newCallSites(sig *types.Signature) *callSites {
	cs := &callSites{}
	for i := range sig.Params().Len() {
		name := sig.Params().At(i).Name()
		if name == "" || name == "_" {
			name = fmt.Sprintf("argument %d", i+1)
		}
		cs.params = append(cs.params, name)
		cs.args = append(cs.args, siteArg{varied: sig.Variadic() && i == sig.Params().Len()-1})
	}
	return cs
}

// add records what one production call passes.
func (cs *callSites) add(call *ast.CallExpr, info *types.Info) {
	cs.n++
	for i := range cs.args {
		a := &cs.args[i]
		if a.varied {
			continue
		}
		value, text := "", ""
		if i < len(call.Args) {
			value, text = constant(call.Args[i], info)
		}
		switch {
		case value == "" || cs.n > 1 && value != a.value:
			a.varied = true
		case cs.n == 1:
			a.value, a.text = value, text
		}
	}
}

// constant returns e's constant value and its source; nil counts as one.
// Both are "" when e is neither.
func constant(e ast.Expr, info *types.Info) (value, text string) {
	tv, ok := info.Types[e]
	switch {
	case !ok:
		return "", ""
	case tv.Value != nil:
		return tv.Value.ExactString(), types.ExprString(e)
	case tv.IsNil():
		return "nil", "nil"
	}
	return "", ""
}

func (l *loader) finishCalls() {
	for d, cs := range l.calls {
		d.Calls, d.Escapes = cs.n, cs.escapes
		if cs.n == 0 {
			continue
		}
		for i, a := range cs.args {
			if !a.varied {
				d.Fixed = append(d.Fixed, FixedArg{Param: cs.params[i], Value: a.text})
			}
		}
	}
}
