#!/usr/bin/env python3
"""Builds docs/tutorial-v2.html from the `tutorial-v2` branch's milestone-NN tags.

The v2 series is milestone-structured: tags mark milestones, each a multi-commit
sequence, and the document has three layers by construction. Milestone intros
are the skim path, the commit story is the middle layer, and anything deeper
folds behind <details>.

Anti-drift, twice over. Every code figure is read out of git at a t2 tag, so it
cannot drift from the branch. And every commit named in this script is matched
against the real `git log` for its milestone; a title that does not match the
branch fails the build, so the story cannot drift from the history either.

    python3 docs/build_tutorial_v2_doc.py

Fragment helpers, the stylesheet, and the page skeleton come from docshell.py,
which every page in docs/ renders through. Sim 1 is embedded from
sim1_distribution.py and Sim 2 from sim2_prototype.py.
"""
import html
import html.parser
import os
import pathlib
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import sim1_distribution as sim1  # noqa: E402
import sim2_prototype as sim2  # noqa: E402
from docshell import (  # noqa: E402
    at, block, checkpoint, excerpt, git, p, page, shell, table)

ROOT = pathlib.Path(__file__).resolve().parent.parent
OUT = ROOT / "docs" / "tutorial-v2.html"

SERIES = "milestone-"


def tag(n):
    return f"{SERIES}{n:02d}"


# Path constants for the t2 series, reflecting the internal/workload package
# split.
TYPES = "api/v1alpha1/podpool_types.go"
ALGO = "internal/workload/algorithm.go"
CTRL = "internal/controller/podpool_controller.go"
HOOK = "internal/webhook/v1alpha1/podpool_webhook.go"


# --------------------------------------------------------------------------
# The deepest layer: figures, folds, and the links between the two views
# --------------------------------------------------------------------------

def standalone(href, label):
    """A link from an embedded figure to its own page.

    Both views render the same widget markup through docshell; this is only the
    signpost between them, for a reader who wants the figure full-width or a
    URL to point somebody at.
    """
    return (f'<p class="standalone"><a href="{href}">'
            f'Open {html.escape(label)} on its own page &#8594;</a></p>')


# What is behind a fold, said before it is opened. A reader skimming 162 closed
# folds was being asked to guess from the summary alone whether the next one
# holds a listing, an argument, a measurement, or something they must not run.
# The kind is a closed set and the build enforces it, so a new fold has to
# declare which of these it is rather than joining an unlabelled majority:
#
#   code        read out of git at the milestone's tag
#   commit      the author's own words, verbatim
#   why         the argument -- what was rejected, and what forced it
#   evidence    something measured, mutated, or otherwise proven
#   background  orientation for a reader who lacks the prerequisite
#   trouble     the failure reachable here, and its fix
#   caution     here for the record; running it is the wrong move
FOLD_KINDS = {
    "code":       "code",
    "commit":     "commit message",
    "why":        "why",
    "evidence":   "evidence",
    "background": "background",
    "trouble":    "trouble",
    "caution":    "do not run",
}


def fold(kind, summary, body_html):
    """Optional depth, labelled by kind.

    Folded by default; print CSS and a beforeprint hook render folds open so
    the content is skippable, never unreachable.
    """
    label = FOLD_KINDS[kind]              # KeyError is the build failure
    return (f'<details class="fold fold-{kind}"><summary>'
            f'<span class="fold-kind">{label}</span>{summary}</summary>'
            f'<div class="fold-body">{body_html}</div></details>')


# --------------------------------------------------------------------------
# Outward references
# --------------------------------------------------------------------------
#
# Where to read the upstream rule a commit is applying. Every commit here
# argues about somebody else's contract -- an API convention, a marker, a
# merge algorithm -- and the argument is only checkable if the reader can get
# to the contract. So the citations are declared once, by key, and cited from
# the commits: one page is never linked under two names, and a URL that rots
# is repaired in one place rather than in however many summaries happened to
# mention it.
#
# They are links, not requests: the page still fetches nothing at render time.
REFS = {
    # kubebuilder
    "kb_quickstart": ("https://book.kubebuilder.io/quick-start",
                      "kubebuilder: quick start"),
    "kb_architecture": ("https://book.kubebuilder.io/architecture",
                        "kubebuilder: project architecture"),
    "kb_gen_crd": ("https://book.kubebuilder.io/reference/generating-crd",
                   "kubebuilder: generating CRDs"),
    "kb_envtest": ("https://book.kubebuilder.io/reference/envtest",
                   "kubebuilder: envtest"),
    "kb_markers_crd": ("https://book.kubebuilder.io/reference/markers/crd",
                       "kubebuilder: CRD markers"),
    "kb_markers_val": (
        "https://book.kubebuilder.io/reference/markers/crd-validation",
        "kubebuilder: CRD validation markers"),
    "kb_markers_webhook": (
        "https://book.kubebuilder.io/reference/markers/webhook",
        "kubebuilder: webhook markers"),
    "kb_webhook": (
        "https://book.kubebuilder.io/cronjob-tutorial/webhook-implementation",
        "kubebuilder: implementing a webhook"),
    "kb_metrics": ("https://book.kubebuilder.io/reference/metrics",
                   "kubebuilder: metrics"),

    # Kubernetes: the API surface this CRD is built on
    "k8s_crd_cel": (
        "https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/"
        "custom-resource-definitions/#validation-rules",
        "Kubernetes: CEL validation rules"),
    "k8s_crd_ratchet": (
        "https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/"
        "custom-resource-definitions/#validation-ratcheting",
        "Kubernetes: validation ratcheting"),
    "k8s_crd_scale": (
        "https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/"
        "custom-resource-definitions/#scale-subresource",
        "Kubernetes: the scale subresource"),
    "k8s_crd_status": (
        "https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/"
        "custom-resource-definitions/#status-subresource",
        "Kubernetes: the status subresource"),
    "k8s_crd_cols": (
        "https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/"
        "custom-resource-definitions/#additional-printer-columns",
        "Kubernetes: additional printer columns"),
    "k8s_explain": (
        "https://kubernetes.io/docs/reference/kubectl/generated/kubectl_explain/",
        "kubectl explain"),

    # Kubernetes: apply, ownership, deletion
    "k8s_ssa": ("https://kubernetes.io/docs/reference/using-api/server-side-apply/",
                "Kubernetes: server-side apply"),
    "k8s_ssa_merge": (
        "https://kubernetes.io/docs/reference/using-api/server-side-apply/"
        "#merge-strategy",
        "Kubernetes: list merge strategy"),
    "k8s_ssa_fields": (
        "https://kubernetes.io/docs/reference/using-api/server-side-apply/"
        "#field-management",
        "Kubernetes: field management"),
    "k8s_controller": (
        "https://kubernetes.io/docs/concepts/architecture/controller/",
        "Kubernetes: controllers and control loops"),
    "k8s_deployment": (
        "https://kubernetes.io/docs/concepts/workloads/controllers/deployment/",
        "Kubernetes: Deployment"),
    "k8s_assign_node": (
        "https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/",
        "Kubernetes: assigning pods to nodes"),
    "k8s_taints": (
        "https://kubernetes.io/docs/concepts/scheduling-eviction/"
        "taint-and-toleration/",
        "Kubernetes: taints and tolerations"),
    "kubectl_wait": (
        "https://kubernetes.io/docs/reference/kubectl/generated/kubectl_wait/",
        "kubectl wait"),
    "k8s_gc": ("https://kubernetes.io/docs/concepts/architecture/garbage-collection/",
               "Kubernetes: garbage collection"),
    "k8s_gc_fg": (
        "https://kubernetes.io/docs/concepts/architecture/garbage-collection/"
        "#foreground-deletion",
        "Kubernetes: foreground cascading deletion"),

    # Kubernetes: naming, labels, scheduling
    "k8s_dnslabel": (
        "https://kubernetes.io/docs/concepts/overview/working-with-objects/names/"
        "#dns-label-names",
        "Kubernetes: DNS label names"),
    "k8s_labels": (
        "https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/",
        "Kubernetes: labels and selectors"),
    "k8s_fieldsel": (
        "https://kubernetes.io/docs/concepts/overview/working-with-objects/"
        "field-selectors/",
        "Kubernetes: field selectors"),
    "k8s_spread": (
        "https://kubernetes.io/docs/concepts/scheduling-eviction/"
        "topology-spread-constraints/",
        "Kubernetes: topology spread constraints"),
    "k8s_affinity": (
        "https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/"
        "#affinity-and-anti-affinity",
        "Kubernetes: affinity and anti-affinity"),
    "k8s_priority": (
        "https://kubernetes.io/docs/concepts/scheduling-eviction/"
        "pod-priority-preemption/",
        "Kubernetes: pod priority and preemption"),
    "k8s_scheduler": (
        "https://kubernetes.io/docs/concepts/scheduling-eviction/kube-scheduler/",
        "Kubernetes: the scheduler"),
    "k8s_pod_conditions": (
        "https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/"
        "#pod-conditions",
        "Kubernetes: pod conditions"),
    "k8s_pod_termination": (
        "https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/"
        "#pod-termination",
        "Kubernetes: pod termination"),

    # Kubernetes: the workload kinds this controller renders
    "k8s_hpa": (
        "https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/",
        "Kubernetes: horizontal pod autoscaling"),
    "k8s_maxsurge": (
        "https://kubernetes.io/docs/concepts/workloads/controllers/deployment/"
        "#max-surge",
        "Kubernetes: maxSurge"),
    "k8s_deadline": (
        "https://kubernetes.io/docs/concepts/workloads/controllers/deployment/"
        "#progress-deadline-seconds",
        "Kubernetes: progressDeadlineSeconds"),
    "k8s_deploy_selector": (
        "https://kubernetes.io/docs/concepts/workloads/controllers/deployment/"
        "#selector",
        "Kubernetes: a Deployment's selector"),
    "k8s_sts_netid": (
        "https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/"
        "#stable-network-id",
        "Kubernetes: StatefulSet network identity"),

    # Kubernetes: admission, authorization, operations
    "k8s_admission": (
        "https://kubernetes.io/docs/reference/access-authn-authz/"
        "extensible-admission-controllers/",
        "Kubernetes: admission webhooks"),
    "k8s_authz_check": (
        "https://kubernetes.io/docs/reference/access-authn-authz/authorization/"
        "#checking-api-access",
        "Kubernetes: checking API access"),
    "k8s_sar": (
        "https://kubernetes.io/docs/reference/kubernetes-api/"
        "authorization-resources/subject-access-review-v1/",
        "Kubernetes: SubjectAccessReview"),
    "k8s_rbac": ("https://kubernetes.io/docs/reference/access-authn-authz/rbac/",
                 "Kubernetes: RBAC"),
    "k8s_rbac_agg": (
        "https://kubernetes.io/docs/reference/access-authn-authz/rbac/"
        "#aggregated-clusterroles",
        "Kubernetes: aggregated ClusterRoles"),
    "k8s_rbac_userfacing": (
        "https://kubernetes.io/docs/reference/access-authn-authz/rbac/"
        "#user-facing-roles",
        "Kubernetes: user-facing roles"),
    "k8s_rbac_escalation": (
        "https://kubernetes.io/docs/reference/access-authn-authz/rbac/"
        "#privilege-escalation-prevention-and-bootstrapping",
        "Kubernetes: privilege escalation prevention"),
    "k8s_apf": (
        "https://kubernetes.io/docs/concepts/cluster-administration/flow-control/",
        "Kubernetes: API priority and fairness"),
    "k8s_probes": (
        "https://kubernetes.io/docs/concepts/configuration/"
        "liveness-readiness-startup-probes/",
        "Kubernetes: readiness and liveness probes"),
    "k8s_events": (
        "https://kubernetes.io/docs/reference/kubernetes-api/cluster-resources/"
        "event-v1/",
        "Kubernetes: the Event API"),
    "k8s_api_conventions": (
        "https://github.com/kubernetes/community/blob/master/contributors/devel/"
        "sig-architecture/api-conventions.md#typical-status-properties",
        "Kubernetes API conventions: status properties"),
    "k8s_logging": (
        "https://github.com/kubernetes/community/blob/master/contributors/devel/"
        "sig-instrumentation/migration-to-structured-logging.md",
        "Kubernetes: structured logging conventions"),
    "kstatus": (
        "https://github.com/kubernetes-sigs/cli-utils/blob/master/pkg/kstatus/"
        "README.md",
        "kstatus: computing a resource's readiness"),

    # Go and the libraries this controller is written against
    "go_errorf": ("https://pkg.go.dev/fmt#Errorf", "godoc: fmt.Errorf and %w"),
    "go_errors_as": ("https://pkg.go.dev/errors#As", "godoc: errors.As"),
    "go_race": ("https://go.dev/doc/articles/race_detector",
                "Go: the race detector"),
    "api_rawextension": (
        "https://pkg.go.dev/k8s.io/apimachinery/pkg/runtime#RawExtension",
        "godoc: runtime.RawExtension"),
    "api_unstructured": (
        "https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1/unstructured",
        "godoc: unstructured"),
    "api_setcondition": (
        "https://pkg.go.dev/k8s.io/apimachinery/pkg/api/meta#SetStatusCondition",
        "godoc: meta.SetStatusCondition"),
    "api_restmapper": (
        "https://pkg.go.dev/k8s.io/apimachinery/pkg/api/meta#RESTMapper",
        "godoc: meta.RESTMapper"),
    "api_isnotfound": (
        "https://pkg.go.dev/k8s.io/apimachinery/pkg/api/errors#IsNotFound",
        "godoc: apierrors.IsNotFound"),
    "api_intstr": ("https://pkg.go.dev/k8s.io/apimachinery/pkg/util/intstr",
                   "godoc: intstr.IntOrString"),
    "cr_reconcile": (
        "https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/reconcile",
        "godoc: the Reconciler contract"),
    "cr_root": ("https://pkg.go.dev/sigs.k8s.io/controller-runtime",
                "godoc: controller-runtime"),
    "api_namespacedname": (
        "https://pkg.go.dev/k8s.io/apimachinery/pkg/types#NamespacedName",
        "godoc: types.NamespacedName"),
    "k8s_api_concepts": (
        "https://kubernetes.io/docs/reference/using-api/api-concepts/"
        "#efficient-detection-of-changes",
        "Kubernetes: watches and resourceVersion"),
    "cr_fake": (
        "https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/client/fake",
        "godoc: the fake client"),
    "cr_preconditions": (
        "https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/client#Preconditions",
        "godoc: delete preconditions"),
    "cr_enqueue_owner": (
        "https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/handler"
        "#EnqueueRequestForOwner",
        "godoc: handler.EnqueueRequestForOwner"),
    "cr_predicate": (
        "https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/predicate",
        "godoc: event predicates"),
    "cr_source_kind": (
        "https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/source#Kind",
        "godoc: source.Kind"),
    "cr_cache": (
        "https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/cache#Options",
        "godoc: cache options"),
    "cr_cache_pkg": (
        "https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/cache",
        "godoc: the informer cache"),
    "cr_builder": (
        "https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/builder",
        "godoc: the controller builder"),
    "cr_reader": (
        "https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/client#Reader",
        "godoc: client.Reader, cached and not"),
    "cr_manager": (
        "https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/manager#Options",
        "godoc: manager options"),
    "cg_recorder": (
        "https://pkg.go.dev/k8s.io/client-go/tools/record#EventRecorder",
        "godoc: the EventRecorder"),
    "cg_rest": ("https://pkg.go.dev/k8s.io/client-go/rest#Config",
                "godoc: client QPS and burst"),
    "cg_discovery": ("https://pkg.go.dev/k8s.io/client-go/discovery",
                     "godoc: discovery"),

    # Everything else this repository leans on
    "rfc7386": ("https://www.rfc-editor.org/rfc/rfc7386", "RFC 7386: JSON merge patch"),
    "kwok": ("https://kwok.sigs.k8s.io/", "KWOK"),
    "kind": ("https://kind.sigs.k8s.io/", "Kind"),
    "certmanager": ("https://cert-manager.io/docs/", "cert-manager"),
    "prom_naming": ("https://prometheus.io/docs/practices/naming/",
                    "Prometheus: metric naming"),
    "prom_instr": ("https://prometheus.io/docs/practices/instrumentation/",
                   "Prometheus: instrumentation practices"),
    "golangci": ("https://golangci-lint.run/docs/linters/",
                 "golangci-lint: the linters"),
    "govulncheck": ("https://go.dev/blog/govulncheck", "govulncheck"),
    "trivy": ("https://trivy.dev/", "Trivy"),
    "hadolint": ("https://github.com/hadolint/hadolint", "hadolint"),
    "actionlint": ("https://github.com/rhysd/actionlint", "actionlint"),
    "gh_hardening": (
        "https://docs.github.com/en/actions/security-for-github-actions/"
        "security-guides/security-hardening-for-github-actions",
        "GitHub: hardening Actions workflows"),
}

# Only documentation, and only over TLS. A citation is a claim about where the
# rule lives, so the set of places it may live is written down rather than left
# to whoever adds the next one.
REF_HOSTS = {
    "book.kubebuilder.io", "kubernetes.io", "github.com", "docs.github.com",
    "pkg.go.dev", "go.dev", "www.rfc-editor.org", "kwok.sigs.k8s.io",
    "kind.sigs.k8s.io", "cert-manager.io", "prometheus.io",
    "golangci-lint.run", "trivy.dev",
}

_ref_cited = set()


def ref(*keys):
    """The upstream pages a commit is standing on.

    An unknown key is a KeyError at import, which is the point: the citation
    and the link it resolves to cannot drift apart, because there is only one
    of each.
    """
    links = []
    for k in keys:
        href, label = REFS[k]
        _ref_cited.add(k)
        links.append(f'<a href="{href}" rel="noopener">{html.escape(label)}</a>')
    joined = '<span class="refs-sep">&middot;</span>'.join(links)
    return (f'<p class="refs"><span class="refs-label">Reference</span>'
            f'{joined}</p>')


# Bare identifiers in prose that are deliberately not fields of this API. The
# check below sees every one of them, so each has to be named here -- which
# makes this list the inventory of the single-word lowercase names the document
# sets in code type and does not mean as ours. Anything capitalised, hyphenated
# or spaced is outside the scan, and so are the figures.
API_FIELD_ALLOW = {
    "base", "burst", "scavenger",                               # example groups
    "capacityFrom", "observeOpportunistic", "sweepAllOrphans",  # our own Go
    "maxSurge", "nodeSelector",                                 # other kinds'
    "make", "kwokctl", "scale", "status", "etcd",               # tools, subresources
    "toolchain",                                                # a go.mod directive
}


def check_api_fields(doc):
    """Prose names no field the API lacks, and names every constraint it has.

    Two directions, because the orientation table got both wrong at once: it
    invented `maxRatio` and `minRatio`, and it omitted `target` and
    `opportunistic`, the two the series is actually about. A forward-only
    check sees the invention and is blind to the omission.

    Nothing here asks the author for anything. A name is checked because it is
    in the document, not because somebody routed it through a helper -- the
    prose that has been wrong is exactly the prose nobody thought to flag, and
    a guard you have to remember to use cannot catch the bug that comes from
    not remembering. Figures are exempt: they are read out of git, so they
    cannot invent a field, and they legitimately name other kinds' fields.
    """
    fields = set(re.findall(r'json:"(\w+)', at(tag(12), TYPES)))
    prose = re.sub(
        r'<script.*?</script>|<nav class="rail".*?</nav>|<pre[^>]*>.*?</pre>',
        " ", doc, flags=re.S)
    bad = [f"prose names {n!r} in code type, which the API does not declare at "
           f"{tag(12)}; add it to API_FIELD_ALLOW if it is not meant to be ours"
           for n in sorted(
               {s for s in re.findall(r"<code>([a-z][A-Za-z0-9]*)</code>", prose)
                if s not in fields and s not in API_FIELD_ALLOW})]

    # The other direction, and only for the one table that claims to enumerate:
    # a reader meets it before anything else, so a constraint missing from it is
    # a constraint they will not know exists.
    declared = set(re.findall(
        r'json:"(\w+)', excerpt(tag(12), TYPES, "type ScalingConstraints struct")))
    if m := re.search(r'<section id="start">.*?(<table>.*?</table>)', doc, re.S):
        if missing := sorted(f for f in declared
                             if f"<code>{f}</code>" not in m.group(1)):
            bad.append("the orientation table never names " + ", ".join(missing)
                       + f", which ScalingConstraints declares at {tag(12)}")
    else:
        bad.append("no table in the orientation section: the reverse check has "
                   "nothing to read, which is itself the drift")
    return bad


def check_markup(doc):
    """Every tag the prose opens is closed, and nothing closes twice.

    The prose in this file is hand-written HTML, so a literal angle bracket in
    a sentence is a tag as far as a browser is concerned -- and an unknown
    element is dropped silently, taking the text that looked like its name with
    it. That failure is invisible in the source and invisible in the rendered
    page, which is the combination worth a guard.
    """
    void = {"area", "base", "br", "col", "embed", "hr", "img", "input", "link",
            "meta", "source", "track", "wbr"}
    stack, bad = [], []

    class Scan(html.parser.HTMLParser):
        def handle_starttag(self, tag, attrs):
            if tag not in void:
                stack.append(tag)

        def handle_endtag(self, tag):
            if tag in void:
                return
            if tag not in stack:
                bad.append(f"</{tag}> closes nothing")
                return
            while stack[-1] != tag:
                bad.append(f"<{stack.pop()}> is never closed")
            stack.pop()

    s = Scan(convert_charrefs=True)
    s.feed(doc)
    bad += [f"<{t}> is never closed" for t in stack]

    # Both ends of a stylesheet comment eat a rule, for different reasons, and
    # CSS fails silently by design -- so the page still renders, just without
    # whatever was eaten. An opener that never closes swallows every rule until
    # the next terminator; a terminator with no opener becomes the head of the
    # following selector, which is then invalid and takes its whole block with
    # it. The second of those is the bug this was written for, and it is the
    # third in this toolchain invisible in the source and invisible in the
    # page, after four colour tokens that were never declared and a note
    # element the parser reparented.
    #
    # Scanned, not counted: CSS comments do not nest, so a `/*` inside a
    # comment is text. Counting depth would fail the build on a valid sheet the
    # first time a comment mentioned one.
    for css in re.findall(r"<style[^>]*>(.*?)</style>", doc, re.S):
        i = 0
        while True:
            opened, stray = css.find("/*", i), css.find("*/", i)
            if stray != -1 and (opened == -1 or stray < opened):
                bad.append("stylesheet: a comment closes that never opened, "
                           "which makes a selector of the rule that follows")
                break
            if opened == -1:
                break
            closed = css.find("*/", opened + 2)
            if closed == -1:
                bad.append("stylesheet: a comment never closes, which silently "
                           "eats every rule that follows")
                break
            i = closed + 2
    return bad


