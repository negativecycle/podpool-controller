#!/usr/bin/env python3
"""Sim 2 prototype — "the scavenger under pressure".

Generator, in the shape the rest of docs/ uses: Python owns the port and the
fixtures, and emits a self-contained HTML page with a JavaScript
transliteration of the same functions.

Ports three things from Go:

  workload.ComputeGroupTargets   internal/workload/algorithm.go
  reconciler.capacityFrom        internal/controller/opportunistic.go
  reconciler.decideProbe         internal/controller/opportunistic.go

Nothing here is copied. The fixtures are read out of the Go tests by
gofixtures.py, and the shape of the machine — the fields of probeState and
probeDecision, the timing constants — is read out of the Go source and checked
against what this port claims to model. Both are emitted into the page, where
the JavaScript port re-checks itself on load and shows a pass/fail badge, so a
drifted port is visible to the reader and not only to the build.

That indirection is the whole point. The first version of this file copied its
fixtures by hand and reported that its ports agreed with all 22 rows while
modelling a decideProbe the controller had already replaced. Copied fixtures
move with the port; these move with the source.

Run:
    python3 sim2_prototype.py            # check ports, print tapes, write HTML
    python3 sim2_prototype.py --quiet    # check ports and write HTML only
"""

import json
import os
import sys

import gofixtures

# ---------------------------------------------------------------------------
# Port: internal/workload/algorithm.go
# ---------------------------------------------------------------------------

TOLERANCE_PCT = 0.5


def group_floor(s):
    return s["min"] if s.get("min") is not None else 0


def target_percent(s):
    """Port of TargetPercent. The target is a percentage string like "30%"."""
    t = s.get("target")
    if t is None or not isinstance(t, str) or not t.endswith("%"):
        return None
    try:
        pct = int(t[:-1])
    except ValueError:
        return None
    return pct if 1 <= pct <= 100 else None


def ceil_div(a, b):
    return (a + b - 1) // b


def group_target(total, s):
    """Port of GroupTarget. Rounds down when the target is itself the ceiling
    (no max), up when the ceiling is elsewhere (max set)."""
    pct = target_percent(s)
    if pct is None:
        return None
    if s.get("max") is not None:
        return min(ceil_div(total * pct, 100), s["max"])
    return total * pct // 100


def is_opportunistic(s):
    return bool(s.get("opportunistic"))


def group_ceiling(total, s):
    """Port of GroupCeiling. Returns (limit, bounded)."""
    if s.get("max") is not None:
        return s["max"], True
    pct = target_percent(s)
    if pct is not None:
        return total * pct // 100, True
    return 0, False


def check_target_degraded(total, targets, groups):
    if total <= 0:
        return False
    for i, g in enumerate(groups):
        s = g["scaling"]
        pct = target_percent(s)
        if pct is None:
            continue
        actual = targets[i] / total * 100.0
        if s.get("max") is None and actual > pct + TOLERANCE_PCT:
            return True
        if s.get("max") is not None and actual < pct - TOLERANCE_PCT:
            return True
    return False


