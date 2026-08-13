#!/usr/bin/env python3
"""The shared document layer: git access, code figures, prose fragments, the
stylesheet, and the page skeleton.

Nothing in here knows which document it is building. The tutorial builder and
the standalone simulator pages all render through `page()`, so they cannot
drift apart on typography, palette, or copy-button behaviour.

Extracted from build_tutorial_doc.py, which built the retired v1 tutorial. Its
v1-only parts went with it: the step-NN tag helper, four prose helpers no
document still calls, and a Python and a JavaScript port of the distribution
algorithm written against the removed minRatio/maxRatio API. Sim 1 supersedes
both ports, and reads its fixtures out of the Go tests rather than restating
them.

Stdlib only, as everything under docs/ is.
"""
import html
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

# Publication origin, e.g. "https://example.com". A share card's url and image
# have to be absolute, and nothing inside the document knows where it will be
# served from -- so while this is empty those two tags are omitted rather than
# guessed, and the card falls back to the kind that needs no image.
SITE = ""

LICENSE_RE = re.compile(r"^/\*\nCopyright.*?\*/\n\n", re.S)


# --------------------------------------------------------------------------
# git access
# --------------------------------------------------------------------------

def git(*args):
    r = subprocess.run(
        ["git", "-C", str(ROOT), *args], capture_output=True, text=True
    )
    if r.returncode != 0:
        sys.exit(f"git {' '.join(args)} failed:\n{r.stderr}")
    return r.stdout


def at(tag, relpath, strip_license=True):
    """The full text of relpath as of tag."""
    text = git("show", f"{tag}:{relpath}")
    if strip_license:
        text = LICENSE_RE.sub("", text)
    return text.rstrip()


# gofmt outdents a label one level, so a label in a function body sits in
# column zero, where it looks exactly like the start of the next declaration.
# Only that case reaches here: this is consulted on column-zero lines alone.
LABEL_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*:$")


def balanced(text):
    """Whether every bracket opened in text is also closed in it, counting
    only code: comments, strings and runes are skipped.

    The column-zero scan below infers where a block ends. When it guesses
    wrong the brackets do not balance, so this is what turns a bad guess into
    a build failure instead of a figure that is quietly missing its tail.
    """
    depth = 0
    i, n = 0, len(text)
    while i < n:
        c = text[i]
        nxt = text[i + 1] if i + 1 < n else ""
        if c == "/" and nxt == "/":
            j = text.find("\n", i)
            i = n if j < 0 else j + 1
        elif c == "/" and nxt == "*":
            j = text.find("*/", i + 2)
            i = n if j < 0 else j + 2
        elif c == "`":
            j = text.find("`", i + 1)
            i = n if j < 0 else j + 1
        elif c in "\"'":
            i += 1
            while i < n and text[i] != c:
                i += 2 if text[i] == "\\" else 1
            i += 1
        else:
            if c in "([{":
                depth += 1
            elif c in ")]}":
                depth -= 1
                if depth < 0:
                    return False
            i += 1
    return depth == 0


