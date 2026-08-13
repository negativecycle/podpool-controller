"""Read Sim 2's fixtures and the shape of its port out of the Go.

A simulator that ports controller logic has exactly one interesting failure
mode: the Go moves and the port does not. Hand-copied fixtures do not catch it,
because they move with the port rather than with the source — the first version
of this page shipped a `decideProbe` modelling a machine the controller had
already replaced, and reported that its ports agreed with all 22 fixture rows.
They did. The rows were copies of the old test.

So nothing here is copied. There are two guards, because a port goes stale in
two different ways.

DATA. The distribution tests are genuinely table-shaped in Go, and the probe
lifecycle is a fixed sequence of calls with literal arguments and literal
expectations. Both are parsed out and become the fixtures the Python and
JavaScript ports check themselves against, on every build and on every page
load. Change an expected number in Go and the ports fail until they agree.

SHAPE. Data guards only catch behaviour that a fixture already covers. The
change that actually happened added a field to a struct and a whole new
terminal state, and no existing row exercised it. What can be checked exactly
is the shape of the machine: the fields of `probeState`, `probeDecision` and
`opportunisticObservation`, and the timing constants. The port declares what it
models; this raises if the Go disagrees. Under that rule the change that
started all this fails the build on the day it lands.

Anything this file cannot parse raises ParseError rather than being skipped.
A silent skip is how the first version passed.
"""

import os
import re

from goparse import ParseError, const_expr, func_body, matching_brace, struct_fields

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)

ALGORITHM_TEST = os.path.join("internal", "workload", "algorithm_test.go")
PROBE_TEST = os.path.join("internal", "controller", "probe_test.go")
OPPORTUNISTIC = os.path.join("internal", "controller", "opportunistic.go")

# Go test identifiers this file is willing to resolve. Anything else raises,
# so a renamed group constant is a build failure rather than a wrong fixture.
GROUP_NAMES = {
    "testGroupBase": "base",
    "testGroupScav": "scav",
    "testGroupBurst": "burst",
}


def read(rel):
    with open(os.path.join(REPO, rel), encoding="utf-8") as fh:
        return fh.read()


# ---------------------------------------------------------------------------
# Group specs
# ---------------------------------------------------------------------------

# {Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{...}},
GROUP_LINE = re.compile(
    r"\{(?:Name: (?P<name>\w+), )?Scaling: podpoolsv1alpha1\.ScalingConstraints\{(?P<c>[^}]*)\}\},"
)
MIN = re.compile(r"Min: ptr\.To\[int32\]\((\d+)\)")
MAX = re.compile(r"Max: ptr\.To\[int32\]\((\d+)\)")
TARGET = re.compile(r"Target: pctTarget\((\d+)\)")
OPPORTUNISTIC_RE = re.compile(r"Opportunistic: (?:opportunistic\(\)|ptr\.To\(true\))")


def group_spec(src, fn, fallback_names=()):
    """The []GroupSpec a fixture helper returns, as the sim's dicts.

    fallback_names supplies names for helpers whose groups are positional
    (threeGroupSpec has no Name field, because nothing in Go needs one).
    """
    body = func_body(src, fn)
    out = []
    for i, m in enumerate(GROUP_LINE.finditer(body)):
        if m.group("name"):
            if m.group("name") not in GROUP_NAMES:
                raise ParseError("unknown group constant %r in %s" % (m.group("name"), fn))
            name = GROUP_NAMES[m.group("name")]
        elif i < len(fallback_names):
            name = fallback_names[i]
        else:
            raise ParseError("group %d in %s has no name and no fallback" % (i, fn))

        c, scaling = m.group("c"), {}
        if mm := MIN.search(c):
            scaling["min"] = int(mm.group(1))
        if mm := MAX.search(c):
            scaling["max"] = int(mm.group(1))
        if mm := TARGET.search(c):
            scaling["target"] = "%d%%" % int(mm.group(1))
        if OPPORTUNISTIC_RE.search(c):
            scaling["opportunistic"] = True

        # Every constraint in the source must be one this understands, or the
        # fixture would silently describe a different group.
        leftovers = re.sub(
            r"Min: ptr\.To\[int32\]\(\d+\)|Max: ptr\.To\[int32\]\(\d+\)|"
            r"Target: pctTarget\(\d+\)|Opportunistic: (?:opportunistic\(\)|ptr\.To\(true\))|[\s,]",
            "", c)
        if leftovers:
            raise ParseError("unhandled scaling field %r in %s" % (leftovers, fn))

        out.append({"name": name, "scaling": scaling})
    if not out:
        raise ParseError("no groups parsed from %s" % fn)
    return out