def compute_group_targets(total, groups, observed=None):
    """Port of ComputeGroupTargets. Four phases, same order, same early exits.

    `phases` is bookkeeping for the UI: phases[i][p] is how many replicas group i
    received in phase p+1. It records what the algorithm did and never influences
    it, so the port stays a transliteration.
    """
    observed = observed or {}
    n = len(groups)
    if n == 0:
        return {"targets": [], "degraded": False, "unplaced": 0, "phases": []}

    targets = [0] * n
    phases = [[0, 0, 0, 0] for _ in range(n)]
    if total <= 0:
        return {"targets": targets, "degraded": False, "unplaced": 0, "phases": phases}

    remaining = total

    # Phase 1: satisfy cascade thresholds (floor) in list order.
    for i, g in enumerate(groups):
        floor = group_floor(g["scaling"])
        if floor > 0:
            t = min(floor, remaining)
            targets[i] = t
            remaining -= t
            phases[i][0] = t

    if remaining <= 0:
        return {"targets": targets,
                "degraded": check_target_degraded(total, targets, groups),
                "unplaced": 0, "phases": phases}

    # Phase 2: chase percentage-based targets.
    for i, g in enumerate(groups):
        target = group_target(total, g["scaling"])
        if target is None:
            continue
        additional = target - targets[i]
        if additional <= 0:
            continue
        give = min(additional, remaining)
        targets[i] += give
        remaining -= give
        phases[i][1] = give
        if remaining <= 0:
            break

    # Phase 3: fill opportunistic groups up to their observed capacity.
    for i, g in enumerate(groups):
        if remaining <= 0:
            break
        if not is_opportunistic(g["scaling"]):
            continue
        # Absent from the map means never sized: offer the whole remainder and
        # let the scheduler say how much lands. That is the cold start.
        want = observed[g["name"]] if g["name"] in observed else remaining
        additional = want - targets[i]
        if additional <= 0:
            continue
        give = min(additional, remaining)
        targets[i] += give
        remaining -= give
        phases[i][2] = give

    # Phase 4: distribute the remainder in list order, respecting ceilings.
    for i, g in enumerate(groups):
        if remaining <= 0:
            break
        # Phase 3 owns opportunistic groups. Falling through would read them as
        # unbounded and let one swallow the whole remainder.
        if is_opportunistic(g["scaling"]):
            continue
        limit, bounded = group_ceiling(total, g["scaling"])
        if not bounded:
            targets[i] += remaining
            phases[i][3] = remaining
            remaining = 0
            continue
        headroom = limit - targets[i]
        if headroom <= 0:
            continue
        give = min(headroom, remaining)
        targets[i] += give
        remaining -= give
        phases[i][3] = give

    return {"targets": targets,
            "degraded": check_target_degraded(total, targets, groups),
            "unplaced": remaining, "phases": phases}


# ---------------------------------------------------------------------------
# Port: internal/controller/opportunistic.go
# ---------------------------------------------------------------------------


class ProbeState:
    """Mirror of probeState. In-memory only, exactly as in the controller.

    The field list is declared to gofixtures.check_shape, so a field added in
    Go and not here stops the build.
    """

    FIELDS = ["outstanding", "lastFailed", "startedAt"]

    def __init__(self):
        self.outstanding = False
        self.last_failed = None  # None == the zero time: never refused
        self.started_at = None   # when the outstanding probe was issued


# probeDecision. issued and await_verdict are separate because target+1 comes
# back from two structurally different places, and a caller that announces the
# question needs to know which.
DECISION_FIELDS = ["target", "issued", "awaitVerdict", "abandoned"]


def decision(target, issued=False, await_verdict=False, abandoned=False):
    return {"target": target, "issued": issued,
            "await_verdict": await_verdict, "abandoned": abandoned}


def _elapsed(now, since):
    """Go's now.Sub(zeroTime) is enormous, which is what makes an unset
    timestamp read as "long ago" rather than "just now"."""
    return float("inf") if since is None else now - since


def decide_probe(st, target, obs, now, heartbeat, timeout=None):
    """Port of decideProbe. Returns a decision dict.

    The probe replica is added OUTSIDE the total, so no other group pays for an
    unproven question.
    """
    if timeout is None:
        timeout = VERDICT_TIMEOUT

    # obs["found"] is the guard, not a formality: against an observation nobody
    # read, the first case below is 0 >= 0 and the probe resolves itself.
    if st.outstanding and obs["found"]:
        if obs["ready"] >= obs["asked"]:
            # Succeeded. Deliberately does NOT stamp last_failed — that is what
            # makes the walk-up immediate.
            st.outstanding = False
        elif obs["unschedulable"] > 0:
            st.outstanding = False
            st.last_failed = now

    # An unanswered probe does not wait forever. Outside the found guard,
    # because a child that became unreadable can never deliver a verdict; below
    # the resolution above, because an answer arriving on the deadline wins.
    if st.outstanding and _elapsed(now, st.started_at) >= timeout:
        st.outstanding = False
        st.last_failed = now
        return decision(target, abandoned=True)

    if st.outstanding:
        if not obs["found"]:
            return decision(target)
        return decision(target + 1, await_verdict=True)

    settled = obs["found"] and obs["asked"] == target and obs["ready"] >= obs["asked"]
    if settled and _elapsed(now, st.last_failed) >= heartbeat:
        st.outstanding = True
        st.started_at = now
        return decision(target + 1, issued=True, await_verdict=True)

    return decision(target)


