"""Just enough Go parsing to read rules out of the controller, shared by the doc generators.

These pages are only worth building if they cannot drift from the code they
describe, which means reading the Go rather than restating it. Nothing here is a
real Go parser: it is a brace matcher, a switch splitter, and a whitelisting
tokenizer for the boolean expressions the controller writes in `case` clauses.

The whitelist is the point. An expression shape a caller has not declared raises
`ParseError`, so a new predicate in the controller stops the build instead of
being silently skipped. Every caller passes the set of identifiers it is willing
to evaluate, and gets back both a Python and a JavaScript rendering of the same
expression — one tokenizer, so an evaluator in either language cannot disagree
with the other about what a case means.
"""

import re


class ParseError(Exception):
    """Raised when Go source does not have the shape a generator expects."""


def matching_brace(src, i):
    """Index of the brace closing the one at i, skipping strings and comments."""
    depth = 0
    while i < len(src):
        c = src[i]
        if c == '"':
            i += 1
            while src[i] != '"':
                i += 2 if src[i] == "\\" else 1
        elif c == "`":
            i = src.index("`", i + 1)
        elif src.startswith("//", i):
            i = src.index("\n", i)
        elif c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                return i
        i += 1
    raise ParseError("unbalanced braces")


def func_body(src, name):
    """The text between the opening and closing brace of a top-level func."""
    m = re.search(r"^func %s\(" % re.escape(name), src, re.M)
    if not m:
        raise ParseError("func %s not found" % name)
    open_brace = src.index("{", src.index(")", m.start()))
    return src[open_brace + 1 : matching_brace(src, open_brace)]


def split_cases(switch_body):
    """Ordered [(case_expr_or_None, body)] for a Go expression-less switch."""
    marks = [
        (m.start(), m.group(1))
        for m in re.finditer(r"^\t*(?:case (.+?)|(default)):$", switch_body, re.M)
    ]
    if not marks:
        raise ParseError("switch with no cases")
    out = []
    for idx, (pos, expr) in enumerate(marks):
        end = marks[idx + 1][0] if idx + 1 < len(marks) else len(switch_body)
        out.append((expr, switch_body[switch_body.index("\n", pos) + 1 : end]))
    return out


def switch_after(body, pos=0):
    """The body of the first `switch {` at or after pos."""
    m = re.compile(r"^\t*switch \{$", re.M).search(body, pos)
    if not m:
        raise ParseError("no switch found after offset %d" % pos)
    open_brace = body.index("{", m.start())
    return body[open_brace + 1 : matching_brace(body, open_brace)]


def struct_fields(src, name):
    """Ordered field names of a top-level struct, comments and blanks skipped."""
    m = re.search(r"^type %s struct \{" % re.escape(name), src, re.M)
    if not m:
        raise ParseError("type %s not found" % name)
    open_brace = src.index("{", m.start())
    body = src[open_brace + 1 : matching_brace(src, open_brace)]
    out = []
    for line in body.split("\n"):
        f = re.match(r"\t(\w+)\s+\S", line)
        if f:
            out.append(f.group(1))
    if not out:
        raise ParseError("struct %s has no fields" % name)
    return out


def const_expr(src, name):
    """The right-hand side of a top-level `const name[ type] = expr` line."""
    m = re.search(r"^const %s(?: [\w.\[\]]+)? = (.+)$" % re.escape(name), src, re.M)
    if not m:
        raise ParseError("const %s not found" % name)
    return m.group(1).strip()


# ---------------------------------------------------------------------------
# Expression transpiler
# ---------------------------------------------------------------------------

TOKEN = re.compile(
    r"""\s*(?:
        (?P<ident>[A-Za-z_]\w*(?:\.\w+)*) |
        (?P<int>\d+) |
        (?P<op>&&|\|\||==|!=|<=|>=|<|>|!|\(|\)|,)
    )""",
    re.X,
)

PY_OPS = {"&&": "and", "||": "or", "!": "not "}


def tokenize(expr):
    out, i = [], 0
    while i < len(expr):
        m = TOKEN.match(expr, i)
        if not m or m.end() == i:
            if expr[i:].strip() == "":
                break
            raise ParseError("cannot tokenize %r at %r" % (expr, expr[i:]))
        i = m.end()
        out.append((m.lastgroup, m.group(m.lastgroup)))
    return out


def transpile(expr, names, py_ref="s[%r]", js_ref="s.%s"):
    """Go boolean expression -> (python_expr, js_expr).

    `names` maps a Go identifier as it appears in the source (`in.ready`,
    `prevShortfall`) to the key both evaluators use for it. An identifier
    outside that mapping raises, which is what makes this a drift guard rather
    than a best-effort translation.
    """
    py, js = [], []
    for kind, tok in tokenize(expr):
        if kind == "int":
            py.append(tok)
            js.append(tok)
        elif kind == "op":
            py.append(PY_OPS.get(tok, tok))
            js.append(tok)
        elif tok == "len":
            py.append("len")
            js.append("LEN")
        else:
            if tok not in names:
                raise ParseError("unknown identifier %r in case %r" % (tok, expr))
            py.append(py_ref % names[tok])
            js.append(js_ref % names[tok])
    return " ".join(py), js_len(" ".join(js))


def js_len(js):
    """LEN(x) -> (x).length, once the tokens are joined."""
    return re.sub(r"LEN \( ([^)]+) \)", r"(\1).length", js)
