"""HTML/CSS/JS for the Sim 2 prototype page.

Split out of sim2_prototype.py so the port and the fixtures stay readable. The
JavaScript below is a transliteration of the Python port in that module — same
phases, same order, same early exits — and checks itself at load against the
fixtures Python emits, so a drifted port shows up on the page.
"""

import json
import re

CSS = """
:root {
  --ground:#F2F4F6; --surface:#FFFFFF; --sunken:#E9ECEF; --ink:#161B1F; --ink-2:#545F6B;
  --ink-3:#7C8894; --rule:#D8DDE3; --accent:#0F5F6E; --warn:#A03428; --code-bg:#EDF0F3;
  --accent-wash:rgba(15,95,110,.06); --warn-wash:rgba(160,52,40,.09);
}
@media (prefers-color-scheme: dark) {
  :root:where(:not([data-theme="light"])) {
    --ground:#0F1318; --surface:#161C23; --sunken:#1B222B; --ink:#E3E9EF; --ink-2:#9BA7B4;
    --ink-3:#6E7B89; --rule:#262F3A; --accent:#52BFD2; --warn:#E8796B; --code-bg:#10161D;
    --accent-wash:rgba(82,191,210,.08); --warn-wash:rgba(232,121,107,.12);
  }
}
:root[data-theme="light"] {
  --ground:#F2F4F6; --surface:#FFFFFF; --sunken:#E9ECEF; --ink:#161B1F; --ink-2:#545F6B;
  --ink-3:#7C8894; --rule:#D8DDE3; --accent:#0F5F6E; --warn:#A03428; --code-bg:#EDF0F3;
  --accent-wash:rgba(15,95,110,.06); --warn-wash:rgba(160,52,40,.09);
}
:root[data-theme="dark"] {
  --ground:#0F1318; --surface:#161C23; --sunken:#1B222B; --ink:#E3E9EF; --ink-2:#9BA7B4;
  --ink-3:#6E7B89; --rule:#262F3A; --accent:#52BFD2; --warn:#E8796B; --code-bg:#10161D;
  --accent-wash:rgba(82,191,210,.08); --warn-wash:rgba(232,121,107,.12);
}

/* ---------------------------------------------------------------------
   Series palette.

   The tutorial's own tokens are PROSE ACCENTS, not a categorical series --
   fed to the validator, teal/green/amber/purple fails in both modes (light:
   normal-vision dE 7.6 between scav and base, below the 15 floor; dark: 9.0,
   plus lightness-band and chroma failures). Teal and green are adjacent hues
   that were never meant to be told apart.

   These three are validated slots, all-pairs, both modes:
     light  worst CVD dE 9.2 (deutan), worst normal-vision dE 24.0
     dark   worst CVD dE 9.4 (deutan), worst normal-vision dE 20.9
   Do not add a fourth: violet as slot 4 fails dark (dE 1.9 protan vs blue),
   which is why the probe is drawn in scav's hue with texture instead.

   aqua is 2.74:1 on the light surface -- under 3:1, so the relief rule
   applies: marks carry visible labels, and the tape and group tables are the
   table-view twin.
   --------------------------------------------------------------------- */
:root { --s-base:#2a78d6; --s-scav:#1baf7a; --s-burst:#eb6834; }
@media (prefers-color-scheme: dark) {
  :root:where(:not([data-theme="light"])) {
    --s-base:#3987e5; --s-scav:#199e70; --s-burst:#d95926;
  }
}
:root[data-theme="light"] { --s-base:#2a78d6; --s-scav:#1baf7a; --s-burst:#eb6834; }
:root[data-theme="dark"]  { --s-base:#3987e5; --s-scav:#199e70; --s-burst:#d95926; }

* { box-sizing: border-box; }
body {
  margin:0; padding:34px 22px 60px; background:var(--ground); color:var(--ink);
  font-family: Charter, "Bitstream Charter", "Sitka Text", Cambria, Georgia, serif;
  font-size:17px; line-height:1.68; -webkit-font-smoothing:antialiased;
}
.wrap { max-width:940px; margin:0 auto; }
.sim2 .s2-h { font: 600 15px/1.3 var(--ui, inherit); margin:26px 0 6px; color:var(--ink); }
h1 { font-size:25px; line-height:1.3; margin:0 0 6px; letter-spacing:-.01em; }
h2 { font-size:16px; margin:30px 0 8px; }
.sub { color:var(--ink-2); font-size:15.5px; margin:0 0 4px; }
p.lede { font-size:15px; color:var(--ink-2); margin:0 0 14px; }
code { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
       font-size:.88em; background:var(--code-bg); padding:1px 4px; border-radius:3px; }

.badge { display:inline-flex; align-items:center; gap:8px; margin:14px 0 26px;
  font-family: ui-monospace, Menlo, monospace; font-size:11.5px;
  padding:5px 11px; border-radius:4px; border:1px solid; }
.badge.ok  { color:var(--s-scav); border-color:var(--s-scav); }
.badge.bad { color:var(--warn); border-color:var(--warn); background:var(--warn-wash); }

.sim { margin:0 0 26px; padding:16px 18px 18px; background:var(--surface);
       border:1px solid var(--rule); border-radius:5px; }
.sim-head { display:flex; align-items:center; justify-content:space-between; gap:12px; flex-wrap:wrap; }
.sim-label { font-size:10px; letter-spacing:.15em; text-transform:uppercase; color:var(--accent);
             font-family: ui-monospace, Menlo, monospace; }
.sim button { font-family: ui-monospace, Menlo, monospace; font-size:11.5px;
  color:var(--ink-3); background:none; border:1px solid var(--rule);
  border-radius:3px; padding:4px 9px; cursor:pointer; }
.sim button:hover { color:var(--accent); border-color:var(--accent); }
.sim button.primary { color:var(--accent); border-color:var(--accent); }
.sim button:disabled { opacity:.4; cursor:default; }
.sim button:disabled:hover { color:var(--ink-3); border-color:var(--rule); }

.row { display:flex; align-items:center; gap:12px; margin:16px 0 0; flex-wrap:wrap; }
.row label { font-size:10.5px; letter-spacing:.1em; text-transform:uppercase;
             color:var(--ink-3); white-space:nowrap; }
.row input[type=range] { flex:1; min-width:150px; max-width:260px; accent-color:var(--accent); }
.val { font-family: ui-monospace, Menlo, monospace; font-size:15px; color:var(--accent); min-width:2.4em; }
.hint { font-size:12.5px; color:var(--ink-3); }

.sep { margin:18px 0 0; padding-top:16px; border-top:1px solid var(--rule); }
.grp { display:flex; gap:8px; flex-wrap:wrap; align-items:center; }
.grp .lead { font-size:12.5px; color:var(--ink-3); margin-right:2px; }
.counter { font-family: ui-monospace, Menlo, monospace; font-size:11.5px; color:var(--ink-3);
           margin-right:4px; white-space:nowrap; }
.counter b { color:var(--accent); font-size:14px; font-weight:600; }

/* 2px surface gap between fills, never a border to separate them. */
.bar { display:flex; height:36px; border-radius:4px; overflow:hidden;
       background:var(--sunken); gap:2px; margin-top:14px; }
.seg { display:flex; align-items:center; justify-content:center; min-width:0;
       transition:flex-grow .14s ease; }
.seg span { font-family: ui-monospace, Menlo, monospace; font-size:11.5px; color:#fff; font-weight:600; }
.seg[data-c="0"] { background:var(--s-base); }
.seg[data-c="1"] { background:var(--s-scav); }
.seg[data-c="2"] { background:var(--s-burst); }
/* Pending pods and the probe are both scav -- same hue, because identity is the
   group. They differ by TEXTURE and LABEL, not colour: pending is a hatched fill
   (pods that exist and were refused), the probe is an outline (a question, not
   yet a pod). A fourth hue here fails the validator in dark mode. */
.seg.pending { background: repeating-linear-gradient(45deg, var(--s-scav), var(--s-scav) 3px, transparent 3px, transparent 7px); }
.seg.pending span { color:var(--s-scav); }
/* The probe is an annotation, not part of the proportional total, so flooring
   its width costs no encoding accuracy and keeps the ring legible. */
.seg.probe { background:none; box-shadow: inset 0 0 0 2px var(--s-scav); min-width:28px; }
.seg.probe span { color:var(--s-scav); }

.keys { display:flex; flex-wrap:wrap; gap:16px; margin-top:10px; }
.key { font-size:13px; color:var(--ink-2); }
.key b { color:var(--ink); font-family: ui-monospace, Menlo, monospace; }
.dot { display:inline-block; width:9px; height:9px; border-radius:2px; margin-right:6px; }
.dot[data-c="0"]{background:var(--s-base)} .dot[data-c="1"]{background:var(--s-scav)} .dot[data-c="2"]{background:var(--s-burst)}
.dot.probe { background:none; box-shadow: inset 0 0 0 2px var(--s-scav); }
.dot.pending { background: repeating-linear-gradient(45deg, var(--s-scav), var(--s-scav) 2px, transparent 2px, transparent 4px); }
.dot.ghost { background:var(--ink-3); opacity:.55; }

/* charts */
.chartwrap { position:relative; margin-top:6px; }
.chartwrap svg { display:block; width:100%; height:auto; }
.gridline { stroke:var(--rule); stroke-width:1; }
.axtext { fill:var(--ink-3); font-family: ui-monospace, Menlo, monospace; font-size:9.5px; }
.panel-t { fill:var(--ink-3); font-family: ui-monospace, Menlo, monospace;
           font-size:9.5px; letter-spacing:.09em; text-transform:uppercase; }
.ln { fill:none; stroke-width:2; stroke-linejoin:round; stroke-linecap:round; }
.ln.ghost { stroke:var(--ink-3); stroke-width:2; opacity:.42; }
/* Event markers: solid hairline, same weight as the grid, so the stimulus sits
   in the same picture as the response without competing with the data. */
.evt-rule { stroke:var(--accent); stroke-width:1; opacity:.55; }
.evt-lab { fill:var(--accent); font-family: ui-monospace, Menlo, monospace; font-size:9px; }
.endlab { font-family: ui-monospace, Menlo, monospace; font-size:10px; font-weight:600; }
.cursor { stroke:var(--ink-3); stroke-width:1; opacity:.8; }
.band { font-family: ui-monospace, Menlo, monospace; font-size:9px; }
.tip { position:absolute; pointer-events:none; opacity:0; transition:opacity .1s;
  background:var(--surface); border:1px solid var(--rule); border-radius:4px;
  padding:7px 9px; font-family: ui-monospace, Menlo, monospace; font-size:11px;
  color:var(--ink); box-shadow:0 3px 12px rgba(0,0,0,.13); white-space:nowrap; z-index:5; }
/* Values lead, labels follow: here the reader already has the series and wants
   the number. Keys are short strokes, not filled boxes -- at tooltip density a
   filled box is data-weight ink doing a label's job. */
.tip .th { color:var(--ink-3); }
.tip .tr { display:flex; align-items:baseline; gap:7px; margin-top:3px; }
.tip .tk { display:inline-block; width:12px; height:2px; border-radius:1px; flex:none;
           align-self:center; }
.tip .tv { color:var(--ink); font-weight:600; font-variant-numeric:tabular-nums;
           min-width:2.4em; text-align:right; }
.tip .tl { color:var(--ink-3); }

/* presets */
.presets { display:flex; gap:8px; flex-wrap:wrap; margin-top:12px; }
.presets button.on { color:var(--accent); border-color:var(--accent); }
.preset-blurb { font-size:13px; color:var(--ink-2); margin-top:10px; line-height:1.55; min-height:1px; }

/* focus & hover: the mark responds, and focus reaches everything hover does */
.seg:hover, .seg:focus-visible { filter:brightness(1.12); }
.seg:focus-visible { outline:none; box-shadow: inset 0 0 0 2px var(--ink); }
.seg.probe:focus-visible { box-shadow: inset 0 0 0 2px var(--ink); }
.chartwrap svg:focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
@media (prefers-reduced-motion: reduce) {
  .seg, .tip { transition:none; }
}

/* group configuration.
   Wide content scrolls inside its own container -- the page body must never
   scroll horizontally. */
.cfgwrap { overflow-x:auto; margin-top:12px; }
table.cfg { border-collapse:collapse; width:100%; min-width:640px; font-size:13px; }
table.cfg th { text-align:right; padding:5px 9px; font-size:10px; letter-spacing:.09em;
  text-transform:uppercase; color:var(--ink-3); font-weight:normal;
  border-bottom:1px solid var(--rule); white-space:nowrap; }
table.cfg th:first-child, table.cfg td:first-child { text-align:left; }
table.cfg th.ph { color:var(--accent); }
table.cfg td { text-align:right; padding:6px 9px; border-bottom:1px solid var(--rule);
  font-family: ui-monospace, Menlo, monospace; font-size:12.5px; white-space:nowrap; }
table.cfg tr:last-child td { border-bottom:none; }
table.cfg td.cons { text-align:left; color:var(--ink-2); }
table.cfg td.zero { color:var(--ink-3); opacity:.45; }
table.cfg td.tot { color:var(--ink); font-weight:600; }
table.cfg td.na { color:var(--ink-3); opacity:.3; }
.cfg-name { display:flex; align-items:center; gap:7px; font-family: ui-monospace, Menlo, monospace; }
.phase-note { font-size:12.5px; color:var(--ink-3); margin-top:9px; line-height:1.55; }

.toggles { display:grid; grid-template-columns:repeat(auto-fit,minmax(250px,1fr)); gap:10px; margin-top:12px; }
.tg { border:1px solid var(--rule); border-radius:4px; padding:9px 11px; background:var(--ground); }
.tg.on { border-color:var(--warn); background:var(--warn-wash); }
.tg label { display:flex; gap:8px; align-items:flex-start; cursor:pointer; font-size:13px;
            color:var(--ink); line-height:1.45; }
.tg input { margin-top:3px; accent-color:var(--warn); }
.tg .why { font-size:12px; color:var(--ink-3); margin-top:5px; line-height:1.5; }

.tapewrap { margin-top:6px; max-height:320px; overflow:auto; border:1px solid var(--rule); border-radius:4px; }
table.tape { border-collapse:collapse; width:100%; font-family: ui-monospace, Menlo, monospace; font-size:12px; }
table.tape th { position:sticky; top:0; background:var(--sunken); color:var(--ink-3); font-weight:normal;
  text-align:right; padding:6px 9px; font-size:10.5px; letter-spacing:.06em; text-transform:uppercase;
  border-bottom:1px solid var(--rule); white-space:nowrap; }
table.tape td { text-align:right; padding:4px 9px; white-space:nowrap; border-bottom:1px solid var(--rule); }
table.tape tr:last-child td { border-bottom:none; }
table.tape tr.evt td { background:var(--accent-wash); }
table.tape tr.here td { box-shadow: inset 2px 0 0 var(--accent); }
table.tape td.note { text-align:left; color:var(--ink-2); font-family:inherit; font-size:12px; white-space:normal; }
/* Probe state is carried by the WORD, not the colour -- "backoff" previously
   wore the same amber as the burst series, reusing a series hue for a state. */
td.st-probe { color:var(--s-scav); } td.st-backoff { color:var(--ink-2); } td.st-settled { color:var(--ink-3); }
td.st-timeout { color:var(--warn); font-weight:600; }
td.hot { color:var(--warn); font-weight:600; }

.churn { display:flex; gap:22px; flex-wrap:wrap; margin-top:14px; padding-top:14px; border-top:1px solid var(--rule); }
.stat .n { font-family: ui-monospace, Menlo, monospace; font-size:21px; color:var(--ink); display:block; line-height:1.2; }
.stat .n.warn { color:var(--warn); } .stat .n.good { color:var(--s-scav); }
.stat .l { font-size:11px; color:var(--ink-3); letter-spacing:.06em; text-transform:uppercase; }
.stat svg { display:block; margin-top:3px; }
.note-i { font-size:13px; color:var(--ink-3); margin-top:12px; line-height:1.6; }
.note-i b { color:var(--ink-2); }
"""