def capacity_from(st, obs, count_unjudged=False):
    """Port of capacityFrom for a single group. Returns capacity or None.

    count_unjudged is BUG TOGGLE 2: dropping the outstanding-probe branch.
    """
    if obs.get("foreign"):
        return 0  # present, someone else's, no capacity — but NOT a cold start
    if not obs["found"]:
        return None  # no child yet -> absent from the map -> cold start

    capacity = obs["asked"] - obs["unschedulable"]
    if not count_unjudged:
        if st.outstanding and obs["ready"] < obs["asked"] and obs["unschedulable"] == 0:
            capacity = obs["asked"] - 1
    return max(0, capacity)


# ---------------------------------------------------------------------------
# Fixtures — read out of the Go tests, never copied.
#
# gofixtures parses the tables and the probe lifecycle out of the test files and
# the machine's shape out of the source. The two declarations below are the
# contract: this port says which fields and constants it implements, and the
# load raises if the controller has grown one it does not.
# ---------------------------------------------------------------------------

MODELLED_FIELDS = {
    "probeState": ProbeState.FIELDS,
    "probeDecision": DECISION_FIELDS,
    "opportunisticObservation": ["found", "foreign", "asked", "ready", "unschedulable"],
}

MODELLED_CONSTS = {
    "probeVerdictTimeout": "2 * time.Minute",
    "probeVerdictRequeue": "15 * time.Second",
    "defaultOpportunisticHeartbeatSeconds": "300",
}

GO = gofixtures.load(MODELLED_FIELDS, MODELLED_CONSTS)

THREE_GROUP = GO["three_group"]
THREE_TIER = GO["three_tier"]
TRACE_FIXTURE = GO["trace"]
OPPORTUNISTIC_FIXTURE = GO["opportunistic"]
PROBE_FIXTURE = GO["probe"]
PROBE_HEARTBEAT = GO["probe_heartbeat"]
VERDICT_TIMEOUT = GO["verdict_timeout"]
VERDICT_REQUEUE = GO["verdict_requeue"]

FIXTURE_ROWS = len(TRACE_FIXTURE) + len(OPPORTUNISTIC_FIXTURE) + len(PROBE_FIXTURE)


def check_ports():
    failures = []

    for total, want in TRACE_FIXTURE:
        got = compute_group_targets(total, THREE_GROUP)["targets"]
        if got != want:
            failures.append(f"scaling trace total={total}: got {got}, want {want}")

    for name, total, observed, want in OPPORTUNISTIC_FIXTURE:
        r = compute_group_targets(total, THREE_TIER, observed)
        if r["targets"] != want:
            failures.append(f"opportunistic {name!r}: got {r['targets']}, want {want}")
        if sum(r["targets"]) + r["unplaced"] != total:
            failures.append(f"opportunistic {name!r}: does not account for {total}")
        # The phase bookkeeping must never change the answer.
        for i, tgt in enumerate(r["targets"]):
            if sum(r["phases"][i]) != tgt:
                failures.append(f"opportunistic {name!r}: phases {r['phases'][i]} "
                                f"do not sum to target {tgt}")

    # The lifecycle is one stateful sequence, so it replays in order against a
    # single ProbeState, exactly as the Go test does.
    st = ProbeState()
    for i, (label, target, obs, at, want) in enumerate(PROBE_FIXTURE, 1):
        d = decide_probe(st, target, obs, at, heartbeat=PROBE_HEARTBEAT)
        for field, expected in want.items():
            key = "await_verdict" if field == "awaitVerdict" else field
            if d[key] != expected:
                failures.append(f"probe step {i} ({label}): {field}={d[key]!r}, "
                                f"want {expected!r}")

    return failures


# ---------------------------------------------------------------------------
# The simulator (terminal version)
# ---------------------------------------------------------------------------

TICK_SECONDS = 10
HEARTBEAT = 60
SKIP = 3  # ignore the cold start, which offers the whole remainder by design