def check_refs():
    """Every citation points somewhere this document is willing to point, no
    page is linked under two names, and every declaration is cited.

    Nothing here reaches the network. That the URL is live is checked when it
    is added; that it is the kind of URL this document cites is checked on
    every build.
    """
    bad = []
    seen = {}
    for k, (href, label) in REFS.items():
        if not href.startswith("https://"):
            bad.append(f"{k}: not https ({href})")
            continue
        host = href.split("/", 3)[2]
        if host not in REF_HOSTS:
            bad.append(f"{k}: {host} is not a documentation host")
        if href in seen:
            bad.append(f"{k} and {seen[href]} are the same page under two names")
        seen[href] = k
    if dead := sorted(set(REFS) - _ref_cited):
        bad.append("declared but never cited: " + ", ".join(dead))
    return bad


# --------------------------------------------------------------------------
# Vocabulary
# --------------------------------------------------------------------------
#
# Terms that arrive cold. Each is (term, margin gloss, full definition, the
# pattern that finds its first use) -- and the marks are placed by the build
# rather than by hand, so "first use" stays true when the prose moves. The
# margin expands and signals; the glossary at the end explains. Nothing is
# added to the sentence itself: the note's first line is the term, aligned to
# the paragraph that introduces it, which is what connects the two.
GLOSSARY = {
    "envtest": (
        "envtest", "a real apiserver and etcd, no scheduler, no kubelet",
        p("A real <code>kube-apiserver</code> and <code>etcd</code> started by "
          "the test process itself, with no scheduler, no kubelet and no "
          "controller-manager. Objects are created, defaulted, validated and "
          "watched for real; nothing ever runs a pod, and nothing ever "
          "garbage-collects one. That single absence is why milestone 07 "
          "needs KWOK and milestone 12 needs Kind.")
        + ref("kb_envtest"), r"envtest"),

    "kwok": (
        "KWOK", "Kubernetes WithOut Kubelet: a real scheduler over fake nodes",
        p("Kubernetes WithOut Kubelet: a real apiserver and a real scheduler "
          "over simulated nodes that accept pods and never run containers. It "
          "answers the one question envtest structurally cannot &mdash; "
          "<em>does this replica fit</em> &mdash; which is the whole basis of "
          "milestone 07.")
        + ref("kwok"), r"KWOK|kwok"),

    "kind": (
        "Kind", "the tool. also an API kind, and source.Kind",
        p("Kubernetes IN Docker: a real cluster, every component included, "
          "inside a container. The heaviest tier and the last one; milestone "
          "12 is the first time the deployed manager, a cert-manager-issued "
          "webhook certificate and the API server's garbage collector are all "
          "real at once.")
        + p("The word is overloaded in this document and upstream. A "
            "<em>kind</em> is also the type name in a GVK &mdash; "
            "<code>Deployment</code>, <code>StatefulSet</code> &mdash; and "
            "<code>source.Kind</code> is controller-runtime's watch source. "
            "Capitalisation does not disambiguate them; context does.")
        + ref("kind"), r"\bKind\b"),

    "subresource": (
        "subresource", "a sub-path with its own endpoint and its own RBAC",
        p("A sub-path on an object with its own endpoint and its own RBAC "
          "verbs: <code>/status</code> and <code>/scale</code> here. A write "
          "to one does not touch spec and does not bump "
          "<code>metadata.generation</code>, which is what lets the controller "
          "publish counts without clobbering a user's edit, and what lets an "
          "HPA scale a pool without being handed its spec.")
        + ref("k8s_crd_status", "k8s_crd_scale"), r"subresource"),

    "cel": (
        "CEL", "expressions in the CRD, run by the API server itself",
        p("Common Expression Language: expressions embedded in the CRD schema "
          "and evaluated by the API server. They run when this controller is "
          "down and they apply to clusters that never installed it. Each rule "
          "is priced against the maximum size of what it can see, which is why "
          "milestone 01's caps commit must land before its CEL commit.")
        + ref("k8s_crd_cel"), r"\bCEL\b"),

    "ssa": (
        "server-side apply", "the API server records who owns each field",
        p("Often <em>SSA</em>. The API server records which <em>field "
          "manager</em> owns each individual field. An apply sends only the "
          "fields you claim, and conflicts are detected per field rather than "
          "per object &mdash; so an external edit to a field the controller "
          "renders is reverted on the next pass, while a field the render "
          "never mentions stays whoever else's it is.")
        + ref("k8s_ssa", "k8s_ssa_fields"), r"server-side apply|\bSSA\b"),

    "gvk": (
        "GVK", "group, version, kind: the identity of an API type",
        p("Group, Version, Kind &mdash; the three-part identity of an API "
          "type, such as <code>apps/v1</code> <code>Deployment</code>. It is "
          "what the scheme is keyed on, what a watch is registered for, and "
          "what admission must resolve to a plural <em>resource</em> before it "
          "can ask whether anyone may create one."), r"\bGVK\b"),

    "unstructured": (
        "unstructured", "an object held as a map, not a Go struct",
        p("A Kubernetes object held as <code>map[string]any</code> rather than "
          "a Go struct, so a kind this binary was never compiled against "
          "round-trips without losing fields. It is why the controller can "
          "render a CRD it has no types for, and why milestone 09 can only "
          "type-check the built-in kinds.")
        + ref("api_unstructured"), r"unstructured"),

    "informer": (
        "informer", "one list-and-watch per kind, feeding the cache",
        p("The cache's per-kind machinery: one list-and-watch against the API "
          "server, feeding a local store and any event handlers attached to "
          "it. <em>Registering a watch</em> means attaching a handler to an "
          "informer, and <em>cache sync</em> is that store finishing its "
          "initial list. Most of milestone 06 is ways this can look "
          "successful while delivering nothing.")
        + ref("cr_cache_pkg", "k8s_api_concepts"), r"informer"),

    "sar": (
        "SubjectAccessReview", "asking the cluster's own authorizer, as the user",
        p("An API call asking the cluster's own authorizer <em>may this user "
          "do this verb on this resource</em>, answered by the same RBAC that "
          "would judge the real request &mdash; the same question "
          "<code>kubectl auth can-i</code> asks. It names a <em>resource</em>, "
          "plural and lowercase, not a Kind, which is the entire subject of "
          "09.6.")
        + ref("k8s_sar", "k8s_authz_check"), r"SubjectAccessReview|\bSAR\b"),

    "restmapper": (
        "discovery", "the API server listing its types; RESTMapper indexes them",
        p("Discovery is the API server telling a client which types exist and "
          "what they are called. A <code>RESTMapper</code> is the client-side "
          "index over it, mapping a GroupKind to the plural resource name. "
          "Guessing the plural instead is a security bug rather than a "
          "cosmetic one.")
        + ref("api_restmapper", "cg_discovery"), r"RESTMapper|discovery"),

    "ratcheting": (
        "validation ratcheting", "a stored object may update without satisfying new rules",
        p("A stored object that already violates a CRD rule may keep being "
          "updated, as long as the update does not make the violated field "
          "worse. A rule added today therefore never repairs yesterday's "
          "objects, which is why the distributor stays defensive and why "
          "milestone 08 warns where it would rather reject.")
        + ref("k8s_crd_ratchet"), r"ratchet"),

    "ownerref": (
        "owner reference", "the field naming a child's parent; drives deletion",
        p("The field on a child naming its parent; exactly one may be the "
          "<em>controller</em> reference. It drives garbage collection &mdash; "
          "delete the pool and the children go with it, without this "
          "controller doing anything &mdash; and its UID is this controller's "
          "entire definition of ownership.")
        + ref("k8s_gc"), r"owner reference|controller reference"),

    "intstr": (
        "IntOrString", "a JSON field that accepts either an int or a string",
        p("An apimachinery type accepting either form in JSON, the same "
          "convention as <code>maxSurge</code>. It preserves what the user "
          "actually typed, which is what makes <code>target: 30</code> "
          "distinguishable from <code>target: \"30%\"</code> &mdash; and "
          "milestone 02 spends two commits on what the first should mean.")
        + ref("api_intstr"), r"IntOrString|intstr"),

    "omitempty": (
        "omitempty", "a zero value is dropped from the wire entirely",
        p("The JSON tag that omits a zero value from the wire. So a count of "
          "0 and a field the kind never publishes are byte-identical to a "
          "reader, and only elapsed time can tell them apart. That is one bug "
          "in milestone 03, another in 05, and two commits of milestone 10."),
        r"omitempty"),

    "predicate": (
        "predicate", "an event filter here; elsewhere just a boolean function",
        p("In milestone 06, a controller-runtime <em>predicate</em> is an "
          "event filter: it decides which changes reach the queue at all. "
          "Elsewhere in this document the word carries its ordinary meaning "
          "&mdash; a function returning true or false, such as "
          "<code>IsBounded</code>. The two are unrelated and the document "
          "uses both.")
        + ref("cr_predicate"), r"predicate"),

    "errwrap": (
        "%w", "keeps the original error reachable through the wrapping",
        p("<code>fmt.Errorf</code>'s <code>%w</code> verb keeps the original "
          "error reachable through the wrapping, and <code>errors.As</code> "
          "walks that chain looking for a specific type. Together they are why "
          "a failure wrapped four times on its way up can still be classified "
          "in milestone 10 &mdash; and why a single <code>%v</code> anywhere "
          "breaks it silently.")
        + ref("go_errorf", "go_errors_as"), r"%w"),

    "spec": (
        "spec", "a test case here; elsewhere the object's desired state",
        p("Two meanings, both unavoidable. A pool's <code>spec</code> is its "
          "desired state, the half of the object a user writes. A <em>spec</em> "
          "in the tests is a single Ginkgo test case &mdash; \"two envtest "
          "specs\" means two test cases, not two schemas. The test suites use "
          "the second sense throughout."),
        r"\bspecs\b"),

    "mutation": (
        "mutation testing", "break the code on purpose; see if a test notices",
        p("Changing the production code on purpose to see whether any test "
          "fails. A test that stays green against a deliberately broken "
          "implementation was not testing what it claimed. Several of this "
          "document's strongest claims &mdash; that a check is defensive, that "
          "one test carries a whole behaviour &mdash; are results of doing "
          "this rather than assertions about the code."),
        r"mutation"),
}


def gloss_html(key):
    term, short, _defn, _pat = GLOSSARY[key]
    # A span, not an aside: an aside inside a paragraph is not valid content,
    # and the parser closes the paragraph around it -- which silently strips
    # the note of the positioned ancestor the whole layout depends on.
    return (f'<span class="gloss"><b>{html.escape(term)}</b>'
            f'<span class="g-short">{html.escape(short)}</span>'
            f'<a href="#g-{key}">in full &#8594;</a></span>')


def glossary_section():
    """The canonical text, in one place, linkable and printable.

    The margin notes are lifted from nothing -- they carry their own short
    gloss -- but the full definition lives here once, which is also what a
    reader coming back to look something up actually wants.
    """
    items = "".join(
        f'<dt id="g-{k}">{html.escape(term)}</dt><dd>{defn}</dd>'
        for k, (term, _short, defn, _pat) in GLOSSARY.items())
    return (f'<section id="glossary" class="closing"><h2>Terms</h2>'
            f'{p("Every term the document uses before it can explain it. Each "
                 "is glossed in the margin where it first appears; this is the "
                 "same set, in full.")}'
            f'<dl class="gloss-list">{items}</dl></section>')


def place_glosses(doc):
    """Put each margin note beside the first paragraph that uses its term.

    Placement is derived, not authored. A mark that had to be written at the
    call site would be wrong the first time a paragraph moved, and nobody
    would notice: the note would still render, just beside the wrong sentence.
    At most one note per paragraph, because they are positioned from the
    paragraph's own top edge and two would sit on top of each other.
    """
    body = re.sub(r'<pre[^>]*>.*?</pre>', lambda m: " " * (m.end() - m.start()),
                  doc, flags=re.S)
    placed, taken = {}, set()
    for m in re.finditer(r"<p>(.*?)</p>", body, re.S):
        if m.start() in taken:
            continue
        text = html.unescape(re.sub(r"<[^>]+>", " ", m.group(1)))
        for key, (_t, _s, _d, pat) in GLOSSARY.items():
            if key in placed or not re.search(pat, text):
                continue
            placed[key] = m.start()
            taken.add(m.start())
            break
    for key, at_pos in sorted(placed.items(), key=lambda kv: -kv[1]):
        doc = doc[:at_pos + 3] + gloss_html(key) + doc[at_pos + 3:]
    return doc, placed


def check_glossary(placed):
    """Every term is glossed once, and every gloss is short enough to be one."""
    bad = [f"{k!r} is defined but never used in prose, so its note has nowhere "
           f"to go" for k in GLOSSARY if k not in placed]
    bad += [f"{k!r}'s margin gloss is {len(short.split())} words; twelve is the "
            f"budget the column has" for k, (_t, short, _d, _p) in GLOSSARY.items()
            if len(short.split()) > 12]
    return bad



def commit(title, teaches_html, figures="", refs=""):
    """One commit of a milestone's story. `title` must match the real commit
    subject on the branch exactly; the build fails otherwise.

    `refs` is rendered in one fixed place for every commit that has any, so a
    reader learns once where to look for the upstream contract rather than
    hunting for it in a different position each time.
    """
    return dict(title=title, teaches=teaches_html, figures=figures, refs=refs)


def _real_commits(n):
    """(subject, body) pairs for milestone n, oldest first, from git."""
    # Milestone 0 begins after the repository's root commit, which is empty on
    # purpose: it exists so the scaffold arrives as a reviewable change rather
    # than as the state the repository began in. It teaches nothing, so it is
    # excluded here rather than documented as a step.
    if n == 0:
        base = git("rev-list", "--max-parents=0", tag(0)).split()[0]
        rng = [f"{base}..{tag(0)}"]
    else:
        rng = [f"{tag(n - 1)}..{tag(n)}"]
    raw = git("log", "--reverse", "--format=%s%x01%b%x02", *rng)
    out = []
    for rec in raw.split("\x02"):
        rec = rec.strip("\n")
        if not rec:
            continue
        subject, _, body = rec.partition("\x01")
        # The attribution trailer is history metadata, not teaching content.
        body = "\n".join(
            l for l in body.splitlines() if not l.startswith("Co-Authored-By:")
        ).strip("\n")
        out.append((subject, body))
    return out


# A milestone is named twice. The rail shows the short label the call site
# passes, because a nav column has no room for a sentence; the section heading
# comes from here and should say what the milestone decides, in the register
# the commit titles already use. Keeping all thirteen together is what makes
# them answerable to each other.
HEADINGS = {
    0: "Tag the generated scaffold, so every later diff is human",
    1: "Seven decisions before a single line of controller logic",
    2: "A pure function you can test exhaustively without a cluster",
    3: "Fetch, render, apply: the smallest controller that works",
    4: "The label boundary that keeps overrides out of user scheduling",
    5: "Making the pool honest about itself: orphans, status, deadlines",
    6: "Until now the controller has heard only about pools",
    7: "Sizing a tier whose right size nobody can write down",
    8: "The first layer that can see two fields at once, and why it arrives late",
    9: "Rendering the child at admission, and refusing to be a privilege ladder",
    10: "The controller works. Now make it legible from outside.",
    11: "The knobs an operator reaches for at 3am",
    12: "Prove it on a real cluster, then take away its permissions",
}


# Somewhere for a reader to stand. The document is read against a checked-out
# tag, so each milestone ends its intro by asking the page and the reader's own
# tree to agree before the commit story starts. `make test` is the command
# throughout because it resolves the envtest binaries itself.
CHECKPOINTS = {
    0: p("Everything after this tag is a human-authored diff against it.")
    + shell("git checkout milestone-00\nmake test")
    + p("Green, and that includes the generated CRD under "
        "<code>config/crd/bases/</code>. A bare <code>kubebuilder init</code> "
        "tree does not have it, which is why the scaffold needed one commit "
        "before it could pass its own tests."),

    1: p("The CRD now carries the API's decisions, not just its fields.")
    + shell("git checkout milestone-01\nmake test\n"
            "grep -A5 'subresources:' config/crd/bases/podpools.dev_podpools.yaml")
    + p("The grep should show both subresources: <code>scale</code>, with the "
        "three paths that let an HPA target a pool, and <code>status</code>."),

    2: p("The distributor is a pure function, so this milestone is the one "
         "that needs no cluster at all.")
    + shell("git checkout milestone-02\ngo test ./internal/workload/...")
    + p("Every rule the intro describes is a row in that table. It is also "
        "the fastest edit-and-rerun loop in the series &mdash; worth using it "
        "to try the phase orderings the commits argue about."),

    3: p("A pool produces children now.")
    + shell("git checkout milestone-03\nmake test")
    + p("One child per group, rendered from the template and written with "
        "server-side apply. Nothing yet removes one."),

    4: p("Children carry the pool's labels, and only the pool's children.")
    + shell("git checkout milestone-04\nmake test")
    + p("Per-group overrides merge onto the rendered child; a workload the "
        "pool does not own is left alone even when its name matches."),

    5: p("The pool tells the truth about itself.")
    + shell("git checkout milestone-05\nmake test")
    + p("Orphans are swept, status is patched once per reconcile and only "
        "when it changed, and <code>Ready</code> resolves through a "
        "precedence table rather than whichever condition was written last."),

    6: p("A child changing now wakes the pool that owns it.")
    + shell("git checkout milestone-06\nmake test")
    + p("Before this tag the two envtest specs that needed a second "
        "reconcile carried a nudge annotation to provoke one. The watch is "
        "what let those crutches go."),

    7: p("The scavenger tier sizes itself from capacity it measured.")
    + shell("git checkout milestone-07\nmake test")
    + p("The probe state machine lives in the unit tests here. The kwok tier "
        "exercises it against a simulated cluster &mdash; "
        "<code>make test-kwok</code>, which needs <code>kwokctl</code> and a "
        "container runtime."),

    8: p("The API can now refuse a spec the distributor could not satisfy.")
    + shell("git checkout milestone-08\nmake test")
    + p("Constraint combinations are rejected at admission rather than "
        "discovered at reconcile, and the cross-group rules are the ones "
        "with a measured failure behind them."),

    9: p("Admission now renders the child it is being asked to authorize.")
    + shell("git checkout milestone-09\nmake test")
    + p("A requester who may not create a Deployment may not create a "
        "PodPool that renders one. Every path that cannot answer the "
        "question denies."),

    10: p("The controller is legible from outside.")
    + shell("git checkout milestone-10\nmake test")
    + p("Metrics, events on state transitions, and the split between a "
        "failure worth retrying and one that will fail identically forever."),

    11: p("The knobs exist, and the cache stopped holding what it never reads.")
    + shell("git checkout milestone-11\nmake test")
    + p("The pause annotation, the flags and their production defaults, and "
        "a clock the tests can move."),

    12: p("The whole thing, on a real cluster, with the permissions it "
          "actually uses.")
    + shell("git checkout milestone-12\nmake test"),
}

# Three milestones have a first failure worth tabling: the scaffold that does
# not build, and the two tiers whose cluster tool the Makefile will not fetch.
# Everywhere else runs on what milestone 00 already set up. Each table is the
# failure actually reachable at that point, in the wording the thing that fails
# actually prints.
TROUBLE = {
    0: fold("trouble", "when it does not build", table(
        ["You see", "Cause", "Fix"],
        [["the generated suite failing against a missing "
          "<code>config/crd/bases/</code>",
          "<code>kubebuilder create api</code> writes Go markers, not YAML",
          "<code>make manifests</code> &mdash; which is why <code>make test</code> "
          "runs it first"],
         ["<code>Error: Failed to set up envtest binaries for version …</code>",
          "no network, or that Kubernetes minor publishes no assets",
          "<code>make setup-envtest</code>. The version is derived from "
          "<code>k8s.io/api</code> in <code>go.mod</code>, so it moves when the "
          "dependency does"],
         ["<code>no matches for kind \"PodPool\"</code>",
          "the CRD is not installed in the cluster your kubeconfig points at",
          "<code>make install</code>"]])),

    7: fold("trouble", "when the KWOK tier will not run", table(
        ["You see", "Cause", "Fix"],
        [["<code>kwokctl is not installed.</code>",
          "KWOK is the one tool the Makefile will not install for you",
          "the same line prints the URL: "
          "<code>kwok.sigs.k8s.io/docs/user/installation/</code>"],
         ["<code>make test-kwok</code> hanging, or failing to reach a cluster",
          "the KWOK cluster is missing, or was left in a bad state",
          "<code>make setup-kwok</code> &mdash; it deletes and recreates a "
          "cluster it cannot reach"]])),

    12: fold("trouble", "when the e2e tier will not run", table(
        ["You see", "Cause", "Fix"],
        [["<code>Kind is not installed.</code>",
          "unlike envtest, Kind is not fetched for you",
          "install Kind, then <code>make setup-test-e2e</code>"],
         ["<code>x509: certificate signed by unknown authority</code> from the "
          "webhook",
          "cert-manager has not issued the serving certificate yet",
          "<code>kubectl -n podpools-system get certificate</code> and wait for "
          "<code>Ready=True</code>"],
         ["cert-manager being installed on every run",
          "the suite installs it by default",
          "<code>CERT_MANAGER_INSTALL_SKIP=true make test-e2e</code> when it is "
          "already on the cluster"]])),
}