# ---------------------------------------------------------------------------
# The distribution tables
# ---------------------------------------------------------------------------

TRACE_ROW = re.compile(r"^\t\t\{(\d+), (\d+), (\d+), (\d+)\},$", re.M)


def scaling_trace(src):
    """[(total, [targets])] from TestComputeGroupTargetsScalingTrace."""
    body = func_body(src, "TestComputeGroupTargetsScalingTrace")
    rows = [[int(g) for g in m.groups()] for m in TRACE_ROW.finditer(body)]
    if not rows:
        raise ParseError("no rows in TestComputeGroupTargetsScalingTrace")
    return [(r[0], r[1:]) for r in rows]


WANT = re.compile(r"want:\s*\[\]int32\{([\d, ]*)\}")
TOTAL = re.compile(r"total:\s*(\d+)")
NAME = re.compile(r'name:\s*"([^"]*)"')
OBSERVED = re.compile(r"observed:\s*map\[string\]int32\{(\w+): (\d+)\}")


def opportunistic_table(src):
    """[(name, total, observed|None, want)] from TestComputeGroupTargetsOpportunistic."""
    body = func_body(src, "TestComputeGroupTargetsOpportunistic")
    open_brace = body.index("{", body.index("tests := []struct"))
    # The struct definition, then the literal. Skip to the literal's brace.
    lit = body.index("{", matching_brace(body, open_brace) + 1)
    rows_src = body[lit + 1 : matching_brace(body, lit)]

    out, depth, start = [], 0, None
    for i, ch in enumerate(rows_src):
        if ch == "{":
            if depth == 0:
                start = i
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0:
                out.append(rows_src[start : i + 1])
    if not out:
        raise ParseError("no rows in TestComputeGroupTargetsOpportunistic")

    parsed = []
    for row in out:
        name = NAME.search(row)
        total = TOTAL.search(row)
        want = WANT.search(row)
        if not (name and total and want):
            raise ParseError("row missing name/total/want: %r" % row[:80])
        obs = OBSERVED.search(row)
        observed = None
        if obs:
            if obs.group(1) not in GROUP_NAMES:
                raise ParseError("unknown group constant %r in observed" % obs.group(1))
            observed = {GROUP_NAMES[obs.group(1)]: int(obs.group(2))}
        targets = [int(v) for v in want.group(1).split(",") if v.strip()]
        parsed.append((name.group(1), int(total.group(1)), observed, targets))
    return parsed


# ---------------------------------------------------------------------------
# The probe lifecycle
#
# Imperative rather than table-driven, so it is read as a sequence: each
# decideProbe call with its literal arguments, then the `if` that follows,
# whose condition is the negation of what the step expects.
# ---------------------------------------------------------------------------

OBS_DECL = re.compile(r"^\t(\w+) := opportunisticObservation\{([^}]*)\}$", re.M)
OBS_FIELD = re.compile(r"(\w+): (true|false|\d+)")

CALL = re.compile(
    r"^\td :?= r\.decideProbe\(pool, (\w+), (\d+), (\w+), (.+?)\)\n"
    r"\tif ([^\n]+?) \{$",
    re.M,
)

