#!/usr/bin/env python3
"""Sim 1 — the distribution explorer, rebuilt for the Target API.

A pure function explorer over ComputeGroupTargets: no time, no cluster. The
reader edits groups and a total; the page shows the split, the per-phase
attribution, and the trace table the Go tests assert.

Follows the sim2 generator discipline: the algorithm port lives ONCE in
sim2_prototype.py and is imported here, the fixtures are read out of the Go
tests rather than restated, and they are emitted into the page where the
JavaScript port re-checks them at load and shows a pass/fail badge. A drifted
port is visible to the reader, not just to the build.

gofixtures parses the group specs and the scaling trace out of
internal/workload/algorithm_test.go, so a retuned percentage or a renamed
helper reaches this page as a build failure rather than as a stale row.

The same widget serves two views: build_tutorial_v2_doc.py embeds sim_html() at
M02, and build_page() puts it on a page of its own. Both take the identical
markup, so the two views cannot disagree about the figure.

Run:
    python3 sim1_distribution.py           # check ports, write the page
    python3 sim1_distribution.py --quiet   # skip the trace table
"""

import html
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import docshell  # noqa: E402
import gofixtures  # noqa: E402
from sim2_prototype import (  # noqa: E402
    compute_group_targets,
    group_ceiling,
    is_opportunistic,
)

# ---------------------------------------------------------------------------
# Shapes: the CEL rule made visible. Four combinations worth exploring, all of
# them legal -- not the whole legal set, which also admits min+max and a bare
# max. opportunistic pairs only with min, because it is itself the ceiling.
# ---------------------------------------------------------------------------

SHAPES = (
    ("min", "min"),
    ("min+target", "min + target"),
    ("max+target", "max + target"),
    ("opportunistic+min", "opportunistic + min"),
)

FIELDS = {
    "min": ("min",),
    "min+target": ("min", "target"),
    "max+target": ("max", "target"),
    "opportunistic+min": ("min",),
}

COLUMNS = ("min", "max", "target", "opportunistic")

DEFAULT_GROUPS = [
    {"name": "base", "shape": "min+target",
     "scaling": {"min": 3, "max": None, "target": "30%", "opportunistic": None}},
    {"name": "scav", "shape": "min+target",
     "scaling": {"min": 0, "max": None, "target": "50%", "opportunistic": None}},
    {"name": "burst", "shape": "min",
     "scaling": {"min": 0, "max": None, "target": None, "opportunistic": None}},
]

TRACE_TOTALS = (1, 3, 4, 5, 7, 10, 12, 15, 20, 25, 30)
DEFAULT_TOTAL = 10
MAX_GROUPS = 5
MAX_TOTAL = 40

# ---------------------------------------------------------------------------
# Fixtures — read out of the Go tests, never restated. Sources named per block.
# ---------------------------------------------------------------------------

_ALGO = gofixtures.read(gofixtures.ALGORITHM_TEST)

# TestComputeGroupTargetsScalingTrace: base(min:3) scav(min:0,target:30%)
# burst(min:0,target:50%). threeGroupSpec leaves its groups unnamed, so the
# names are this page's; every number is the test's.
TRACE_GROUPS = gofixtures.group_spec(_ALGO, "threeGroupSpec", ("base", "scav", "burst"))
TRACE_FIXTURE = gofixtures.scaling_trace(_ALGO)

# TestComputeGroupTargetsOpportunistic: base(min:3,target:35%) scav(opp)
# burst(min:0). Pins the phase ordering, which is the whole design.
THREE_TIER = gofixtures.group_spec(_ALGO, "threeTierSpec")
OPP_FIXTURE = gofixtures.opportunistic_table(_ALGO)

# TestComputeGroupTargetsCappedTrace: ceilings that cannot between them reach
# 100%, so some of every total goes unplaced. It is the one table the Go test
# computes rather than tabulates -- it sweeps 0..60 and asserts each group's
# floored share -- so five representative totals are chosen here and put
# through that same arithmetic, over the percentages parsed above. Retune
# either pctTarget in the Go and these rows move with it.
CAPPED_GROUPS = gofixtures.group_spec(_ALGO, "cappedGroupSpec", ("a", "b"))


def _capped_row(total):
    want = [total * int(g["scaling"]["target"].rstrip("%")) // 100
            for g in CAPPED_GROUPS]
    return (total, want, total - sum(want))