def milestone(n, label, intro_html, commits):
    """Renders the skim block and the commit story for milestone n, verifying
    the authored commit list against the branch.

    `label` is the rail's short form; the section heading comes from HEADINGS,
    falling back to the label for a milestone that has not been given one.
    """
    real = _real_commits(n)
    want = [c["title"] for c in commits]
    have = [s for s, _ in real]
    if want != have:
        sys.exit(
            f"build_tutorial_v2_doc: milestone {n:02d} drifted from the branch.\n"
            f"  script: {want}\n"
            f"  branch: {have}\n"
            f"  reconcile the milestone() call with `git log {tag(n)}`")

    story = []
    nav = []
    for i, (c, (subject, body)) in enumerate(zip(commits, real), start=1):
        # The number already on screen is the link to it: a reader who wants
        # to send someone a single commit can read its address off the page.
        cid = f"cmt-{n:02d}-{i}"
        parts = [f'<article class="cmt" id="{cid}">'
                 f'<h4><a class="cmt-n" href="#{cid}">{n:02d}.{i}</a>'
                 f'{html.escape(subject)}</h4>'
                 f'{c["teaches"]}{c["refs"]}']
        if body:
            parts.append(fold(
                "commit", "in full",
                f'<pre class="msg">{html.escape(body)}</pre>'))
        parts.append(c["figures"])
        parts.append("</article>")
        story.append("".join(parts))
        nav.append((cid, f"{n:02d}.{i}", subject))

    return dict(
        id=tag(n), num=f"{n:02d}", tag=tag(n), nav=nav,
        label=label, title=HEADINGS.get(n, label),
        body=(f'<div class="ms-intro">{intro_html}</div>'
              f'{checkpoint(CHECKPOINTS[n] + TROUBLE.get(n, "")) if n in CHECKPOINTS else ""}'
              f'<h3 class="cmt-head">The commits</h3>{"".join(story)}'))


# --------------------------------------------------------------------------
# Milestones M00-M12, in the order they were built
# --------------------------------------------------------------------------

M00 = milestone(0, "Scaffold", p(
    "Everything kubebuilder generates, nothing hand-written. The point of "
    "tagging it is that every later diff in this tutorial is a legible, "
    "human-authored change against a known-green baseline; the reader never "
    "wonders whether a person wrote a hunk or a generator did.",
    "The scaffold is not green on its own, which is the first honest lesson: "
    "<code>kubebuilder create api</code> writes Go markers, not YAML, so the "
    "generated test suite fails against the missing "
    "<code>config/crd/bases/</code> until <code>make manifests</code> has run "
    "once. And this repository gates every milestone with its final lint "
    "config rather than whatever each milestone happens to carry, which the "
    "scaffold predates. Two mechanical commits settle both debts before any "
    "real work starts."), [
    commit(
        "Initial kubebuilder scaffold for PodPool CRD",
        p("The scaffold is the commit that already exists on the branch, so "
          "there is nothing to generate: check out <code>milestone-00</code> and you "
          "have it.")
        + fold("caution", "the commands that produced it",
               p("Running these gives an empty kubebuilder project carrying "
                 "this repository's module path, sharing no history with the "
                 "branch every other instruction here refers to.")
               + shell("""
kubebuilder init --domain dev \\
  --repo github.com/negativecycle/podpool-controller

kubebuilder create api --group podpools --version v1alpha1 \\
  --kind PodPool --resource --controller
""")),
        refs=ref("kb_quickstart", "kb_architecture")),
    commit(
        "Generate the CRD manifest and manager role",
        p("Pure <code>controller-gen</code> output, and the first instance of "
          "a rule the whole series keeps: nothing under <code>config/</code> "
          "is edited by hand, it is regenerated from markers in Go. Two of "
          "its outputs carry the series. The CRD is the schema the API server "
          "will enforce, and the generated suite sets "
          "<code>ErrorIfCRDPathMissing</code>, so envtest refuses to boot "
          "until it exists. <code>role.yaml</code> is the manager's "
          "permissions, collected from <code>+kubebuilder:rbac</code> markers "
          "sitting next to the code that needs them &mdash; which is what "
          "makes the milestone 12 audit possible at all: the role can be "
          "checked against the call sites because that is where it was "
          "written."),
        fold("code", "what the scaffold grants itself, before a line is written",
             p("Every verb below came from a marker the generator emitted, "
               "not from a decision anybody made. Milestone 12 takes most of "
               "them away, and this is the figure to compare against.")
             + block(tag(0), "config/rbac/role.yaml", lang="yaml",
                     caption="config/rbac/role.yaml — the scaffold's grant")),
        refs=ref("kb_gen_crd", "kb_markers_crd")),
    commit(
        "Conform generated code to the repository lint config",
        p("Blank lines, trailing periods, one dead nolint directive. A style "
          "fix applied after tagging means rewriting every tag above it, so "
          "the gate runs the final config from tag zero and this commit pays "
          "the difference once. Doc comments ship in the CRD schema, so even "
          "the periods regenerate its descriptions."),
        refs=ref("golangci")),
    commit(
        "Trim the scaffold's CI triggers, cache the toolchain, pin kind",
        p("The scaffold declares <code>on: push:</code> with no branch "
          "filter alongside <code>on: pull_request:</code>, so every "
          "branch push runs the whole suite and opening the PR runs it "
          "again — six runs per milestone, three of them redundant.",
          "Worse is what each run spends its time on. Every job calls a "
          "make target, every make target calls "
          "<code>go-install-tool</code>, and that compiles golangci-lint "
          "from source whenever <code>bin/</code> is empty — which it "
          "always is, because nothing caches it. Measured here: linting "
          "takes 0.8s warm and 21s cold, while the CI job averages 5.6 "
          "minutes. The remaining five minutes are construction, repeated "
          "on every push. So: pull_request alone, a concurrency group "
          "that cancels the run a force-push superseded, and a cache over "
          "<code>bin/</code> keyed on a hash of the Makefile — which is "
          "where every tool version is pinned, so a bumped pin can never "
          "restore a stale binary.",
          "The fourth change is a pin rather than a saving. The "
          "scaffold's e2e workflow fetches kind from the unversioned "
          "<code>dl/latest</code> URL, so it runs against whatever "
          "released most recently — the exact drift this repository refuses "
          "everywhere else, where kwokctl is pinned and a test asserts "
          "it. An unpinned dependency in the e2e path fails a "
          "milestone's CI for reasons that have nothing to do with the "
          "commit under review."),
        fold("why", "two details that only bite later",
             p("The download gains <code>-f</code>. Without it curl "
               "writes the 404 body to <code>./kind</code>, "
               "<code>chmod +x</code> makes it executable, and the "
               "failure surfaces as a bewildering exec error rather than "
               "at the fetch that caused it.",
               "And the cache action is SHA-pinned like every other "
               "action here. An unpinned <code>actions/cache@v4</code> "
               "would work perfectly today and fail the supply-chain "
               "guard that arrives in milestone 12, a hundred commits "
               "later. Pinning it now costs nothing; discovering it then "
               "costs a rewrite of every tag above this one.")),
        refs=ref("gh_hardening")),
])

M01 = milestone(1, "The API", p(
    "The CRD, status, and scale subresources, with no controller logic at "
    "all. Seven commits because the API is seven decisions, and each carries "
    "its own failure mode: unbounded lists price CEL rules out of the "
    "API server's budget, atomic lists let two field managers wipe each "
    "other's groups, and a print column truncates any message over about 60 "
    "characters, which is a constraint every later status writer inherits "
    "from here.",
    "One ordering in this milestone is enforced by the API server rather "
    "than by taste: the caps commit precedes the CEL commit, because the "
    "cost estimator prices each rule against the maximum size of what it "
    "can see. Measured without <code>MaxItems=32</code>, the target rule "
    "alone prices at 2.3&times; over budget and the CRD is rejected at "
    "install time."), [
    commit(
        "Define PodPoolSpec: groups, workloadTemplate, replicas",
        p("Three ideas: a total to distribute, one workloadTemplate carried "
          "as <code>runtime.RawExtension</code> so any workload kind with a "
          "pod template works, and an ordered list of named groups. Doc "
          "comments here are published API; they are what "
          "<code>kubectl explain</code> prints.")
        + p("The template is the decision with the longest reach. Marked "
            "<code>Schemaless</code> and <code>PreserveUnknownFields</code>, "
            "it is bytes the API server stores without inspecting &mdash; "
            "which is what makes a CRD kind nobody compiled against work at "
            "all, and equally what means no schema anywhere will ever "
            "complain about what is inside it. Every validation this "
            "controller performs on a child, four milestones from now at "
            "admission, exists because this field bought openness by giving "
            "up the API server's opinion."),
        fold("code", "PodPoolSpec at this tag",
             block(tag(1), TYPES, start="type PodPoolSpec struct",
                   caption="api/v1alpha1/podpool_types.go")),
        refs=ref("api_rawextension", "k8s_explain", "kb_markers_val")),
    commit(
        "Define ScalingConstraints: min, max, target, opportunistic",
        p("A group declares its share with one <code>Target</code>, a "
          "percentage string like <code>\"30%\"</code>, the same convention "
          "as <code>maxSurge</code>. <code>MaxLength=4</code> is not "
          "cosmetic; it is what keeps the CEL rule affordable two commits "
          "from now.")
        + p("Every field is a pointer, and that is the load-bearing choice: "
            "<code>min: 0</code> and no min at all are different "
            "instructions, and only a pointer keeps them apart. The type is "
            "<code>intstr.IntOrString</code> so a user cannot write "
            "<code>target: 30</code> and get 30 percent by accident &mdash; "
            "the accident happens anyway, and milestone 02 spends two commits "
            "on what it should mean."),
        block(tag(1), TYPES, start="type ScalingConstraints struct",
              caption="api/v1alpha1/podpool_types.go — ScalingConstraints"),
        refs=ref("api_intstr", "k8s_maxsurge")),
    commit(
        "Bound every list and number",
        p("<code>MaxItems=32</code>, <code>Maximum=1000000</code>, "
          "<code>MaxLength=53</code>. Each cap closes its own hole: "
          "etcd-sized status writes, int32 overflow in the distribution "
          "arithmetic, and the child's share of the DNS label budget.")
        + p("A bound written into the schema is enforced by the API server "
            "for every client, forever, including the ones that bypass this "
            "controller entirely &mdash; and unlike a check in Go it costs "
            "nothing at runtime. The 53 is arithmetic, not taste: it caps "
            "the <em>group</em> name, because the child is named "
            "<code>&lt;pool&gt;-&lt;group&gt;</code> and that whole string "
            "has to survive as a DNS label at 63 bytes, separator included."),
        fold("code", "one bounded field, and where the number came from",
             block(tag(1), TYPES, start="Replicas int32 `json:\"replicas\"`",
                   caption="api/v1alpha1/podpool_types.go — spec.replicas")),
        refs=ref("kb_markers_val", "k8s_dnslabel")),
    commit(
        "Add CEL XValidation for constraint combinations",
        p("Three rules, three targeted messages: min &le; max, opportunistic "
          "pairs only with min, target matches the percentage pattern. CEL "
          "guards <em>admission</em>, not the store; the algorithm milestone "
          "shows why the distributor must stay defensive anyway.")
        + p("The reason to spend the effort here rather than in the webhook "
            "two milestones later is availability. A CEL rule is part of the "
            "CRD, so it runs inside the API server: it cannot be down, it "
            "needs no certificate, and it applies to a cluster that has never "
            "deployed this controller. What it cannot do is compare one group "
            "against another, and that limit is exactly the shape of "
            "milestone 08."),
        fold("code", "what three markers become in the CRD",
             block(tag(1), "config/crd/bases/podpools.dev_podpools.yaml",
                   lang="yaml", start="x-kubernetes-validations:",
                   end="                  required:",
                   caption="config/crd/bases/podpools.dev_podpools.yaml — "
                           "generated, never edited")),
        refs=ref("k8s_crd_cel", "kb_markers_val")),
    commit(
        "Add listType=map and listMapKey markers",
        p("Atomic is the default, and under server-side apply (SSA, as every "
          "later milestone abbreviates it) an atomic list "
          "is replaced whole. Keying groups by name makes each an "
          "independently owned entry, and buys duplicate-name rejection with "
          "no webhook involved.")
        + p("Concretely: under an atomic list, whoever applies "
            "<code>spec.groups</code> owns the whole list, so a second "
            "manager applying its own view of one group silently drops every "
            "group it did not mention. Keyed by name, each entry is owned on "
            "its own and both writes survive &mdash; two lines of marker "
            "standing in for the whole conflict-detection machinery."),
        fold("code", "the marked field",
             block(tag(1), TYPES, start="Groups []GroupSpec",
                   caption="api/v1alpha1/podpool_types.go — spec.groups")),
        refs=ref("k8s_ssa_merge", "kb_markers_val")),
    commit(
        "Define status fields and conditions",
        p("The whole observed surface in one commit, including "
          "<code>unplacedReplicas</code>: replicas that no ceiling accepts "
          "are reported, not silently absorbed, because the pool "
          "deliberately runs below <code>spec.replicas</code> rather than "
          "overspending on a tier the user capped on purpose.")
        + p("Status is defined here and written eight commits into milestone "
            "05, which is deliberate: a status field is API, and adding one "
            "later is a schema change, while leaving one unwritten costs "
            "nothing. <code>conditions</code> uses the standard "
            "<code>metav1.Condition</code> shape rather than a bespoke one, "
            "so <code>kubectl wait --for=condition=Ready</code> works on a "
            "pool without anybody teaching it what a pool is."),
        fold("code", "PodPoolStatus at this tag",
             block(tag(1), TYPES, start="type PodPoolStatus struct",
                   caption="api/v1alpha1/podpool_types.go")),
        refs=ref("k8s_api_conventions", "k8s_crd_status", "kubectl_wait")),
    commit(
        "Add scale and status subresources, and six print columns",
        p("The scale subresource is three marker paths and no Go, which is "
          "what lets a standard HPA target a PodPool. The Status column "
          "projects the Ready condition's message so <code>kubectl get</code> "
          "answers \"what is wrong\" without a describe.")
        + p("The status subresource is the quieter half and changes how "
            "everything above it behaves: spec and status become separately "
            "writable, so the controller cannot clobber a user's edit while "
            "publishing counts, a status write does not bump "
            "<code>metadata.generation</code>, and RBAC can hand out "
            "<code>podpools/status</code> without handing out the spec. "
            "Milestone 05's patch-once-per-reconcile rule is only expressible "
            "because of it."),
        block(tag(1), TYPES, start="// +kubebuilder:object:root=true",
              end="type PodPool struct",
              caption="api/v1alpha1/podpool_types.go — subresources and print columns"),
        refs=ref("k8s_crd_scale", "k8s_crd_status", "k8s_crd_cols", "k8s_hpa")),
])

M02 = milestone(2, "Distribution algorithm", p(
    "A pure function in <code>internal/workload</code>, exhaustively testable "
    "with a table and no cluster. The package boundary comes first because "
    "the admission webhook will call the same functions the reconciler does, "
    "and building everything in the controller package would make that an "
    "import cycle the day admission arrives.",
    "The milestone ends by replaying a real bug class in miniature: pin the "
    "current behaviour in a table with the intended rows marked skipped, "
    "then land the fix as exactly the flip of those skips. The bug is worth "
    "the ceremony because it is quiet: a <code>target</code> the distributor "
    "cannot parse used to make its group the unbounded overflow sink, so a "
    "typo on the most expensive tier silently absorbed the whole pool."), [
    commit(
        "Scaffold internal/workload with algorithm.go",
        p("Package boundary first, before any logic exists to put behind "
          "it.")
        + p("What the boundary is for is what may not cross it. Nothing in "
            "this package takes a client, a context, or a "
            "<code>ctrl.Request</code>; its imports stop at this project's "
            "own API types and the value helpers underneath them. That is "
            "what makes the tests a table "
            "with no cluster, keeps admission and the reconciler asking the "
            "same function rather than two copies of one rule, and &mdash; "
            "unplanned but decisive later &mdash; makes the algorithm small "
            "enough to port to JavaScript for the simulator four commits "
            "from here.")),
    commit(
        "Add ComputeGroupTargets and DistributionResult",
        p("The signature and the result shape. The degenerate cases are "
          "pinned now because every later phase must preserve them.")
        + p("A struct rather than a bare <code>[]int32</code>, because the "
            "function has two things to say and the second one is the "
            "interesting one: how many replicas it could not place. Returning "
            "only the targets would force every caller to re-derive that by "
            "subtraction, and a caller that forgets simply reports a pool as "
            "fully scheduled when it is not.")),
    commit(
        "Phase 1: cascade thresholds via GroupFloor",
        p("<code>min</code> is a threshold to satisfy before filling later "
          "groups, not a guaranteed floor: when the total cannot cover every "
          "min, earlier groups fill first and later ones go short. List "
          "order is priority order.")
        + p("The alternative &mdash; proportional shortfall, everybody a "
            "little short &mdash; sounds fairer and is worse for the thing "
            "this controller is for: the tiers are ranked by how much you "
            "want them, and a budget cut should empty the cheap tier, not "
            "take a slice off the one carrying production traffic. Making "
            "list order mean priority is what lets a user express that "
            "without a priority field."),
        fold("code", "GroupFloor at this tag",
             block(tag(2), ALGO, start="func GroupFloor(",
                   caption="internal/workload/algorithm.go"))),
    commit(
        "Phase 2: percentage targets via TargetPercent and GroupTarget",
        p("Two helpers rather than inline arithmetic, because admission "
          "needs the same parse. The arithmetic is int64 from the first "
          "line it exists: at total=25,000,000 an int32 product wraps "
          "negative and the phase silently skips. Rounding direction is a "
          "policy, pinned empirically in both directions."),
        fold("code", "GroupTarget at this tag",
             block(tag(2), ALGO, start="func GroupTarget(",
                   caption="internal/workload/algorithm.go")),
        refs=ref("api_intstr")),
    commit(
        "The overflow phase: remainder in list order, respecting GroupCeiling",
        p("Ceilings are absolute. Replicas no ceiling accepts stay unplaced "
          "and are reported, rather than forced onto a tier the user capped "
          "on purpose. Two invariants now run on every table row: targets "
          "plus unplaced accounts for the whole total, and no group exceeds "
          "its own ceiling except by its own min.")
        + p("This is the one place in the tutorial where reading the code "
            "genuinely does not tell you what it does: three phases "
            "interacting over integer division. So the trace table is "
            "driven by the reader rather than printed. The badge in the "
            "corner is the simulator checking its own JavaScript port "
            "against the same rows the Go tests assert.")
        + sim1.sim_html()
        + standalone("sim1-distribution.html", "the distribution simulator")
        + p("An <code>opportunistic</code> group here shows its replicas as "
            "an <em>offer</em>: sized by observed capacity, which this "
            "simulator does not have. That dead end is honest, and it is "
            "what the second simulator (arriving with the capacity "
            "milestone) exists to explore."),
        fold("code", "the full ComputeGroupTargets listing at this tag",
             block(tag(2), ALGO, start="func ComputeGroupTargets(",
                   caption="internal/workload/algorithm.go"))),
    commit(
        "Add checkTargetDegraded",
        p("Directional on purpose: with no max the target is itself the "
          "ceiling and only a larger min can breach it; with max present "
          "the target is a soft floor and only the max can pin the group "
          "under it. Half a percentage point of tolerance, because pods are "
          "integers and percentages are not.")
        + p("The reason this is a predicate in the algorithm package rather "
            "than a comparison in the controller is that \"missed its "
            "target\" needs one definition. The controller will publish it as "
            "a condition, the webhook will warn about the configurations that "
            "guarantee it, and a second implementation of the rule would put "
            "the two layers a rounding error apart on exactly the pools where "
            "somebody is already looking closely."),
        refs=ref("k8s_api_conventions")),
    commit(
        "Pin what a malformed target means, before deciding it",
        p("One table, eleven target shapes, both questions the code asks of "
          "the field: what percentage does it name, and does it bind. The "
          "rows describing the intended behaviour are marked skipped, so "
          "the next commit's diff is exactly the flip of those skips.")
        + p("This is the series' third rule in its purest form. A commit that "
            "changes a decision has to write down the old behaviour first, "
            "because otherwise the fix arrives as an assertion &mdash; here "
            "is the new rule, trust me &mdash; and nothing in the diff shows "
            "what it displaced. Written this way, the next commit is "
            "unfalsifiable only if the skips do not flip.")),
    commit(
        "Treat an unreadable target as a ceiling that binds at zero",
        p("The user who typed <code>target: 30</code> unquoted asked for a "
          "cap and mistyped it. Binding at zero keeps the group at its "
          "floor and surfaces the mistake through "
          "<code>unplacedReplicas</code>; and the defence is permanent, not "
          "transitional, because CRD validation ratcheting lets a stored "
          "object update forever without re-running the rule."),
        block(tag(2), ALGO, start="func GroupCeiling(",
              caption="internal/workload/algorithm.go — GroupCeiling after the fix"),
        refs=ref("k8s_crd_ratchet")),
    commit(
        'Give every layer one definition of "bounded"',
        p("<code>workload.IsBounded</code>, exported now so admission can "
          "never grow its own copy of the answer. A pure refactor only "
          "because of the previous commit: the presence-based definition "
          "became correct when the distributor moved to presence.")
        + p("Worth naming what would have happened otherwise. Admission "
            "wants to know \"can this group absorb the overflow\", and the "
            "obvious webhook-side answer &mdash; no max and no target &mdash; "
            "is right until an unreadable target appears, at which point the "
            "webhook and the distributor disagree about the same pool and "
            "each is self-consistent. Six commits later, milestone 08 asks "
            "this exact question twice and both answers have to come from "
            "here."),
        fold("code", "IsBounded at this tag",
             block(tag(2), ALGO, start="func IsBounded(",
                   caption="internal/workload/algorithm.go"))),
])