# The moments a probe step is stamped with: now, now.Add(2*time.Second), and
# afterHeartbeat.Add(time.Second), where afterHeartbeat is itself
# now.Add(2 * time.Minute) -- which is why _seconds has to resolve a name
# before it can add to it. gofmt spaces binary operators at statement level
# but not inside a call argument, so both spellings occur.
DURATION = re.compile(r"(?:(\d+)\s*\*\s*)?time\.(Second|Minute)")
UNITS = {"Second": 1, "Minute": 60}

# d.target != 5 | !d.awaitVerdict | d.issued
CHECK = re.compile(r"(!)?d\.(\w+)(?: != (\d+))?")


def _duration(inner, expr):
    """The seconds named by a `.Add()` argument."""
    total, consumed = 0, 0
    for d in DURATION.finditer(inner):
        total += int(d.group(1) or 1) * UNITS[d.group(2)]
        consumed += len(d.group(0))
    # Only the durations and the separators between them may remain.
    if re.sub(r"(?:\d+\s*\*\s*)?time\.(?:Second|Minute)|[\s+]", "", inner):
        raise ParseError("unhandled term in probe timestamp %r" % expr)
    if not consumed:
        raise ParseError("no duration in %r" % expr)
    return total


def _seconds(expr, body="", _seen=None):
    """Evaluate a probe timestamp to seconds since the base clock.

    `now` is the base. Anything else is either an .Add() on another moment or
    the name of one: the test is free to name a moment it uses twice
    (`afterHeartbeat := now.Add(2 * time.Minute)`) rather than spelling it out
    at each call, so a name is resolved from its assignment in the same
    function body.
    """
    expr = expr.strip()
    if expr == "now":
        return 0

    m = re.fullmatch(r"(\w+)\.Add\((.+)\)", expr)
    if m:
        return _seconds(m.group(1), body, _seen) + _duration(m.group(2), expr)

    if re.fullmatch(r"\w+", expr):
        _seen = _seen or set()
        if expr in _seen:
            raise ParseError("probe timestamp %r is defined in terms of itself" % expr)
        _seen.add(expr)
        a = re.search(r"^\t%s :?= (.+)$" % re.escape(expr), body, re.M)
        if not a:
            raise ParseError("cannot evaluate probe timestamp %r" % expr)
        return _seconds(a.group(1), body, _seen)

    raise ParseError("cannot evaluate probe timestamp %r" % expr)


def _expectations(cond):
    """The `if` guarding a Fatalf is the negation of the step's expectation."""
    want = {}
    for part in cond.split("||"):
        m = CHECK.fullmatch(part.strip())
        if not m:
            raise ParseError("cannot read probe expectation %r" % part.strip())
        neg, field, num = m.groups()
        if num is not None:
            want[field] = int(num)          # `d.target != 5` fails unless target == 5
        else:
            want[field] = bool(neg)         # `!d.awaitVerdict` fails unless it is true
    return want


def probe_lifecycle(src):
    """[(label, target, obs, at_seconds, expectations)] from TestDecideProbeLifecycle."""
    body = func_body(src, "TestDecideProbeLifecycle")

    observations = {}
    for m in OBS_DECL.finditer(body):
        fields = {}
        for f in OBS_FIELD.finditer(m.group(2)):
            k, v = f.group(1), f.group(2)
            fields[k] = True if v == "true" else False if v == "false" else int(v)
        observations[m.group(1)] = {
            "found": fields.get("found", False),
            "asked": fields.get("asked", 0),
            "ready": fields.get("ready", 0),
            "unschedulable": fields.get("unschedulable", 0),
        }
    if not observations:
        raise ParseError("no opportunisticObservation literals in TestDecideProbeLifecycle")

    steps = []
    for m in CALL.finditer(body):
        group, target, obs_name, at, cond = m.groups()
        if group not in GROUP_NAMES:
            raise ParseError("unknown group constant %r in decideProbe call" % group)
        if obs_name not in observations:
            raise ParseError("decideProbe called with unknown observation %r" % obs_name)
        steps.append((obs_name, int(target), observations[obs_name],
                      _seconds(at, body), _expectations(cond)))
    if not steps:
        raise ParseError("no decideProbe calls found in TestDecideProbeLifecycle")
    return steps