JS = r"""
"use strict";
var FIX = __FIXTURES__;
var TICK_SECONDS = FIX.tickSeconds, HEARTBEAT = FIX.heartbeat, SKIP = FIX.skip;
// Read out of the Go, not chosen here: retune probeVerdictTimeout and this
// page's fourth state moves with it.
var VERDICT_TIMEOUT = FIX.verdictTimeout;
var RAMP = 2, STATUS_LAG = 1;

/* =====================================================================
   Ports. Transliterations of the Python in sim2_prototype.py, which is a
   transliteration of the Go -- same phases, same order, same early exits.
   ===================================================================== */
var TOLERANCE_PCT = 0.5;
function groupFloor(s) { return s.min != null ? s.min : 0; }
function targetPercent(s) {
  var t = s.target;
  if (t == null || typeof t !== 'string' || t.charAt(t.length - 1) !== '%') return null;
  var pct = parseInt(t.slice(0, -1), 10);
  if (isNaN(pct) || pct < 1 || pct > 100) return null;
  return pct;
}
function ceilDiv(a, b) { return Math.floor((a + b - 1) / b); }
function groupTarget(total, s) {
  var pct = targetPercent(s);
  if (pct === null) return null;
  if (s.max != null) return Math.min(ceilDiv(total * pct, 100), s.max);
  return Math.floor(total * pct / 100);
}
function isOpportunistic(s) { return !!s.opportunistic; }
function groupCeiling(total, s) {
  if (s.max != null) return { limit: s.max, bounded: true };
  var pct = targetPercent(s);
  if (pct !== null) return { limit: Math.floor(total * pct / 100), bounded: true };
  return { limit: 0, bounded: false };
}
function checkTargetDegraded(total, targets, groups) {
  if (total <= 0) return false;
  for (var i = 0; i < groups.length; i++) {
    var s = groups[i].scaling, pct = targetPercent(s);
    if (pct === null) continue;
    var actual = targets[i] / total * 100;
    if (s.max == null && actual > pct + TOLERANCE_PCT) return true;
    if (s.max != null && actual < pct - TOLERANCE_PCT) return true;
  }
  return false;
}
// `phases` is bookkeeping for the UI: it records what the algorithm did and
// never influences it.
function computeGroupTargets(total, groups, observed) {
  observed = observed || {};
  var n = groups.length, i, give, additional;
  if (n === 0) return { targets: [], degraded: false, unplaced: 0, phases: [] };
  var targets = [], phases = [];
  for (i = 0; i < n; i++) { targets.push(0); phases.push([0, 0, 0, 0]); }
  if (total <= 0) return { targets: targets, degraded: false, unplaced: 0, phases: phases };

  var remaining = total;
  // Phase 1: satisfy cascade thresholds (floor) in list order.
  for (i = 0; i < n; i++) {
    var floor = groupFloor(groups[i].scaling);
    if (floor > 0) { var t = Math.min(floor, remaining); targets[i] = t; remaining -= t; phases[i][0] = t; }
  }
  if (remaining <= 0)
    return { targets: targets, degraded: checkTargetDegraded(total, targets, groups), unplaced: 0, phases: phases };

  // Phase 2: chase percentage-based targets.
  for (i = 0; i < n; i++) {
    var tgt = groupTarget(total, groups[i].scaling);
    if (tgt === null) continue;
    additional = tgt - targets[i];
    if (additional <= 0) continue;
    give = Math.min(additional, remaining);
    targets[i] += give; remaining -= give; phases[i][1] = give;
    if (remaining <= 0) break;
  }
  // Phase 3: fill opportunistic groups up to their observed capacity.
  for (i = 0; i < n; i++) {
    if (remaining <= 0) break;
    if (!isOpportunistic(groups[i].scaling)) continue;
    // Absent from the map means never sized: offer the whole remainder and let
    // the scheduler say how much lands. That is the cold start.
    var want = Object.prototype.hasOwnProperty.call(observed, groups[i].name)
      ? observed[groups[i].name] : remaining;
    additional = want - targets[i];
    if (additional <= 0) continue;
    give = Math.min(additional, remaining);
    targets[i] += give; remaining -= give; phases[i][2] = give;
  }
  // Phase 4: distribute the remainder in list order, respecting ceilings.
  for (i = 0; i < n; i++) {
    if (remaining <= 0) break;
    // Phase 3 owns opportunistic groups. Falling through would read them as
    // unbounded and let one swallow the whole remainder.
    if (isOpportunistic(groups[i].scaling)) continue;
    var c = groupCeiling(total, groups[i].scaling);
    if (!c.bounded) { targets[i] += remaining; phases[i][3] = remaining; remaining = 0; continue; }
    var headroom = c.limit - targets[i];
    if (headroom <= 0) continue;
    give = Math.min(headroom, remaining);
    targets[i] += give; remaining -= give; phases[i][3] = give;
  }
  return { targets: targets, degraded: checkTargetDegraded(total, targets, groups),
           unplaced: remaining, phases: phases };
}

function newProbeState() { return { outstanding: false, lastFailed: null, startedAt: null }; }

function decision(target, issued, awaitVerdict, abandoned) {
  return { target: target, issued: !!issued, awaitVerdict: !!awaitVerdict,
           abandoned: !!abandoned };
}
// Go's now.Sub(zeroTime) is enormous, which is what makes an unset timestamp
// read as "long ago" rather than "just now".
function elapsedSince(now, since) { return since === null ? Infinity : now - since; }

function decideProbe(st, target, obs, now, heartbeat) {
  // obs.found is the guard, not a formality: against an observation nobody
  // read, the first branch below is 0 >= 0 and the probe resolves itself.
  if (st.outstanding && obs.found) {
    if (obs.ready >= obs.asked) {
      // Succeeded. Deliberately does NOT stamp lastFailed -- that is what makes
      // the walk-up immediate.
      st.outstanding = false;
    } else if (obs.unschedulable > 0) {
      st.outstanding = false; st.lastFailed = now;
    }
  }
  // An unanswered probe does not wait forever. Outside the found guard, because
  // a child that became unreadable can never deliver a verdict; below the
  // resolution above, because an answer arriving on the deadline still wins.
  if (st.outstanding && elapsedSince(now, st.startedAt) >= VERDICT_TIMEOUT) {
    st.outstanding = false; st.lastFailed = now;
    return decision(target, false, false, true);
  }
  if (st.outstanding) {
    if (!obs.found) return decision(target);
    return decision(target + 1, false, true);
  }
  var settled = obs.found && obs.asked === target && obs.ready >= obs.asked;
  if (settled && elapsedSince(now, st.lastFailed) >= heartbeat) {
    st.outstanding = true; st.startedAt = now;
    return decision(target + 1, true, true);
  }
  return decision(target);
}

function capacityFrom(st, obs, countUnjudged) {
  if (obs.foreign) return 0;                 // present, someone else's, no capacity
  if (!obs.found) return null;               // no child yet -> cold start
  var capacity = obs.asked - obs.unschedulable;
  if (!countUnjudged && st.outstanding && obs.ready < obs.asked && obs.unschedulable === 0)
    capacity = obs.asked - 1;
  return Math.max(0, capacity);
}

/* ---- self-check against the fixtures Python emitted ---- */
function checkPorts() {
  var fails = [], i;
  for (i = 0; i < FIX.trace.length; i++) {
    var got = computeGroupTargets(FIX.trace[i][0], FIX.threeGroup, null).targets;
    if (got.join() !== FIX.trace[i][1].join()) fails.push('scaling trace total=' + FIX.trace[i][0]);
  }
  for (i = 0; i < FIX.opportunistic.length; i++) {
    var c = FIX.opportunistic[i];
    var r = computeGroupTargets(c[0], FIX.threeTier, c[1]);
    if (r.targets.join() !== c[2].join()) fails.push('opportunistic #' + (i + 1));
    var sum = r.targets.reduce(function (a, b) { return a + b; }, 0);
    if (sum + r.unplaced !== c[0]) fails.push('opportunistic #' + (i + 1) + ' loses replicas');
    r.targets.forEach(function (t, gi) {
      var ps = r.phases[gi].reduce(function (a, b) { return a + b; }, 0);
      if (ps !== t) fails.push('opportunistic #' + (i + 1) + ' phase sum');
    });
  }
  // One stateful sequence, replayed in order, exactly as the Go test runs it.
  var st = newProbeState();
  for (i = 0; i < FIX.probe.length; i++) {
    var f = FIX.probe[i];
    var d = decideProbe(st, f.target, f.obs, f.at, FIX.probeHeartbeat);
    for (var k in f.want) {
      if (Object.prototype.hasOwnProperty.call(f.want, k) && d[k] !== f.want[k])
        fails.push('probe step ' + (i + 1) + ' (' + f.label + '): ' + k);
    }
  }
  return fails;
}

/* =====================================================================
   The simulator
   ===================================================================== */
function Sim(total, free) {
  this.groups = FIX.threeTier;
  this.total = total; this.free = free;
  this.now = 0; this.tickNo = 0;
  this.probe = newProbeState();
  this.scavIdx = 1;
  this.specReplicas = null; this.statusReplicas = 0; this.ready = 0;
  this.pipe = []; this.rows = []; this.states = [];
  this.ramp = RAMP;  // per-sim: some scenarios need a slower start to be legible
  this.gateOnStatus = false; this.countUnjudged = false; this.noHeartbeat = false;
  // A wedged scheduler answers nothing: the probe pod is never placed and never
  // refused, so no PodScheduled condition is ever written.
  this.wedged = false;
}
Sim.prototype.snapshot = function () {
  return { now: this.now, tickNo: this.tickNo, free: this.free, total: this.total,
    outstanding: this.probe.outstanding, lastFailed: this.probe.lastFailed,
    startedAt: this.probe.startedAt, wedged: this.wedged,
    specReplicas: this.specReplicas, statusReplicas: this.statusReplicas,
    ready: this.ready, pipe: this.pipe.slice() };
};
Sim.prototype.restore = function (s) {
  this.now = s.now; this.tickNo = s.tickNo; this.free = s.free; this.total = s.total;
  this.probe.outstanding = s.outstanding; this.probe.lastFailed = s.lastFailed;
  this.probe.startedAt = s.startedAt; this.wedged = s.wedged;
  this.specReplicas = s.specReplicas; this.statusReplicas = s.statusReplicas;
  this.ready = s.ready; this.pipe = s.pipe.slice();
};
// Resuming from an earlier point is a state restore, not an inverse computation
// -- the tick is not invertible (decideProbe mutates probe state, the status
// pipe shifts). states[i] is the state AFTER rows[i].
Sim.prototype.resumeAt = function (i) {
  this.restore(this.states[i]);
  this.rows.length = i + 1; this.states.length = i + 1;
};
Sim.prototype.clusterStep = function () {
  if (this.specReplicas === null || this.wedged) return;
  this.pipe.push(this.specReplicas);
  if (this.pipe.length > STATUS_LAG) this.statusReplicas = this.pipe.shift();
  var placeable = Math.min(this.specReplicas, this.free);
  if (this.ready < placeable) this.ready = Math.min(placeable, this.ready + this.ramp);
  else if (this.ready > placeable) this.ready = placeable;   // preemption is immediate
};
Sim.prototype.observe = function () {
  if (this.specReplicas === null)
    return { found: false, foreign: false, asked: 0, ready: 0, unschedulable: 0 };
  // BUG TOGGLE 1: status.replicas lags during a scale-up, so the group never
  // waits for one to finish before probing again.
  var asked = this.gateOnStatus ? this.statusReplicas : this.specReplicas;
  var obs = { found: true, foreign: false, asked: asked, ready: this.ready, unschedulable: 0 };
  if (obs.ready < obs.asked && !this.wedged) {
    var refused = Math.max(0, this.specReplicas - this.free);
    obs.unschedulable = Math.min(refused, obs.asked - obs.ready);
  }
  return obs;
};
// `mutate` runs BEFORE the reconcile but is recorded in the row, so the ghost
// run can replay the identical cluster conditions.
Sim.prototype.tick = function (note, mutate, evt) {
  if (mutate) mutate();
  this.tickNo++;
  var obs = this.observe();
  var capacity = capacityFrom(this.probe, obs, this.countUnjudged);
  var observed = {};
  if (capacity !== null) observed[this.groups[this.scavIdx].name] = capacity;

  var result = computeGroupTargets(this.total, this.groups, observed);
  var distributed = result.targets[this.scavIdx];

  // "Disable" means an INFINITE interval, not zero -- zero would probe on every
  // reconcile, which is a different bug entirely.
  var hb = this.noHeartbeat ? Infinity : HEARTBEAT;
  var d = decideProbe(this.probe, distributed, obs, this.now, hb);

  // Apply. The probe replica sits OUTSIDE the total, so the other groups'
  // targets are the distribution's, untouched.
  var applied = result.targets.slice();
  applied[this.scavIdx] = d.target;
  var probing = d.target > distributed;
  this.specReplicas = d.target;

  // Four states, not three. The fourth exists because a probe can end without
  // a verdict, and a reader who never sees it cannot tell a slow answer from
  // no answer at all.
  var state = d.abandoned ? 'timeout'
    : this.probe.outstanding ? 'probe?'
    : (this.probe.lastFailed !== null && this.now - this.probe.lastFailed < hb) ? 'backoff' : 'settled';

  // What the scheduler will do with what we just wrote. Scav's own replicas are
  // placed first; the probe takes the last slot, so it is the first thing to go
  // Pending -- which is exactly how a probe gets refused.
  var scavRunning = Math.min(distributed, this.free);
  var scavPending = distributed - scavRunning;
  var probePending = probing && this.free < d.target;

  this.rows.push({
    tick: this.tickNo, t: this.now, free: this.free, total: this.total, state: state,
    asked: obs.asked, ready: obs.ready, uns: obs.unschedulable,
    cap: capacity === null ? '—' : capacity,
    targets: applied, distributed: distributed, probing: probing, phases: result.phases,
    scavRunning: scavRunning, scavPending: scavPending, probePending: probePending,
    unplaced: result.unplaced, pending: scavPending + (probePending ? 1 : 0),
    note: note || '', evt: evt || ''
  });
  this.now += TICK_SECONDS;
  this.clusterStep();
  this.states.push(this.snapshot());
  return this.rows[this.rows.length - 1];
};
Sim.prototype.advanceToHeartbeat = function () {
  if (this.probe.lastFailed !== null) this.now = Math.max(this.now, this.probe.lastFailed + HEARTBEAT);
};

function churnOf(rows) {
  var t = rows.slice(SKIP).map(function (r) { return r.targets; });
  var out = [0, 0, 0];
  for (var i = 1; i < t.length; i++)
    for (var g = 0; g < 3; g++) out[g] += Math.abs(t[i][g] - t[i - 1][g]);
  return out;
}

// The ghost is the same simulator with every toggle off, replaying the exact
// cluster conditions the live run saw. The only difference is the bug, so any
// divergence between the two lines IS the bug.
function ghostRows() {
  if (!sim.rows.length) return [];
  var g = new Sim(sim.rows[0].total, sim.rows[0].free);
  g.ramp = sim.ramp;   // the only difference between ghost and live is the bug
  sim.rows.forEach(function (r) {
    g.tick(null, function () { g.free = r.free; g.total = r.total; g.now = r.t; });
  });
  return g.rows;
}

/* =====================================================================
   UI
   ===================================================================== */
var sim, timer = null, view = -1, xMode = 'clock';
var $ = function (id) { return document.getElementById(id); };
function esc(v) { return String(v).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'); }
function anyBug() { return $('s2-gate').checked || $('s2-unjudged').checked || $('s2-nohb').checked; }

function buildSim() {
  var s = new Sim(parseInt($('s2-total').value, 10), parseInt($('s2-free').value, 10));
  s.gateOnStatus = $('s2-gate').checked; s.countUnjudged = $('s2-unjudged').checked;
  s.noHeartbeat = $('s2-nohb').checked;
  return s;
}
function reset() { stop(); sim = buildSim(); view = -1; activePreset = null; render(); }

/* ---- scenario presets ----
   A direct mirror of run_spec() in sim2_prototype.py, replaying the same action
   lists Python emits — so a lesson cannot drift between the tape and the page. */
var activePreset = null;
function runPreset(spec) {
  stop();
  $('s2-gate').checked = spec.compare === 'gate_on_status';
  $('s2-unjudged').checked = spec.compare === 'count_unjudged';
  $('s2-nohb').checked = spec.compare === 'no_heartbeat';
  $('s2-total').value = spec.total; $('s2-totalv').textContent = spec.total;
  $('s2-free').value = spec.free; $('s2-freev').textContent = spec.free;

  sim = buildSim();
  sim.ramp = spec.ramp || RAMP;
  spec.actions.forEach(function (a) {
    for (var k = 0; k < (a.repeat || 1); k++) {
      var moved = true;
      sim.tick(a.note || '', function () {
        if (a.op === 'preempt') sim.free = Math.max(0, sim.free - a.n);
        else if (a.op === 'free') sim.free += a.n;
        else if (a.op === 'wedge') sim.wedged = true;
        else if (a.op === 'unwedge') sim.wedged = false;
        else if (a.op === 'hb') { var b = sim.now; sim.advanceToHeartbeat(); moved = sim.now !== b; }
      }, a.evt || '');
      // A heartbeat advance with nothing to wait out is a no-op; marking it
      // would put ten rules on the chart for one thing actually happening.
      if (!moved) {
        var row = sim.rows[sim.rows.length - 1];
        row.evt = ''; row.note = '';
      }
    }
  });
  view = sim.rows.length - 1;
  activePreset = spec.key;
  $('s2-preset-blurb').textContent = spec.blurb;
  ['tg-status', 'tg-unjudged', 'tg-hb'].forEach(function (id, ix) {
    $(id).classList.toggle('on', [$('s2-gate'), $('s2-unjudged'), $('s2-nohb')][ix].checked);
  });
  Array.prototype.forEach.call($('s2-presets').children, function (b) {
    b.classList.toggle('on', b.getAttribute('data-key') === spec.key);
  });
  render();
}

(function buildPresets() {
  FIX.scenarios.forEach(function (spec) {
    var b = document.createElement('button');
    b.type = 'button';
    b.setAttribute('data-key', spec.key);
    b.textContent = spec.label;           // untrusted-by-convention: never innerHTML
    b.onclick = function () { runPreset(spec); };
    $('s2-presets').appendChild(b);
  });
})();
function syncToggles() {
  if (!sim) return;
  sim.gateOnStatus = $('s2-gate').checked; sim.countUnjudged = $('s2-unjudged').checked;
  sim.noHeartbeat = $('s2-nohb').checked;
  $('s2-tg-status').classList.toggle('on', $('s2-gate').checked);
  $('s2-tg-unjudged').classList.toggle('on', $('s2-unjudged').checked);
  $('s2-tg-hb').classList.toggle('on', $('s2-nohb').checked);
  clearPreset();
  render();
}
function stop() { if (timer) { clearInterval(timer); timer = null; }
  $('s2-run').textContent = 'run ▶▶'; $('s2-run').classList.remove('primary'); }

function clearPreset() {
  if (!activePreset) return;
  activePreset = null;
  $('s2-preset-blurb').textContent = '';
  Array.prototype.forEach.call($('s2-presets').children, function (b) { b.classList.remove('on'); });
}
function step(note, mutate, evt) {
  // Ticking from a scrubbed-back position branches: the future is discarded.
  if (view >= 0 && view < sim.rows.length - 1) sim.resumeAt(view);
  clearPreset();
  sim.tick(note, mutate, evt);
  view = sim.rows.length - 1;
  render();
}
function seek(i) {
  view = Math.max(0, Math.min(sim.rows.length - 1, i));
  render();
}

/* ---- charts ---- */
var SVGNS = 'http://www.w3.org/2000/svg';
function el(name, attrs, text) {
  var e = document.createElementNS(SVGNS, name);
  for (var k in attrs) if (attrs[k] !== null && attrs[k] !== undefined) e.setAttribute(k, attrs[k]);
  if (text !== undefined) e.textContent = text;
  return e;
}
var W = 900, PADL = 30, PADR = 52, PADT = 12;
var H1 = 128, GAP = 20, H2 = 74, GAP2 = 12, H3 = 16, AXIS = 20;
var Y1 = PADT, Y2 = Y1 + H1 + GAP, Y3 = Y2 + H2 + GAP2, HH = Y3 + H3 + AXIS;

function renderCharts() {
  var host = $('s2-chart');
  // The SVG is rebuilt every render, which would drop keyboard focus on every
  // arrow press. Carry it across.
  var hadFocus = host.contains(document.activeElement);
  host.innerHTML = '';
  var rows = sim.rows, n = rows.length;
  var svg = el('svg', { viewBox: '0 0 ' + W + ' ' + HH, role: 'img', tabindex: '0',
    'aria-label': 'group targets, capacity and probe state over reconciles. '
      + 'Focus and use the left and right arrow keys to read each reconcile.' });
  svg.addEventListener('focus', showTipAtView);
  svg.addEventListener('blur', function () { $('s2-tip').style.opacity = 0; });
  host.appendChild(svg);
  if (!n) return;

  var plotW = W - PADL - PADR;

  // Two honest x-axes, and they answer different questions.
  //
  //   'reconcile' spaces every tick equally -- right for comparing runs, and it
  //   gives each reconcile equal visual weight, which is what makes an
  //   oscillation legible.
  //
  //   'clock' spaces by wall time -- right for reading DURATION. It is the
  //   default because the alternative quietly lies: advancing to the heartbeat
  //   jumps the clock 40s while reconcile-spacing draws that gap the same width
  //   as a 10s one, flattening the exact quantity this milestone teaches.
  var byClock = xMode === 'clock';
  var t0 = rows[0].t, tN = rows[n - 1].t, span = tN - t0;
  var X = function (i) {
    if (n === 1) return PADL;
    if (byClock && span > 0) return PADL + ((rows[i].t - t0) / span) * plotW;
    return PADL + (i / (n - 1)) * plotW;
  };

  var ghosts = anyBug() ? ghostRows() : [];
  var sBase = function (r) { return r.targets[0]; },
      sScav = function (r) { return r.distributed; },
      sBurst = function (r) { return r.targets[2]; };

  // -- panel 1: group targets over time -------------------------------
  var maxY = 1;
  rows.concat(ghosts).forEach(function (r) {
    maxY = Math.max(maxY, sBase(r), sScav(r), sBurst(r));
  });
  maxY = Math.ceil(maxY / 5) * 5 || 5;
  var Y1v = function (v) { return Y1 + H1 - (v / maxY) * H1; };

  svg.appendChild(el('text', { x: PADL, y: Y1 - 3, class: 'panel-t' }, 'group targets'));
  [0, maxY / 2, maxY].forEach(function (v) {
    svg.appendChild(el('line', { x1: PADL, x2: W - PADR, y1: Y1v(v), y2: Y1v(v), class: 'gridline' }));
    svg.appendChild(el('text', { x: PADL - 6, y: Y1v(v) + 3, class: 'axtext', 'text-anchor': 'end' }, v));
  });

  function path(data, fn, Yf) {
    var d = '';
    for (var i = 0; i < data.length; i++) d += (i ? 'L' : 'M') + X(i).toFixed(1) + ',' + Yf(fn(data[i])).toFixed(1);
    return d;
  }
  var series = [
    { name: 'base', fn: sBase, css: 'var(--s-base)' },
    { name: 'scav', fn: sScav, css: 'var(--s-scav)' },
    { name: 'burst', fn: sBurst, css: 'var(--s-burst)' }
  ];
  // Ghosts first, so the live series read on top.
  ghosts.length && series.forEach(function (s) {
    svg.appendChild(el('path', { d: path(ghosts, s.fn, Y1v), class: 'ln ghost' }));
  });
  // Direct-label the endpoint: 3 series, so labels rather than legend-only.
  // Two series ending on the same value would stack their labels on top of each
  // other, so nudge them apart before drawing.
  function placeLabels(items, minGap) {
    items.sort(function (a, b) { return a.y - b.y; });
    for (var i = 1; i < items.length; i++)
      if (items[i].y - items[i - 1].y < minGap) items[i].y = items[i - 1].y + minGap;
    items.forEach(function (it) {
      svg.appendChild(el('text', { x: W - PADR + 6, y: it.y + 3.5, class: 'endlab', fill: it.css }, it.text));
    });
  }
  var labs1 = [];
  series.forEach(function (s) {
    svg.appendChild(el('path', { d: path(rows, s.fn, Y1v), class: 'ln', stroke: s.css }));
    var last = s.fn(rows[n - 1]);
    labs1.push({ y: Y1v(last), css: s.css, text: s.name + ' ' + last });
  });
  placeLabels(labs1, 13);

  // -- panel 2: capacity vs ask ---------------------------------------
  var maxC = 1;
  rows.forEach(function (r) { maxC = Math.max(maxC, r.free, r.targets[1]); });
  maxC = Math.ceil(maxC / 5) * 5 || 5;
  var Y2v = function (v) { return Y2 + H2 - (v / maxC) * H2; };
  svg.appendChild(el('text', { x: PADL, y: Y2 - 3, class: 'panel-t' },
    'what we asked for vs what the scheduler will take'));
  [0, maxC].forEach(function (v) {
    svg.appendChild(el('line', { x1: PADL, x2: W - PADR, y1: Y2v(v), y2: Y2v(v), class: 'gridline' }));
    svg.appendChild(el('text', { x: PADL - 6, y: Y2v(v) + 3, class: 'axtext', 'text-anchor': 'end' }, v));
  });
  // The shaded wedge between ask and capacity IS the Pending pods.
  var up = '', dn = '';
  for (var i = 0; i < n; i++) {
    up += (i ? 'L' : 'M') + X(i).toFixed(1) + ',' + Y2v(rows[i].targets[1]).toFixed(1);
  }
  for (i = n - 1; i >= 0; i--) dn += 'L' + X(i).toFixed(1) + ',' + Y2v(Math.min(rows[i].free, rows[i].targets[1])).toFixed(1);
  svg.appendChild(el('path', { d: up + dn + 'Z', fill: 'var(--s-scav)', opacity: .16 }));
  svg.appendChild(el('path', { d: path(rows, function (r) { return r.free; }, Y2v),
    class: 'ln', stroke: 'var(--ink-3)' }));
  svg.appendChild(el('path', { d: path(rows, function (r) { return r.targets[1]; }, Y2v),
    class: 'ln', stroke: 'var(--s-scav)' }));
  placeLabels([
    { y: Y2v(rows[n - 1].free), css: 'var(--ink-3)', text: 'free ' + rows[n - 1].free },
    { y: Y2v(rows[n - 1].targets[1]), css: 'var(--s-scav)', text: 'ask ' + rows[n - 1].targets[1] }
  ], 13);

  // -- panel 3: probe state band --------------------------------------
  svg.appendChild(el('text', { x: PADL, y: Y3 - 3, class: 'panel-t' }, 'probe state'));
  // A state HOLDS from its reconcile until the next one, so each block spans the
  // interval rather than sitting centred on a point. Under clock spacing that is
  // what makes a 40s backoff four times wider than a 10s one.
  for (i = 0; i < n; i++) {
    var st = rows[i].state;
    var fill = st === 'probe?' ? 'var(--s-scav)' : st === 'backoff' ? 'var(--ink-3)' : 'var(--rule)';
    var op = st === 'probe?' ? .85 : st === 'backoff' ? .45 : .9;
    var x0 = X(i);
    // The last state has no "next" reconcile to run to, so give it a stub that
    // is visible rather than a 1px sliver.
    var x1 = i < n - 1 ? X(i + 1)
      : Math.min(W - PADR + 12, X(i) + Math.max(8, n > 1 ? X(i) - X(i - 1) : plotW));
    svg.appendChild(el('rect', { x: x0, y: Y3, width: Math.max(1, x1 - x0 - 1), height: H3,
      fill: fill, opacity: op }));
  }

  // x-axis. Labels are skipped rather than crammed when clock spacing bunches
  // points together.
  var lastLabX = -1e9;
  for (i = 0; i < n; i++) {
    if (X(i) - lastLabX < 34) continue;
    lastLabX = X(i);
    svg.appendChild(el('text', { x: X(i), y: Y3 + H3 + 13, class: 'axtext', 'text-anchor': 'middle' },
      byClock ? rows[i].t + 's' : rows[i].tick));
  }
  svg.appendChild(el('text', { x: W - PADR, y: Y3 + H3 + 13, class: 'axtext', 'text-anchor': 'start',
    dx: 6 }, byClock ? 'clock' : 'reconcile'));

  // -- event markers ---------------------------------------------------
  // The chart already showed the response; without these the stimulus lived
  // only in the tape, so cause and effect sat in two different views.
  var lastEvtX = -1e9;
  for (i = 0; i < n; i++) {
    if (!rows[i].evt) continue;
    var ex = X(i);
    svg.appendChild(el('line', { x1: ex, x2: ex, y1: Y1, y2: Y3 + H3, class: 'evt-rule' }));
    if (ex - lastEvtX >= 26) {
      lastEvtX = ex;
      svg.appendChild(el('text', { x: ex, y: Y1 - 3, class: 'evt-lab', 'text-anchor': 'middle' },
        rows[i].evt));
    }
  }

  // -- cursor + hit layer ---------------------------------------------
  var vi = view < 0 ? n - 1 : view;
  svg.appendChild(el('line', { x1: X(vi), x2: X(vi), y1: Y1, y2: Y3 + H3, class: 'cursor' }));

  var hit = el('rect', { x: 0, y: 0, width: W, height: HH, fill: 'transparent', style: 'cursor:crosshair' });
  svg.appendChild(hit);
  function idxFromEvent(e) {
    var b = svg.getBoundingClientRect();
    var px = (e.clientX - b.left) / b.width * W;
    if (n === 1) return 0;
    return Math.max(0, Math.min(n - 1, Math.round((px - PADL) / plotW * (n - 1))));
  }
  hit.addEventListener('mousemove', function (e) { showTip(idxFromEvent(e), e); });
  hit.addEventListener('mouseleave', function () { $('s2-tip').style.opacity = 0; });
  hit.addEventListener('click', function (e) { seek(idxFromEvent(e)); });

  if (hadFocus) { svg.focus(); showTipAtView(); }
}

// Series names are inserted with textContent, never innerHTML concatenation.
// They are build-time strings today; they stop being build-time strings the
// moment Sim 1 lets a reader name a group.
function tipRow(color, value, label) {
  var row = document.createElement('div'); row.className = 'tr';
  if (color) {
    var k = document.createElement('span'); k.className = 'tk';
    k.style.background = color; row.appendChild(k);
  }
  var v = document.createElement('span'); v.className = 'tv';
  v.textContent = value; row.appendChild(v);
  var l = document.createElement('span'); l.className = 'tl';
  l.textContent = label; row.appendChild(l);
  return row;
}

function buildTip(r) {
  var tip = $('s2-tip');
  tip.textContent = '';
  var h = document.createElement('div'); h.className = 'th';
  h.textContent = 'reconcile ' + r.tick + ' · t=' + r.t + 's';
  tip.appendChild(h);
  tip.appendChild(tipRow('var(--s-base)', r.targets[0], 'base'));
  tip.appendChild(tipRow('var(--s-scav)', r.distributed, r.probing ? 'scav (+1 probe)' : 'scav'));
  tip.appendChild(tipRow('var(--s-burst)', r.targets[2], 'burst'));
  tip.appendChild(tipRow(null, r.free, 'free slots'));
  tip.appendChild(tipRow(null, r.pending, 'pending'));
  tip.appendChild(tipRow(null, r.state, 'probe state'));
  return tip;
}

function placeTip(tip, clientX, clientY) {
  var host = $('s2-chartwrap').getBoundingClientRect();
  tip.style.opacity = 1;
  var x = clientX - host.left + 14, y = clientY - host.top + 12;
  if (x + tip.offsetWidth > host.width) x = clientX - host.left - tip.offsetWidth - 14;
  tip.style.left = Math.max(0, x) + 'px'; tip.style.top = y + 'px';
}

function showTip(i, e) {
  var r = sim.rows[i];
  if (!r) return;
  placeTip(buildTip(r), e.clientX, e.clientY);
}

// Keyboard reaches everything hover does: focusing the chart shows the readout
// for the reconcile the cursor is on, and the arrow keys move it.
function showTipAtView() {
  var n = sim.rows.length;
  if (!n) return;
  var i = view < 0 ? n - 1 : view, r = sim.rows[i];
  var svg = document.querySelector('#s2-chart svg');
  if (!svg) return;
  var b = svg.getBoundingClientRect();
  var cur = svg.querySelector('.cursor');
  var fx = cur ? b.left + (+cur.getAttribute('x1')) / W * b.width : b.left + b.width / 2;
  placeTip(buildTip(r), fx, b.top + 30);
}

function sparkline(vals, color) {
  if (vals.length < 2) return '';
  var w = 68, h = 18, mx = Math.max.apply(null, vals) || 1, d = '';
  for (var i = 0; i < vals.length; i++)
    d += (i ? 'L' : 'M') + (i / (vals.length - 1) * w).toFixed(1) + ',' + (h - vals[i] / mx * h).toFixed(1);
  return '<svg width="' + w + '" height="' + h + '" viewBox="0 0 ' + w + ' ' + h + '">' +
    '<path d="' + d + '" fill="none" stroke="' + color + '" stroke-width="1.5"/></svg>';
}

/* ---- tape / config / bar ---- */
function rowHTML(r) {
  var stClass = r.state === 'probe?' ? 'st-probe' : r.state === 'timeout' ? 'st-timeout'
    : r.state === 'backoff' ? 'st-backoff' : 'st-settled';
  var cells = [['', r.tick], ['', r.t], ['', r.free], [stClass, r.state], ['', r.asked], ['', r.ready],
    [r.uns > 0 ? 'hot' : '', r.uns], ['', r.cap], ['', r.targets[0]], ['', r.targets[1]],
    ['', r.targets[2]], [r.unplaced > 0 ? 'hot' : '', r.unplaced]];
  var html = '';
  for (var i = 0; i < cells.length; i++) html += '<td class="' + cells[i][0] + '">' + esc(cells[i][1]) + '</td>';
  return html + '<td class="note">' + esc(r.note) + '</td>';
}
function renderTape() {
  var vi = view < 0 ? sim.rows.length - 1 : view, html = '';
  for (var i = 0; i < sim.rows.length; i++) {
    var r = sim.rows[i], cls = [];
    if (r.note) cls.push('evt');
    if (i === vi) cls.push('here');
    html += '<tr' + (cls.length ? ' class="' + cls.join(' ') + '"' : '') + '>' + rowHTML(r) + '</tr>';
  }
  $('s2-tape').innerHTML = html;
  var tr = $('s2-tape').children[vi];
  if (tr) { var w = document.querySelector('.tapewrap');
    var top = tr.offsetTop - w.clientHeight / 2; w.scrollTop = Math.max(0, top); }
}
function renderConfig(r) {
  var names = ['base', 'scav', 'burst'], html = '';
  var total = r ? r.total : sim.total;
  for (var i = 0; i < sim.groups.length; i++) {
    var g = sim.groups[i], s = g.scaling, opp = isOpportunistic(s), cons = [];
    if (s.min != null) cons.push('min ' + s.min);
    if (s.max != null) cons.push('max ' + s.max);
    if (s.target != null) cons.push('target ' + s.target);
    if (opp) cons.push('opportunistic');
    var ceil;
    if (opp) ceil = r && r.cap !== '—' ? 'observed ' + r.cap : 'observed';
    else { var c = groupCeiling(total, s); ceil = c.bounded ? String(c.limit) : 'unbounded'; }
    var ph = r ? r.phases[i] : [0, 0, 0, 0];
    var tgt = r ? (i === sim.scavIdx ? r.distributed : r.targets[i]) : 0;
    var probe = (r && i === sim.scavIdx && r.probing) ? '+1' : '';
    var spec = r ? r.targets[i] : 0;
    // A phase that structurally cannot apply to this group reads "—", not 0.
    var cells = '';
    for (var p = 0; p < 4; p++) {
      var na = (p === 1 && targetPercent(s) === null) || (p === 2 && !opp) || (p === 3 && opp);
      cells += na ? '<td class="na">—</td>'
                  : '<td class="' + (ph[p] ? '' : 'zero') + '">' + ph[p] + '</td>';
    }
    html += '<tr><td><span class="cfg-name"><span class="dot" data-c="' + i + '"></span>' + names[i] + '</span></td>'
      + '<td class="cons">' + esc(cons.join(', ')) + '</td>'
      + '<td class="' + (ceil === 'unbounded' ? 'cons' : '') + '">' + esc(ceil) + '</td>' + cells
      + '<td class="tot">' + tgt + '</td>'
      + '<td class="' + (probe ? 'st-probe' : 'na') + '">' + (probe || '—') + '</td>'
      + '<td class="tot">' + spec + '</td></tr>';
  }
  $('s2-cfg').innerHTML = html;
  var note = 'Phases run in order and each only tops a group up to its own bound. '
    + 'Phase 3 sits after targets so a declared share is never undercut by free capacity, '
    + 'and before overflow so free capacity is spent before burst buys more. burst is the '
    + 'only unbounded group, so it is where the remainder lands.';
  if (r && r.unplaced > 0) note += ' ' + r.unplaced + ' replicas are unplaced: every ceiling is full.';
  $('s2-phasenote').textContent = note;
}

function render() {
  var n = sim.rows.length;
  var vi = view < 0 ? n - 1 : Math.min(view, n - 1);
  var last = n ? sim.rows[vi] : null;
  var bar = $('s2-bar'), keys = $('s2-keys');
  bar.innerHTML = ''; keys.innerHTML = '';

  // The bar draws the DISTRIBUTION split by what the scheduler will accept.
  // r.targets[scav] already carries the probe (it is spec.replicas), so scav's
  // own segments use `distributed` -- drawing both would count the probe twice.
  var targets = last ? last.targets.slice() : [0, 0, 0];
  if (last) targets[sim.scavIdx] = last.distributed;
  var names = ['base', 'scav', 'burst'];
  var probeExtra = last && last.probing ? 1 : 0;
  var running = last ? last.scavRunning : 0, pending = last ? last.scavPending : 0;

  // Each segment is its own hit AND focus target, carrying the same readout on
  // hover and on focus. Native title= was doing this before: unstyled, delayed,
  // and unreachable from the keyboard.
  function seg(cls, c, num, label, what) {
    if (num <= 0) return;
    var d = document.createElement('div');
    d.className = 'seg' + (cls ? ' ' + cls : '');
    if (c !== null) d.setAttribute('data-c', c);
    d.style.flexGrow = num;
    var sp = document.createElement('span');
    sp.textContent = label === undefined ? num : label;
    d.appendChild(sp);
    if (what) {
      d.tabIndex = 0;
      d.setAttribute('role', 'img');
      d.setAttribute('aria-label', num + ' ' + what);
      var show = function (ev) {
        var tip = $('s2-tip');
        tip.textContent = '';
        tip.appendChild(tipRow(null, num, what));
        var b = d.getBoundingClientRect();
        placeTip(tip, ev && ev.clientX ? ev.clientX : b.left + b.width / 2, b.bottom);
      };
      d.addEventListener('mousemove', show);
      d.addEventListener('focus', show);
      d.addEventListener('mouseleave', function () { $('s2-tip').style.opacity = 0; });
      d.addEventListener('blur', function () { $('s2-tip').style.opacity = 0; });
    }
    bar.appendChild(d);
  }
  for (var i = 0; i < 3; i++) {
    if (i === sim.scavIdx) {
      // scav, its pending pods, and its probe stay adjacent: they are one group.
      seg('', i, running, undefined, 'scav running');
      seg('pending', null, pending, undefined, 'scav pending — refused by the scheduler');
      if (probeExtra) seg('probe', null, 1, '+1',
        'probe replica, outside the total'
        + (last.probePending ? ' — currently Pending, so the answer is no' : ''));
      continue;
    }
    seg('', i, targets[i], undefined, names[i]);
  }
  // A label is only drawn inside a mark when it fits with padding; the value
  // stays in the tooltip, the legend, and the two tables.
  Array.prototype.forEach.call(bar.querySelectorAll('.seg'), function (s) {
    var sp = s.querySelector('span');
    if (sp && s.offsetWidth < sp.scrollWidth + 10) sp.style.display = 'none';
  });
  if (targets[0] + targets[1] + targets[2] + probeExtra === 0)
    bar.innerHTML = '<div style="flex:1;display:flex;align-items:center;justify-content:center;'
      + 'font-size:12px;color:var(--ink-3)">no replicas</div>';

  for (i = 0; i < 3; i++) {
    var k = document.createElement('span'); k.className = 'key';
    var extra = (i === sim.scavIdx && pending > 0)
      ? ' <span style="color:var(--ink-3)">(' + running + ' running, ' + pending + ' pending)</span>' : '';
    k.innerHTML = '<span class="dot" data-c="' + i + '"></span><b>' + names[i] + '</b> ' + targets[i] + extra;
    keys.appendChild(k);
  }
  function addKey(cls, label, tail) {
    var e = document.createElement('span'); e.className = 'key';
    e.innerHTML = '<span class="dot ' + cls + '"></span><b>' + label + '</b> ' + tail;
    keys.appendChild(e);
  }
  if (pending > 0) addKey('pending', 'pending', 'refused by the scheduler');
  if (probeExtra) addKey('probe', 'probe', '+1 outside the total'
    + (last.probePending ? ' <span style="color:var(--ink-3)">(pending)</span>' : ''));
  if (anyBug()) addKey('ghost', 'ghost', 'the same run with the bug switched off');

  // Controls follow the viewed state.
  if (last) { $('s2-free').value = last.free; $('s2-freev').textContent = last.free;
              $('s2-total').value = last.total; $('s2-totalv').textContent = last.total; }
  var wb = $('s2-btn-wedge');
  if (wb) {
    wb.textContent = sim.wedged ? 'un-wedge the scheduler' : 'wedge the scheduler';
    wb.setAttribute('aria-pressed', sim.wedged ? 'true' : 'false');
  }
  var sc = $('s2-scrub');
  sc.max = Math.max(1, n); sc.value = vi + 1; sc.disabled = n < 2;
  sc.setAttribute('aria-valuetext', n ? 'reconcile ' + (vi + 1) + ' of ' + n : 'no reconciles');
  $('s2-counter').textContent = n ? String(vi + 1) : '0';
  $('s2-countertot').textContent = '/ ' + n;

  var c = churnOf(sim.rows);
  $('s2-c1').textContent = c[1];
  $('s2-c2').textContent = c[2];
  $('s2-c2').className = 'n ' + (c[2] > 0 ? 'warn' : 'good');
  var burstDelta = [];
  for (i = SKIP + 1; i < sim.rows.length; i++)
    burstDelta.push(Math.abs(sim.rows[i].targets[2] - sim.rows[i - 1].targets[2]));
  $('s2-spark').innerHTML = sparkline(burstDelta, 'var(--warn)');
  $('s2-pend').textContent = last ? last.pending : 0;
  $('s2-pend').className = 'n ' + (last && last.pending > 0 ? 'warn' : '');

  // Say which axis is in play, and flag when the two would differ -- an uneven
  // gap is exactly when reconcile-spacing would misrepresent a duration.
  var gaps = [], uneven = false;
  for (i = 1; i < n; i++) gaps.push(sim.rows[i].t - sim.rows[i - 1].t);
  for (i = 1; i < gaps.length; i++) if (gaps[i] !== gaps[0]) uneven = true;
  $('s2-xhint').textContent = xMode === 'clock'
    ? (uneven ? 'Spaced by wall clock: the wide gap is a heartbeat wait, and it really is that '
              + 'much longer than a normal reconcile interval.'
              : 'Spaced by wall clock. Every reconcile is one interval apart so far, so this '
              + 'looks the same as reconcile spacing.')
    : (uneven ? 'Spaced one reconcile apart — good for comparing runs, but it draws a heartbeat '
              + 'wait the same width as a normal interval. Switch to clock to see the durations.'
              : 'Spaced one reconcile apart.');

  renderConfig(last);
  renderCharts();
  renderTape();
  // ▶ is never disabled: at the end of history it computes the next reconcile,
  // which is exactly what it must do when there is no history at all.
  $('s2-back').disabled = n === 0 || vi <= 0;
}

/* ---- wiring ---- */
// One action for "forward in time": replay the next reconcile if the history
// already has one, otherwise compute a new one. Having a separate `tick` button
// beside the ▶ arrow was two controls for the same verb.
function advance() {
  if (view >= 0 && view < sim.rows.length - 1) seek(view + 1);
  else step();
}
$('s2-fwd').onclick = function () { stop(); advance(); };
$('s2-back').onclick = function () { stop(); seek((view < 0 ? sim.rows.length - 1 : view) - 1); };
$('s2-reset').onclick = reset;
$('s2-run').onclick = function () {
  if (timer) { stop(); return; }
  $('s2-run').textContent = 'stop ■'; $('s2-run').classList.add('primary');
  timer = setInterval(advance, 300);
};
$('s2-scrub').oninput = function () { stop(); seek(parseInt(this.value, 10) - 1); };
function setXMode(m) {
  xMode = m;
  $('s2-xclock').classList.toggle('primary', m === 'clock');
  $('s2-xtick').classList.toggle('primary', m === 'reconcile');
  render();
}
$('s2-xclock').onclick = function () { setXMode('clock'); };
$('s2-xtick').onclick = function () { setXMode('reconcile'); };
$('s2-total').oninput = function () { $('s2-totalv').textContent = this.value; if (sim) sim.total = +this.value; };
$('s2-free').oninput = function () { $('s2-freev').textContent = this.value; if (sim) sim.free = +this.value; };
['gate', 'unjudged', 'nohb'].forEach(function (id) { $(id).onchange = syncToggles; });
document.addEventListener('keydown', function (e) {
  if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT') return;
  if (e.key === 'ArrowRight') { e.preventDefault(); stop(); advance(); }
  if (e.key === 'ArrowLeft') { e.preventDefault(); stop(); seek((view < 0 ? sim.rows.length - 1 : view) - 1); }
});
document.querySelectorAll('[data-ev]').forEach(function (b) {
  b.onclick = function () {
    stop();
    var ev = b.getAttribute('data-ev');
    // The mutation runs inside the tick so stepping back undoes it with the row.
    // The third argument is the short tag the chart marks the event with.
    if (ev === 'preempt') step('PREEMPT 4 — a higher-priority workload took the nodes',
      function () { sim.free = Math.max(0, sim.free - 4); }, 'preempt −4');
    else if (ev === 'free') step('FREE 10 — nodes drain; nothing notices yet',
      function () { sim.free += 10; }, 'free +10');
    // Wedging models the case countUnschedulable structurally cannot see: no
    // pod is created, so none is scheduled and none is refused. The probe is
    // left waiting for a verdict that will never be written.
    else if (ev === 'wedge') step(
      sim.wedged ? 'scheduler recovered — verdicts resume'
                 : 'WEDGE — quota blocks the pod; no verdict can arrive',
      function () { sim.wedged = !sim.wedged; }, sim.wedged ? 'unwedge' : 'wedge');
    else {
      var moved = true;
      step('clock advanced to the heartbeat',
        function () { var b = sim.now; sim.advanceToHeartbeat(); moved = sim.now !== b; },
        'heartbeat');
      if (!moved) {
        var row = sim.rows[sim.rows.length - 1];
        row.evt = ''; row.note = 'nothing to wait out — no probe has been refused yet';
        render();
      }
    }
  };
});

(function init() {
  var fails = checkPorts(), b = $('s2-badge');
  if (fails.length) {
    b.className = 'badge bad';
    b.textContent = 'PORT DRIFT — ' + fails.length + ' of ' + FIX.rows
      + ' Go fixture rows disagree: ' + fails.slice(0, 3).join(', ');
  } else {
    b.className = 'badge ok';
    b.textContent = '✓ ports agree with all ' + FIX.rows + ' fixture rows read out of the Go tests';
  }
  reset();
})();
"""