M03 = milestone(3, "Core reconcile loop", p(
    "The minimum viable controller: fetch, render, apply. By the end of the "
    "milestone a pool produces one child workload per group, sized by the "
    "distribution and owned through a controller reference, against both "
    "built-in kinds and CRDs the controller has no compiled types for. No "
    "status is written yet, nothing watches the children, and nobody checks "
    "who owns an existing child; those gaps are the next three milestones, "
    "and two of them are visible as deliberate crutches in this milestone's "
    "own tests.",
    "The envtest suite grows its real shape here: a manager running the "
    "reconciler, and minimal Rollout and CloneSet CRDs so every property is "
    "proven against the unstructured path, not just Deployments. Because the "
    "controller does not manage selectors yet and apps/v1 demands one, the "
    "Deployment fixtures carry their own; because nothing watches children "
    "yet, the drift specs nudge the pool to trigger a pass. Both crutches "
    "are labelled in the tests, and each is a later milestone's opening "
    "argument."), [
    commit(
        "Reconcile skeleton: fetch the pool",
        p("The request carries only a name; everything starts from a fresh "
          "Get, because the object may have changed, or vanished, since the "
          "event that queued it. NotFound maps to a clean stop, not an "
          "error: requeueing a name that will never resolve again buys "
          "nothing.")
        + p("This is the level-triggered contract, and it is the one idea "
            "that has to land before any of the rest reads correctly. "
            "Reconcile is not told what changed and must never care: it "
            "reads the world, decides what the world should look like, and "
            "makes the difference. Every subsequent milestone gets its "
            "correctness from that &mdash; the sweep, the probe, the status "
            "write are all safe to run again because none of them is a "
            "response to an event.")
        + p("The whole of the reconciler's state is two fields: an embedded "
            "<code>client.Client</code> and a <code>Scheme</code>. It owns no "
            "queue, no worker pool and no cache; the manager owns all three "
            "and hands it a client. The signature is the rest of the "
            "contract. A <code>ctrl.Request</code> is a "
            "<code>types.NamespacedName</code> and nothing else &mdash; a "
            "namespace and a name, carrying no object and no description of "
            "what happened &mdash; and a <code>ctrl.Result</code> says only "
            "<em>come back after this long</em>. Thirteen milestones of "
            "behaviour are learned through those two fields and expressed "
            "through that one struct.")
        + p("The embedded client is the detail that costs the most later, "
            "because it does not read the API server. It reads a cache the "
            "manager fills by listing a kind once and watching it "
            "thereafter, which is what lets a controller reconcile thousands "
            "of objects without melting an apiserver &mdash; and it means "
            "every read in this document returns something that was true a "
            "moment ago. Almost everywhere that is fine, because the pass is "
            "level-triggered and the next one corrects it. Twice it is not, "
            "and both times the fix is to reach past the cache: milestone 04 "
            "confirms an absence with an uncached read before force-applying "
            "over it, and milestone 05 confirms a child with one before "
            "deleting it. Neither is a special case. They are the two places "
            "where being one revision behind cannot be repaired on the next "
            "pass."),
        fold("code", "the shape of the thing, at this tag",
             p("Three listings, and none of them is this commit's diff: they "
               "are where milestone 03 arrives. The struct is what the "
               "reconciler carries, <code>SetupWithManager</code> is how it "
               "reaches the manager and declares which kind it is for, and "
               "<code>Reconcile</code> is the loop the next seven commits "
               "fill in &mdash; already using <code>req.NamespacedName</code>, "
               "the only thing a request carries, to go and read the object "
               "for itself.")
             + block(tag(3), CTRL, start="type PodPoolReconciler struct",
                     caption="internal/controller/podpool_controller.go — what it carries")
             + block(tag(3), CTRL,
                     start="func (r *PodPoolReconciler) SetupWithManager(",
                     caption="internal/controller/podpool_controller.go — how it is wired")
             + block(tag(3), CTRL,
                     start="func (r *PodPoolReconciler) Reconcile(",
                     caption="internal/controller/podpool_controller.go — the loop itself")),
        refs=ref("cr_reconcile", "cr_root", "api_namespacedname",
                 "api_isnotfound", "cr_cache_pkg", "k8s_api_concepts")),
    commit(
        "Guard DeletionTimestamp immediately after Get",
        p("Three lines that prevent a fight with the garbage collector: "
          "children carry blockOwnerDeletion, so recreating one mid-teardown "
          "makes foreground deletion unwinnable. The fake-client harness "
          "lands with the test, and it is what lets Reconcile run end to end "
          "in a plain Go test with no manager and no API server.")
        + p("Worth being precise about the deadlock, because it is not "
            "obvious: foreground deletion holds the pool in the store until "
            "every blocking child is gone, and this controller's own "
            "reconcile is what re-creates a missing child. Without the "
            "guard, the pool's deletion waits on children that a healthy "
            "controller keeps putting back. Nothing errors, and the object "
            "simply never goes away."),
        refs=ref("k8s_gc", "k8s_gc_fg", "cr_fake")),
    commit(
        "Add workload.ExtractGVK and workload.ChildName",
        p("<code>ExtractGVK</code> reads the group, version and kind out of "
          "the template's own <code>apiVersion</code> and <code>kind</code>. "
          "That triple &mdash; the GVK, and the abbreviation the rest of this "
          "document uses without expanding it again &mdash; is the identity "
          "of an API type, and it is what the scheme is keyed on, what a "
          "watch is registered for, and what admission must resolve to a "
          "plural resource before it can ask whether anyone may create one.")
        + p("The child-name rule (<code>&lt;pool&gt;-&lt;group&gt;</code>) "
            "will be derived by the render path, the status path, and the "
            "orphan sweep; three derivations of one rule always drift, so it "
            "gets one home before a second derivation can exist.")
        + p("It is six lines, and milestone 05 is where it pays: the sweep "
            "decides what to delete by name, and a name computed one way "
            "here and another way there is a destructive operation working "
            "off two different answers."),
        fold("code", "the whole rule",
             block(tag(3), "internal/workload/render.go", start="func ChildName(",
                   caption="internal/workload/render.go")),
        refs=ref("k8s_dnslabel")),
    commit(
        "Add workload.ParseTemplate and BuildChildWorkload on unstructured",
        p("Convert values into the template, never the template into your "
          "own types: a typed round-trip silently drops every field the "
          "vendored types do not know, and returns a nil error while doing "
          "it. Parse once per reconcile, deep-copy per group; the "
          "idempotency and leak tests pin both halves from day one."),
        fold("code", "BuildChildWorkload at this tag",
             block(tag(3), "internal/workload/render.go",
                   start="func BuildChildWorkload(",
                   caption="internal/workload/render.go")),
        refs=ref("api_unstructured")),
    commit(
        "Strip pasted metadata",
        p("A template copied from <code>kubectl get -o yaml</code> carries "
          "uid, resourceVersion, managedFields, and a status block. The uid "
          "is the sharp one: the create fails with a uid mismatch, the child "
          "is never created, and being a conflict it retries forever.")
        + p("The list is worth reading as a list, because each entry fails "
            "differently and only one of them fails loudly. A stale "
            "resourceVersion is a conflict on every pass; managedFields "
            "hands the apply somebody else's ownership record; a pasted "
            "status is silently discarded by the API server. This is the "
            "commit that turns \"copy an object you already have\" from a "
            "trap into the documented way to write a template."),
        fold("code", "stripPastedMetadata at this tag",
             block(tag(3), "internal/workload/render.go",
                   start="func stripPastedMetadata(",
                   caption="internal/workload/render.go")),
        refs=ref("k8s_ssa_fields")),
    commit(
        "Write children with server-side apply",
        p("Create-only owned each child for one instant; server-side apply "
          "owns them continuously. An external edit to a rendered field is "
          "reverted on the next pass, while fields the render never mentions "
          "stay whoever else's they are. A spec hash or whole-spec compare "
          "cannot make that distinction, because the server defaults fields "
          "inside spec you never set; field-level ownership is the only "
          "comparison that separates drift from defaulting, and the API "
          "server already keeps it. Only a real API server can show a "
          "converged apply leaving resourceVersion untouched, which is "
          "exactly what the envtest spec asserts."),
        fold("code", "the apply call, and the two options that are not optional",
             p("<code>FieldOwner</code> names the manager whose ownership "
               "record all of the above depends on, and it has to be stable "
               "across restarts or every rollout adopts its own fields "
               "afresh. <code>ForceOwnership</code> settles the conflict a "
               "previous create-then-update controller leaves behind: "
               "without it the first apply after an upgrade fails on fields "
               "the old code owned, and the pool never converges.")
             + block(tag(3), CTRL,
                     start="func (r *PodPoolReconciler) applyChild(",
                     caption="internal/controller/podpool_controller.go")),
        refs=ref("k8s_ssa", "k8s_ssa_fields")),
    commit(
        "Add workload.ReadInt32 returning (value, found)",
        p("The child status contract: readyReplicas is omitempty, so a count "
          "of zero and a field the kind never publishes are the same bytes "
          "on the wire. ok=false means \"zero or unpublished\", and only "
          "elapsed time can tell those apart; conflating them is a bug a "
          "later milestone gets to relive against a real API server."),
        fold("code", "sixteen lines that milestone 10 spends two commits on",
             block(tag(3), "internal/workload/render.go", start="func ReadInt32(",
                   caption="internal/workload/render.go")),
        refs=ref("api_unstructured", "k8s_api_conventions")),
    commit(
        "Iterate all groups, collecting errors",
        p("One bad group must not freeze the pool: failures collect, later "
          "groups proceed, and the aggregate names each failed group. Every "
          "wrap is %w because failure classification will be built on "
          "errors.As, and a single %v anywhere breaks it silently. The "
          "planned wrap-audit commit dissolved into this one; the milestone "
          "is eight commits, not nine.")
        + p("The discipline is cheap here and unpayable later. "
            "<code>%w</code> keeps the original error reachable through the "
            "wrapping; <code>%v</code> flattens it to a string that reads "
            "identically in a log and answers nothing. Milestone 10 asks "
            "\"is this failure the spec's fault\" of an error that has been "
            "wrapped four times on its way up, and gets an answer only "
            "because every one of those four sites was written this way."),
        refs=ref("go_errorf", "go_errors_as")),
])

M04 = milestone(4, "Overrides and ownership", p(
    "Per-group customization and the label boundary that protects user "
    "scheduling. One override mechanism (an RFC 7386 merge patch over the "
    "whole workload object) rather than dedicated fields plus a merge, "
    "because two mechanisms need an ordering rule between them. The "
    "controller takes ownership of the child's selector and labels, which "
    "retires the milestone-old crutch of templates carrying their own "
    "selectors, and draws a line it then refuses to cross: scheduling "
    "selectors inside the pod spec belong to the user.",
    "The milestone ends on the sharpest correctness commit so far: the "
    "create path's TOCTOU. A stale cache reads a stranger's workload as "
    "absent, the create force-applies over it, and SSA stamps our owner "
    "reference onto it, after which the next pass's ownership check accepts "
    "what we just wrote. The refusal therefore has to confirm absence with "
    "an uncached read, and the tests need a harness where the cache and the "
    "API server genuinely disagree."), [
    commit(
        "Add workload.MergeMaps: RFC-7386 deep merge with null deletion",
        p("Maps merge, null deletes, everything else replaces. Exported "
          "because admission will validate overrides by performing the same "
          "merge; and it never mutates its base, so one parsed template can "
          "be merged once per group.")
        + p("Picking a specified algorithm rather than inventing one is the "
            "whole point. RFC 7386 is what <code>kubectl patch "
            "--type=merge</code> already does, so an override behaves the way "
            "a user's hands already expect &mdash; including the part nobody "
            "likes, where a list is replaced wholesale rather than merged. "
            "Writing that rule down as somebody else's standard is cheaper "
            "than defending a bespoke one in every bug report."),
        fold("code", "MergeMaps at this tag",
             block(tag(4), "internal/workload/render.go", start="func MergeMaps(",
                   caption="internal/workload/render.go")),
        refs=ref("rfc7386")),
    commit(
        "Apply per-group overrides to rendered children",
        p("Merged after the deep copy and before the metadata strip, so an "
          "override cannot smuggle a pasted uid back in. The group-failure "
          "test upgrades to the failure a user would actually produce: a "
          "null that deletes .spec.template.")
        + p("Ordering is the entire content of this commit. Merge before the "
            "deep copy and one group's override lands on every group; merge "
            "after the strip and an override is a second, unguarded way to "
            "put a uid on the object. There is one position in the render "
            "that is correct, and it is the kind of detail that a test only "
            "catches if somebody thought to write it as a null."),
        refs=ref("rfc7386")),
    commit(
        "Define workload/labels.go: pool, group, managed-by",
        p("The identity strings in one place, including the field-manager "
          "name server-side apply writes under. Identity strings that live "
          "in two packages eventually disagree.")
        + p("Nine lines of constants, and three separate mechanisms end up "
            "keyed off them: the child selector two commits from now, the "
            "orphan sweep's search in milestone 05, and the cache scoping in "
            "milestone 11 that makes the controller stop paying for other "
            "people's objects. Change one string here and all three move "
            "together, which is the only way they stay consistent."),
        fold("code", "the whole file",
             block(tag(4), "internal/workload/labels.go",
                   caption="internal/workload/labels.go")),
        refs=ref("k8s_labels")),
    commit(
        "Set child selector and pod template labels",
        p("The child's spec.selector answers \"which pods does this child "
          "own\": derived from pool and group labels, stamped on the pod "
          "template so it selects, immutable after create. managed-by stays "
          "out of the selector because it is provenance, not identity. Two "
          "groups rendered from one template no longer fight over the same "
          "pods, and the fixtures drop their carried selectors."),
        fold("code", "BuildChildWorkload at this tag",
             block(tag(4), "internal/workload/render.go",
                   start="func BuildChildWorkload(",
                   caption="internal/workload/render.go")),
        refs=ref("k8s_labels", "k8s_deploy_selector")),
    commit(
        "Pass scheduling selectors through untouched",
        p("Selectors inside topologySpreadConstraints and affinity answer a "
          "different question: which pods should the scheduler compare "
          "against. They are the user's, often deliberately about pods that "
          "are not ours, and merging pool labels in makes a required "
          "affinity unsatisfiable and an anti-affinity vacuous. There is no "
          "production diff; the rule is an absence, pinned by a test suite "
          "precisely because an earlier implementation of this controller "
          "had the rule undone by a review that read it as a gap."),
        refs=ref("k8s_spread", "k8s_affinity")),
    commit(
        "Refuse to touch a child the pool does not own",
        p("Ownership is the controller reference's UID at every call site, "
          "including the create path, where a lagging cache reads a "
          "stranger as absent and the force-apply silently adopts it. "
          "Absence is confirmed with an uncached read that fails closed; a "
          "live read that finds our own child falls through. The split-view "
          "test harness exists because one fake client cannot make the "
          "cache and the API server disagree.")
        + p("The reason force-apply is dangerous here and safe everywhere "
            "else in the milestone is that it does not ask. A forced apply "
            "on an object we believe is absent will happily create our "
            "fields on an object that is present, and the owner reference "
            "goes on with them &mdash; so the check that would have caught "
            "the mistake is looking at evidence the mistake planted."),
        refs=ref("k8s_ssa_fields", "k8s_gc")),
])

M05 = milestone(5, "Lifecycle management", p(
    "The pool becomes honest about itself: orphan cleanup, a full status "
    "surface, and the machinery that turns elapsed time into a verdict. The "
    "milestone opens with its most dangerous code and spends five commits on "
    "it, because the sweep is the one operation the next reconcile cannot "
    "repair: each commit closes a way a stale read could delete something "
    "alive, ending with a delete bound to the exact UID that was confirmed.",
    "Status arrives in deliberate stages. Conditions land first with a naive "
    "every-pass write whose sharp edges are called out in the commit that "
    "introduces them; the per-group rows land next and immediately teach a "
    "real dependency (a failed group must carry its previous row forward or "
    "the stale-kind sweep goes blind); and the patch-once commit then fixes "
    "exactly the problems the naive era demonstrated. The deadline closes "
    "the milestone: ready-below-desired is byte-identical for a young "
    "rollout and a wedged pool, and only a stamped clock plus a requeue can "
    "tell them apart."), [
    commit(
        "Add sweepOrphans: delete children whose group left the spec",
        p("The first destructive action, keyed off spec.groups so a group "
          "that merely failed this pass does not look orphaned. Labels are "
          "the search; ownership is the authority.")
        + p("Everything else this controller does is idempotent and "
            "self-repairing: a wrong apply is corrected next pass, a wrong "
            "status is overwritten. A wrong delete is not. That asymmetry is "
            "why the sweep gets five commits and why each of the next four "
            "is a way that a read which was true a moment ago can authorize "
            "a deletion that is wrong now."),
        refs=ref("k8s_gc", "k8s_labels")),
    commit(
        "Add sweepAllOrphans and staleWorkloadGVKs",
        p("A template kind change leaves old-kind children the spec no "
          "longer remembers; the workloadRef each group's status records is "
          "how they are found. The stale sweep gates on the replacement "
          "having reconciled, so a failed swap never costs running "
          "capacity.")
        + p("The reason status has to remember is that the spec cannot. "
            "Change <code>workloadTemplate</code> from Deployment to "
            "StatefulSet and the spec now describes only StatefulSets; the "
            "Deployments are still running and nothing in the desired state "
            "mentions them. Status is the controller's memory of what it "
            "made, and this is the commit where that memory becomes "
            "load-bearing rather than informational.")),
    commit(
        "Decide the sweep on the child name, not the group label",
        p("The label is user-writable and read through a cache: drift plus "
          "a stale read deletes a healthy child. The name is spec-derived "
          "and immutable, so a stale read can at worst defer a real orphan. "
          "The right failure asymmetry for a destructive operation.")
        + p("Both readings are wrong sometimes; the question is what they do "
            "when they are wrong. Deciding on the label fails towards "
            "deleting something alive. Deciding on the name fails towards "
            "leaving something dead lying around for one more pass. For an "
            "operation with no undo, the second kind of wrong is the only "
            "acceptable one."),
        refs=ref("k8s_labels")),
    commit(
        "Confirm sweep candidates against the API server",
        p("Ownership still came from the cache: a user adopting an orphan "
          "by removing our controller reference would have it deleted off a "
          "read that was true a moment ago. Every candidate is re-read "
          "uncached; already-gone is success, unverifiable aborts loudly.")
        + p("The cache is not wrong, it is late, and for every other read in "
            "this controller late is fine &mdash; the next pass corrects it. "
            "A delete is where late stops being fine, so this is the one "
            "place worth paying for a round trip to the API server. Note "
            "which way the two failure branches point: already-gone means the "
            "job is done, and cannot-tell means stop."),
        refs=ref("cr_reader")),
    commit(
        "Bind the orphan delete to the UID it was confirmed against",
        p("Confirm and delete are two calls, and a name freed and reused "
          "between them would take the newcomer. UID, not ResourceVersion: "
          "identity is the question, and an RV precondition would 409 on "
          "any unrelated write. A fired precondition is a race to leave "
          "alone, not an error."),
        fold("code", "the sweep at the end of all five commits",
             p("Read it as the accumulated answer rather than as this "
               "commit's diff: the name-derived candidate list, the uncached "
               "confirmation, the ownership check against our own UID, and "
               "the precondition that makes the delete refer to the object "
               "that was confirmed rather than to the name it happened to "
               "hold.")
             + block(tag(5), CTRL,
                     start="func (r *PodPoolReconciler) sweepOrphans(",
                     caption="internal/controller/podpool_controller.go")),
        refs=ref("cr_preconditions")),
    commit(
        "Add conditions.go: Available, Progressing, TargetDegraded, GroupsReady",
        p("Four independent signals, each answering one question, fed by the "
          "child counts the ReadInt32 contract promised two milestones ago. "
          "An unparseable template becomes a reported condition instead of a "
          "returned error, because no retry fixes a spec.")
        + p("Conditions are the one part of a CRD's status that other "
            "software already knows how to read, which is why they are worth "
            "the ceremony of the standard shape: type, status, reason, "
            "message, observedGeneration, lastTransitionTime. "
            "<code>SetStatusCondition</code> maintains the transition "
            "timestamp, and that timestamp only means anything if the writer "
            "stops rewriting identical conditions &mdash; a rule this "
            "milestone does not yet follow, and fixes seven commits from "
            "here."),
        refs=ref("k8s_api_conventions", "api_setcondition")),
    commit(
        "Add the summary Ready condition as a precedence table",
        p("Four conditions with no single answer forces every consumer to "
          "recompute pool health, each slightly wrong. The switch is a "
          "precedence table ordered most-serious-first: a new state is one "
          "row, not another branch. Messages hold to the 60-character column "
          "budget under a hostile-input sweep test."),
        fold("code", "summaryReady at this tag",
             block(tag(5), "internal/controller/conditions.go",
                   start="func summaryReady(",
                   caption="internal/controller/conditions.go")),
        refs=ref("k8s_api_conventions")),
    commit(
        "Prune condition types this controller no longer publishes",
        p("Renaming a condition type is not a refactor: SetStatusCondition "
          "can only upsert, so a renamed type sits beside its replacement "
          "forever. The mechanism is built while the retired list is empty, "
          "and it is deliberately a list of our own retired names, never an "
          "allow-list, because the conditions array is a shared contract "
          "other actors write to."),
        refs=ref("api_setcondition")),
    commit(
        "Add workload.ChildDetail and TruncateRunes",
        p("The group row says what the child's own conditions say, probed in "
          "kstatus order and suppressed when the child's controller has not "
          "observed its current spec. Runes, not bytes: truncating mid-rune "
          "puts invalid UTF-8 in a status field. The kind-swap spec "
          "immediately taught the carry-forward rule: a failed group keeping "
          "its previous row is what keeps the stale sweep sighted."),
        fold("code", "the group reason ladder at this tag",
             block(tag(5), "internal/controller/podpool_controller.go",
                   start="func assignGroupReasons(",
                   caption="internal/controller/podpool_controller.go")),
        refs=ref("kstatus")),
    commit(
        "Clamp a lying child's counts, and say so",
        p("A CRD child's schema is its author's: an integer field with no "
          "int32 format stores the whole int64 range, and an unchecked "
          "narrowing reads 2^32-1 as -1, wedging the pool on its own 422ing "
          "status patch. Clamp at the read, high to MaxInt32 because a group "
          "that reads full is one nothing scales up on; and report the clamp "
          "once per group, because silently repaired numbers are how a "
          "broken child hides.")),
    commit(
        "Patch status once per reconcile, and only when changed",
        p("A deferred MergeFrom patch against a deep copy of what the pass "
          "read: every exit publishes exactly once, a no-op pass publishes "
          "nothing. Writing every pass resets LastTransitionTime on "
          "identical conditions and wakes the controller with its own write "
          "event, a quiet self-sustaining loop."),
        refs=ref("k8s_crd_status", "api_setcondition")),
    commit(
        "Set status.selector above the early returns",
        p("The scale subresource's selectorpath points here, and an HPA "
          "reads /scale with no idea whether the pool is healthy. Derived "
          "from the pool name alone, so no early path lacks anything: "
          "derived status fields go above every exit.")
        + p("What an empty selector actually does is worth stating, because "
            "it is not an error anywhere: the HPA reads /scale, gets a blank "
            "selector, matches no pods, computes utilisation over an empty "
            "set, and declines to scale while reporting that it cannot "
            "gather metrics. A pool that is merely unhealthy would become a "
            "pool that also cannot autoscale, for the duration of the "
            "unhealthiness."),
        refs=ref("k8s_crd_scale", "k8s_hpa")),
    commit(
        "Add Progressing deadline with jittered requeue",
        p("Each group's lastProgressTime is stamped by explicit rules, and "
          "deliberately not when ready falls: a regression runs the deadline "
          "from the original shortfall. Every pass requeues on a jittered "
          "floor, shortened near a deadline so it fires precisely; without "
          "jitter a manager restart reconciles every pool in lockstep "
          "forever. The clock is injected, because deadline behaviour is "
          "untestable against time.Now."),
        refs=ref("k8s_deadline")),
])