class Sim:
    """One PodPool over a cluster whose free capacity the reader controls.

    A tick is one reconcile, in the controller's own order:
        observe -> decideProbe -> capacityFrom -> ComputeGroupTargets -> apply
    """

    def __init__(self, groups, total, free_slots, heartbeat=HEARTBEAT,
                 tick_seconds=TICK_SECONDS, ramp=2, gate_on_status=False,
                 count_unjudged=False, no_heartbeat=False, status_lag=1):
        self.groups = groups
        self.total = total
        self.free_slots = free_slots
        self.heartbeat = heartbeat
        self.tick_seconds = tick_seconds
        self.ramp = ramp  # pods that become ready per tick
        # How many reconciles behind spec.replicas the child's status.replicas
        # runs. The controller asks for a fast requeue while a probe is
        # outstanding, so it can easily reconcile faster than the workload
        # controller republishes status.
        self.status_lag = status_lag
        self._status_pipe = []

        self.gate_on_status = gate_on_status
        self.count_unjudged = count_unjudged
        self.no_heartbeat = no_heartbeat

        # A wedged scheduler answers nothing: the probe pod is never placed and
        # never refused. Quota blocks the create, or the scheduler is down —
        # either way no PodScheduled condition is ever written, so the verdict
        # the probe is waiting for cannot arrive.
        self.wedged = False

        self.now = 0
        self.tick_no = 0
        self.probe = ProbeState()

        self.scav = next(g["name"] for g in groups if is_opportunistic(g["scaling"]))
        self.scav_idx = next(i for i, g in enumerate(groups)
                             if is_opportunistic(g["scaling"]))
        self.spec_replicas = None   # None == no child yet
        self.status_replicas = 0
        self.ready = 0
        self.rows = []

    def cluster_step(self):
        """What happens between reconciles: the ReplicaSet observes, pods start
        or fail to schedule."""
        if self.spec_replicas is None or self.wedged:
            return
        self._status_pipe.append(self.spec_replicas)
        if len(self._status_pipe) > self.status_lag:
            self.status_replicas = self._status_pipe.pop(0)
        placeable = min(self.spec_replicas, self.free_slots)
        if self.ready < placeable:
            self.ready = min(placeable, self.ready + self.ramp)
        elif self.ready > placeable:
            self.ready = placeable  # preemption is immediate

    def observe(self):
        """Port of observeOpportunistic for the one opportunistic group.

        BUG TOGGLE 1: gate_on_status reads status.replicas instead of
        spec.replicas. status lags during a scale-up, so the group never waits
        for one to finish before probing again.
        """
        if self.spec_replicas is None:
            return dict(found=False, asked=0, ready=0, unschedulable=0)

        asked = self.status_replicas if self.gate_on_status else self.spec_replicas
        obs = dict(found=True, foreign=False, asked=asked, ready=self.ready,
                   unschedulable=0)
        if obs["ready"] < obs["asked"] and not self.wedged:
            refused = max(0, self.spec_replicas - self.free_slots)
            obs["unschedulable"] = min(refused, obs["asked"] - obs["ready"])
        return obs

    def tick(self, note=""):
        self.tick_no += 1
        obs = self.observe()

        capacity = capacity_from(self.probe, obs, self.count_unjudged)
        observed = {} if capacity is None else {self.scav: capacity}

        result = compute_group_targets(self.total, self.groups, observed)
        distributed = result["targets"][self.scav_idx]

        # "Disable" means an INFINITE interval, not zero -- zero would probe on
        # every reconcile, which is a different bug entirely.
        hb = float("inf") if self.no_heartbeat else self.heartbeat
        d = decide_probe(self.probe, distributed, obs, self.now, hb)
        final = d["target"]

        # Apply: the probe replica sits outside the total, so the other groups'
        # targets are the distribution's, untouched.
        applied = list(result["targets"])
        applied[self.scav_idx] = final
        self.spec_replicas = final

        # Four states, not three. The fourth exists because a probe can end
        # without a verdict, and a reader who never sees that state has no way
        # to tell a slow answer from no answer at all.
        state = ("timeout" if d["abandoned"] else
                 "probe?" if self.probe.outstanding else
                 "backoff" if self.probe.last_failed is not None
                 and self.now - self.probe.last_failed < hb else "settled")

        self.rows.append(dict(
            tick=self.tick_no, t=self.now, free=self.free_slots, state=state,
            asked=obs["asked"], ready=obs["ready"], unsched=obs["unschedulable"],
            cap="-" if capacity is None else capacity,
            targets=applied, distributed=distributed,
            unplaced=result["unplaced"], note=note,
        ))

        self.now += self.tick_seconds
        self.cluster_step()
        return self.rows[-1]

    def wedge(self):
        self.wedged = True

    def unwedge(self):
        self.wedged = False

    def preempt(self, n):
        self.free_slots = max(0, self.free_slots - n)

    def free(self, n):
        self.free_slots += n

    def advance_to_heartbeat(self):
        if self.probe.last_failed is not None:
            self.now = max(self.now, self.probe.last_failed + self.heartbeat)

    def tape(self, title):
        names = [g["name"] for g in self.groups]
        head = (f"{'tick':>4} {'t':>4} {'free':>4} {'probe':>8} "
                f"{'asked':>5} {'ready':>5} {'uns':>3} {'cap':>4}  "
                + " ".join(f"{n:>5}" for n in names) + f" {'unpl':>4}  note")
        out = [f"\n{title}", "-" * len(head), head, "-" * len(head)]
        for r in self.rows:
            out.append(
                f"{r['tick']:>4} {r['t']:>4} {r['free']:>4} {r['state']:>8} "
                f"{r['asked']:>5} {r['ready']:>5} {r['unsched']:>3} {str(r['cap']):>4}  "
                + " ".join(f"{v:>5}" for v in r["targets"])
                + f" {r['unplaced']:>4}  {r['note']}")
        return "\n".join(out)