def excerpt(tag, relpath, start, end=None, strip_license=True):
    """A slice of relpath at tag, delimited by substrings rather than line
    numbers so that edits elsewhere in the file cannot silently shift it.

    start and end are matched as substrings of a line; the first line
    containing each wins. end is exclusive; omit it to run to the end of the
    enclosing top-level block (the next line that starts in column zero after
    at least one indented line).
    """
    lines = at(tag, relpath, strip_license).splitlines()

    try:
        i = next(n for n, l in enumerate(lines) if start in l)
    except StopIteration:
        sys.exit(f"excerpt: {start!r} not found in {tag}:{relpath}")

    # Include any doc comment immediately above the anchor.
    anchor = i
    while i > 0 and lines[i - 1].lstrip().startswith("//"):
        i -= 1

    if end is not None:
        try:
            j = next(n for n, l in enumerate(lines) if end in l and n > i)
        except StopIteration:
            sys.exit(f"excerpt: {end!r} not found after {start!r} in {tag}:{relpath}")
        return "\n".join(lines[i:j]).rstrip()

    # Everything below reads Go's block structure: column zero, an opening
    # brace, a closing brace or paren. No other language is understood, and the
    # balance check at the end is no safety net either -- YAML mostly has no
    # brackets, so a truncated span of it balances and a one-line figure would
    # ship looking complete.
    if not relpath.endswith(".go"):
        sys.exit(
            f"excerpt: only Go is extracted by block, but {tag}:{relpath} is "
            f"not Go. Pass an explicit end=, or omit start= to take the whole "
            f"file.")

    # The forward scan starts at the declaration, never at a comment: a comment
    # sits in column zero, so scanning from one would read its second line as
    # the end of the block and return that single line. That holds whether the
    # comment came from the doc-comment walk above or because start matched
    # inside the comment itself.
    scan = anchor
    while scan < len(lines) - 1 and (
            lines[scan].lstrip().startswith("//") or not lines[scan].strip()):
        scan += 1

    # A declaration that opens no block is the whole block: "type A = B" ends
    # on its own line. Scanning on would swallow whatever follows it, because
    # the next declaration's own opening brace reads as a split signature.
    decl = lines[scan]
    if balanced(decl) and not decl.rstrip().endswith(("{", "(")):
        return "\n".join(lines[i:scan + 1]).rstrip()

    j = scan + 1
    seen_body = False
    in_raw = False
    while j < len(lines):
        l = lines[j]
        # A raw string can hold anything, including column-zero YAML. Nothing
        # inside one delimits a Go block.
        if in_raw:
            if l.count("`") % 2:
                in_raw = False
            seen_body = True
            j += 1
            continue
        if l and not l[0].isspace():
            # A signature split across lines closes in column zero as well,
            # and is not the end of anything: it always ends with the opening
            # brace, which is what tells the two apart. An outdented label is
            # body too.
            if not l.rstrip().endswith("{") and not LABEL_RE.match(l.strip()):
                # Take the closer with the block: "}" for a func or type, ")"
                # for a const or var group.
                if seen_body and (l.startswith("}") or l.startswith(")")):
                    j += 1
                break
        if l.strip():
            seen_body = True
        if l.count("`") % 2:
            in_raw = True
        j += 1

    text = "\n".join(lines[i:j]).rstrip()
    if not balanced(text):
        sys.exit(
            f"excerpt: the block at {start!r} in {tag}:{relpath} does not "
            f"close ({len(text.splitlines())} lines taken). The column-zero "
            f"scan stopped in the wrong place; pass an explicit end= for this "
            f"figure.")
    return text

# --------------------------------------------------------------------------
# code figures
# --------------------------------------------------------------------------

_uid = [0]


def _next_id():
    _uid[0] += 1
    return f"c{_uid[0]}"


# Highlighting happens here, at build time, rather than in the browser: a
# runtime highlighter would be the document's only external request, and the
# markup it produces is the same every time anyway. Tokens are matched in one
# pass so that a keyword inside a comment, or a // inside a string, stays what
# it is.
_LEXERS = {
    "go": re.compile(r"""
        (?P<comment>//[^\n]*|/\*.*?\*/)
      | (?P<string>`[^`]*`|"(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*')
      | (?P<keyword>\b(?:break|case|chan|const|continue|default|defer|else
                      |fallthrough|for|func|go|goto|if|import|interface|map
                      |package|range|return|select|struct|switch|type|var)\b)
      | (?P<literal>\b(?:true|false|nil|iota)\b)
      | (?P<number>\b\d[\d_.]*\b)
    """, re.X | re.S),

    "yaml": re.compile(r"""
        (?P<comment>\#[^\n]*)
      | (?P<string>"(?:\\.|[^"\\\n])*"|'[^'\n]*')
      | (?P<key>^[ \t-]*[\w.\-/]+(?=:))
      | (?P<literal>\b(?:true|false|null)\b)
      | (?P<number>\b\d[\d.]*\b)
    """, re.X | re.M),

    "shell": re.compile(r"""
        (?P<comment>\#[^\n]*)
      | (?P<string>"(?:\\.|[^"\\])*"|'[^']*')
      | (?P<cmd>^[A-Za-z][\w./-]*)
      | (?P<flag>-{1,2}[A-Za-z][\w-]*)
    """, re.X | re.M),
}


