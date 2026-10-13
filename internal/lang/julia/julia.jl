# Extracts a Julia package's modules, files and top-level declarations for
# codemesh, as JSON on stdout. Run as: julia --startup-file=no julia.jl <root>
#
# It uses Julia's own parser (Base.JuliaSyntax), so every span and name is the
# one Julia sees. Files are reached the way Julia reaches them: from
# src/<Name>.jl, ext/ and test/runtests.jl, following include() calls, so each
# file lands in the module that includes it.

const JS = Base.JuliaSyntax

struct Decl
    name::String
    kind::String
    mod::String
    file::String
    test::Bool
    start::Int
    stop::Int
    params::Int
    complexity::Int
    nesting::Int
    sig::String
    args::String
    shape::String
    refs::Vector{String}
end

mutable struct Mod
    path::String
    exports::Set{String}
    imports::Set{String}
    errors::Vector{String}
end

const ROOT = abspath(ARGS[1])
const mods = Dict{String,Mod}()
const files = Dict{String,Tuple{String,Bool}}()   # rel path → (module, test)
const code = Dict{String,Set{Int}}()              # rel path → lines holding code
const decls = Decl[]
const seen = Set{String}()
const aliases = Dict{String,String}()              # const M = SomeModule, or an extension

rel(p) = replace(relpath(p, ROOT), '\\' => '/')
modof(path) = get!(() -> Mod(path, Set{String}(), Set{String}(), String[]), mods, path)
line(n) = JS.source_line(n)
lastline(n) = JS.source_line(n.source, JS.last_byte(n))
kids(n) = something(JS.children(n), JS.SyntaxNode[])

function parsefile(file, mod, test)
    r = rel(file)
    (r in seen || !isfile(file)) && return
    push!(seen, r)
    files[r] = (mod, test)
    src = read(file, String)
    code[r] = codelines(src)
    tree = try
        JS.parseall(JS.SyntaxNode, src; filename=file, ignore_errors=true)
    catch err
        push!(modof(mod).errors, "$r: $(sprint(showerror, err))")
        return
    end
    stream = JS.ParseStream(src)
    JS.parse!(stream; rule=:all)
    for d in stream.diagnostics
        d.level == :error || continue
        push!(modof(mod).errors, "$r:$(JS.source_line(JS.SourceFile(src), JS.first_byte(d))): $(d.message)")
    end
    body(tree, file, mod, test)
end

# codelines lists the lines holding a token other than whitespace or a comment.
function codelines(src)
    sf = JS.SourceFile(src)
    out = Set{Int}()
    for t in JS.tokenize(src)
        JS.kind(t) in (JS.K"Whitespace", JS.K"NewlineWs", JS.K"Comment") && continue
        isempty(t.range) && continue
        union!(out, JS.source_line(sf, first(t.range)):JS.source_line(sf, last(t.range)))
    end
    out
end

function body(n, file, mod, test)
    for c in kids(n)
        top(c, file, mod, test, line(c))
    end
end

# top handles one top-level form; start is where its declaration begins, so a
# docstring or a macro before a definition is part of it.
function top(n, file, mod, test, start)
    k = JS.kind(n)
    if k == JS.K"module"
        cs = kids(n)
        name = string(Expr(cs[1]))
        sub = isempty(mod) ? name : mod * "." * name
        modof(sub)
        r = rel(file)
        isempty(files[r][1]) && (files[r] = (sub, test))
        body(cs[end], file, sub, test)
    elseif k == JS.K"doc"
        top(kids(n)[end], file, mod, test, start)
    elseif k in (JS.K"block", JS.K"toplevel")
        body(n, file, mod, test)
    elseif k == JS.K"export"
        union!(modof(mod).exports, string.(Expr(n).args))
    elseif k in (JS.K"using", JS.K"import")
        using!(n, mod)
    elseif k == JS.K"macrocall"
        ex = Expr(n)
        cs = kids(n)
        name = string(ex.args[1])
        if test && name == "@testset"
            testset(n, ex, file, mod, start)
        elseif !isempty(cs) && JS.kind(cs[end]) in DEFS
            top(cs[end], file, mod, test, start)
        end
    elseif k == JS.K"call" && include!(n, file, mod, test)
    elseif k in DEFS
        def(n, file, mod, test, start)
    end