CAPPED_FIXTURE = [_capped_row(t) for t in (0, 7, 10, 33, 60)]


def check_ports():
    """Fail the build if the shared Python port disagrees with the fixtures."""
    for total, want in TRACE_FIXTURE:
        got = compute_group_targets(total, TRACE_GROUPS)["targets"]
        if got != want:
            sys.exit(f"sim1: trace drifted: total={total} got={got} want={want}")

    for total, want, unplaced in CAPPED_FIXTURE:
        r = compute_group_targets(total, CAPPED_GROUPS)
        if r["targets"] != want or r["unplaced"] != unplaced:
            sys.exit(f"sim1: capped trace drifted: total={total} "
                     f"got={r['targets']}/{r['unplaced']} want={want}/{unplaced}")

    for name, total, observed, want in OPP_FIXTURE:
        got = compute_group_targets(total, THREE_TIER, observed)["targets"]
        if got != want:
            sys.exit(f"sim1: opportunistic row drifted: {name}: got={got} want={want}")


def fixtures_json():
    return json.dumps({
        "trace": {"groups": TRACE_GROUPS,
                  "rows": [[t, w] for t, w in TRACE_FIXTURE]},
        "capped": {"groups": CAPPED_GROUPS,
                   "rows": [[t, w, u] for t, w, u in CAPPED_FIXTURE]},
        "opp": {"groups": THREE_TIER,
                "rows": [[t, o, w] for _, t, o, w in OPP_FIXTURE]},
    })


# ---------------------------------------------------------------------------
# Page fragment. Server-side render of the default state so the sim is
# meaningful without JavaScript; the JS re-renders on interaction.
# ---------------------------------------------------------------------------

def _rows(groups):
    out = []
    for i, g in enumerate(groups):
        opts = "".join(
            f'<option value="{v}"{" selected" if g["shape"] == v else ""}>{html.escape(lbl)}</option>'
            for v, lbl in SHAPES)
        cells = []
        for f in COLUMNS:
            active = f in FIELDS[g["shape"]]
            val = g["scaling"].get(f)
            if f == "opportunistic":
                mark = "&#10003;" if g["shape"] == "opportunistic+min" else ""
                cells.append(f'<td class="s1-n s1-opp">{mark}</td>')
            elif f == "target":
                cells.append(
                    f'<td class="s1-n"><input type="text" data-field="target" '
                    f'value="{html.escape(val or "", quote=True)}" size="5" maxlength="4" '
                    f'placeholder="30%"{"" if active else " disabled"} '
                    f'aria-label="target, group {i + 1}"></td>')
            else:
                cells.append(
                    f'<td class="s1-n"><input type="number" min="0" step="1" '
                    f'data-field="{f}" value="{"" if val is None else val}"'
                    f'{"" if active else " disabled"} '
                    f'aria-label="{f}, group {i + 1}"></td>')
        out.append(
            f'<tr data-i="{i}">'
            f'<td class="s1-name"><span class="s1-dot" data-c="{i % 5}"></span>'
            f'<input type="text" data-field="name" value="{html.escape(g["name"], quote=True)}" '
            f'maxlength="20" aria-label="name, group {i + 1}"></td>'
            f'<td class="s1-shape"><select data-field="shape" '
            f'aria-label="constraints, group {i + 1}">{opts}</select></td>'
            + "".join(cells)
            + f'<td class="s1-ops">'
            f'<button type="button" data-op="up"{" disabled" if i == 0 else ""} '
            f'aria-label="move group {i + 1} earlier">&uarr;</button>'
            f'<button type="button" data-op="del"{" disabled" if len(groups) == 1 else ""} '
            f'aria-label="remove group {i + 1}">&times;</button>'
            f'</td></tr>')
    return "".join(out)