def highlight(code, lang):
    """Wrap the tokens of `code` in spans. Unknown languages are escaped only."""
    rx = _LEXERS.get(lang)
    if rx is None:
        return html.escape(code)

    out, last = [], 0
    for m in rx.finditer(code):
        out.append(html.escape(code[last:m.start()]))
        out.append(f'<span class="t-{m.lastgroup}">{html.escape(m.group())}</span>')
        last = m.end()
    out.append(html.escape(code[last:]))
    return "".join(out)


def figure(caption, code, lang="go", hint=None):
    cid = _next_id()
    hint_html = f'<span class="hint">{html.escape(hint)}</span>' if hint else ""
    return (
        f'<figure class="code">'
        f'<figcaption><span class="fname">{html.escape(caption)}</span>{hint_html}'
        f'<button class="copy" data-for="{cid}" type="button">copy</button></figcaption>'
        f'<pre data-lang="{lang}"><code id="{cid}">{highlight(code, lang)}</code></pre>'
        f"</figure>"
    )


def block(tag, relpath, lang="go", start=None, end=None, caption=None):
    """A code figure read from the reference branch."""
    code = excerpt(tag, relpath, start, end) if start else at(tag, relpath)
    return figure(caption or relpath, code, lang, hint=f"git show {tag}:{relpath}")


def shell(code, caption="shell"):
    return figure(caption, code.strip(), "shell")


def checkpoint(body_html):
    """What the reader can confirm before going on.

    A document read against a checked-out tag needs somewhere to stand: this
    is where the page and the reader's own tree are asked to agree.
    """
    return (f'<div class="checkpoint"><div class="cp-label">Checkpoint</div>'
            f'{body_html}</div>')

# --------------------------------------------------------------------------
# prose fragments
# --------------------------------------------------------------------------

def table(headers, rows):
    h = "".join(f"<th>{c}</th>" for c in headers)
    b = "".join(
        "<tr>" + "".join(f"<td>{c}</td>" for c in r) + "</tr>" for r in rows
    )
    return f'<div class="tw"><table><thead><tr>{h}</tr></thead><tbody>{b}</tbody></table></div>'


def p(*paras):
    return "".join(f"<p>{t}</p>" for t in paras)


def ul(*items):
    return "<ul>" + "".join(f"<li>{i}</li>" for i in items) + "</ul>"


def h3(t):
    return f"<h3>{html.escape(t)}</h3>"


# --------------------------------------------------------------------------
# stylesheet
# --------------------------------------------------------------------------