M06 = milestone(6, "Dynamic watches", p(
    "A child going "
    "unready, or an operator editing one by hand, changes nothing about the "
    "pool and so reaches nobody: two envtest specs have been carrying a "
    "nudge annotation since the drift milestone purely to force the pass a "
    "real cluster would never give them. Deleting that crutch is this "
    "milestone's proof, and the specs that replace it are stronger, because "
    "they wait for a reconcile the child edit itself caused.",
    "The builder milestone 03 wired up takes a second call beside "
    "<code>For()</code>: <code>Owns(&amp;appsv1.Deployment{})</code>, which "
    "says <em>watch this kind and enqueue whichever pool owns the object "
    "that changed</em>. It is the whole of child-watching for an ordinary "
    "controller, and it "
    "cannot express what this controller "
    "needs, since its kinds come out of a user-supplied template at runtime "
    "and some are CRDs that need not exist yet. So the watch is registered "
    "on first sight, and the next four commits are each a way that "
    "registration can look successful while delivering nothing: the wrong "
    "informer lookup, a Watch call whose nil return promises nothing, a "
    "record that survives the informer it describes, and a first sync "
    "mistaken for a failure. The last is the one that cannot be fixed by "
    "looking harder, because a missing CRD and a filling cache are "
    "identical at any single instant and differ only in how long they "
    "last."), [
    commit(
        "Scaffold watch.go and SetupWithManager",
        p("Predicates go on the primary watch and only there. Filtering the "
          "pool watch to generation, annotation and label changes drops the "
          "controller's own status writes; the same predicate on a child "
          "watch would discard exactly the events it exists for, since "
          "readyReplicas moving does not touch the generation."),
        refs=ref("cr_builder", "cr_predicate")),
    commit(
        "Implement ensureWatch for runtime GVKs",
        p("The watch enqueues the owner, not the child, because the pool is "
          "the only object this controller reconciles. Build rather than "
          "Complete, since Complete discards the handle watches attach to. "
          "And the mutex is not optional: two pools naming one kind at the "
          "same instant race on the map, and a concurrent map write in Go "
          "panics rather than losing an update."),
        fold("evidence", "the nudge removal, which is the milestone's proof",
             p("Both drift specs now assert what they always meant. One "
               "waits for the repair with nothing touching the pool; the "
               "other waits for the reconcile counter to move before "
               "holding its assertion open, which is strictly stronger "
               "than forcing a pass and assuming. Both fail if the watch is "
               "removed. The no-op apply spec keeps its nudge deliberately: "
               "it needs passes that change nothing, and a converged child "
               "emits no events by definition.")),
        refs=ref("cr_enqueue_owner", "cr_source_kind")),
    commit(
        "Use GetInformer with the unstructured object",
        p("GetInformerForKind builds the object itself through the scheme. "
          "For a CRD kind that fails outright; for a known kind it succeeds "
          "and is worse, because the cache keys informers by object type as "
          "well as GVK, so the typed lookup lands in the structured map "
          "while the handler sits on the unstructured one. Same kind, two "
          "informers, and every check that follows would interrogate the "
          "one with no handler attached."),
        refs=ref("cr_cache_pkg", "api_unstructured")),
    commit(
        "Verify the informer synced before caching the GVK",
        p("Kind.Start hands the lookup to a goroutine and returns "
          "immediately, so a nil from Watch promises nothing: an RBAC "
          "denial or an absent CRD gets recorded as a healthy watch, and "
          "the record is what stops anything retrying. Three states, not "
          "two, because Watch must happen exactly once while sync must be "
          "re-checked until confirmed."),
        refs=ref("cr_source_kind")),
    commit(
        "Track informer instances, not sync state",
        p("Treating synced as terminal never notices an informer that dies "
          "later. The subtler half is that a replaced informer is perfectly "
          "healthy and carries no handler, so any check asking whether a "
          "live informer exists gets a cheerful yes while the pool is deaf. "
          "One rule replaces the three states: Watch once per informer "
          "instance, verify liveness every pass."),
        fold("code", "ensureWatch once the instance is the state",
             block(tag(6), "internal/controller/watch.go",
                   start="func (r *PodPoolReconciler) ensureWatch(",
                   caption="internal/controller/watch.go")),
        refs=ref("cr_cache_pkg")),
    commit(
        "Give the first sync a grace window",
        p("The sync check fails deterministically the first time any pool "
          "names a kind, because the informer was created by that very "
          "call. Reporting it means every manager start warns per kind "
          "about something that resolves in milliseconds, which teaches "
          "operators to ignore the real one. No predicate can separate the "
          "cases, so a sentinel and a quiet requeue cover the window and a "
          "real failure follows it: the direction that swallows genuine "
          "failures has its own test."),
        refs=ref("cr_cache_pkg")),
    commit(
        "Write conditions on the watch-failure exit",
        p("The only exit that never called setConditions, so status showed "
          "no diff and nothing was written: a pool healthy until its CRD "
          "was uninstalled kept publishing Ready=True and a full set of "
          "counts while the controller could not see one of its children. "
          "The branch deliberately does not stamp observedGeneration, and a "
          "class-guard test pins that every early exit writes Ready, "
          "because the next one added will not be caught by reading the "
          "code."),
        refs=ref("k8s_api_conventions")),
])

M07 = milestone(7, "Capacity and opportunistic sizing", p(
    "A scavenger tier runs in whatever on-demand slack exists at "
    "expendable priority, and its right size is a number nobody can "
    "write down. A static percentage is wrong in both directions: too "
    "small and free compute goes unused while burst buys spot nodes "
    "that were not needed; too large and the group holds replicas that "
    "sit Pending forever while the pool runs below spec.replicas. So "
    "this milestone adds a fourth scaling shape whose ceiling is "
    "discovered by asking the scheduler, never declared and never "
    "predicted.",
    "The KWOK harness lands first, because envtest runs no scheduler "
    "and so cannot answer the only question being asked. Then the "
    "distribution gains an observed-capacity argument and a phase that "
    "consumes it, and stays a pure function throughout. The rest is "
    "the controller learning to fill that argument, which is where "
    "every interesting failure lives: four of the ten commits are "
    "corrections to the naive version of that reading."), [
    commit(
        "Add the KWOK test harness",
        p("envtest has no scheduler and no kubelet, so \u201cdoes this replica "
          "fit\u201d has no answer there and a test asserting a group shrank "
          "would be asserting against numbers it wrote itself. KWOK gives a "
          "real apiserver and a real scheduler over simulated nodes. It "
          "lands before the feature deliberately: a harness written "
          "afterwards grows the fixtures that make the existing "
          "implementation look right."),
        refs=ref("kwok", "kb_envtest", "k8s_scheduler")),
    commit(
        "Add observed capacity as an input to ComputeGroupTargets",
        p("The distributor could read the cluster itself, which would make "
          "it a function of the world at the moment it is called, or take "
          "the number as an argument. Taking it keeps the function pure, "
          "the tests a table, and the simulator portable. Absence in the "
          "map is a distinct value from zero: never sized, versus sized and "
          "found to have room for nothing.")),
    commit(
        "Size opportunistic groups from observed capacity",
        p("The plan had phase 3 and the overflow skip as separate commits, "
          "and they cannot be. An opportunistic group is (min)-only on "
          "paper, so phase 4 reads it as unbounded and hands it the same "
          "remainder phase 3 just did. Deleting phase 3 from the finished "
          "commit leaves all six table rows passing, which is what a "
          "commit that cannot be falsified looks like from the inside."),
        fold("why", "the phase order, and why both boundaries are load-bearing",
             p("After the target phase, so a declared share is honoured "
               "before free capacity is spent: the reliable tier must not "
               "be undercut by the expendable one. Before the overflow, so "
               "free compute is consumed before an unbounded group buys "
               "more. Moving phase 3 above phase 2 strands base at its min "
               "and fails the table; removing the phase 4 skip lets the "
               "scavenger swallow the remainder and fails it differently.")
             + block(tag(7), "internal/workload/algorithm.go",
                     start="func ComputeGroupTargets(",
                     caption="internal/workload/algorithm.go")),
        refs=ref("k8s_priority")),
    commit(
        "Add the probe state machine",
        p("Asking whether one more replica fits means writing target+1 and "
          "reading what the scheduler does with it. The extra replica is "
          "added outside the distribution\u2019s total, so no other group pays "
          "for an unproven question. decideProbe returns a struct because "
          "target+1 comes back from two structurally different places: a "
          "new probe and a re-assertion of an outstanding one produce the "
          "same number, so a bare pair cannot tell a caller which it is."),
        fold("why", "why the record is in memory and nowhere else",
             p("Status is the obvious place and the wrong one: other "
               "actors read it, an HPA writes to it, and a stored "
               "\u201cI am currently asking\u201d would have to be reconciled "
               "against a cluster that moved on during the restart that "
               "forgot the question. Losing it costs one early probe and "
               "the withdrawal of at most one Pending pod. Both harmless, "
               "and the next pass re-derives everything from the cluster."))
        + fold("code", "decideProbe at the end of the milestone",
               p("Four more commits correct this function before the tag, so "
                 "read the listing as where the argument lands rather than "
                 "as what this commit wrote: the verdict switch first, then "
                 "the timeout the milestone's last commit adds beneath it, "
                 "and only then the re-assertion of a probe still "
                 "outstanding.")
               + block(tag(7), "internal/controller/opportunistic.go",
                       start="func (r *PodPoolReconciler) decideProbe(",
                       caption="internal/controller/opportunistic.go"))),
    commit(
        "Observe the scheduler's verdict with an uncached reader",
        p("Pods are listed, never watched. A pod watch would be the largest "
          "memory decision in this controller, bought for a question asked "
          "at most once per heartbeat per group and only when that group is "
          "short. Three filters go server-side and the one that matters "
          "cannot: status.conditions[].reason is not indexable and the API "
          "server rejects the selector, which is exactly why the other "
          "three exist."),
        fold("why", "the ambiguity the API does not resolve",
             p("One Unschedulable covers a full node, a taint, an affinity "
               "rule and a topology spread constraint alike. The controller "
               "treats them the same, as capacity that is not available. "
               "Saying so beats implying a precision that is not there.")),
        refs=ref("k8s_pod_conditions", "k8s_fieldsel", "cr_reader")),
    commit(
        "Distinguish a missing child from an unreadable one",
        p("One boolean answered four questions, and only genuine absence is "
          "the cold start that phase 3 answers with the whole remainder. A "
          "transient read error, a child the cache cannot see, "
          "and a child under another owner all returned that same false. "
          "The damage does not stay local: phase 3 subtracts what it "
          "grants, so one misread child reprices every group after it, and "
          "the test asserts on the group that was not the problem."),
        refs=ref("cr_reader")),
    commit(
        "Hold the unjudged probe out of observed capacity",
        p("The probe writes target+1 and the next pass reads it back as "
          "real capacity, so the distribution hands this group a replica "
          "and takes one from burst \u2014 terminating a running spot pod to ask "
          "a question whose answer may be no. Capacity is what has been "
          "proven, not what has been requested.")),
    commit(
        "Gate on the child's spec.replicas, not status.replicas",
        p("During a scale-up the ReplicaSet lags: spec says 8, status says "
          "5, readyReplicas has caught up to 5. Read against status, "
          "ready >= asked is true, so every scale-up looks like a "
          "successful probe and the group grows by one replica every "
          "reconcile for as long as the rollout takes. No error, no event, "
          "and each step is the walk-up behaving exactly as designed.")),
    commit(
        "Add opportunisticHeartbeatSeconds with a schema default",
        p("Free capacity appears without any event this controller "
          "watches, so the only way to notice is to look again. That makes "
          "the requeue part of the feature, and the deadline helpers move "
          "into opportunistic.go with it. A pool with no opportunistic "
          "group returns zero, and that zero is control flow: read as a "
          "duration it would mean no requeue at all and silently disable "
          "every progress deadline in the cluster."),
        refs=ref("kb_markers_val")),
    commit(
        "Bound an outstanding probe, and warn when it times out",
        p("countUnschedulable only counts pods that exist carrying "
          "PodScheduled=False, so a pod blocked by a ResourceQuota is never "
          "created and never judged. Nothing stops: the group sits one "
          "replica below its real capacity, spinning on a 15-second "
          "requeue, reporting nothing wrong, forever. The check sits "
          "outside the found guard, because an unreadable child is exactly "
          "the case that can never answer, and below the verdict switch, "
          "because an answer arriving on the deadline is still an answer.")
        + p("Every lesson in this milestone is a property of a sequence rather "
            "than of a call. A walk-up, a backoff and a runaway all produce "
            "perfectly ordinary numbers at any single reconcile, which is why "
            "the first simulator structurally could not show them and why this "
            "one steps through time. A tick is one reconcile in the "
            "controller's own order.")
        + sim2.sim_html()
        + standalone("sim2-scavenger.html", "the scavenger simulator")
        + p("The three toggles are not illustrations. Each defeats one branch "
            "in the controller and replays the identical run, so the gap "
            "between the live lines and the ghost is the bug, measured. They "
            "are the same three corrections this milestone made: the gate on "
            "spec.replicas, the unjudged probe held out of capacity, and the "
            "heartbeat that has to exist because nothing else fires when a "
            "node frees up.")
        + p("Wedging the scheduler is what this last commit is for. The probe "
            "is written and then nothing happens to it: no pod, so no verdict, "
            "so no resolution. Watch the state hold at probe? for two minutes "
            "of simulated time and then read timeout \u2014 the only state a "
            "reader could not otherwise distinguish from a slow answer."),
        fold("evidence", "how the simulator is kept honest",
             p("The badge in the corner is the page checking its own JavaScript "
               "port against fixtures read out of the Go tests \u2014 not copied "
               "from them. Copies move with the port rather than with the "
               "source, which is exactly how the first version of this page "
               "came to model a decideProbe the controller had already "
               "replaced while reporting that all its rows agreed.")
             + p("Data alone is not enough, because the change that broke it "
                 "added a struct field and a whole new terminal state that no "
                 "existing row exercised. So the build also reads the shape of "
                 "the machine \u2014 the fields of probeState and probeDecision, "
                 "and the timing constants \u2014 and fails if the port claims to "
                 "model something the source no longer says.")),
        refs=ref("k8s_pod_conditions")),
])


M08 = milestone(8, "Admission: structure", p(
    "The first validation layer that can see more than one field at a "
    "time \u2014 and it arrives deliberately late. By now the distributor has "
    "been made defensive against every input this webhook rejects, so "
    "nothing here is load-bearing for correctness. It is load-bearing for "
    "the user\u2019s afternoon: the difference between a pool that quietly "
    "does the wrong thing and one that never gets stored.",
    "Most of the milestone is deciding what NOT to validate. CEL already "
    "covers the per-field combinations and, unlike this process, runs when "
    "this process is down, so the two rules duplicated here name the rule "
    "they duplicate and say why they are kept. Three of the nine commits "
    "are about the line between an error and a warning, and they keep "
    "reaching the same argument: a rule tightened after an object was "
    "admitted must not take away the only lever its operator has left."), [
    commit(
        "Scaffold the webhook with kubebuilder markers",
        p("A webhook is not a Go file. It is a Go file plus a Service, a "
          "Certificate, an Issuer, a CA-injection replacement rule, a "
          "NetworkPolicy and a patch mounting the serving cert \u2014 all of it "
          "shipped commented out. The markers on the two hooks generate the "
          "registration, so what the cluster calls and what answers cannot "
          "disagree about paths or verbs."),
        fold("why", "why the webhook gets its own envtest suite",
             p("The two suites need opposite worlds. This one installs the "
               "webhook configurations so a Create goes through admission; "
               "the controller\u2019s deliberately does not, and that absence is "
               "what lets its CEL tests prove the schema rejects something "
               "on its own. One suite could not say which layer said no.")),
        refs=ref("k8s_admission", "kb_webhook", "kb_markers_webhook",
                 "certmanager")),
    commit(
        "Add the defaulting webhook",
        p("min=0 when neither bound is present, and only then. A schema "
          "default would be better \u2014 it survives this process being down \u2014 "
          "but it cannot carry the condition, and a group that declared only "
          "a ceiling would come back with a floor its author never wrote. "
          "The test that asserts the defaulter does NOT write is the one a "
          "schema default could not satisfy."),
        refs=ref("kb_markers_val", "k8s_admission")),
    commit(
        "Validate scaling constraints per group",
        p("The two CEL-duplicated rules each name the rule they duplicate. "
          "Unreachable through admission in a healthy cluster, kept for a "
          "stale CRD and for direct unit calls \u2014 written down because a "
          "duplicated rule with no explanation is exactly what a later "
          "reader deletes as redundant, correctly, and then rediscovers.")
        + p("ValidateUpdate gains the one rule no single object can violate: "
            "the workload kind is immutable. The violation exists only in "
            "the relationship between the stored object and the new one, "
            "which is why no marker and no expression over self can see it."),
        refs=ref("k8s_crd_cel")),
    commit(
        "Validate cross-group rules",
        p("The webhook\u2019s actual justification. An opportunistic group "
          "cannot be last, because the replicas it cannot place have "
          "nowhere to go; and no unbounded group may precede it, because "
          "phase 4 gives the remainder to the first unbounded group in list "
          "order and one placed ahead intercepts the spill onto a tier that "
          "is already full. Measured on kwok, where an uncapped base "
          "swallowed the entire pool while burst sat at zero."),
        fold("evidence", "two rows that proved nothing until they were rewritten",
             p("The lifted \u201cnowhere to spill\u201d case put an unbounded group "
               "ahead of the opportunistic one, so both rules fired on it "
               "and disabling either left the other to keep the test green. "
               "Each row now isolates the rule it names, and one uses two "
               "opportunistic groups \u2014 the only shape on which the shared "
               "IsBounded and asking GroupCeiling give different answers."))
        + block(tag(8), HOOK, start="func validateOpportunistic(",
                caption="both cross-group rules, and the list order they "
                        "read"),
        refs=ref("kwok", "k8s_admission")),
    commit(
        "Format pointer fields in error messages",
        p("Every scaling constraint is a pointer, so absent and zero stay "
          "distinguishable, and interpolating one with %v prints its "
          "address. The previous commit did exactly that: min=0x215c99514060 "
          "in a message otherwise naming the right field and giving the "
          "right advice. The third test is the one that matters \u2014 returning "
          "\u201c\u201d for an unset pointer passes the other two while losing min=0, "
          "the value the defaulter injects and so the one most likely to "
          "appear.")),
    commit(
        "Bound the pool name at 63 characters",
        p("The pool name becomes a label value on every child, and a label "
          "value stops at 63 bytes. Create rejects; update does not. "
          "metadata.name is immutable, so an existing over-long pool cannot "
          "be renamed into compliance and rejecting its updates would leave "
          "its operator unable to scale it to zero \u2014 taking away the only "
          "lever left on the object the rule is complaining about."),
        refs=ref("k8s_labels", "k8s_dnslabel")),
    commit(
        "Warn when the pool is fully capped",
        p("Cap every tier and the pool has a hard ceiling; ask for more than "
          "it and the difference is left unplaced. A legitimate choice with "
          "a surprising consequence, so a warning. It shares IsBounded with "
          "the distributor, which is what makes an unreadable target count "
          "as capped here \u2014 correctly, since the distributor binds such a "
          "group at zero."),
        refs=ref("k8s_admission")),
    commit(
        "Reject a second unbounded group",
        p("An error this time, and the distinction is the point. Fully-capped "
          "is a choice; a second overflow sink cannot do what it says at any "
          "replica count, under any cluster state, ever. Here the shared "
          "predicate earns its keep in the opposite direction: an "
          "opportunistic group beside an unbounded one is fine, and a "
          "presence check would reject the configuration this project exists "
          "to run.")),
    commit(
        "Warn about a target the distributor cannot read",
        p("The sharpest instance of the error-versus-warning argument. CEL "
          "rejects these on create, so anything reaching here is a stored "
          "object, and ratcheting has left it scalable but not editable. "
          "Rejecting would close the one operation still open, on a pool "
          "whose operator may need to scale it down precisely because it is "
          "overspending. The wording is copied from the CEL message, so an "
          "operator who meets both is not left working out that they are the "
          "same complaint."),
        refs=ref("k8s_crd_ratchet")),
])