def _split(groups, total):
    r = compute_group_targets(total, [
        {"name": g["name"], "scaling": {k: v for k, v in g["scaling"].items() if v is not None}}
        for g in groups])
    targets = r["targets"]
    placed = sum(targets)

    if placed <= 0:
        bar = '<div class="s1-bar"><div class="s1-empty">nothing to place</div></div>'
    else:
        segs = []
        for i, t in enumerate(targets):
            if t <= 0:
                continue
            opp = " s1-offered" if groups[i]["shape"] == "opportunistic+min" else ""
            segs.append(f'<div class="s1-seg{opp}" data-c="{i % 5}" style="flex:{t}">'
                        f"<span>{t}</span></div>")
        bar = '<div class="s1-bar">' + "".join(segs) + "</div>"

    keys = []
    for i, g in enumerate(groups):
        extra = ""
        if g["shape"] == "opportunistic+min":
            extra = ('<span class="s1-offer">offered &mdash; the scheduler decides '
                     "what sticks (Sim&nbsp;2)</span>")
        keys.append(
            f'<span class="s1-key"><span class="s1-dot" data-c="{i % 5}"></span>'
            f'{html.escape(g["name"])} <b>{targets[i]}</b>{extra}</span>')

    notes = []
    if r["unplaced"] > 0:
        notes.append(f'<span class="s1-note s1-short">{r["unplaced"]} unplaced &mdash; '
                     "no ceiling accepts them</span>")
    if r["degraded"]:
        notes.append('<span class="s1-note s1-degraded">TargetDegraded</span>')

    return (f'<div class="s1-out">{bar}<div class="s1-keys">{"".join(keys)}</div>'
            f'<div class="s1-notes">{"".join(notes)}</div></div>')


def sim_html():
    g = DEFAULT_GROUPS
    return (
        f'<div class="sim1" data-default="{html.escape(json.dumps(g), quote=True)}" '
        f'data-totals="{html.escape(json.dumps(list(TRACE_TOTALS)), quote=True)}" '
        f'data-maxgroups="{MAX_GROUPS}">'
        f'<div class="s1-head"><span class="s1-label">Simulator</span>'
        f'<span id="s1-badge" class="s1-badge">checking port&hellip;</span>'
        f'<button type="button" class="s1-reset">reset</button></div>'
        f'<div class="tw"><table class="s1-in"><thead><tr>'
        f"<th>group</th><th>constraints</th>"
        + "".join(f"<th><code>{c}</code></th>" for c in COLUMNS)
        + f'<th></th></tr></thead><tbody class="s1-body">{_rows(g)}</tbody></table></div>'
        f'<div class="s1-add"><button type="button" class="s1-addbtn">+ group</button>'
        f'<span class="s1-hint">An empty box is an unset pointer, exactly as in Go. '
        f"The shape selector offers only the combinations the CEL rules admit.</span></div>"
        f'<div class="s1-total"><label for="s1-total">total replicas</label>'
        f'<input type="range" id="s1-total" min="0" max="{MAX_TOTAL}" '
        f'value="{DEFAULT_TOTAL}"><output class="s1-tv">{DEFAULT_TOTAL}</output></div>'
        f'{_split(g, DEFAULT_TOTAL)}'
        f'<div class="s1-tracewrap"></div>'
        f"</div>")