CSS = """
:root {
  --ground:  #F2F4F6;
  --surface: #FFFFFF;
  --sunken:  #E9ECEF;
  --ink:     #161B1F;
  --ink-2:   #545F6B;
  --ink-3:   #7C8894;
  --rule:    #D8DDE3;
  --accent:  #0F5F6E;
  --go:      #63549C;
  --detour:  #B0761B;
  --warn:    #A03428;
  --code-bg: #EDF0F3;
  --go-wash:     rgba(99, 84, 156, .07);
  --detour-wash: rgba(176, 118, 27, .08);
  --accent-wash: rgba(15, 95, 110, .06);
  /* Simulator bar segments carry text, so these are the darker cousins of
     --detour and friends: on --ground they clear 4.5:1, which the mid-tone
     amber the detour asides use does not. */
  /* Aliases. These four are what the components ask for by name -- a fold
     wants an edge, not a rule -- and they were being used without ever being
     declared, so every border, background and mono font that named one was
     silently dropped. Defined through var() so the dark scheme's overrides
     below carry to them without being restated. */
  --edge:  var(--rule);
  --wash:  var(--sunken);
  --faint: var(--ink-3);
  --mono:  ui-monospace, "SF Mono", "JetBrains Mono", "Cascadia Mono", Menlo,
           Consolas, monospace;
  --t-comment: #6C7A87;
  --t-string:  #1F6B4C;
  --t-keyword: #7A4CA8;
  --t-literal: #A03428;
  --t-number:  #8A5A12;
  --t-key:     #0F5F6E;
  --t-cmd:     #7A4CA8;
  --t-flag:    #8A5A12;
  --sim-c:   #8A5A12;
  --sim-d:   #35684A;
  --sim-e:   #9A5068;
}
@media (prefers-color-scheme: dark) {
  :root {
    --ground:  #0F1318;
    --surface: #161C23;
    --sunken:  #1B222B;
    --ink:     #E3E9EF;
    --ink-2:   #9BA7B4;
    --ink-3:   #6E7B89;
    --rule:    #262F3A;
    --accent:  #52BFD2;
    --go:      #A296CE;
    --detour:  #E0A845;
    --warn:    #E8796B;
    --code-bg: #10161D;
    --go-wash:     rgba(162, 150, 206, .10);
    --detour-wash: rgba(224, 168, 69, .10);
    --accent-wash: rgba(82, 191, 210, .08);
    --t-comment: #6E7B89;
    --t-string:  #7FC8A0;
    --t-keyword: #C0A0E8;
    --t-literal: #E8796B;
    --t-number:  #E0A845;
    --t-key:     #52BFD2;
    --t-cmd:     #C0A0E8;
    --t-flag:    #E0A845;
    --sim-c:   #E0A845;
    --sim-d:   #6FBF93;
    --sim-e:   #D98FA5;
  }
}
:root[data-theme="light"] {
  --ground:#F2F4F6; --surface:#FFFFFF; --sunken:#E9ECEF; --ink:#161B1F; --ink-2:#545F6B;
  --ink-3:#7C8894; --rule:#D8DDE3; --accent:#0F5F6E; --go:#63549C; --detour:#B0761B;
  --warn:#A03428; --code-bg:#EDF0F3;
  --go-wash:rgba(99,84,156,.07); --detour-wash:rgba(176,118,27,.08); --accent-wash:rgba(15,95,110,.06);
  --sim-c:#8A5A12; --sim-d:#35684A; --sim-e:#9A5068;
}
:root[data-theme="dark"] {
  --ground:#0F1318; --surface:#161C23; --sunken:#1B222B; --ink:#E3E9EF; --ink-2:#9BA7B4;
  --ink-3:#6E7B89; --rule:#262F3A; --accent:#52BFD2; --go:#A296CE; --detour:#E0A845;
  --warn:#E8796B; --code-bg:#10161D;
  --go-wash:rgba(162,150,206,.10); --detour-wash:rgba(224,168,69,.10); --accent-wash:rgba(82,191,210,.08);
  --sim-c:#E0A845; --sim-d:#6FBF93; --sim-e:#D98FA5;
}

* { box-sizing: border-box; }

body {
  margin: 0;
  background: var(--ground);
  color: var(--ink);
  font-family: Charter, "Bitstream Charter", "Sitka Text", Cambria, "Iowan Old Style", Georgia, serif;
  font-size: 17px;
  line-height: 1.68;
  -webkit-font-smoothing: antialiased;
}

.mono, code, pre, h1, h2, h3, h4, .eyebrow, th, .num, figcaption, nav,
.cp-label, .gn-label, .dt-label, .rail-part, .steptag {
  font-family: var(--mono);
}

/* ---------- shell ---------- */
.shell { display: grid; grid-template-columns: 1fr; }
@media (min-width: 1100px) {
  .shell {
    grid-template-columns: 264px minmax(0, 1fr);
    gap: 52px; max-width: 1220px; margin: 0 auto;
  }
}

main { min-width: 0; padding: 56px 22px 120px; }
@media (min-width: 1100px) { main { padding: 56px 32px 160px 0; } }

/* ---------- step rail ---------- */
nav.rail { display: none; }
@media (min-width: 1100px) {
  nav.rail {
    display: block; position: sticky; top: 0; align-self: start;
    max-height: 100vh; overflow-y: auto; padding: 56px 0 80px 28px;
    font-size: 12.5px; line-height: 1.45;
  }
}
nav.rail ol { list-style: none; margin: 0 0 18px; padding: 0; }
nav.rail a {
  display: flex; gap: 9px; padding: 4px 10px; color: var(--ink-3);
  text-decoration: none; border-left: 2px solid transparent;
  transition: color .15s, border-color .15s;
}
nav.rail a:hover { color: var(--ink); }
nav.rail a.active { color: var(--accent); border-left-color: var(--accent); }
nav.rail a .num { flex: none; opacity: .65; font-variant-numeric: tabular-nums; }
.rail-part {
  font-size: 10px; letter-spacing: .15em; text-transform: uppercase;
  color: var(--ink-3); padding: 16px 10px 6px; opacity: .8;
}
.rail-head {
  font-size: 10.5px; letter-spacing: .14em; text-transform: uppercase;
  color: var(--ink-3); padding: 0 10px 12px; border-bottom: 1px solid var(--rule);
  margin-bottom: 4px;
}

/* ---------- masthead ---------- */
header.masthead { max-width: 74ch; margin-bottom: 64px; }
.eyebrow {
  font-size: 11px; letter-spacing: .16em; text-transform: uppercase;
  color: var(--accent); margin-bottom: 18px;
}
h1 {
  font-size: clamp(30px, 5vw, 44px); line-height: 1.15; margin: 0 0 20px;
  letter-spacing: -.02em; text-wrap: balance; font-weight: 600;
}
.standfirst { font-size: 19px; color: var(--ink-2); margin: 0 0 26px; }

.facts {
  display: flex; flex-wrap: wrap; gap: 0; border-top: 1px solid var(--rule);
  border-bottom: 1px solid var(--rule); padding: 14px 0; margin-top: 30px;
}
.facts div { flex: 1 1 130px; padding-right: 18px; }
.facts dt {
  font-size: 10px; letter-spacing: .13em; text-transform: uppercase;
  color: var(--ink-3); font-family: ui-monospace, Menlo, monospace;
}
.facts dd {
  margin: 3px 0 0; font-size: 15px; font-variant-numeric: tabular-nums;
  font-family: ui-monospace, Menlo, monospace;
}

/* ---------- sections ---------- */
section { max-width: 74ch; margin-bottom: 76px; scroll-margin-top: 24px; }
section > p, section > ul, section > ol, section > .tw { max-width: 68ch; }

h2 {
  font-size: 23px; line-height: 1.25; margin: 0 0 6px; font-weight: 600;
  letter-spacing: -.01em; text-wrap: balance;
}
h3 {
  font-size: 16px; margin: 40px 0 10px; font-weight: 600;
  color: var(--ink); letter-spacing: -.005em;
}
h4 { font-size: 13.5px; margin: 0 0 6px; font-weight: 600; }

.steptag {
  display: inline-block; font-size: 11px; letter-spacing: .1em;
  text-transform: uppercase; color: var(--ink-3); margin-bottom: 10px;
}
.steptag b { color: var(--accent); font-weight: 600; }

p { margin: 0 0 17px; }
a { color: var(--accent); text-underline-offset: 2px; }
ul, ol { margin: 0 0 17px; padding-left: 22px; }
li { margin-bottom: 7px; }
strong { font-weight: 600; }

code {
  font-size: .87em; background: var(--code-bg); padding: 1px 5px;
  border-radius: 3px; word-break: break-word;
}

/* ---------- code figures ---------- */
figure.code { margin: 24px 0; border: 1px solid var(--rule); border-radius: 5px; overflow: hidden; background: var(--surface); }
figcaption {
  display: flex; align-items: center; gap: 12px;
  padding: 7px 12px; background: var(--sunken); border-bottom: 1px solid var(--rule);
  font-size: 11.5px; color: var(--ink-2);
}
figcaption .fname { font-weight: 600; color: var(--ink); }
figcaption .hint {
  color: var(--ink-3); font-size: 10.5px; margin-left: auto;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
figcaption .copy {
  flex: none; font: inherit; font-size: 10.5px; cursor: pointer;
  background: var(--surface); color: var(--ink-2);
  border: 1px solid var(--rule); border-radius: 3px; padding: 2px 9px;
  font-family: ui-monospace, Menlo, monospace;
}
figcaption .hint + .copy { margin-left: 0; }
.t-comment { color: var(--t-comment); font-style: italic; }
.t-string  { color: var(--t-string); }
.t-keyword { color: var(--t-keyword); }
.t-literal { color: var(--t-literal); }
.t-number  { color: var(--t-number); }
.t-key     { color: var(--t-key); }
.t-cmd     { color: var(--t-cmd); font-weight: 600; }
.t-flag    { color: var(--t-flag); }

figcaption .copy:hover { color: var(--accent); border-color: var(--accent); }
figcaption .copy.done { color: var(--accent); border-color: var(--accent); }
figure.code pre {
  margin: 0; padding: 14px 16px; overflow-x: auto;
  background: var(--code-bg); font-size: 12.5px; line-height: 1.62;
}
figure.code pre code { background: none; padding: 0; font-size: inherit; }

/* ---------- checkpoint ---------- */
.checkpoint {
  margin: 30px 0; padding: 18px 20px 6px; background: var(--sunken);
  border-left: 3px solid var(--accent); border-radius: 0 5px 5px 0;
}
.cp-label {
  font-size: 10px; letter-spacing: .15em; text-transform: uppercase;
  color: var(--accent); margin-bottom: 10px;
}
.checkpoint figure.code { margin: 14px 0; }
.checkpoint p:last-child { margin-bottom: 14px; }

/* ---------- simulator ---------- */
.sim {
  margin: 30px 0; padding: 14px 18px 18px; background: var(--surface);
  border: 1px solid var(--rule); border-radius: 5px;
}
.sim-head { display: flex; align-items: center; justify-content: space-between; }
.sim-label {
  font-size: 10px; letter-spacing: .15em; text-transform: uppercase;
  color: var(--accent);
  font-family: ui-monospace, Menlo, monospace;
}
.sim button {
  font-family: ui-monospace, Menlo, monospace; font-size: 11px;
  color: var(--ink-3); background: none; border: 1px solid var(--rule);
  border-radius: 3px; padding: 3px 8px; cursor: pointer;
}
.sim button:hover { color: var(--accent); border-color: var(--accent); }
.sim button:disabled { opacity: .4; cursor: default; }
.sim button:disabled:hover { color: var(--ink-3); border-color: var(--rule); }
.sim .tw { margin: 12px 0 0; }
.sim table { font-size: 13px; }
.sim th, .sim td { padding: 6px 10px 6px 0; vertical-align: middle; }
.sim input, .sim select {
  font-family: ui-monospace, Menlo, monospace; font-size: 12.5px;
  color: var(--ink); background: var(--code-bg);
  border: 1px solid var(--rule); border-radius: 3px; padding: 4px 6px;
}
.sim input:focus, .sim select:focus { outline: 2px solid var(--accent); outline-offset: -1px; }
.sim input[type="number"] { width: 62px; }
.sim input[type="number"]:disabled {
  background: none; border-color: transparent; color: var(--ink-3); opacity: .45;
}
.sim-name { white-space: nowrap; }
.sim-name input { width: 92px; }
.sim-dot {
  display: inline-block; width: 9px; height: 9px; border-radius: 2px;
  margin-right: 7px; vertical-align: baseline;
}
.sim-ops { white-space: nowrap; }
.sim-ops button { padding: 2px 6px; margin-left: 3px; }
.sim-add { display: flex; align-items: baseline; gap: 12px; margin-top: 12px; flex-wrap: wrap; }
.sim-hint { font-size: 12.5px; color: var(--ink-3); line-height: 1.5; }
.sim-total {
  display: flex; align-items: center; gap: 12px;
  margin: 20px 0 14px; padding-top: 16px; border-top: 1px solid var(--rule);
}
.sim-total label {
  font-size: 10.5px; letter-spacing: .1em; text-transform: uppercase;
  color: var(--ink-3); white-space: nowrap;
}
.sim-total input[type="range"] { flex: 1; max-width: 320px; accent-color: var(--accent); }
.sim-tv {
  font-family: ui-monospace, Menlo, monospace; font-size: 15px;
  color: var(--accent); min-width: 2.4em;
}
.sim-bar {
  display: flex; height: 34px; border-radius: 4px; overflow: hidden;
  background: var(--sunken); gap: 1px;
}
.sim-seg {
  display: flex; align-items: center; justify-content: center;
  min-width: 0; transition: flex-grow .12s ease;
}
.sim-seg span {
  font-family: ui-monospace, Menlo, monospace; font-size: 11.5px;
  color: var(--ground); font-weight: 600;
}
.sim-empty {
  flex: 1; display: flex; align-items: center; justify-content: center;
  font-size: 12px; color: var(--ink-3);
}
.sim-keys { display: flex; flex-wrap: wrap; gap: 16px; margin-top: 10px; }
.sim-key { font-size: 13px; color: var(--ink-2); }
.sim-key b { color: var(--ink); font-family: ui-monospace, Menlo, monospace; }
.sim-pct { color: var(--ink-3); margin-left: 7px; font-size: 12px; }
.sim-notes { display: flex; gap: 10px; flex-wrap: wrap; margin-top: 10px; min-height: 1px; }
.sim-note {
  font-size: 11.5px; padding: 2px 8px; border-radius: 3px;
  font-family: ui-monospace, Menlo, monospace;
}
.sim-degraded { color: var(--detour); background: var(--detour-wash); }
.sim-short { color: var(--warn); background: rgba(160, 52, 40, .09); }
.sim-tracewrap { margin-top: 20px; padding-top: 4px; border-top: 1px solid var(--rule); }
.sim-tr td { font-family: ui-monospace, Menlo, monospace; font-size: 12.5px; }
.sim-tr td:first-child { color: var(--ink-3); }
.sim-flag { color: var(--detour); font-size: 11px !important; }
.sim-dot[data-c="0"], .sim-seg[data-c="0"] { background: var(--accent); }
.sim-dot[data-c="1"], .sim-seg[data-c="1"] { background: var(--go); }
.sim-dot[data-c="2"], .sim-seg[data-c="2"] { background: var(--sim-c); }
.sim-dot[data-c="3"], .sim-seg[data-c="3"] { background: var(--sim-d); }
.sim-dot[data-c="4"], .sim-seg[data-c="4"] { background: var(--sim-e); }

/* ---------- go note ---------- */
.gonote {
  margin: 26px 0; padding: 16px 20px 4px; background: var(--go-wash);
  border-left: 3px solid var(--go); border-radius: 0 5px 5px 0;
}
.gn-label, .dt-label {
  font-size: 10px; letter-spacing: .15em; text-transform: uppercase;
  margin-bottom: 8px;
}
.gn-label { color: var(--go); }
.gonote h4 { color: var(--go); }
.gonote figure.code { margin: 13px 0; }

/* ---------- detour ---------- */
.detour {
  margin: 26px 0; padding: 15px 20px 4px; background: var(--detour-wash);
  border-left: 3px solid var(--detour); border-radius: 0 5px 5px 0;
  font-size: 15.5px;
}
.dt-label { color: var(--detour); }
.detour a { color: var(--detour); }

/* ---------- tables ---------- */
.tw { overflow-x: auto; margin: 22px 0; }
table { border-collapse: collapse; width: 100%; font-size: 14px; }
th, td {
  text-align: left; padding: 8px 14px 8px 0;
  border-bottom: 1px solid var(--rule); vertical-align: top;
}
th {
  font-size: 10.5px; letter-spacing: .1em; text-transform: uppercase;
  color: var(--ink-3); font-weight: 500; white-space: nowrap;
}
td code { font-size: .84em; }
td:first-child { white-space: nowrap; }

/* ---------- rules ---------- */
hr.part {
  border: 0; border-top: 1px solid var(--rule); margin: 0 0 56px;
  max-width: 74ch;
}
.partmark {
  font-size: 10.5px; letter-spacing: .16em; text-transform: uppercase;
  color: var(--ink-3); margin-bottom: 14px;
  font-family: ui-monospace, Menlo, monospace;
}

@media (prefers-reduced-motion: reduce) {
  * { animation: none !important; transition: none !important; scroll-behavior: auto !important; }
}
"""