M09 = milestone(9, "Admission: rendered children and authorization", p(
    "The previous milestone validated the PodPool. This one validates the "
    "thing that actually runs \u2014 the child workloads, produced by a render "
    "the user never sees from a template plus overrides plus a replica "
    "count the distributor computes. A pool whose every field is legal can "
    "still produce a child that cannot be applied.",
    "Then the security half, which is the reason this controller needs an "
    "authorization check at all: whoever can create a PodPool makes it "
    "render an arbitrary pod spec and apply it under the CONTROLLER\u2019s "
    "RBAC. That is Deployment-create by another name, granted through a "
    "permission that looks like it is about pools. Two of the nine commits "
    "are neither of those, and exist because tightening validation is "
    "retroactively destructive in a way nothing at review time makes "
    "visible."), [
    commit(
        "Render every group's child at admission",
        p("Not a simulation: BuildChildWorkload, the real distribution, every "
          "group. Errors are collected per group rather than globally, which "
          "looks like bookkeeping and is what makes the update path two "
          "commits later able to compare a group against itself. The first "
          "rule it enables is that a PodPool may not be its own workload "
          "template \u2014 the render would succeed, and each generation would "
          "divide the replicas again."),
        refs=ref("k8s_admission")),
    commit(
        "Strict-decode children whose GVK the scheme knows",
        p("Unstructured is as permissive as JSON, so replicas: \u201cthree\u201d is a "
          "perfectly good map entry until the API server sees it. Decoding "
          "into the real Go type turns that into a rejection naming the "
          "group."),
        fold("why", "the ceiling this does not reach, stated rather than implied",
             p("A CRD template has no compiled type to decode against, so a "
               "misspelt CloneSet field is still admitted. Built-in workload "
               "kinds are type-checked; everything else is shape-checked. "
               "Unknown fields warn rather than reject, because unknown means "
               "unknown to this binary, not to the cluster.")),
        refs=ref("api_unstructured")),
    commit(
        "Reject overrides that clobber controller-owned paths",
        p("An override setting spec.replicas is merged and then overwritten "
          "on the next reconcile. Nothing errors; the field is simply not "
          "there afterwards. A setting with no effect is worse than a "
          "rejected one \u2014 it looks like configuration, it gets copied into "
          "the next pool, and the first time it appears to work will be a "
          "coincidence."),
        refs=ref("rfc7386")),
    commit(
        "Downgrade pre-existing errors to warnings on update",
        p("Ship a new render check and every stored pool that trips it "
          "becomes unpatchable \u2014 including by the patch that would fix it. "
          "So the update path renders twice, old and new, and an error "
          "present in both becomes a warning. Matching is on type plus "
          "detail rather than field path, because the path carries the group "
          "index and reordering groups would re-raise everything as new."),
        refs=ref("k8s_admission")),
    commit(
        "Check the requester may create the workload kind",
        p("A SubjectAccessReview for the workload the template names, using "
          "the requesting user\u2019s identity from the admission request. The "
          "subtlety is that a SAR authorizes a *resource*, and nothing "
          "computes one from a Kind: a plural is whatever a CRD\u2019s "
          "spec.names.plural says. Cannot resolve means cannot authorize "
          "means denied, and no SAR is sent for an unresolved resource, "
          "because it would authorize a different noun.")
        + p("This commit resolves it from a hardcoded table of three entries "
            "&mdash; Deployment, StatefulSet, DaemonSet &mdash; which covers "
            "the built-in workload kinds and nothing else. That is the "
            "guess the next commit is about."),
        refs=ref("k8s_sar", "k8s_authz_check", "k8s_rbac_escalation")),
    commit(
        "Ask discovery for the plural before guessing it",
        p("meta.RESTMapper knows what the table cannot. The key is GroupKind, "
          "not Kind, and that is the security-relevant half: a CRD may "
          "declare Kind Deployment in its own group, and answering "
          "\u201cdeployments\u201d for it authorizes against apps/v1 instead."),
        fold("why", "why neither half of that bug is visible alone",
             p("Consulting the table first is invisible while it is "
               "GroupKind-keyed, because a CRD group never hits it. Keying on "
               "Kind alone is invisible while discovery answers first. Only "
               "both together resolve example.com/v1 Deployment to "
               "\u201cdeployments\u201d \u2014 which is what four of these tests catch when "
               "that exact shape is put back."))
        + block(tag(9), HOOK,
                start="func (v *PodPoolCustomValidator) pluralResource(",
                caption="discovery first, the three-entry table only as "
                        "fallback"),
        refs=ref("api_restmapper", "cg_discovery")),
    commit(
        "Fail closed, but honestly",
        p("Two failures, two answers. The requester lacking permission is "
          "about them: Forbidden. The authorizer being unreachable is about "
          "us: InternalError, because telling an operator their "
          "workloadTemplate is wrong when they wrote nothing wrong sends "
          "them to the wrong place entirely. Only the classification moves; "
          "both still deny.")
        + p("The update path gains the other half: a create-time SAR vouches "
            "for the GVK admitted then, and only carries forward while the "
            "stored GVK is readable and unchanged. The condition states that "
            "invariant rather than testing for the unreadable case, so the "
            "authorization cannot quietly vanish if the immutability rule is "
            "ever relaxed."),
        block(tag(9), HOOK,
              start="func (v *PodPoolCustomValidator) checkWorkloadAuthorization(",
              caption="the SubjectAccessReview guard: every path that cannot "
                      "answer denies"),
        refs=ref("k8s_sar")),
    commit(
        "Skip spec validation when the spec is unchanged",
        p("Pausing a pool &mdash; the annotation milestone 11 adds, two "
          "milestones from here &mdash; is a metadata write, so gating "
          "metadata writes on spec validity makes a pool unpausable in "
          "exactly the states that make someone want to pause it. The rule "
          "has to be written now because admission is being built now, and "
          "the feature it protects does not exist yet. What licenses the "
          "early return is precise: everything below is a function of spec "
          "plus three immutable metadata fields. The tests count "
          "SubjectAccessReviews, because a short-circuit that fails to "
          "engage still admits the request \u2014 only the zero count separates "
          "\u201creturned early\u201d from \u201cran everything and agreed\u201d.")),
    commit(
        "Warn on the StatefulSet ordinal budget",
        p("Pods are named <code>&lt;sts&gt;-&lt;ordinal&gt;</code> and that "
          "becomes a hostname "
          "bounded at 63 bytes, so pool, group and a number nobody wrote "
          "share one budget. A warning and not an error: the controller does "
          "not own the child type\u2019s semantics, and the moment it starts "
          "rejecting on those it has to be right about all of them, for "
          "every kind, forever."),
        refs=ref("k8s_sts_netid", "k8s_dnslabel")),
])


M10 = milestone(10, "Observability and hardening", p(
    "Whether anyone can tell is a separate question "
    "— at 3am, from a dashboard, without reading the source. Metrics answer "
    "“what did it look like at 03:14 last Tuesday”, which status never can; "
    "events answer “what happened”, which conditions overwrite on recovery; "
    "and the terminal classification answers the question an operator is "
    "actually asking of a fleet: which pools are waiting on a human, and "
    "which are merely slow.",
    "The running theme is that every signal needs a gate, every gate needs "
    "a key, and the key is a design decision. Group events gate on a reason "
    "persisted in status; once-only warnings gate on a process-lifetime map "
    "keyed by GVK; and getting a key wrong is silent — either spam nobody "
    "reads or a transition nobody hears."), [
    commit(
        "Add metrics.go: replica and group gauges",
        p("Eight gauges, and cardinality is the entire design constraint: "
          "every label is bounded by the API — names are DNS labels, groups "
          "cap at 32 — and nothing user-varied is ever a label. The "
          "deletions matter more than the writes: a deleted pool’s series "
          "otherwise report its last known values forever, and an alert on "
          "ready < desired fires eternally against an object nobody can "
          "fix."),
        fold("evidence", "the 56-year stall",
             p("A group at its target has no last-progress timestamp, and "
               "its gauge gets the series deleted rather than set to zero. "
               "Zero is a real unix timestamp — January 1970 — so a "
               "dashboard plotting “time since last progress” against a "
               "zeroed series reads 56 years of stall on a healthy group.")),
        refs=ref("kb_metrics", "prom_naming", "prom_instr")),
    commit(
        "Export conditions as metrics",
        p("A condition is already a time series; the question is who turns "
          "it into one. The alternative is every consumer maintaining a "
          "kube-state-metrics config that mirrors this CRD’s status schema "
          "in someone else’s repository. One series per (type, status) "
          "pair, all three statuses written every pass — write only the "
          "active one and a True→False flip leaves the True series reading "
          "1 forever, so the obvious alert matches two contradictory "
          "series."),
        refs=ref("prom_instr", "k8s_api_conventions")),
    commit(
        "Register gauges in slices so cleanup cannot be forgotten",
        p("Three hand-maintained lists have become three slices that "
          "registration and cleanup both iterate: a gauge added to a slice "
          "arrives with its cleanup already written. The registry sweep "
          "test enumerates nothing itself, so gauge number ten is covered "
          "the day it is added."),
        fold("evidence", "the mutation that found the same bug in the tests",
             p("The drop-a-group test iterated groupGauges — the very list "
               "under test — so deleting an entry shrank the assertion in "
               "lockstep and the test stayed green while the gauge leaked. "
               "It now counts series from the registry by group label. A "
               "test that derives its expectations from the list it is "
               "checking cannot see that list shrink."))),
    commit(
        "Add the event recorder for state transitions",
        p("Events mark transitions; conditions carry state. These are Event "
          "objects in the API, which is a different thing wearing the word "
          "milestone 06 spent seven commits using for watch notifications. "
          "Eight sites "
          "become events, and every one is gated, because a pool wakes on a "
          "requeue floor whether or not anyone touches it — an ungated "
          "warning is one event per interval per pool for as long as the "
          "fault lasts. classifyGroupError decides each failure’s class "
          "once, so the condition and the event can never disagree about "
          "what happened."),
        refs=ref("k8s_events", "cg_recorder")),
    commit(
        "Gate each group's event on that group's own reason",
        p("The gate the recorder shipped with reads the pool-level "
          "GroupsReady tuple, and a pool-level signal cannot answer a "
          "question about one group. It silences a real transition — a "
          "group moving from retryable failure to ownership conflict leaves "
          "the tuple unmoved — and announces unreal ones, re-emitting a "
          "group’s unchanged failure whenever a neighbour recovers. The "
          "gate becomes per group, against the reason already persisted in "
          "status.groups[], so it needs no bookkeeping and survives a "
          "restart."),
        fold("evidence", "the mutation only one test can see",
             p("Compare against this pass’s own status instead of the deep "
               "copy from the top of Reconcile and every reason equals "
               "itself: total silence. Every “emits exactly one” assertion "
               "in the suite is satisfied by zero. "
               "TestFirstFailureAlwaysEmits exists for precisely this "
               "mutation, and it takes six tests down with it."))),
    commit(
        "Deduplicate once-only warnings per GVK",
        p("The omitempty trap made real: absent readyReplicas means “zero "
          "or never published”, and a kind that never publishes readiness "
          "reads 0 ready forever with nothing saying why. The warning "
          "cannot use the group-event gate — nothing about it is persisted "
          "to diff against — and it is a fact about the kind, not the "
          "pool, so per-pool dedup would repeat it once per pool. A "
          "process-lifetime map keyed by GVK, and the two dedup designs now "
          "coexist with the reason stated rather than unified badly.")),
    commit(
        "Warn about unpublished readiness at the deadline, not on sight",
        p("The gate above false-fires on every healthy pool, for a measured "
          "reason: a Deployment mid-rollout with zero ready pods stores "
          "readyReplicas absent — byte-identical to a kind that never "
          "publishes it. Only elapsed time separates the two readings, and "
          "the progress deadline is already “long enough”. The map inverts "
          "from “kinds complained about” to “kinds proven to publish”, "
          "latching, so a proven kind’s later zero is never misread as "
          "unsupported."),
        fold("evidence", "pinning the premise against Kubernetes itself",
             p("status_wire_state_test.go creates a real Deployment, writes "
               "readyReplicas as 0 through the status subresource, reads it "
               "back unstructured, and asserts the key is absent. The whole "
               "bug family lives in what the API server actually stores, so "
               "the premise is pinned against the API server — if a future "
               "Kubernetes drops omitempty, that test fails before the "
               "inference quietly stops being true.")),
        refs=ref("k8s_api_conventions")),
    commit(
        "Publish metrics beside the one status write, not at the bottom",
        p("The recording call sat where the aggregates are computed, which "
          "is unreachable from every early exit — and the early exits are "
          "the transitions worth alerting on. A broken template froze the "
          "gauges at the pool’s last healthy values: alerting keyed on the "
          "metric never fires, alerting keyed on the object cannot see the "
          "metric. The call moves into the same defer as the status patch "
          "and derives everything from pool.Status, so a future early "
          "return gets correct metrics by construction rather than by "
          "remembering."),
        refs=ref("kb_metrics")),
    commit(
        "Demote per-reconcile logging to V(4), adopt klog key conventions",
        p("One line per pass is noise at 2 pools and an outage at 2000. The "
          "counterweight is the orphan sweep’s deletion staying at level "
          "0: deleting a running workload is the most consequential thing "
          "this controller does, and a sweep that ran silently is "
          "indistinguishable from one that never ran. The tests install a "
          "recording logger on the context and drive a real reconcile — "
          "asserting what V(4) means proves nothing about the call "
          "sites."),
        refs=ref("k8s_logging")),
    commit(
        "Classify terminal failures in errors.go",
        p("Backoff assumes retrying eventually works. For a spec error it "
          "never does, and the retries bury the pools that need a human "
          "among the pools that are merely slow. isTerminal uses errors.As "
          "because every error is wrapped on its way up — the bill for the "
          "%w discipline held since the first error return. Suppression is "
          "all-or-nothing, and the count comparison quietly protects the "
          "orphan sweep: a failed delete is in the error list but never in "
          "terminalGroups, so it still requeues."),
        fold("why", "two orderings that look load-bearing and are not",
             p("An ownership conflict is never terminal, so in both the "
               "classifier and the GroupsReady switch the arms are disjoint "
               "and swapping them changes nothing — mutation proved it. "
               "What actually produces “ownership outranks a spec error” "
               "is the all-or-nothing guard. The comments now say "
               "“defensive” rather than repeating a claim the code does "
               "not support.")),
        refs=ref("go_errors_as")),
    commit(
        "Handle the 500-vs-422 trap",
        p("Mistype a template field and server-side apply fails with "
          "“failed to create typed patch object” — an HTTP 500 with an "
          "empty reason, not a 422 — so IsInvalid misses the single most "
          "common user error and the pool wedges. The classifier matches "
          "the message, deliberately: other 500s must stay retryable, "
          "because a child type’s own webhook being down is also a 500. If "
          "upstream rewords it, the match falls back to retryable, which "
          "is the recoverable direction. 403 stays retryable too — RBAC "
          "is fixed without touching the spec."),
        fold("evidence", "the envtest canary for a message match",
             p("A unit table pins our fabricated copy of the error, which "
               "agrees with itself forever. terminal_envtest_test.go "
               "provokes the real rejection from a real apiserver, so an "
               "upstream rewording fails there while the unit rows keep "
               "passing — the divergence is the signal to update the "
               "match."))
        + block(tag(10), "internal/controller/errors.go",
                start="func isTerminalAPIError(",
                caption="the classifier: which failures are the spec's fault "
                        "and which are the cluster's"),
        refs=ref("api_isnotfound", "k8s_ssa")),
])


M11 = milestone(11, "Operability", p(
    "The knobs an operator reaches for at 3am, and the memory footprint "
    "they inherit. Half the milestone is the pause annotation — the escape "
    "hatch for “stop touching my cluster while I work” — and half is the "
    "manager growing up: a cache that stops paying for other people's "
    "objects, flags for the tuning every incident review eventually asks "
    "about, and readiness that stops lying about a webhook with no "
    "certificate.",
    "Pause looks like one feature and lands as four commits, because the "
    "obvious implementation is wrong three separate ways: it writes "
    "conditions outside the one writer, it reports a spec it never acted "
    "on as observed, and its resume announcement re-fires without bound. "
    "Each wrongness gets pinned by a failing test before it gets fixed."), [
    commit(
        "Extract the group rollup from Reconcile",
        p("The milestone opens by paying down a debt it did not incur. "
          "<code>Reconcile</code> stands at 30 by gocyclo, one branch under "
          "the scaffold's default, and the pause work below spends it — so "
          "the budget would otherwise be discovered by a failing lint on the "
          "commit that happened to be last, rather than by the commits that "
          "filled it.",
          "Two blocks move out, and neither is new: stamping progress "
          "timestamps and the int64 rollup that computes the pool's totals "
          "have been inline since the lifecycle milestone. Both are total "
          "functions over a slice of rows — no client, no pool beyond the "
          "previous status, nothing to mock — which is the tell that they "
          "were never part of what Reconcile is for. Four branches leave and "
          "the function reads 26."),
        fold("why", "why the group loop stays, and why the number lies",
             p("Moving the loop itself would mean threading the pool, the "
               "status it was read with, the parsed template, the resolved "
               "kind and the computed targets in, and five values back "
               "out — and the deferred status write at the top of the pass "
               "needs everything below it in one scope. That is a worse "
               "function than the one it replaces.",
               "It is also worth saying what the metric does not measure. "
               "gocyclo counts a flat guard clause exactly like a nested "
               "branch, and this function is overwhelmingly flat: 134 of "
               "its 177 code lines sit at a single level of indentation. "
               "The number overstates the difficulty, which is why the "
               "answer here is to move the two blocks that genuinely did "
               "not belong rather than to raise the threshold until the "
               "complaint stops.")),
        refs=ref("golangci")),
    commit(
        "Pin the clock seam, and forbid time.Now by lint",
        p("The Clock field is not new — it arrived with the progress "
          "deadline, because a deadline is untestable against the wall "
          "clock. This commit is the ratchet: a forbidigo rule bans "
          "time.Now in production code, so the next “how long since X” "
          "reaches for the injected clock rather than compiling fine, "
          "reviewing clean, and being untestable from that day on. The "
          "envtest suite now leaves Clock unset, so one construction path "
          "proves the production nil-default instead of every path "
          "swapping in a fake."),
        refs=ref("golangci")),
    commit(
        "Pause reconciliation via annotation, honouring its value",
        p("podpools.dev/paused stops the controller creating, updating, or "
          "deleting children, while status, metrics and the scale "
          "selector keep publishing — a paused pool is frozen, not "
          "invisible. The commit's real content is that the VALUE is "
          "parsed: presence-only is what Cluster API does, but a chart "
          "rendering paused: {{ .Values.paused }} produces the literal "
          "string “false”, and a presence-only check then freezes the "
          "pool while the manifest says the opposite. Anything "
          "unparseable pauses — someone who typed a value meaning to "
          "pause should get a pause, not a silently running pool."),
        fold("why", "why pause must survive a broken spec",
             p("Pause is an annotation, and the webhook returns early on "
               "any update that does not change spec — so a pool whose "
               "stored spec no longer passes today's validation is still "
               "pausable. That is not a loophole; it is the point. The "
               "states that make an operator reach for pause are exactly "
               "the states where revalidating the whole spec would lock "
               "them out."))),
    commit(
        "Fold the paused case into setConditions",
        p("The exit shipped hand-writing its two conditions, duplicating "
          "the generation stamp and the message outside the one writer "
          "this codebase already has. Routing it through conditionInputs "
          "buys a behaviour, not just tidiness: the retired-type prune now "
          "runs for paused pools — and a paused pool is precisely the one "
          "that sits untouched for months carrying a stale type across a "
          "controller upgrade. The end-to-end test fails against the "
          "previous commit, which is the point of cutting them "
          "separately."),
        refs=ref("api_setcondition")),
    commit(
        "Stop reporting a paused pool's spec as observed",
        p("setConditions stamped ObservedGeneration before it branched, so "
          "a pool edited during a pause reported the edit as settled to "
          "kubectl wait, CI gates and Argo alike. The worse half corrupts "
          "state: genChanged is derived from the stored value, so the "
          "stamp makes it false on resume, LastProgressTime keeps its "
          "pre-pause value, and a pause longer than the deadline reports "
          "ProgressDeadlineExceeded the instant it resumes. The paused "
          "arm moves above the stamp and shares haltedConditions with the "
          "watch-failure exit — “stopped before doing any work” is one "
          "idea with two causes."),
        refs=ref("k8s_api_conventions")),
    commit(
        "Emit Paused and Resumed where conditions change",
        p("Both events reuse the condition-tuple gate — no third dedup "
          "mechanism. The trap is Resumed: gate it on the Ready reason "
          "read from etcd at the top of the pass, and every pass that "
          "exits without writing announces the resume again. Measured: a "
          "pool unpaused while its workload CRD was missing produced five "
          "Resumed events in one 30-second sync-grace window. The read "
          "and the announcement split — wasPaused is snapshotted, and "
          "announceResume fires only at the three exits that actually "
          "rewrite conditions."),
        fold("evidence", "a defensive check, proven defensive by mutation",
             p("Dropping announceResume's tuple check passes the whole "
               "suite: every current call site sits directly after a "
               "write that moves Ready off Paused, so wasPaused alone "
               "already implies the change. What breaks the five-events "
               "loop is the call-site placement, not the check — the "
               "comment says so, rather than claiming a mechanism the "
               "code cannot exhibit.")),
        refs=ref("k8s_events")),
    commit(
        "Strip cached objects with DefaultTransform",
        p("The controller caches children it never reads in full, and the "
          "two heaviest fields are ones it never reads at all: "
          "managedFields, which SSA maintains and which routinely "
          "outweighs the spec, and the last-applied annotation, a full "
          "JSON copy of the object. Dropping both at cache-insert is the "
          "cheapest memory win available. DefaultTransform rather than "
          "per-type, so the unstructured informers ensureWatch creates at "
          "runtime inherit it — which is where the weight actually "
          "lives."),
        refs=ref("cr_cache", "k8s_ssa_fields")),
    commit(
        "Scope the cache with DefaultLabelSelector",
        p("Cache only what carries managed-by, or a pool whose template "
          "names Deployment caches every Deployment in the cluster and "
          "the controller's memory scales with other people's workloads. "
          "The trap: an empty ByObject{} entry does not opt the PodPool "
          "out — a nil Label cascades to the default selector, every "
          "user-created pool vanishes from the cache, and the controller "
          "goes silently deaf. Opt out with Label: labels.Everything()."),
        fold("why", "what a cache miss means once the cache is blind",
             p("A label-scoped cache reports an unlabelled stranger as "
               "NotFound, and adopting on that miss would force-apply our "
               "labels over someone else's object. The envtest proves the "
               "uncached absence check still refuses it — and that a "
               "child the pool genuinely owns but which lost its labels "
               "is re-adopted and re-labelled so the cache can see it "
               "again.")),
        refs=ref("cr_cache", "k8s_labels")),
    commit(
        "Expose concurrency and rate-limiter flags; settle production defaults",
        p("max-concurrent-reconciles, the backoff bounds, and client "
          "QPS/burst — each validated at startup, because "
          "controller-runtime silently substitutes 1 for a zero worker "
          "count and inverted backoff bounds fail at runtime rather than "
          "at parse. The QPS default is -1 on purpose: client-side "
          "throttling is disabled in favour of server-side API Priority "
          "and Fairness, and re-enabling it while raising concurrency "
          "just starves workers. The production defaults settle in the "
          "same pass: JSON logging, a stable leader-election ID, and a "
          "graceful-shutdown window that actually fits inside the pod's "
          "termination grace period."),
        block(tag(11), "cmd/main.go", start="func bindFlags(",
              caption="every flag, its default, and the help text an "
                      "operator reads at 3am"),
        refs=ref("cr_manager", "cg_rest", "k8s_apf", "k8s_pod_termination")),
    commit(
        "Gate readiness on cache sync and webhook certs",
        p("Ready-before-serving means the first admission request after a "
          "rollout hits a webhook with no certificate, and failurePolicy: "
          "Fail turns that into a rejected kubectl apply. The cache half "
          "is a one-shot latch, and one-shot is the design decision: a "
          "live WaitForCacheSync waits on every informer ensureWatch ever "
          "registers, so one PodPool naming an uninstalled CRD would pull "
          "every replica out of the webhook Service at once. Standbys "
          "must become ready too — the Service selects every pod — so the "
          "latch declines leader election, and liveness stays a bare ping "
          "so an apiserver blip cannot restart the fleet."),
        refs=ref("k8s_probes", "certmanager", "cr_manager")),
])