CSS = """
.sim1 { border:1px solid var(--edge); border-radius:8px; padding:16px 18px;
  margin:22px 0; background:var(--wash); }
.s1-head { display:flex; align-items:center; gap:10px; margin-bottom:10px; }
.s1-label { font:600 12px/1 var(--mono); letter-spacing:.12em;
  text-transform:uppercase; color:var(--accent); }
.s1-badge { font:11px/1 var(--mono); padding:3px 8px; border-radius:99px;
  border:1px solid var(--edge); color:var(--faint); }
.s1-badge.ok { color:var(--accent); border-color:var(--accent); }
.s1-badge.bad { color:#b4232a; border-color:#b4232a; font-weight:700; }
.s1-reset { margin-left:auto; font:11px var(--mono); padding:3px 10px;
  border:1px solid var(--edge); background:transparent; border-radius:5px;
  cursor:pointer; color:var(--faint); }
.s1-in input, .s1-in select { font:12px var(--mono); padding:3px 5px;
  border:1px solid var(--edge); border-radius:4px; background:var(--surface);
  color:inherit; }
.s1-in input[type=number] { width:4.5em; }
.s1-in input[disabled] { opacity:.3; }
.s1-n, .s1-opp { text-align:center; }
.s1-opp { color:var(--accent); font-weight:700; }
.s1-dot { display:inline-block; width:9px; height:9px; border-radius:99px;
  margin-right:6px; vertical-align:baseline; }
.s1-dot[data-c="0"], .s1-seg[data-c="0"] { background:#3f7fbf; }
.s1-dot[data-c="1"], .s1-seg[data-c="1"] { background:#2fa4a0; }
.s1-dot[data-c="2"], .s1-seg[data-c="2"] { background:#d98032; }
.s1-dot[data-c="3"], .s1-seg[data-c="3"] { background:#8f6bb8; }
.s1-dot[data-c="4"], .s1-seg[data-c="4"] { background:#7c8a4f; }
.s1-ops button { font:12px var(--mono); border:1px solid var(--edge);
  background:transparent; border-radius:4px; cursor:pointer; padding:1px 7px;
  color:var(--faint); }
.s1-ops button[disabled] { opacity:.3; cursor:default; }
.s1-add { display:flex; align-items:baseline; gap:12px; margin:8px 0 14px; }
.s1-addbtn { font:12px var(--mono); padding:3px 10px; border:1px dashed var(--edge);
  background:transparent; border-radius:5px; cursor:pointer; color:var(--faint); }
.s1-hint { font-size:12.5px; color:var(--faint); }
.s1-total { display:flex; align-items:center; gap:12px; margin:10px 0; }
.s1-total label { font:600 12px var(--mono); color:var(--faint); }
.s1-total input[type=range] { flex:1; }
.s1-tv { font:700 14px var(--mono); min-width:2ch; }
.s1-bar { display:flex; height:38px; border-radius:6px; overflow:hidden;
  border:1px solid var(--edge); }
.s1-seg { display:flex; align-items:center; justify-content:center;
  color:#fff; font:700 12px var(--mono); min-width:1.4em; }
.s1-seg.s1-offered { background-image:repeating-linear-gradient(45deg,
  rgba(255,255,255,.28) 0 6px, transparent 6px 12px); }
.s1-empty { margin:auto; font:12px var(--mono); color:var(--faint); }
.s1-keys { display:flex; flex-wrap:wrap; gap:8px 18px; margin-top:8px; }
.s1-key { font:12.5px var(--mono); }
.s1-key b { font-size:14px; }
.s1-offer { display:block; font-size:11px; color:var(--faint); }
.s1-notes { margin-top:6px; display:flex; gap:12px; }
.s1-note { font:12px var(--mono); padding:2px 8px; border-radius:4px; }
.s1-short { background:#fdf0e0; color:#8a4b12; }
.s1-degraded { background:#f6e2e2; color:#8a1f1f; }
.s1-tr td, .s1-tr th { text-align:right; font:12.5px var(--mono); }
.s1-tr td:first-child, .s1-tr th:first-child { text-align:left; }
.s1-flag { color:#8a1f1f; }
@media (prefers-color-scheme: dark) {
  .s1-short { background:#3a2a14; color:#e8b478; }
  .s1-degraded { background:#3a1c1c; color:#e89a9a; }
}
"""