# --------------------------------------------------------------------------
# page skeleton
# --------------------------------------------------------------------------


def page(title, description, body, extra_css="", extra_js="", slug=None):
    """Wraps rendered fragments in the document skeleton.

    Every page this toolchain emits goes through here, which is what keeps a
    standalone simulator page and the same simulator embedded in the tutorial
    looking like one product. `extra_css` is appended after the shared sheet so
    a page can add to it without forking it.

    `slug` is the page's filename, and only matters once SITE is set: an
    absolute URL is the one thing a share card cannot be given from inside the
    document.
    """
    scripts = f"<script>{extra_js}</script>" if extra_js else ""

    # A card with a title and no image still renders; one that promises a large
    # image and has none renders worse than no card at all.
    social = [
        '<meta property="og:type" content="article">',
        f'<meta property="og:title" content="{html.escape(title)}">',
        f'<meta property="og:description" content="{html.escape(description)}">',
    ]
    if SITE and slug:
        stem = slug.rsplit(".", 1)[0]
        social += [
            f'<meta property="og:url" content="{SITE}/{slug}">',
            f'<meta property="og:image" content="{SITE}/og-{stem}.png">',
            '<meta name="twitter:card" content="summary_large_image">',
        ]
    else:
        social.append('<meta name="twitter:card" content="summary">')

    return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{html.escape(title)}</title>