M12 = milestone(12, "E2E, RBAC, and CI", p(
    "Prove it on a real cluster, then lock it down. Everything below the "
    "webhook has been tested against envtest (no scheduler, no garbage "
    "collector) or KWOK (no kubelet, manager in-process); this milestone is "
    "the first time the deployed manager, the cert-manager-issued webhook, "
    "and the API server's garbage collector are all real at once. Then the "
    "lockdown: the manager's RBAC shrinks to the verbs the code exercises, "
    "humans get /scale, and every gate built along the way — drift check, "
    "build-tagged suites, security scans, the linters themselves — gets "
    "wired into CI and asserted by a test, because a guard nothing "
    "verifies is a guard that gets deleted in a refactor.",
    "The milestone kept teaching while it was being cut. Running make "
    "test-e2e for the first time in weeks found the e2e package did not "
    "even build (a literal 50% in a Sprintf format string), and running "
    "make lint-config found two misspelled linter settings that had been "
    "silently ignored all along. Both breaks had the same shape: a check "
    "that exists but runs nowhere is indistinguishable from a check that "
    "passes."), [
    commit(
        "Set up the Kind e2e cluster and harden the deploy path",
        p("Four fixes, all found by actually running the scaffold's e2e "
          "path: recreate a Kind cluster that survived a reboot in name "
          "only; commit the image pin that make deploy's kustomize edit "
          "writes into the working tree; record why the Prometheus "
          "overlay stays off (ServiceMonitor is an operator CRD, and "
          "nothing else ever applied these manifests); and wait for the "
          "webhook Service endpoints and CA bundles, because deployed is "
          "not ready and an early spec reads cert-manager latency as a "
          "controller bug."),
        refs=ref("kind", "certmanager")),
    commit(
        "Run the e2e suite: the PodPool lifecycle on a real cluster",
        p("Create a two-group pool and watch the 3/3 split appear; scale "
          "to 10 and watch base stop at its 50% target while burst "
          "absorbs the rest; drop the burst group and watch the orphan "
          "sweep take its child; delete the pool and watch "
          "ownerReferences cascade. The fixture caps base deliberately — "
          "an uncapped first group swallows the pool and the distribution "
          "specs would pass with the algorithm deleted. make test and "
          "make test-e2e both gain -race: four mutexes guard concurrent "
          "state across five concurrent reconciles, and none of it is "
          "checked by a detector that is not running."),
        fold("evidence", "the fixture literal that did not build",
             p("The manifest lives inside a fmt.Sprintf format string, "
               "where a lone % is an unknown verb — so the helper spells "
               "it 50%%. In the history this tutorial is based on, the "
               "unescaped literal shipped, and the package silently "
               "stopped building: only make test-e2e builds the e2e tag, "
               "and nobody had re-run it since the fixture changed. The "
               "same rot the kwok tier already taught, on the other "
               "tag.")),
        refs=ref("go_race", "k8s_gc")),
    commit(
        "Drive the four mutexes so -race has something to catch",
        p("The detector is on; nothing was executing the guarded paths. A "
          "race detector reports only what a test actually runs, and "
          "no test had ever put two goroutines at the same map, so the "
          "four mutexes were asserted by comment and by nothing else.",
          "What the concurrency <em>is</em> decides the shape of the "
          "tests. controller-runtime never runs two reconciles for the "
          "same object key at once, so no two workers contend for a "
          "single pool's entry — they contend for the map, each holding "
          "a different pool. Racing two goroutines over one key would "
          "test an interleaving the workqueue cannot produce. So each "
          "test points sixteen distinct pools at one shared reconciler, "
          "and the failure it looks for is a lost or corrupted "
          "<em>neighbouring</em> entry: the readiness latch dropped by a "
          "concurrent lazy map init, a probe record overwritten so the "
          "group re-probes forever, a warning gate lost so the pool "
          "re-announces on every heartbeat. ensureWatch is the one place "
          "workers legitimately converge on a single key — distinct "
          "pools naming the same workload kind, the ordinary case on a "
          "manager start — and a second handler on one informer cannot "
          "be removed, so every child event is delivered twice for the "
          "life of the process."),
        fold("evidence", "each guard removed, one at a time",
             p("Every mutation is caught: readyPublishedMu and probeMu "
               "produce a fatal concurrent map write on top of the race "
               "report, outOfRangeMu and watchMu report the race alone. "
               "The first draft of the probe test asserted the wrong "
               "invariant — it fed the same settled observation to every "
               "worker and expected one probe, when a settled "
               "observation after an outstanding probe is a "
               "<em>success</em> and licenses the next one by design. "
               "The test failed, and it was the test that was wrong.")),
        refs=ref("go_race", "cr_cache_pkg")),
    commit(
        "Audit RBAC: least privilege by subtraction",
        p("The scaffold grants the manager create, update, patch, and "
          "delete on its own CRD; the controller calls none of them. The "
          "verbs are pinned from two directions, because the two "
          "regressions fail opposite ways: a golden test compares "
          "role.yaml's complete rule set against a reviewed inventory "
          "(a widened marker passes the drift check — the manifests are "
          "in sync — and fails here), and an envtest impersonates a "
          "ServiceAccount bound to the shipped rules and asks the real "
          "authorizer both ways, so over-trimming fails like a "
          "production 403 instead of in review."),
        fold("code", "the manager role in full at this tag",
             p("Worth reading beside the scaffold's grant in milestone 00: "
               "same file, same generator, and the difference is the audit.")
             + block(tag(12), "config/rbac/role.yaml", lang="yaml",
                     caption="what survived the subtraction")),
        refs=ref("k8s_rbac")),
    commit(
        "Split human roles from the manager role",
        p("RBAC treats a subresource as a distinct resource, so the trim "
          "left every human role unable to kubectl scale — resources: "
          "[podpools] does not match podpools/scale. Admin and editor "
          "get it; the viewer does not, because /scale carries update. "
          "The editor role now states the privilege equivalence out "
          "loud — PodPool write access is workload write access, M09's "
          "SubjectAccessReview is why the role is safe to hand out — and "
          "SECURITY.md carries the full analysis. role_binding moves to "
          "an aggregated ClusterRole, the sanctioned extension point for "
          "new workload types, proven live on Kind because envtest runs "
          "no kube-controller-manager and structurally cannot see "
          "aggregation fire."),
        refs=ref("k8s_rbac_userfacing", "k8s_rbac_agg", "k8s_crd_scale")),
    commit(
        "Check generated output for drift in CI",
        p("verify-generate regenerates everything, tidies go.mod, and "
          "fails on git status --porcelain — porcelain, not diff "
          "--exit-code, because diff cannot see the newly generated file "
          "that a new API type produces. YEAR is pinned to a literal: "
          "$(shell date +%Y) rewrites the boilerplate on 1 January and "
          "fails every PR with a message pointing at a copyright line. "
          "And the check auto-repairs disk-side edits (regeneration runs "
          "first), so what it actually guards is the committed side — "
          "markers changed without regenerating — which is exactly the "
          "half the RBAC golden cannot cover."),
        refs=ref("kb_gen_crd")),
    commit(
        "Run the KWOK suite in CI",
        p("A separate workflow with a pinned kwokctl and a cached 443 MB "
          "of control-plane binaries, non-required until a flake rate is "
          "measured. The pin and the cache are coupled: an unpinned "
          "kwokctl breaks CI on a commit that changed nothing, and the "
          "cache key is the version, so without the pin the cache would "
          "never be warm either."),
        refs=ref("kwok")),
    commit(
        "Pin the toolchain, not just the language version",
        p("<code>go 1.26.0</code> in go.mod is a statement about language "
          "features. CI reads the same line as a toolchain version — "
          "setup-go's go-version-file resolves it and installs exactly "
          "1.26.0 — and that release carries twenty known standard-library "
          "vulnerabilities across crypto/tls, crypto/x509, net/url, "
          "net/textproto and mime.",
          "None of them are in this code. Every trace govulncheck reports "
          "ends in the standard library, reached through an ordinary client "
          "Get or an exec, and every one is fixed in a patch release. So the "
          "scan added next fails on a machine that has never run this "
          "project while passing on any developer's machine running a "
          "current Go — and that gap is invisible until something scans."),
        fold("why", "why a toolchain line rather than bumping the go line",
             p("The language requirement has not moved; only the floor on "
               "the build has. A <code>toolchain</code> directive says "
               "exactly that, survives <code>go mod tidy</code>, and holds "
               "even when a runner installs an older Go, because the go "
               "command honours the directive itself and fetches the newer "
               "toolchain. Raising the go line instead would tell every "
               "consumer their language version was insufficient, which is "
               "not what was discovered.",
               "It lands before the scanner rather than after it, so the "
               "commit that introduces vulnerability scanning is green on "
               "the pass that introduces it. A check that arrives red "
               "teaches its reader to ignore it.")),
        refs=ref("govulncheck")),
    commit(
        "Add security scanning to CI and a local pre-commit hook",
        p("govulncheck with reachability analysis, hadolint on the "
          "Dockerfile, and a Trivy image scan that fails only on "
          "fixable HIGH/CRITICAL findings — an unfixable base-image CVE "
          "is not actionable, and a permanently red gate teaches people "
          "to stop reading it. The weekly schedule is the part a push "
          "trigger cannot give: CVEs disclose on their own calendar. "
          "hack/pre-commit is the local half, and the hadolint CI job "
          "exists precisely because the hook is skippable and silently "
          "skips when the binary is missing."),
        refs=ref("govulncheck", "trivy", "hadolint")),
    commit(
        "Adopt the linters as a ratchet",
        p("Honesty first: this configuration has gated every commit since "
          "M00 from outside the tree, so nothing earlier violates it. "
          "The commit records how you would adopt the set, and which "
          "linters are load-bearing: errorlint protects M10's errors.As "
          "classification from one careless %v; nolintlint fails "
          "suppressions whose linter reports nothing; kube-api-linter "
          "enforces by machine what M01 applied by hand. The custom "
          "golangci-lint binary is deleted — it existed to carry one "
          "plugin that a stock linter now covers."),
        fold("evidence", "the misspelled settings that were never applied",
             p("make lint-config found str-concat for strconcat and "
               "klogr for klog: run ignores an unknown settings key "
               "silently, so both options had never been applied, and "
               "nothing failed until config verify looked. The setting "
               "you think you have is exactly the one that is not "
               "applied.")),
        refs=ref("golangci")),
    commit(
        "Add test/ci: assert the CI wiring exists",
        p("Tests that read the repository's own configuration: the "
          "build-tagged suites compile (both tiers — the e2e one earned "
          "its guard this milestone), the lint config carries the kwok "
          "tag, the kwok workflow pins and caches, every action is "
          "SHA-pinned, and actionlint validates the workflow YAML "
          "itself. The package is untagged so make test runs it — the "
          "guards guard themselves. The self-check feeds every probe a "
          "minimal conforming input and requires it to accept, because "
          "a probe that cannot parse its input goes green for the wrong "
          "reason and stays green when the gate it watches is "
          "deleted."),
        refs=ref("actionlint", "gh_hardening")),
    commit(
        "Run each workflow only when its inputs can have changed",
        p("Every job reruns on every pull request regardless of what the "
          "pull request touched. The tutorial document is the case that "
          "makes it obvious: it changes docs/ and nothing else, and it "
          "would otherwise spend about sixteen minutes proving that Go "
          "nobody edited still compiles.",
          "Measured here — e2e 5.8 minutes, tests 3.9 to 4.8, lint 2.1 to "
          "2.6, the image scan 1.9, govulncheck 0.3, hadolint 0.1 — and "
          "those last two are why this is one coarse paths-ignore rather "
          "than a filter per job. Six seconds of hadolint does not repay a "
          "conditional somebody has to reason about later, so the filter "
          "skips only what provably cannot matter."),
        fold("why", "the asymmetry, and the bill it defers",
             p("The Go tiers ignore <code>.github</code> because workflow "
               "YAML cannot change what the compiler thinks. The security "
               "workflow does not, and that difference is the whole point: "
               "actionlint reads those files for a living, so ignoring them "
               "would switch the check off in exactly the case it exists to "
               "cover.",
               "The cost, recorded because someone will pay it: a pull "
               "request that changes only a workflow no longer runs that "
               "workflow, so its own edit goes unproven until it lands. "
               "Removing that one line restores the proof at the price of a "
               "full suite on every CI tweak.",
               "And a trap for later — paths-ignore does not compose with "
               "required status checks. A required check that a filter "
               "skips reports pending rather than satisfied, and the pull "
               "request can never merge. Nothing here is required today; "
               "making one required means moving to a change-detection job "
               "that gates with <code>if:</code>, so a skipped job still "
               "reports.")),
        refs=ref("actionlint")),
    commit(
        "Ship the verification script the document promises",
        p("This document told the reader that "
          "<code>hack/verify-tutorial-steps.sh</code> proves the series "
          "green. The file was not in the repository. That is the defect "
          "this milestone spends ten commits arguing against, in its least "
          "visible form — prose reads exactly as convincingly whether or "
          "not the thing behind it exists, and nothing compiles a claim.",
          "The script checks out each point in the series, builds it and "
          "runs its tests: tagged milestones by default, every commit with "
          "<code>--all-commits</code>. It begins at milestone zero rather "
          "than at the root, and the two commits it steps over are why the "
          "claim needed correcting as well as implementing — the root is "
          "empty, and the scaffold cannot pass its own tests until the "
          "manifest is generated. The sentence above now says that instead "
          "of claiming every commit without qualification."),
        fold("evidence", "a verifier that cannot fail proves nothing",
             p("Both directions were exercised before this landed. Pointed "
               "at a milestone it reports build and tests green; pointed at "
               "the scaffold commit it reports TEST FAILED, quoting the "
               "suite's own ErrorIfCRDPathMissing failure, and exits "
               "non-zero.",
               "test/ci asserts the file exists and is executable, and "
               "deliberately does not run it — checking out every commit "
               "costs far more than a unit test should. What is guarded is "
               "that the claim has something behind it, which is precisely "
               "what was missing.")),
        refs=ref("kb_envtest")),
])


MILESTONES = [M00, M01, M02, M03, M04, M05, M06, M07, M08, M09, M10, M11, M12]


# --------------------------------------------------------------------------
# Orientation
#
# Milestone 01 opens with seven API decisions, which is unreadable without
# knowing what the object is for. This is the four minutes that buys the four
# hours: what problem the controller solves, what it actually creates, and the
# one property -- idempotence -- that the whole reconcile pattern rests on.
# --------------------------------------------------------------------------

PROLOGUE = f"""
<section id="start">
  <h2>What a PodPool is</h2>
  {p(
    "Spot instances are dramatically cheaper than on-demand ones and can be "
    "reclaimed at a few minutes&#8217; notice &mdash; every cloud sets its own "
    "discount and its own warning, and neither is a number this document "
    "needs. The obvious way to exploit "
    "them is to run part of your fleet there, and a Deployment cannot: it has "
    "exactly one pod template, so one <code>nodeSelector</code>, one priority "
    "class, one set of tolerations. Every pod it manages is identical. There "
    "is no way to say <em>three of these on reliable hardware and the rest "
    "wherever is cheap</em>.",

    "So teams hand-roll it &mdash; several near-identical Deployments, an HPA "
    "on each, and a spreadsheet-in-your-head relationship between their "
    "replica counts. Scale up and you have to adjust all of them in the right "
    "proportion. Scale down and you have to remember which one holds the "
    "guaranteed floor.")}
  {ref("k8s_deployment", "k8s_assign_node", "k8s_taints", "k8s_priority")}
  {p(

    "A PodPool collapses that into one object with one replica count. The "
    "<code>/scale</code> subresource means a single HPA targets the pool, and "
    "the controller works out the internal split on every change. Groups are "
    "ordered, earlier means filled first, and every group is the same triple: "
    "a floor, a best-effort target, and a ceiling. The floor is always "
    "<code>min</code>, defaulting to zero. What changes between groups is "
    "what bounds them from above:")}
  {table(
    ["Bounded above by", "Written as", "What it means"],
    [["nothing",
      "<code>min</code> alone",
      "Absorbs whatever the earlier groups left. At most one group in a pool "
      "may be shaped this way &mdash; a second overflow sink cannot do what it "
      "says, and admission rejects it."],
     ["an absolute count",
      "<code>min</code> + <code>max</code>",
      "Never more than <code>max</code> pods, whatever the pool grows to."],
     ["a share of the pool",
      "<code>min</code> + <code>target</code>",
      "<code>target</code> is a percentage string such as "
      "<code>\"30%\"</code>. With no <code>max</code> set, it is the ceiling."],
     ["both",
      "<code>min</code> + <code>target</code> + <code>max</code>",
      "<code>target</code> softens into a best-effort share the group may "
      "grow past, and <code>max</code> becomes the hard ceiling."],
     ["whatever actually fits",
      "<code>min</code> + <code>opportunistic</code>",
      "No ceiling is declared at all: it is discovered by asking the "
      "scheduler what the cluster will accept. Milestone 07 is this row."]])}
  {fold("background", "the restaurant, if that has not landed yet",
    p("You run a restaurant. Tonight you need ten cooks, and you have three "
      "ways to get them. Full-time staff are expensive but always show up, "
      "and you need at least three on every shift. Discount contractors are "
      "cheaper and work in your kitchen, but you send them home first when "
      "things get tight &mdash; never more than 30% of the floor. Temp agency "
      "workers are cheapest by far, but the agency can recall them with two "
      "minutes&#8217; notice, so never more than half.",

      "Somebody has to do the arithmetic: given <em>I need ten tonight</em>, "
      "how many of each do I call in? And when the dinner rush pushes that to "
      "thirty, redo it &mdash; without ever dropping below three "
      "full-timers. PodPool is that manager. You hand it one number and the "
      "rules; it works out the split.",

      "Now swap the words. Cooks are pods. Full-time staff are pods on "
      "on-demand nodes, expensive and never taken away. Temp workers are pods "
      "on spot instances, a fraction of the price but reclaimable at any "
      "moment. "
      "Discount contractors are low-priority pods that share the reliable "
      "nodes and get evicted the instant something more important needs the "
      "room."))}
  {p(
    "The manager does not interview cooks. They phone three agencies and say "
    "<em>send three</em>, <em>send three</em>, <em>send four</em>, and each "
    "agency handles its own paperwork and its own sick days. PodPool works "
    "the same way: it never creates a pod. It creates one child workload per "
    "group &mdash; a Deployment, a StatefulSet, an Argo Rollout, whatever the "
    "template names &mdash; and sets a replica count on each. The workload "
    "controllers do the actual pod management, which is why this is sometimes "
    "called a controller of controllers.")}
  {fold("background", "how a controller thinks, if this is your first one",
    p("A Kubernetes controller is a thermostat, not a script. A script says "
      "<em>turn the heat on</em>. A thermostat says <em>the target is 20&#176;, "
      "look at the room, act on the difference</em>. It re-reads the room "
      "constantly and never remembers what it did last time.")
    + '<blockquote class="cite"><p>In Kubernetes, controllers are control '
      'loops that watch the state of your cluster, then make or request '
      'changes where needed.</p>'
      '<footer>the Kubernetes documentation on controllers, which is where '
      'the thermostat comes from</footer></blockquote>'
    + ref("k8s_controller")
    + p(

      "<code>Reconcile</code> is that check, and milestone 03 is where it "
      "first appears. Something happened &mdash; anything &mdash; so read the "
      "PodPool, recompute the entire desired split from scratch, and write "
      "whatever corrections are needed. Run it a hundred times with nothing "
      "changed and it makes exactly zero writes. That property is "
      "idempotence, and it is the whole reason the pattern survives crashes, "
      "restarts and duplicate events."))}
  {p(
    "One pool at ten replicas fans out into three Deployments: "
    "<code>base</code> at 3 from its floor, <code>scavenger</code> at 3 under "
    "a 30% ceiling, <code>burst</code> taking the remaining 4. Every child "
    "carries an owner reference, so deleting the pool deletes all of them "
    "without the controller doing anything. That fan-out is the whole job; "
    "the thirteen milestones below are the controller learning to do it "
    "safely.")}
</section>
"""