JS = r"""
(function () {
  var FIX = __SIM1_FIXTURES__;
  var root = document.querySelector('.sim1');
  if (!root) return;

  // ---- port of internal/workload/algorithm.go (kept in step with the
  // Python port in sim2_prototype.py; both are checked against the same rows,
  // which reach this side as FIX) ----
  function targetPercent(s) {
    var t = s.target;
    if (t == null || typeof t !== 'string' || !/%$/.test(t)) return null;
    var pct = parseInt(t.slice(0, -1), 10);
    if (isNaN(pct) || pct < 1 || pct > 100) return null;
    return pct;
  }
  function groupTarget(total, s) {
    var pct = targetPercent(s);
    if (pct == null) return null;
    if (s.max != null) return Math.min(Math.ceil(total * pct / 100), s.max);
    return Math.floor(total * pct / 100);
  }
  function isOpp(s) { return s.opportunistic === true; }
  function hasStaticCeiling(s) { return s.max != null || s.target != null; }
  function groupCeiling(total, s) {
    if (!hasStaticCeiling(s)) return [0, false];
    if (s.max != null) return [s.max, true];
    var pct = targetPercent(s);
    if (pct == null) return [0, true];
    return [Math.floor(total * pct / 100), true];
  }
  function checkDegraded(total, targets, groups) {
    if (total <= 0) return false;
    for (var i = 0; i < groups.length; i++) {
      var s = groups[i].scaling, pct = targetPercent(s);
      if (pct == null) continue;
      var actual = targets[i] / total * 100;
      if (s.max == null && actual > pct + 0.5) return true;
      if (s.max != null && actual < pct - 0.5) return true;
    }
    return false;
  }
  function compute(total, groups, observed) {
    observed = observed || {};
    var n = groups.length, targets = [], i, g, give;
    for (i = 0; i < n; i++) targets.push(0);
    if (n === 0 || total <= 0)
      return { targets: targets, degraded: false, unplaced: 0 };
    var remaining = total;
    for (i = 0; i < n; i++) {
      var floor = groups[i].scaling.min != null ? groups[i].scaling.min : 0;
      if (floor > 0) { var t = Math.min(floor, remaining); targets[i] = t; remaining -= t; }
    }
    if (remaining <= 0)
      return { targets: targets, degraded: checkDegraded(total, targets, groups), unplaced: 0 };
    for (i = 0; i < n; i++) {
      var want = groupTarget(total, groups[i].scaling);
      if (want == null) continue;
      var add = want - targets[i];
      if (add <= 0) continue;
      give = Math.min(add, remaining); targets[i] += give; remaining -= give;
      if (remaining <= 0) break;
    }
    for (i = 0; i < n; i++) {
      if (remaining <= 0) break;
      g = groups[i];
      if (!isOpp(g.scaling)) continue;
      var cap = (g.name in observed) ? observed[g.name] : remaining;
      var add3 = cap - targets[i];
      if (add3 <= 0) continue;
      give = Math.min(add3, remaining); targets[i] += give; remaining -= give;
    }
    for (i = 0; i < n; i++) {
      if (remaining <= 0) break;
      if (isOpp(groups[i].scaling)) continue;
      var cb = groupCeiling(total, groups[i].scaling);
      if (!cb[1]) { targets[i] += remaining; remaining = 0; continue; }
      var head = cb[0] - targets[i];
      if (head <= 0) continue;
      give = Math.min(head, remaining); targets[i] += give; remaining -= give;
    }
    return { targets: targets, degraded: checkDegraded(total, targets, groups),
             unplaced: remaining };
  }

  // ---- self-check against the emitted fixtures ----
  function same(a, b) { return JSON.stringify(a) === JSON.stringify(b); }
  function checkPort() {
    var fails = 0;
    FIX.trace.rows.forEach(function (row) {
      if (!same(compute(row[0], FIX.trace.groups).targets, row[1])) fails++;
    });
    FIX.capped.rows.forEach(function (row) {
      var r = compute(row[0], FIX.capped.groups);
      if (!same(r.targets, row[1]) || r.unplaced !== row[2]) fails++;
    });
    FIX.opp.rows.forEach(function (row) {
      var obs = row[1] || {};
      if (!same(compute(row[0], FIX.opp.groups, obs).targets, row[2])) fails++;
    });
    return fails;
  }
  var badge = document.getElementById('s1-badge');
  var fails = checkPort();
  if (fails) {
    badge.className = 's1-badge bad';
    badge.textContent = 'port drifted: ' + fails + ' fixture rows fail';
  } else {
    badge.className = 's1-badge ok';
    badge.textContent = 'port verified against ' +
      (FIX.trace.rows.length + FIX.capped.rows.length + FIX.opp.rows.length) +
      ' Go test rows';
  }

  // ---- interactive state ----
  var SHAPES = { 'min': ['min'], 'min+target': ['min', 'target'],
                 'max+target': ['max', 'target'], 'opportunistic+min': ['min'] };
  var COLS = ['min', 'max', 'target', 'opportunistic'];
  var state = JSON.parse(root.dataset.default);
  var totals = JSON.parse(root.dataset.totals);
  var maxGroups = parseInt(root.dataset.maxgroups, 10);
  var total = parseInt(root.querySelector('#s1-total').value, 10);

  function scaling(g) {
    var active = SHAPES[g.shape], out = {};
    active.forEach(function (f) { if (g.scaling[f] != null) out[f] = g.scaling[f]; });
    if (g.shape === 'opportunistic+min') out.opportunistic = true;
    return out;
  }
  function groupsForCompute() {
    return state.map(function (g) { return { name: g.name, scaling: scaling(g) }; });
  }
  function esc(s) {
    return String(s).replace(/[&<>"]/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c];
    });
  }

  function renderRows() {
    var body = root.querySelector('.s1-body');
    body.innerHTML = state.map(function (g, i) {
      var opts = [['min', 'min'], ['min+target', 'min + target'],
                  ['max+target', 'max + target'],
                  ['opportunistic+min', 'opportunistic + min']]
        .map(function (o) {
          return '<option value="' + o[0] + '"' +
            (g.shape === o[0] ? ' selected' : '') + '>' + o[1] + '</option>';
        }).join('');
      var cells = COLS.map(function (f) {
        var active = SHAPES[g.shape].indexOf(f) >= 0;
        if (f === 'opportunistic') {
          return '<td class="s1-n s1-opp">' +
            (g.shape === 'opportunistic+min' ? '&#10003;' : '') + '</td>';
        }
        if (f === 'target') {
          return '<td class="s1-n"><input type="text" data-field="target" value="' +
            esc(g.scaling.target || '') + '" size="5" maxlength="4" placeholder="30%"' +
            (active ? '' : ' disabled') + ' aria-label="target, group ' + (i + 1) + '"></td>';
        }
        var v = g.scaling[f];
        return '<td class="s1-n"><input type="number" min="0" step="1" data-field="' + f +
          '" value="' + (v == null ? '' : v) + '"' + (active ? '' : ' disabled') +
          ' aria-label="' + f + ', group ' + (i + 1) + '"></td>';
      }).join('');
      return '<tr data-i="' + i + '">' +
        '<td class="s1-name"><span class="s1-dot" data-c="' + (i % 5) + '"></span>' +
        '<input type="text" data-field="name" value="' + esc(g.name) +
        '" maxlength="20" aria-label="name, group ' + (i + 1) + '"></td>' +
        '<td class="s1-shape"><select data-field="shape" aria-label="constraints, group ' +
        (i + 1) + '">' + opts + '</select></td>' + cells +
        '<td class="s1-ops"><button type="button" data-op="up"' +
        (i === 0 ? ' disabled' : '') + ' aria-label="move group ' + (i + 1) +
        ' earlier">&uarr;</button><button type="button" data-op="del"' +
        (state.length === 1 ? ' disabled' : '') + ' aria-label="remove group ' +
        (i + 1) + '">&times;</button></td></tr>';
    }).join('');
  }

  function renderOut() {
    var groups = groupsForCompute();
    var r = compute(total, groups);
    var placed = r.targets.reduce(function (a, b) { return a + b; }, 0);
    var bar;
    if (placed <= 0) {
      bar = '<div class="s1-bar"><div class="s1-empty">nothing to place</div></div>';
    } else {
      bar = '<div class="s1-bar">' + state.map(function (g, i) {
        var t = r.targets[i];
        if (t <= 0) return '';
        var opp = g.shape === 'opportunistic+min' ? ' s1-offered' : '';
        return '<div class="s1-seg' + opp + '" data-c="' + (i % 5) +
          '" style="flex:' + t + '"><span>' + t + '</span></div>';
      }).join('') + '</div>';
    }
    var keys = state.map(function (g, i) {
      var extra = g.shape === 'opportunistic+min'
        ? '<span class="s1-offer">offered &mdash; the scheduler decides what sticks (Sim&nbsp;2)</span>'
        : '';
      return '<span class="s1-key"><span class="s1-dot" data-c="' + (i % 5) + '"></span>' +
        esc(g.name) + ' <b>' + r.targets[i] + '</b>' + extra + '</span>';
    }).join('');
    var notes = '';
    if (r.unplaced > 0)
      notes += '<span class="s1-note s1-short">' + r.unplaced +
        ' unplaced &mdash; no ceiling accepts them</span>';
    if (r.degraded)
      notes += '<span class="s1-note s1-degraded">TargetDegraded</span>';
    root.querySelector('.s1-out').innerHTML =
      bar + '<div class="s1-keys">' + keys + '</div>' +
      '<div class="s1-notes">' + notes + '</div>';

    var head = state.map(function (g) { return '<th>' + esc(g.name) + '</th>'; }).join('');
    var rows = totals.map(function (tt) {
      var rr = compute(tt, groups);
      return '<tr><td>' + tt + '</td>' +
        rr.targets.map(function (v) { return '<td>' + v + '</td>'; }).join('') +
        (rr.degraded ? '<td class="s1-flag">degraded</td>' : '<td></td>') + '</tr>';
    }).join('');
    root.querySelector('.s1-tracewrap').innerHTML =
      '<div class="tw"><table class="s1-tr"><thead><tr><th>total</th>' + head +
      '<th></th></tr></thead><tbody>' + rows + '</tbody></table></div>';
  }

  function renderAll() { renderRows(); renderOut(); }

  root.addEventListener('input', function (e) {
    var tr = e.target.closest('tr[data-i]');
    if (e.target.id === 's1-total') {
      total = parseInt(e.target.value, 10);
      root.querySelector('.s1-tv').textContent = total;
      renderOut();
      return;
    }
    if (!tr) return;
    var g = state[parseInt(tr.dataset.i, 10)];
    var f = e.target.dataset.field;
    if (!f) return;
    if (f === 'name') { g.name = e.target.value || 'group'; renderOut(); return; }
    if (f === 'shape') { g.shape = e.target.value; renderAll(); return; }
    if (f === 'target') {
      g.scaling.target = e.target.value.trim() || null; renderOut(); return;
    }
    g.scaling[f] = e.target.value === '' ? null : parseInt(e.target.value, 10);
    renderOut();
  });

  root.addEventListener('click', function (e) {
    var b = e.target.closest('button');
    if (!b) return;
    if (b.classList.contains('s1-reset')) {
      state = JSON.parse(root.dataset.default);
      total = 10; root.querySelector('#s1-total').value = 10;
      root.querySelector('.s1-tv').textContent = 10;
      renderAll(); return;
    }
    if (b.classList.contains('s1-addbtn')) {
      if (state.length >= maxGroups) return;
      state.push({ name: 'group' + (state.length + 1), shape: 'min',
                   scaling: { min: 0, max: null, target: null, opportunistic: null } });
      renderAll(); return;
    }
    var tr = e.target.closest('tr[data-i]');
    if (!tr) return;
    var i = parseInt(tr.dataset.i, 10);
    if (b.dataset.op === 'del' && state.length > 1) { state.splice(i, 1); renderAll(); }
    if (b.dataset.op === 'up' && i > 0) {
      var tmp = state[i - 1]; state[i - 1] = state[i]; state[i] = tmp; renderAll();
    }
  });

  renderOut();
})();
"""