<meta name="description" content="{html.escape(description)}">
{chr(10).join(social)}
<style>{CSS}{extra_css}</style>
</head>
<body>
{body}{scripts}</body>
</html>
"""


# One figure on its own page. The widget markup is the same string the tutorial
# embeds, so the two views cannot disagree about the figure -- only about what
# surrounds it.
FIGURE_CSS = """
.figpage { max-width:940px; margin:0 auto; padding:38px 24px 72px; }
.figpage .backlink { font:12px/1 var(--mono); letter-spacing:.06em;
  text-transform:uppercase; color:var(--faint); text-decoration:none; }
.figpage .backlink:hover { color:var(--accent); }
.figpage h1 { margin:18px 0 6px; }
.figpage .standfirst { color:var(--faint); margin:0 0 26px; max-width:62ch; }
.figpage footer { margin-top:44px; padding-top:18px;
  border-top:1px solid var(--edge); color:var(--faint); font-size:14px; }
"""


def figure_page(title, heading, description, standfirst_html, widget_html,
                extra_css="", extra_js="", back="tutorial-v2.html",
                back_label="PodPool tutorial", slug=None):
    """A standalone page for one interactive figure.

    The figure keeps its own stylesheet in `extra_css`; everything around it is
    the shared sheet, which is not merely cosmetic -- both simulators style
    themselves with this sheet's design tokens and cannot render without it.
    """
    body = f"""<div class="figpage">
<a class="backlink" href="{back}">&#8592; {html.escape(back_label)}</a>
<h1>{html.escape(heading)}</h1>
<p class="standfirst">{standfirst_html}</p>
{widget_html}
<footer>Generated from the <code>tutorial-v2</code> branch. The same figure is
embedded in <a href="{back}">{html.escape(back_label)}</a>.</footer>
</div>
"""

    return page(title, description, body, slug=slug,
                extra_css=FIGURE_CSS + extra_css, extra_js=extra_js)