end

const DEFS = (JS.K"function", JS.K"macro", JS.K"struct", JS.K"abstract", JS.K"primitive", JS.K"const", JS.K"=", JS.K"global")

function include!(n, file, mod, test)
    ex = Expr(n)
    (ex.args[1] == :include && length(ex.args) == 2 && ex.args[2] isa String) || return false
    parsefile(joinpath(dirname(file), ex.args[2]), mod, test)
    true
end

function using!(n, mod)
    for a in Expr(n).args
        a isa Expr || continue
        a.head == :(:) && (a = a.args[1])
        a.head == :as && (a = a.args[1])
        a.head == :. || continue
        parts = string.(a.args)
        # ..X is a sibling of this module; X alone may be any module.
        ups = count(==("."), parts)
        names = filter(!=("."), parts)
        isempty(names) && continue
        if ups > 0
            base = split(mod, '.')
            base = base[1:max(0, length(base) - ups + 1)]
            # A module binds its own name: ..M from M's child is M itself.
            !isempty(base) && names[1] == base[end] && (names = names[2:end])
            push!(modof(mod).imports, join(vcat(base, names), '.'))
        else
            push!(modof(mod).imports, join(names, '.'))
        end
    end
end

# testset makes a test set one test, or its inner sets when it has some, so
# a suite wrapped in one outer set still counts its tests apart.
function testset(n, ex, file, mod, start)
    block = kids(n)[end]
    if JS.kind(block) == JS.K"block" && any(c -> JS.kind(c) == JS.K"macrocall" && string(Expr(c).args[1]) == "@testset", kids(block))
        body(block, file, mod, true)
        return
    end
    title = something(findfirst(a -> a isa String, ex.args), 0)
    name = title == 0 ? "testset" : "@testset " * repr(ex.args[title])
    push!(decls, Decl(name, "func", mod, rel(file), true, start, lastline(n), 0,
        complexity(ex), nesting(ex), "", "", shape(ex), refs(ex)))
end

function def(n, file, mod, test, start)
    k = JS.kind(n)
    ex = Expr(n)
    if k in (JS.K"function", JS.K"macro") || (k == JS.K"=" && callee(ex.args[1]) !== nothing)
        sig = ex.args[1]
        call = callee(sig)
        call === nothing && return
        name = fname(call.args[1])
        name === nothing && return
        k == JS.K"macro" && (name = "@" * name)
        params = count(a -> !(a isa Expr && a.head == :parameters), call.args[2:end])
        for a in call.args[2:end]
            a isa Expr && a.head == :parameters && (params += length(a.args))
        end
        b = length(ex.args) >= 2 ? ex.args[2] : nothing
        push!(decls, Decl(name, "func", mod, rel(file), test, start, lastline(n), params,
            complexity(b), nesting(b), string(Base.remove_linenums!(deepcopy(sig))), argtypes(call), shape(ex), refs(b)))
    elseif k in (JS.K"struct", JS.K"abstract", JS.K"primitive")
        head = k == JS.K"struct" ? ex.args[2] : ex.args[1]
        name = tname(head)
        name === nothing && return
        push!(decls, Decl(name, "type", mod, rel(file), test, start, lastline(n), 0, 0, 0,
            string(Base.remove_linenums!(deepcopy(ex))), "", shape(ex), refs(ex)))
    elseif k in (JS.K"const", JS.K"global", JS.K"=")
        a = k == JS.K"=" ? ex : ex.args[1]
        a isa Expr && a.head == :(=) || return
        lhs = a.args[1]
        lhs isa Expr && lhs.head == :(::) && (lhs = lhs.args[1])
        lhs isa Symbol || return
        alias!(string(lhs), a.args[2])
        push!(decls, Decl(string(lhs), k == JS.K"const" ? "const" : "var", mod, rel(file), test, start, lastline(n), 0, 0, 0,
            "", "", shape(ex), refs(a.args[2])))
    end