def sim_js():
    return JS.replace("__SIM1_FIXTURES__", fixtures_json())


STANDFIRST = (
    "One call to the distributor, with every constraint combination the CRD "
    "admits. Set a pool size and each group's <code>(min, target, max)</code> "
    "triple and read back the split the controller would compute. The "
    "JavaScript is a transliteration of "
    "<code>workload.ComputeGroupTargets</code>, checked against the Go tests' "
    "own fixture rows every time this page loads &#8212; the badge says so. "
    "There is no clock here: a walk-up, a backoff and a runaway are properties "
    "of a <em>sequence</em>, which is what Sim 2 is for."
)


def build_page():
    return docshell.figure_page(
        title="Replica distribution — PodPool Sim 1",
        heading="Replica distribution",
        description="Explore how a PodPool splits its replicas across groups "
                    "under min, target and max constraints.",
        standfirst_html=STANDFIRST,
        widget_html=sim_html(),
        slug="sim1-distribution.html",
        extra_css=CSS,
        extra_js=sim_js(),
    )


def main():
    quiet = "--quiet" in sys.argv[1:]

    check_ports()
    rows = len(TRACE_FIXTURE) + len(CAPPED_FIXTURE) + len(OPP_FIXTURE)
    print(f"sim1: Python port verified against {rows} fixture rows")

    if not quiet:
        groups = TRACE_GROUPS
        print("\ntotal  " + "  ".join(g["name"] for g in groups))
        for total, want in TRACE_FIXTURE:
            got = compute_group_targets(total, groups)["targets"]
            print(f"{total:>5}  " + "  ".join(f"{t:>4}" for t in got)
                  + ("" if got == want else "  DRIFTED"))
        print()

    out = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                       "sim1-distribution.html")
    with open(out, "w", encoding="utf-8") as fh:
        fh.write(build_page())
    print(f"wrote {out}")


if __name__ == "__main__":
    main()