BODY = """
<h1>The scavenger under pressure</h1>
<p class="sub">Sim 2 prototype — a time-stepped simulator over a cluster whose free capacity you
control. One tick is one reconcile, in the controller's own order:
<code>observe</code> → <code>decideProbe</code> → <code>capacityFrom</code> →
<code>ComputeGroupTargets</code> → <code>apply</code>.</p>
<div id="s2-badge" class="badge">checking ports…</div>

<div class="sim">
  <div class="sim-head">
    <span class="sim-label">Cluster</span>
    <div class="grp">
      <span class="counter"><b id="s2-counter">0</b> <span id="s2-countertot">/ 0</span> reconciles</span>
      <button type="button" id="s2-back" aria-label="previous reconcile">◀</button>
      <button type="button" id="s2-fwd" class="primary" aria-label="next reconcile">▶</button>
      <button type="button" id="s2-run">run ▶▶</button>
      <button type="button" id="s2-reset">reset</button>
    </div>
  </div>

  <div class="row">
    <label for="s2-total">pool replicas</label>
    <input type="range" id="s2-total" min="0" max="80" value="40">
    <output class="val" id="s2-totalv">40</output>
  </div>
  <div class="row">
    <label for="s2-free">free slots</label>
    <input type="range" id="s2-free" min="0" max="60" value="6">
    <output class="val" id="s2-freev">6</output>
    <span class="hint">how many scavenger replicas the scheduler will actually accept</span>
  </div>

  <div class="sep">
    <span class="sim-label">Scenarios — the same runs the terminal generator prints</span>
    <div class="presets" id="s2-presets"></div>
    <p class="preset-blurb" id="s2-preset-blurb"></p>
  </div>

  <div class="sep grp">
    <span class="lead">events</span>
    <button type="button" data-ev="preempt">preempt 4 pods</button>
    <button type="button" data-ev="free">free 10 slots</button>
    <button type="button" data-ev="hb">advance to heartbeat</button>
    <button type="button" data-ev="wedge" id="s2-btn-wedge">wedge the scheduler</button>
  </div>

  <div class="bar" id="s2-bar"></div>
  <div class="keys" id="s2-keys"></div>

  <div class="sep">
    <div class="sim-head">
      <span class="sim-label">Over time — click or scrub to inspect any reconcile</span>
      <div class="grp">
        <span class="lead">x axis</span>
        <button type="button" id="s2-xclock" class="primary">clock</button>
        <button type="button" id="s2-xtick">reconcile</button>
      </div>
    </div>
    <div class="chartwrap" id="s2-chartwrap">
      <div id="s2-chart"></div>
      <div class="tip" id="s2-tip"></div>
    </div>
    <p class="hint" id="s2-xhint"></p>
    <div class="row">
      <label for="s2-scrub">reconcile</label>
      <input type="range" id="s2-scrub" min="1" max="1" value="1">
    </div>
  </div>

  <div class="sep">
    <span class="sim-label">Group configuration — and which phase paid for each replica</span>
    <div class="cfgwrap">
      <table class="cfg">
        <thead><tr>
          <th>group</th><th>constraints</th><th>ceiling</th>
          <th class="ph">1 floor</th><th class="ph">2 target</th>
          <th class="ph">3 capacity</th><th class="ph">4 overflow</th>
          <th>target</th><th>probe</th><th>spec</th>
        </tr></thead>
        <tbody id="s2-cfg"></tbody>
      </table>
    </div>
    <p class="phase-note" id="s2-phasenote"></p>
  </div>

  <div class="churn">
    <div class="stat"><span class="n" id="s2-c2">0</span><span class="l">spot pods killed</span>
      <span id="s2-spark"></span></div>
    <div class="stat"><span class="n" id="s2-c1">0</span><span class="l">scav churn</span></div>
    <div class="stat"><span class="n" id="s2-pend">0</span><span class="l">pending pods</span></div>
  </div>
  <p class="note-i">Churn is total absolute movement in a group's target since the cold start.
    <b>Every unit of burst churn is a spot pod created or killed.</b> A healthy loop holds it at zero.</p>
</div>

<h2>Bug toggles</h2>
<p class="lede">Each defeats a specific branch in the controller. Turn one on and a <b>ghost</b> of the
same run with the bug switched off is drawn behind the live lines — the gap between them is the bug.</p>
<div class="toggles">
  <div class="tg" id="s2-tg-status"><label>
    <input type="checkbox" id="s2-gate"><span>Gate on <code>status.replicas</code></span></label>
    <div class="why">Defeats the <code>obs.asked</code> gate in <code>observeOpportunistic</code>.
      status lags, so a shortfall is never attributed and the loop never converges — watch burst churn.</div>
  </div>
  <div class="tg" id="s2-tg-unjudged"><label>
    <input type="checkbox" id="s2-unjudged"><span>Count the unjudged probe</span></label>
    <div class="why">Defeats the outstanding-probe branch in <code>capacityFrom</code>.
      Burst is cut a reconcile early, funding a question the scheduler has not answered.</div>
  </div>
  <div class="tg" id="s2-tg-hb"><label>
    <input type="checkbox" id="s2-nohb"><span>Disable the heartbeat</span></label>
    <div class="why">Removes the timed requeue. Nothing this controller watches fires when a node
      frees up, so freed capacity is never discovered at all.</div>
  </div>
</div>

<h2>Tape</h2>
<div class="tapewrap">
  <table class="tape">
    <thead><tr>
      <th>tick</th><th>t</th><th>free</th><th>probe</th><th>asked</th><th>ready</th>
      <th>uns</th><th>cap</th><th>base</th><th>scav</th><th>burst</th><th>unpl</th>
      <th style="text-align:left">note</th>
    </tr></thead>
    <tbody id="s2-tape"></tbody>
  </table>
</div>
"""