def churn(rows, skip=SKIP):
    """Total absolute movement in each group's target, ignoring the cold start.

    Every unit of burst churn is a spot pod created or killed.
    """
    t = [r["targets"] for r in rows[skip:]]
    if len(t) < 2:
        return [0] * len(rows[0]["targets"])
    return [sum(abs(t[i][g] - t[i - 1][g]) for i in range(1, len(t)))
            for g in range(len(t[0]))]


# ---------------------------------------------------------------------------
# Scenarios — declared once, as data.
#
# The terminal tapes and the page's preset buttons replay the SAME definitions,
# so a lesson cannot drift between the two. `compare` names the toggle a
# scenario is about: Python runs it both ways and diffs, the page sets that
# toggle and lets the ghost draw the difference.
#
# An action's mutation happens immediately before its tick, matching the way
# the page applies events inside the tick so stepping back undoes both together.
# ---------------------------------------------------------------------------

SCENARIOS = [
    {
        "key": "coldstart",
        "label": "cold start & walk-up",
        "title": "A. Cold start, then the walk-up (free=8)",
        "blurb": "The cold start is offered the whole remainder; the scheduler takes 8 "
                 "and refuses the rest. Then the probe walks up one replica at a time "
                 "until it is refused.",
        "total": 40, "free": 8, "ramp": 2, "compare": None,
        "actions": [
            {"op": "tick", "note": "cold start: offered the remainder"},
            {"op": "tick", "repeat": 7},
        ],
    },
    {
        "key": "preempt",
        "label": "preemption",
        "title": "B. Preemption: capacity collapses in one tick, burst absorbs it",
        "blurb": "A higher-priority workload takes the nodes. Capacity is lost in a "
                 "single reconcile and burst picks up the shortfall immediately.",
        "total": 40, "free": 12, "ramp": 2, "compare": None,
        "actions": [
            {"op": "tick", "repeat": 6},
            {"op": "preempt", "n": 8, "evt": "preempt -8",
             "note": "PREEMPT 8 -- higher-priority workload took the nodes"},
            {"op": "tick", "repeat": 3},
        ],
    },
    {
        "key": "freed",
        "label": "freed capacity",
        "title": "C. Freed capacity is discovered only by the heartbeat, then walks up",
        "blurb": "Nodes drain and 10 slots sit idle -- and nothing happens, because "
                 "nothing this controller watches fires when a node frees up. Only the "
                 "heartbeat finds it. Then the walk-up is one replica per reconcile: "
                 "capacity is lost in one tick and regained one at a time.",
        "total": 40, "free": 6, "ramp": 2, "compare": None,
        "actions": [
            {"op": "tick", "repeat": 5},
            {"op": "preempt", "n": 3, "evt": "preempt -3",
             "note": "PREEMPT 3 -- forces a refusal, starting the backoff"},
            {"op": "tick"},
            {"op": "free", "n": 10, "evt": "free +10",
             "note": "FREE 10 -- nodes drain; nothing notices yet"},
            {"op": "tick"},
            {"op": "hb", "evt": "heartbeat", "note": "heartbeat elapsed -- probe issues"},
            {"op": "tick", "repeat": 5},
        ],
    },
    {
        "key": "bug-gate",
        "label": "bug: status.replicas",
        "title": "D. BUG 1 -- gate on status.replicas instead of spec.replicas",
        "blurb": "Not a runaway: the loop never converges. status lags, so the shortfall "
                 "is never attributed and scav flips between the cold-start ask and "
                 "near-zero every reconcile, with burst moving inversely. Watch the "
                 "ghost sit flat while the live lines thrash.",
        "total": 40, "free": 6, "ramp": 2, "compare": "gate_on_status",
        "actions": [{"op": "tick", "repeat": 40}],
        "tape_rows": 10,
    },
    {
        "key": "bug-unjudged",
        "label": "bug: unjudged probe",
        "title": "E. BUG 2 -- the unjudged probe counted as capacity",
        "blurb": "The window only exists during a walk-up: the probe replica fits, so "
                 "nothing refuses it, but it takes a reconcile to become ready. Counted "
                 "as capacity, it cuts burst one reconcile early -- every step of the "
                 "climb.",
        "total": 40, "free": 6, "ramp": 1, "compare": "count_unjudged",
        "actions": [
            {"op": "tick", "repeat": 7},
            {"op": "free", "n": 14, "evt": "free +14",
             "note": "FREE 14 -- room to walk up into"},
            {"op": "hb", "evt": "heartbeat"},
            {"op": "tick", "repeat": 10},
        ],
    },
    {
        "key": "bug-noheartbeat",
        "label": "bug: no heartbeat",
        "title": "F. BUG 3 -- no heartbeat requeue",
        "blurb": "With no timer, a refusal is never retried. 20 slots sit free and the "
                 "scavenger never finds them.",
        "total": 40, "free": 6, "ramp": 2, "compare": "no_heartbeat",
        "actions": [
            {"op": "tick", "repeat": 4},
            {"op": "preempt", "n": 4, "evt": "preempt -4",
             "note": "PREEMPT 4 -- refusal starts the backoff"},
            {"op": "free", "n": 20, "evt": "free +20",
             "note": "FREE 20 -- 20 slots now sitting idle"},
            {"op": "hb", "evt": "heartbeat", "repeat": 10},
        ],
    },
    {
        "key": "wedge",
        "label": "wedged scheduler",
        "title": "G. A question nobody answers, and the bound that ends it",
        "blurb": "The probe replica is written and then nothing happens to it: no pod "
                 "is created, so none is scheduled and none is refused. Without a "
                 "bound the group would hold at one below its capacity forever, "
                 "spinning on the short requeue and reporting nothing wrong. Watch "
                 "the state sit at probe? for two minutes and then read timeout.",
        "total": 40, "free": 10, "ramp": 2, "compare": None,
        "actions": [
            {"op": "tick", "repeat": 5},
            {"op": "hb", "evt": "heartbeat", "note": "heartbeat elapsed"},
            {"op": "wedge", "evt": "wedge",
             "note": "WEDGE -- quota blocks the pod; no verdict can arrive"},
            {"op": "tick", "repeat": 15},
        ],
        "tape_rows": 12,
    },
]