def probe_heartbeat(src):
    """The heartbeat TestDecideProbeLifecycle configures, in seconds."""
    m = re.search(r"pool := probePool\((\d+)\)", func_body(src, "TestDecideProbeLifecycle"))
    if not m:
        raise ParseError("TestDecideProbeLifecycle does not call probePool with a literal")
    return int(m.group(1))


def duration_seconds(expr):
    """`2 * time.Minute` -> 120. Raises on anything else, so a constant that
    grows a term is a build failure rather than a silently wrong number."""
    total, seen = 0, False
    for d in DURATION.finditer(expr):
        total += int(d.group(1) or 1) * UNITS[d.group(2)]
        seen = True
    if not seen or re.sub(r"(?:\d+\s*\*\s*)?time\.(?:Second|Minute)|[\s+]", "", expr):
        raise ParseError("cannot read %r as a duration" % expr)
    return total


# ---------------------------------------------------------------------------
# The shape guard
# ---------------------------------------------------------------------------

def check_shape(modelled_fields, modelled_consts):
    """Raise unless the Go's structs and constants are the ones being modelled.

    modelled_fields maps a Go type name to the field list the port implements;
    modelled_consts maps a constant name to its expected right-hand side. A
    field the port does not implement is the failure this exists for: it is a
    piece of the machine the simulator would otherwise pretend does not exist.
    """
    src = read(OPPORTUNISTIC)

    for typename, expected in modelled_fields.items():
        actual = struct_fields(src, typename)
        if actual != list(expected):
            raise ParseError(
                "%s has fields %s, but the port models %s.\n"
                "The simulator is describing a machine the controller no longer runs; "
                "port the change before regenerating." % (typename, actual, list(expected)))

    for name, expected in modelled_consts.items():
        actual = const_expr(src, name)
        if actual != expected:
            raise ParseError(
                "const %s is %r, but the port assumes %r" % (name, actual, expected))


# ---------------------------------------------------------------------------
# Everything, as one bundle
# ---------------------------------------------------------------------------

def load(modelled_fields, modelled_consts):
    check_shape(modelled_fields, modelled_consts)

    algo = read(ALGORITHM_TEST)
    probe = read(PROBE_TEST)
    opp = read(OPPORTUNISTIC)

    return {
        # Read rather than restated, so a retuned constant reaches the page.
        "verdict_timeout": duration_seconds(const_expr(opp, "probeVerdictTimeout")),
        "verdict_requeue": duration_seconds(const_expr(opp, "probeVerdictRequeue")),
        "default_heartbeat": int(const_expr(opp, "defaultOpportunisticHeartbeatSeconds")),
        "three_group": group_spec(algo, "threeGroupSpec", ("base", "scav", "burst")),
        "three_tier": group_spec(algo, "threeTierSpec"),
        "trace": scaling_trace(algo),
        "opportunistic": opportunistic_table(algo),
        "probe": probe_lifecycle(probe),
        "probe_heartbeat": probe_heartbeat(probe),
    }


if __name__ == "__main__":
    import json

    data = load(
        {"probeState": ["outstanding", "lastFailed", "startedAt"],
         "probeDecision": ["target", "issued", "awaitVerdict", "abandoned"],
         "opportunisticObservation": ["found", "foreign", "asked", "ready", "unschedulable"]},
        {"probeVerdictTimeout": "2 * time.Minute",
         "probeVerdictRequeue": "15 * time.Second",
         "defaultOpportunisticHeartbeatSeconds": "300"},
    )
    print(json.dumps(data, indent=2, default=str))