# This module no longer owns a page skeleton. Both views of Sim 2 -- the
# standalone page built by sim2_prototype.build_page() and the figure embedded
# in the tutorial -- render through docshell, so the widget is always a
# component and never a document. What follows is what makes that possible.
#
# ---------------------------------------------------------------------------
# Embedding
#
# The standalone page owns the document: it paints :root, styles body, and uses
# .wrap. Dropped into the tutorial that would repaint the whole article, since
# eleven of its token names are the tutorial's own. So the embed re-scopes the
# palette to the widget and drops the two rules that belong to a page rather
# than to a component.
# ---------------------------------------------------------------------------

# (selector in the standalone sheet, selector in the embed)
RESCOPE = [
    (':root[data-theme="dark"]', ':root[data-theme="dark"] .sim2'),
    (':root[data-theme="light"]', ':root[data-theme="light"] .sim2'),
    (':root:where(:not([data-theme="light"]))', ':root:where(:not([data-theme="light"])) .sim2'),
    (':root', '.sim2'),
]

# Rules that style the document rather than the widget.
PAGE_ONLY = ("body {", ".wrap {")


def embed_css():
    out, skip, depth = [], False, 0
    for line in CSS.split("\n"):
        stripped = line.strip()
        if not skip and any(stripped.startswith(s) for s in PAGE_ONLY):
            skip = True
        if skip:
            depth += line.count("{") - line.count("}")
            if depth <= 0:
                skip, depth = False, 0
            continue
        for old, new in RESCOPE:
            if re.match(re.escape(old) + r"(\s*\{|\s*,)", stripped):
                line = line.replace(old, new, 1)
                break
        out.append(line)
    css = "\n".join(out)
    # Anything still selecting the document would repaint the article around it.
    stray = [m.group(0) for m in re.finditer(r"(?m)^(?::root|body|html)[^{]*\{", css)
             if ".sim2" not in m.group(0)]
    if stray:
        raise AssertionError("embed_css left document-level rules behind: %s" % stray)
    return css


def sim_html():
    """BODY as a widget: no page title, and headings demoted so the tutorial's
    own heading levels keep their meaning."""
    body = BODY
    start = body.index("<h1>")
    end = body.index("</p>", body.index('<p class="sub">')) + len("</p>")
    body = body[:start] + body[end:]
    body = body.replace("<h2>", '<p class="s2-h">').replace("</h2>", "</p>")
    return '<div class="sim2">' + body + "</div>"


def sim_js(fixtures, isolate=True):
    """The page's script with its fixtures inlined.

    Isolated by default. Standalone, this script owns the document and its ~50
    top-level names are harmless; embedded, it shares a page with the tutorial's
    own script and every other figure, and the first collision would be a
    silent one. sim1 is already IIFE-wrapped for the same reason.
    """
    js = JS.replace("__FIXTURES__", json.dumps(fixtures))
    if not isolate:
        return js
    return "(function () {\n" + js + "\n}());"