# --------------------------------------------------------------------------
# Style and behaviour on top of the v1 sheet
# --------------------------------------------------------------------------

V2_CSS = """
.ms-intro > p:first-child { font-size: 1.06em; }
.cmt-head { margin-top: 28px; }
.cmt { margin: 22px 0 26px; padding-left: 16px; border-left: 3px solid var(--edge); }
.cmt h4 { margin: 0 0 6px; font-size: 15.5px; }
.cmt-n { font: 600 11px/1 var(--mono); color: var(--faint); margin-right: 10px;
  letter-spacing: .06em; }
/* It is a permalink, but it should read as the number it has always been. */
a.cmt-n { text-decoration: none; }
a.cmt-n:hover, a.cmt-n:focus { color: var(--accent); }
/* An anchor jump has to land inside the band the scrollspy watches, or the
   thing you clicked sits above it and the next one reads as current. */
section[id], article.cmt[id] { scroll-margin-top: 96px; }
/* One source of truth for "you are here": the same pass that marks the rail
   marks the article and its milestone, so the three cannot disagree. The
   milestone's bar is drawn beside its heading rather than around it, so
   arriving somewhere never shifts the text you came to read. */
.cmt.current { border-left-color: var(--accent); }
section[id] > h2 { position: relative; }
section[id].current > h2::before {
  content: ""; position: absolute; left: -14px; top: .2em; bottom: .2em;
  width: 3px; border-radius: 2px; background: var(--accent);
}
/* A term expanded where it first appears, in the column the layout already
   reserves: prose caps well short of the main column, so this space was there
   and empty. Nothing is added to the sentence -- no underline, no marker --
   because the note's first line is the term itself, aligned to the paragraph
   that introduces it, and that is what connects the two. The document's best
   property is its prose; nineteen marks in it would be the first thing ever
   to interrupt that. */
main p { position: relative; }
span.gloss {
  position: absolute; top: 2px; left: calc(100% + 36px); width: 196px;
  font: 11.5px/1.55 var(--mono); color: var(--faint);
}
span.gloss b { display: block; color: var(--ink-2); font-weight: 600; }
span.gloss .g-short { display: block; margin: 2px 0 3px; }
span.gloss a { color: var(--faint); text-decoration: none;
  border-bottom: 1px dotted var(--edge); }
span.gloss a:hover, span.gloss a:focus { color: var(--accent);
  border-bottom-color: var(--accent); }
/* Below the width that reserves a column, the same markup becomes a block
   above its paragraph. One mechanism, no second code path, no JS. */
@media (max-width: 1239px) {
  span.gloss { display: block; position: static; width: auto;
    margin: 0 0 8px; padding-left: 10px;
    border-left: 2px solid var(--edge); }
  span.gloss b { display: inline; }
  span.gloss .g-short { display: inline; }
  span.gloss .g-short::before { content: " \2014  "; }
}
dl.gloss-list { margin: 0; }
dl.gloss-list dt { margin-top: 22px; font: 600 13px/1.5 var(--mono);
  color: var(--ink); }
dl.gloss-list dd { margin: 4px 0 0; }
dl.gloss-list dd p { margin: 6px 0; }
/* A borrowed sentence, marked as borrowed. The analogy in the fold above it is
   upstream's, and kubernetes.io is CC BY 4.0, so the attribution is what the
   licence asks for rather than a courtesy. */
blockquote.cite { margin: 14px 0 6px; padding-left: 14px;
  border-left: 2px solid var(--edge); }
blockquote.cite p { margin: 0; font-style: italic; color: var(--ink-2); }
blockquote.cite footer { margin-top: 5px; font: 11.5px/1.6 var(--mono);
  color: var(--faint); }
/* A citation line reads as a footnote, not as a call to action: it is the
   quietest thing in the commit, in the same place under every commit that has
   one, so a reader who does not want the upstream page never has to step over
   it to get to the next paragraph. */
p.refs { margin: 8px 0 12px; font: 11.5px/1.9 var(--mono); }
.refs-label { color: var(--faint); text-transform: uppercase;
  letter-spacing: .07em; margin-right: 10px; }
.refs-sep { color: var(--faint); margin: 0 8px; }
p.refs a { color: var(--ink-2); text-decoration: none;
  border-bottom: 1px dotted var(--edge); }
p.refs a:hover, p.refs a:focus { color: var(--accent);
  border-bottom-color: var(--accent); }
details.fold { margin: 10px 0; border: 1px solid var(--edge); border-radius: 6px;
  background: var(--wash); }
details.fold > summary { cursor: pointer; padding: 7px 12px;
  font: 600 12px/1.4 var(--mono); color: var(--faint); list-style-position: inside; }
details.fold[open] > summary { border-bottom: 1px solid var(--edge); }
/* The kind, before the summary, so a closed fold says what opening it costs:
   a listing, an argument, a measurement, or something not to run. Carried on
   the label rather than on the whole box -- 162 tinted boxes would be louder
   than the prose they sit in.

   The hues come from the syntax-token palette rather than from --go and
   --detour: those are tuned for rules and washes, and at 10.5px three of the
   seven landed under 4.5:1 in the light scheme. The token colours are the
   ones this sheet already trusts to carry small text in both schemes. */
.fold-kind { display: inline-block; margin-right: 9px; padding: 1px 6px;
  border-radius: 3px; font-size: 10.5px; letter-spacing: .06em;
  text-transform: uppercase; background: var(--sunken);
  border: 1px solid var(--edge); color: var(--ink-2); }
.fold-code .fold-kind     { color: var(--t-keyword); border-color: var(--t-keyword); }
.fold-why .fold-kind      { color: var(--t-number);  border-color: var(--t-number); }
.fold-evidence .fold-kind { color: var(--t-key);     border-color: var(--t-key); }
.fold-trouble .fold-kind  { color: var(--t-literal); border-color: var(--t-literal); }
/* The one kind that is an instruction rather than a description, and the only
   one that fills: a reader opening folds at speed must not run this one. */
.fold-caution .fold-kind  { background: var(--warn); border-color: var(--warn);
  color: var(--surface); }
.fold-caution { border-color: var(--warn); }
.fold-body { padding: 4px 12px 10px; }
pre.msg { white-space: pre-wrap; font: 12.5px/1.55 var(--mono);
  color: var(--ink); margin: 8px 0; }
p.standalone { margin: -12px 0 22px; font: 12px/1.5 var(--mono); }
p.standalone a { color: var(--faint); text-decoration: none;
  border-bottom: 1px dotted var(--edge); }
p.standalone a:hover { color: var(--accent); border-bottom-color: var(--accent); }
/* The checkout is an invitation, not an instruction: this document is read. */
.steptag-hint { text-transform: none; letter-spacing: 0; color: var(--faint); }
.closing { border-top: 1px solid var(--rule); margin-top: 46px; padding-top: 30px; }
.closing h2 { font-size: 17px; margin-top: 30px; }
.closing h2:first-child { margin-top: 0; }
.closing ul { margin: 0 0 17px; padding-left: 20px; }
.closing li { margin-bottom: 12px; }
/* The rail's second level: present for every milestone, shown for one. */
nav.rail ol.sub { margin: 2px 0 6px; }
nav.rail li:not(.open) > ol.sub { display: none; }
nav.rail ol.sub a {
  padding-left: 22px; font-size: 12px; color: var(--ink-3); opacity: .85;
}
nav.rail ol.sub a:hover { opacity: 1; }
nav.rail ol.sub a .num { font-size: 10.5px; }
nav.rail ol.sub a.active { opacity: 1; }

@media print {
  details.fold > summary { list-style: none; }
  details.fold:not([open]) > .fold-body { display: block; }
  /* No margin on paper either, so the notes reflow inline and the glossary
     prints as what it is: a section. */
  span.gloss { display: block; position: static; width: auto;
    margin: 0 0 8px; padding-left: 10px;
    border-left: 2px solid var(--edge); }
}
""" + sim1.CSS

V2_JS = r"""
document.addEventListener('click', function (e) {
  var b = e.target.closest('button.copy');
  if (!b) return;
  var c = document.getElementById(b.dataset.for);
  if (!c) return;
  navigator.clipboard.writeText(c.textContent).then(function () {
    var t = b.textContent; b.textContent = 'copied';
    setTimeout(function () { b.textContent = t; }, 1200);
  });
});
// Folds print open: skippable, never unreachable. The print stylesheet does
// that on its own, for a reader with JS off; flipping the attribute as well is
// what makes a fold that was closed print like one that was already open.
window.addEventListener('beforeprint', function () {
  document.querySelectorAll('details.fold').forEach(function (d) {
    d.dataset.wasOpen = d.open ? '1' : '';
    d.open = true;
  });
});
window.addEventListener('afterprint', function () {
  document.querySelectorAll('details.fold').forEach(function (d) {
    d.open = d.dataset.wasOpen === '1';
  });
});

// Where am I. The rail marks the milestone being read and opens its commits,
// so the second level costs nothing until it is the level you are on.
(function () {
  var rail = document.querySelector('nav.rail');
  if (!rail || !window.IntersectionObserver) return;

  var top = {}, sub = {};
  rail.querySelectorAll('a[href]').forEach(function (a) {
    var id = a.getAttribute('href').slice(1);
    (a.closest('ol.sub') ? sub : top)[id] = a;
  });

  var at = null;
  function mark(id) {
    if (id === at) return;
    at = id;
    rail.querySelectorAll('.active').forEach(function (n) {
      n.classList.remove('active');
    });
    rail.querySelectorAll('li.open').forEach(function (n) {
      n.classList.remove('open');
    });
    document.querySelectorAll('.current').forEach(function (n) {
      n.classList.remove('current');
    });
    var here = document.getElementById(id);
    if (here) {
      here.classList.add('current');
      var sec = here.closest('section[id]');
      if (sec && sec !== here) sec.classList.add('current');
    }

    var msId = id, commit = null;
    if (id.lastIndexOf('cmt-', 0) === 0) {
      msId = 'milestone-' + id.split('-')[1];
      commit = sub[id];
    }
    var a = top[msId];
    if (a) { a.classList.add('active'); a.parentNode.classList.add('open'); }
    if (commit) commit.classList.add('active');

    // Keep the marker in view without moving the page: the rail scrolls on
    // its own, so nudge only that.
    var seen = commit || a;
    if (!seen) return;
    var r = rail.getBoundingClientRect(), b = seen.getBoundingClientRect();
    if (b.top < r.top + 8) rail.scrollTop -= r.top + 8 - b.top;
    else if (b.bottom > r.bottom - 8) rail.scrollTop += b.bottom - r.bottom + 8;
  }

  // Watch the headings, not the boxes they head: a section encloses its own
  // commits, so its top is always the higher one and it would win every
  // comparison against the article the reader is actually inside.
  var io = new IntersectionObserver(function (entries) {
    var best = null;
    entries.forEach(function (e) {
      if (!e.isIntersecting) return;
      if (!best || e.boundingClientRect.top < best.boundingClientRect.top) best = e;
    });
    if (!best) return;
    var owner = best.target.closest('article.cmt[id], section[id]');
    if (owner) mark(owner.id);
  }, { rootMargin: '-76px 0px -70% 0px' });

  document.querySelectorAll('section[id] > h2, article.cmt[id] > h4').forEach(
    function (t) { io.observe(t); });

  // Clicking the rail settles it immediately rather than waiting for the
  // scroll to land, and the observer agrees once it does because the target
  // stops inside the band.
  rail.addEventListener('click', function (e) {
    var a = e.target.closest('a[href^="#"]');
    if (a) mark(a.getAttribute('href').slice(1));
  });
})();
"""


def render():
    n_cut = len(MILESTONES)

    rail = ['<nav class="rail" aria-label="Contents">',
            '<div class="rail-head">The PodPool controller, v2</div>',
            '<div class="rail-part">Start here</div><ol>'
            '<li><a href="#start"><span class="num">&middot;</span>'
            '<span>What a PodPool is</span></a></li></ol>',
            '<div class="rail-part">Milestones</div><ol>']
    for m in MILESTONES:
        # The commits ride along collapsed. Only the milestone being read
        # opens, so the rail stays a thirteen-milestone overview until the
        # reader is inside something, and then becomes a map of where they are.
        sub = "".join(
            f'<li><a href="#{cid}" data-ms="{m["id"]}">'
            f'<span class="num">{num.split(".")[1]}</span>'
            f'<span>{html.escape(subject)}</span></a></li>'
            for cid, num, subject in m["nav"])
        rail.append(f'<li><a href="#{m["id"]}"><span class="num">{m["num"]}</span>'
                    f'<span>{html.escape(m["label"])}</span></a>'
                    f'<ol class="sub">{sub}</ol></li>')
    rail.append("</ol>")
    rail.append('<div class="rail-part">End</div><ol>'
                '<li><a href="#end"><span class="num">&rarr;</span>'
                '<span>Where to go next</span></a></li>'
                '<li><a href="#glossary"><span class="num">&middot;</span>'
                '<span>Terms</span></a></li></ol>')
    rail.append("</nav>")

    body = []
    for m in MILESTONES:
        body.append(f'<section id="{m["id"]}">')
        body.append(
            f'<div class="steptag">Milestone <b>{m["num"]}</b>'
            f' &nbsp;&middot;&nbsp; <code>git checkout {m["tag"]}</code>'
            f' <span class="steptag-hint">to read along in your editor</span></div>')
        body.append(f'<h2>{html.escape(m["title"])}</h2>')
        body.append(m["body"])
        body.append("</section>")

    # Almost everything installs itself: the Makefile fetches kustomize,
    # controller-gen, golangci-lint, govulncheck and the envtest binaries into
    # bin/ on demand. Only two of the five below are tools it refuses to fetch,
    # and each of those is needed for one milestone -- which is worth saying,
    # because the list looks much longer than it is.
    prereq = fold("background", "what you need, and how to start", table(
        ["Requirement", "Version", "Check", "Needed for"],
        [["Go", "1.26.0", "<code>go version</code>", "everything"],
         ["Docker", "any recent", "<code>docker version</code>",
          "Kind, milestone 12"],
         ["kubectl", "within a minor of the cluster",
          "<code>kubectl version --client</code>", "milestones 07 and 12"],
         ["kwokctl", "not fetched for you", "<code>kwokctl --version</code>",
          "milestone 07 only"],
         ["kind", "not fetched for you", "<code>kind version</code>",
          "milestone 12 only"]])
        + p("Everything else the build needs, it downloads into "
            "<code>bin/</code> itself &mdash; kustomize, controller-gen, "
            "golangci-lint, govulncheck, and the envtest control-plane "
            "binaries. There is no separate install step, and no setup "
            "sequence to follow: with the repository checked out, two "
            "commands put you at the first milestone with a green suite.")
        + shell("git checkout milestone-00\n"
                "make test   # fetches envtest, regenerates manifests, runs the suite")
        + p("envtest's Kubernetes version is derived rather than pinned: the "
            "Makefile reads the <code>k8s.io/api</code> minor out of "
            "<code>go.mod</code>, which is <code>v0.36.0</code> here, so it "
            "sets up 1.36. Hardcoding a version means fighting it. And for "
            "the two tools it cannot fetch, the Makefile fails with the "
            "install URL in the message &mdash; trust that over guessing.")
        + p("All commands assume a POSIX shell with GNU <code>make</code>. On "
            "Windows use WSL2; the Makefile targets, the envtest binaries and "
            "the KWOK tier are not tested against native PowerShell."))

    masthead = f"""
<header class="masthead">
  <div class="eyebrow">A tested tutorial &middot; series two</div>
  <h1>The PodPool controller, milestone by milestone</h1>
  <p class="standfirst">A finished Kubernetes controller, read in the order it
  was built. Thirteen milestones, 110 commits that each build and pass their
  tests alone. Nothing here asks you to type along &mdash; every milestone is a
  tag, and the fastest way through is to check it out and keep your editor open
  beside this page.</p>
  {prereq}
  <p>The skim path is the milestone intros. The commit story is the middle
  layer, quoting the real messages from the branch. Anything deeper folds.
  Every figure captioned with a <code>git show</code> hint is read out of the
  <code>tutorial-v2</code> branch at that tag, and every commit title in this
  document is checked against the branch's history at build time.</p>
  <p><code>hack/verify-tutorial-steps.sh --all-commits</code> checks out every
  commit from milestone zero onward, builds it, and runs its tests &mdash; not
  just the tagged ones. It starts there rather than at the root because the
  root commit is empty and the scaffold after it cannot pass its own tests
  until the commit that generates the CRD manifest; milestone zero exists to
  settle exactly those two debts. All {n_cut} milestones are cut.</p>
  <p>Three rules shape the series. One decision per commit; every commit
  builds and passes its tests alone, which is the rule that script proves; and
  a commit that overturns an earlier decision pins the old behaviour with a
  test before changing it, so the diff has to argue with something rather than
  simply assert. The third is the one that matters &mdash; it is what keeps a
  walkthrough from teaching a design nobody tried.</p>
</header>
"""

    closing = """
<section id="end" class="closing">
  <h2>What you have read</h2>
  <p>A controller that distributes a replica budget across named groups,
  renders each group's workload from a template, and keeps those children
  reconciled against a spec that changes underneath it. It sizes its
  opportunistic tier from capacity it measured rather than capacity it
  assumed, refuses at admission the specs it could not satisfy at reconcile,
  and reports all of it through conditions, metrics and events &mdash; under a
  role trimmed to the verbs the code actually calls, behind CI that fails on
  drift.</p>

  <h2>Where to go next</h2>
  <ul>
    <li><b>Break it on purpose.</b> At <code>milestone-12</code>, delete the
    <code>&amp;&amp; !reconciledChildren[name]</code> half of the sweep guard in
    <code>sweepAllOrphans</code>, then run <code>make test</code>. Two specs in
    <code>sweep_envtest_test.go</code> fail and no others: the one that swaps a
    template from Deployment to StatefulSet and expects the old child gone, and
    the one that expects it kept while its replacement is still failing. That
    guard is the whole difference between those two situations, and the suite
    makes the point faster than a paragraph can.</li>
    <li><b>Add a scaling shape.</b> The distributor understands floors,
    ceilings, percentages and opportunistic. Add a fifth &mdash; a group that
    mirrors another group's target &mdash; and keep the discipline the series
    uses: pin the behaviour with a test that fails first. Milestone 02 is the
    entire surface you need, and it runs without a cluster.</li>
    <li><b>Point it at a CRD you own.</b> <code>workloadTemplate</code> takes
    any kind the scheme knows. Milestone 09's plural-resolution table is where
    a third-party kind bites first, and the reason it bites is a security
    one.</li>
  </ul>

  <h2>If you are coming back</h2>
  <p>Three milestones stand alone. <a href="#milestone-02">02</a> is the distribution
  algorithm: a pure function, exhaustively testable, no cluster anywhere near
  it. <a href="#milestone-07">07</a> is the probe state machine, the only place this
  controller asks the scheduler a question it cannot answer itself.
  <a href="#milestone-09">09</a> is admission-time authorization, and the one to read
  closely before trusting any of this near a real cluster.</p>
</section>
"""

    content = f"""<div class="shell">
{''.join(rail)}
<main>
{masthead}
{PROLOGUE}
{''.join(body)}
{closing}
{glossary_section()}
</main>
</div>
<script>{V2_JS}</script>
<script>{sim1.sim_js()}</script>
<script>{sim2.sim_js()}</script>
"""

    return page(
        "A Kubernetes controller, milestone by milestone — the PodPool walkthrough",
        "A finished Kubernetes controller read in the order it was built: "
        "13 milestones, 110 commits that each build and pass their tests alone.",
        content,
        slug="tutorial-v2.html",
        extra_css=V2_CSS + sim2.embed_css(),
    )


if __name__ == "__main__":
    sim1.check_ports()
    if failures := sim2.check_ports():
        raise SystemExit("sim2 port drift: " + "; ".join(failures))
    if failures := check_refs():
        raise SystemExit("reference drift: " + "; ".join(failures))
    doc, placed = place_glosses(render())
    if failures := check_glossary(placed):
        raise SystemExit("glossary: " + "; ".join(failures))
    if failures := check_api_fields(doc):
        raise SystemExit("API drift: " + "; ".join(failures))
    if failures := check_markup(doc):
        raise SystemExit("markup: " + "; ".join(failures))
    OUT.write_text(doc)
    print(f"wrote {OUT} ({OUT.stat().st_size:,} bytes)")