end

# alias! notes a name bound to a module, so M.f through it reaches the
# module's f: const MapLibre = Base.get_extension(HyperSignal, :HyperSignalMapLibreExt).
function alias!(name, rhs)
    if rhs isa Expr && rhs.head == :call && string(rhs.args[1]) == "Base.get_extension" && length(rhs.args) == 3 && rhs.args[3] isa QuoteNode
        aliases[name] = string(rhs.args[3].value)
    elseif rhs isa Symbol || (rhs isa Expr && rhs.head == :.)
        aliases[name] = string(rhs)
    end
end

# callee finds the call in a signature: f(x), f(x)::T, f(x) where T.
function callee(sig)
    while sig isa Expr && sig.head in (:where, :(::))
        sig = sig.args[1]
    end
    sig isa Expr && sig.head == :call ? sig : nothing
end

# argtypes is what tells one method from another: the positional argument
# types, (IO, Any, Int). Keywords do not take part in dispatch.
function argtypes(call)
    ts = String[]
    for a in call.args[2:end]
        a isa Expr && a.head == :parameters && continue
        a isa Expr && a.head == :kw && (a = a.args[1])
        dots = a isa Expr && a.head == :... ? "..." : ""
        dots == "" || (a = a.args[1])
        t = a isa Expr && a.head == :(::) ? string(a.args[end]) : "Any"
        push!(ts, t * dots)
    end
    "(" * join(ts, ", ") * ")"
end

# fname names a method: f, Base.show, (::T) for a functor.
function fname(f)
    f isa Symbol && return string(f)
    f isa Expr && f.head == :. && return string(f.args[1]) * "." * string(f.args[end] isa QuoteNode ? f.args[end].value : f.args[end])
    f isa Expr && f.head == :(::) && return "(" * string(f) * ")"
    f isa Expr && f.head == :curly && return fname(f.args[1])
    nothing
end

function tname(h)
    h isa Symbol && return string(h)
    h isa Expr && h.head in (:curly, :<:) && return tname(h.args[1])
    nothing
end

const BRANCH = (:if, :elseif, :for, :while, :&&, :||, :try)

function complexity(ex)
    n = 1
    walk(ex) do e
        e isa Expr || return
        e.head in BRANCH && (n += 1)
        e.head == :generator && (n += length(e.args) - 1)
        e.head == :filter && (n += 1)
    end
    n
end

nesting(ex) = ex isa Expr ? depth(ex) : 0
function depth(ex)
    ex isa Expr || return 0
    d = maximum((depth(a) for a in ex.args); init=0)
    ex.head in (:if, :for, :while, :try, :let, :do) ? d + 1 : d
end

function walk(f, ex)
    f(ex)
    ex isa Expr && foreach(a -> walk(f, a), ex.args)
end

# refs lists the names a body uses, qualified ones as M.f, for codemesh to
# match against declarations by name: Julia dispatches at run time, so this
# is an inference, not a resolution.
function refs(ex)
    out = Set{String}()
    walk(ex) do e
        if e isa Symbol
            push!(out, string(e))
        elseif e isa Expr && e.head == :. && (q = dotted(e)) !== nothing
            push!(out, q)
        elseif e isa Expr && e.head == :macrocall && e.args[1] isa Symbol
            push!(out, string(e.args[1]))
        end
    end
    sort!(collect(out))
end

# dotted spells A.B.f, or nothing for a field access on a value.
function dotted(e)
    e isa Symbol && return string(e)
    e isa Expr && e.head == :. && length(e.args) == 2 && e.args[2] isa QuoteNode || return nothing
    head = dotted(e.args[1])
    head === nothing ? nothing : head * "." * string(e.args[2].value)
end

shape(ex) = string(Base.remove_linenums!(deepcopy(ex)))