def run_spec(spec, **toggles):
    """Replay a scenario's action list. The JS `runPreset` mirrors this exactly."""
    s = Sim(THREE_TIER, total=spec["total"], free_slots=spec["free"],
            ramp=spec.get("ramp", 2), **toggles)
    for a in spec["actions"]:
        for _ in range(a.get("repeat", 1)):
            if a["op"] == "preempt":
                s.preempt(a["n"])
            elif a["op"] == "wedge":
                s.wedge()
            elif a["op"] == "unwedge":
                s.unwedge()
            elif a["op"] == "free":
                s.free(a["n"])
            elif a["op"] == "hb":
                s.advance_to_heartbeat()
            s.tick(a.get("note", ""))
    return s


def scenario_text(spec):
    """The terminal tape, plus the comparison a bug scenario is there to make."""
    if not spec["compare"]:
        return run_spec(spec).tape(spec["title"])

    off = run_spec(spec, **{spec["compare"]: False})
    on = run_spec(spec, **{spec["compare"]: True})
    shown = on
    if spec.get("tape_rows"):
        shown = Sim(THREE_TIER, spec["total"], spec["free"])
        shown.rows = on.rows[:spec["tape_rows"]]
    out = shown.tape(spec["title"])

    co, cb = churn(off.rows), churn(on.rows)
    out += (f"\n\n  churn over {len(on.rows)} reconciles   base   scav  burst\n"
            f"    without the bug          {co[0]:>5}  {co[1]:>5}  {co[2]:>5}\n"
            f"    with the bug             {cb[0]:>5}  {cb[1]:>5}  {cb[2]:>5}\n")

    b_off = [r["targets"][2] for r in off.rows]
    b_on = [r["targets"][2] for r in on.rows]
    early = sum(1 for a, b in zip(b_off, b_on) if b < a)
    if early:
        out += (f"\n  burst is below where it should be on {early} of {len(b_off)} "
                f"reconciles.\n  Each is a spot pod killed to fund a question the "
                f"scheduler had not answered.\n")
    out += (f"\n  final scav target: without={off.rows[-1]['targets'][1]}, "
            f"with={on.rows[-1]['targets'][1]}\n")
    return out