function jstr(io, s)
    print(io, '"')
    for c in s
        if c == '"' print(io, "\\\"")
        elseif c == '\\' print(io, "\\\\")
        elseif c == '\n' print(io, "\\n")
        elseif c == '\r' print(io, "\\r")
        elseif c == '\t' print(io, "\\t")
        elseif c < ' ' print(io, "\\u", string(UInt16(c), base=16, pad=4))
        else print(io, c)
        end
    end
    print(io, '"')
end

function jarr(io, xs)
    print(io, '[')
    for (i, x) in enumerate(xs)
        i > 1 && print(io, ',')
        jstr(io, x)
    end
    print(io, ']')
end

function main()
    proj = read(joinpath(ROOT, "Project.toml"), String)
    m = match(r"^name\s*=\s*\"([^\"]+)\""m, proj)
    name = m === nothing ? basename(ROOT) : m.captures[1]
    entry = joinpath(ROOT, "src", name * ".jl")
    isfile(entry) && parsefile(entry, "", false)
    ext = joinpath(ROOT, "ext")
    if isdir(ext)
        for f in sort(readdir(ext))
            p = joinpath(ext, f)
            isfile(p) && endswith(f, ".jl") && parsefile(p, "", false)
            isdir(p) && parsefile(joinpath(p, f * ".jl"), "", false)
        end
    end
    tests = joinpath(ROOT, "test")
    isfile(joinpath(tests, "runtests.jl")) && parsefile(joinpath(tests, "runtests.jl"), name, true)
    # Files nothing includes still belong to the package's root module.
    for dir in ("src", "ext", "test"), (d, _, fs) in walkdir(joinpath(ROOT, dir))
        for f in sort(fs)
            endswith(f, ".jl") && parsefile(joinpath(d, f), name, dir == "test")
        end
    end
    io = stdout
    print(io, "{\"module\":")
    jstr(io, name)
    print(io, ",\"aliases\":{")
    for (i, (k, v)) in enumerate(sort(collect(aliases)))
        i > 1 && print(io, ',')
        jstr(io, k); print(io, ':'); jstr(io, v)
    end
    print(io, "},\"modules\":[")
    for (i, p) in enumerate(sort(filter(!isempty, collect(keys(mods)))))
        md = mods[p]
        i > 1 && print(io, ',')
        print(io, "{\"path\":"); jstr(io, p)
        print(io, ",\"exports\":"); jarr(io, sort(collect(md.exports)))
        print(io, ",\"imports\":"); jarr(io, sort(collect(md.imports)))
        print(io, ",\"errors\":"); jarr(io, md.errors)
        print(io, '}')
    end
    print(io, "],\"files\":[")
    for (i, f) in enumerate(sort(collect(keys(files))))
        i > 1 && print(io, ',')
        mod, test = files[f]
        print(io, "{\"path\":"); jstr(io, f)
        print(io, ",\"module\":"); jstr(io, mod)
        print(io, ",\"test\":", test, ",\"lines\":", length(get(code, f, ())), '}')
    end
    print(io, "],\"decls\":[")
    for (i, d) in enumerate(decls)
        i > 1 && print(io, ',')
        print(io, "{\"name\":"); jstr(io, d.name)
        print(io, ",\"kind\":"); jstr(io, d.kind)
        print(io, ",\"module\":"); jstr(io, d.mod)
        print(io, ",\"file\":"); jstr(io, d.file)
        print(io, ",\"test\":", d.test, ",\"lines\":", count(in(get(code, d.file, Set{Int}())), d.start:d.stop), ",\"start\":", d.start, ",\"end\":", d.stop, ",\"params\":", d.params,
            ",\"complexity\":", d.complexity, ",\"nesting\":", d.nesting)
        print(io, ",\"sig\":"); jstr(io, d.sig)
        print(io, ",\"args\":"); jstr(io, d.args)
        print(io, ",\"shape\":"); jstr(io, d.shape)
        print(io, ",\"refs\":"); jarr(io, d.refs)
        print(io, '}')
    end
    println(io, "]}")
end

main()