# ---------------------------------------------------------------------------
# HTML generation
# ---------------------------------------------------------------------------

import docshell  # noqa: E402
from sim2_page import embed_css, sim_html  # noqa: E402
from sim2_page import sim_js as _sim_js  # noqa: E402


def sim_js(fixtures=None, isolate=True):
    """The page's script, with the fixtures inlined."""
    return _sim_js(page_fixtures() if fixtures is None else fixtures, isolate)


STANDFIRST = (
    "A pool whose cheap tier lives in another tier's spare capacity, stepped "
    "one reconcile at a time in the controller's own order: observe, "
    "<code>decideProbe</code>, <code>capacityFrom</code>, "
    "<code>ComputeGroupTargets</code>, apply. Preempt pods, free compute or "
    "have the scheduler refuse a probe, and watch the machine react. The three "
    "toggles are not hypotheticals &#8212; each one reproduces a bug this "
    "controller actually had: gating on <code>status.replicas</code>, counting "
    "an unjudged probe as capacity, and disabling the heartbeat."
)


def build_page():
    """Sim 2 on a page of its own.

    The widget markup and the fixture payload are the same ones the tutorial
    embeds. Only the surroundings differ, and those come from docshell, so the
    standalone view and the embedded view cannot drift apart. The script stays
    un-isolated here because the standalone page owns the document and the
    console should be able to reach it.
    """
    return docshell.figure_page(
        title="The scavenger under pressure — PodPool Sim 2",
        heading="The scavenger under pressure",
        description="Step a PodPool's opportunistic tier through preemption, "
                    "probe refusals and heartbeat expiry, one reconcile at a "
                    "time.",
        standfirst_html=STANDFIRST,
        widget_html=sim_html(),
        slug="sim2-scavenger.html",
        extra_css=embed_css(),
        extra_js=sim_js(page_fixtures(), isolate=False),
    )


def page_fixtures():
    """The payload both the standalone page and the tutorial embed use,
    assembled once so the two cannot disagree."""
    return {
        "threeTier": THREE_TIER,
        "trace": [[t, w] for t, w in TRACE_FIXTURE],
        "threeGroup": THREE_GROUP,
        "opportunistic": [[t, o, w] for _, t, o, w in OPPORTUNISTIC_FIXTURE],
        "probe": [{"label": lbl, "target": tgt, "obs": obs, "at": at,
                   # awaitVerdict keeps its Go spelling on the wire so the
                   # JS check reads the same field name the source does.
                   "want": want}
                  for lbl, tgt, obs, at, want in PROBE_FIXTURE],
        "probeHeartbeat": PROBE_HEARTBEAT,
        "verdictTimeout": VERDICT_TIMEOUT,
        "verdictRequeue": VERDICT_REQUEUE,
        "rows": FIXTURE_ROWS,
        "tickSeconds": TICK_SECONDS,
        "heartbeat": HEARTBEAT,
        "skip": SKIP,
        "scenarios": SCENARIOS,
    }


def main():
    quiet = "--quiet" in sys.argv

    failures = check_ports()
    if failures:
        print("PORT DRIFT — the Python port disagrees with the Go fixtures:\n")
        for f in failures:
            print("  " + f)
        return 1
    print(f"ports agree with all {FIXTURE_ROWS} fixture rows read from the Go tests")

    if not quiet:
        for spec in SCENARIOS:
            print(scenario_text(spec))
        print()

    here = os.path.dirname(os.path.abspath(__file__))
    out = os.path.join(here, "sim2-scavenger.html")
    with open(out, "w", encoding="utf-8") as fh:
        fh.write(build_page())

    print(f"wrote {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
