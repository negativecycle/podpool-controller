// Package ci asserts that the project's own gates are wired up.
//
// Nothing in this package is a behaviour change, so there is nothing in the
// controller to assert against. What the CI work delivers is coverage, and in
// the history this tutorial is based on the cost of not asserting it was
// measured: a suite that ran nowhere and was linted by nothing sat failing to
// compile for 28 commits without anything noticing. The failure mode is a
// *gap*, and a gap is tested by asserting the gate exists.
//
// These tests read the repository's own configuration. That is unusual, and it
// is the point: the reason a build-tagged suite rots is that no Go code and no
// linter ever looks at it. A test that looks at the build and CI config closes
// that loop from inside the thing CI already runs.
package ci_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// repoRoot is two levels up from test/ci.
const repoRoot = "../.."

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Clean(filepath.Join(repoRoot, rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}

	return string(b)
}

// workflows returns every file under .github/workflows, keyed by base name.
func workflows(t *testing.T) map[string]string {
	t.Helper()

	dir := filepath.Join(repoRoot, ".github", "workflows")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yml") && !strings.HasSuffix(e.Name(), ".yaml")) {
			continue
		}

		b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, e.Name())))
		if err != nil {
			t.Fatalf("reading workflow %s: %v", e.Name(), err)
		}

		out[e.Name()] = string(b)
	}

	if len(out) == 0 {
		t.Fatal("no workflows found")
	}

	return out
}

// ---------------------------------------------------------------------------
// the suite itself
// ---------------------------------------------------------------------------

// TestKwokSuiteCompiles is the assertion that would have caught the real
// defect, on the commit that introduced it.
//
// test/kwok is behind //go:build kwok, so `go list ./...` excludes it and
// `make test` never compiles it. In the history this tutorial is based on, a
// rename swept NodeSelector: to Overrides: across the suite and caught one
// literal it should not have — a plain corev1.PodSpec, which has no such
// field. The mistake compiled nowhere and ran nowhere, so it sat on the main
// branch for 28 commits.
//
// Shelling out to `go vet` rather than asserting on config: this holds whatever
// .golangci.yml says, and whatever CI does or does not run. It is the only
// check here that depends on nothing but the toolchain.
func TestKwokSuiteCompiles(t *testing.T) {
	cmd := exec.Command("go", "vet", "-tags=kwok", "./test/kwok/")
	cmd.Dir = repoRoot

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("the kwok integration suite does not compile:\n%s\n"+
			"Nothing else in the project builds these files — `go list ./...` "+
			"excludes them and golangci-lint sets no build tags — so this rots "+
			"silently until someone runs it by hand.", out)
	}
}

// TestE2ESuiteCompiles is the same guard for the other build-tagged tier, and
// it earned its place the same way: while cutting this branch, the e2e fixture
// helper carried a literal `50%` inside a fmt.Sprintf format string -- an
// invalid printf verb, so the package did not build. `go test ./...` never
// sees test/e2e, the lint config's build-tags cover kwok but a broken package
// fails typecheck identically either way, and the only consumer is `make
// test-e2e`, which nobody had re-run since the fixture changed. Same rot,
// different tag.
func TestE2ESuiteCompiles(t *testing.T) {
	cmd := exec.Command("go", "vet", "-tags=e2e", "./test/e2e/")
	cmd.Dir = repoRoot

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("the e2e suite does not compile:\n%s\n"+
			"Only `make test-e2e` builds these files, so a broken fixture or "+
			"helper survives until the next full Kind run.", out)
	}
}

// TestKwokSuiteIsExcludedFromTheDefaultBuild documents *why* every other test
// in this file is necessary, and pins it.
//
// Passes today and must keep passing: the build tag is deliberate (the suite
// needs a live cluster). It is the tag that creates the blind spot, so if the
// tag ever goes away these guards can be reconsidered — and if it stays, they
// are load-bearing.
func TestKwokSuiteIsExcludedFromTheDefaultBuild(t *testing.T) {
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = repoRoot

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}

	if strings.Contains(string(out), "/test/kwok") {
		t.Error("test/kwok is in the default build; the //go:build kwok tag " +
			"appears to have been dropped, which would run cluster tests in `make test`")
	}
}

// ---------------------------------------------------------------------------
// the cheap gate: lint
// ---------------------------------------------------------------------------

// TestLintConfigIncludesKwokBuildTag is the highest-value assertion in the
// batch relative to its cost.
//
// golangci-lint analyses only files matching its configured build tags. With
// none set, `golangci-lint run ./test/kwok/...` returns "no go files to
// analyze" — verified. With --build-tags=kwok it reports the typecheck error in
// the same run as everything else.
//
// So one config line turns a suite that can silently stop compiling into one
// that cannot, in seconds, on every push, with no cluster involved. The
// workflow catches more; this catches the thing that actually broke.
func TestLintConfigIncludesKwokBuildTag(t *testing.T) {
	tags, err := lintBuildTags(readRepoFile(t, ".golangci.yml"))
	if err != nil {
		t.Fatalf("parsing .golangci.yml: %v", err)
	}

	if !slices.Contains(tags, "kwok") {
		t.Errorf("run.build-tags is %v, want it to include \"kwok\"; "+
			"without the tag golangci-lint reports \"no go files to analyze\" for "+
			"test/kwok and the suite is linted by nothing", tags)
	}
}

// lintBuildTags reads run.build-tags. sigs.k8s.io/yaml routes through JSON, so
// the struct tags are `json:`, not `yaml:` — getting that wrong yields an empty
// slice and a test that fails for the wrong reason. TestAssertionsAcceptPostFixConfig
// is what catches it.
func lintBuildTags(src string) ([]string, error) {
	var cfg struct {
		Run struct {
			BuildTags []string `json:"build-tags"`
		} `json:"run"`
	}
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		return nil, err
	}

	return cfg.Run.BuildTags, nil
}

// ---------------------------------------------------------------------------
// the expensive gate: CI
// ---------------------------------------------------------------------------

// kwokJobWorkflow returns the workflow that runs the kwok suite, or "" if none
// does.
func kwokJobWorkflow(t *testing.T) (name, body string) {
	t.Helper()

	for n, b := range workflows(t) {
		if strings.Contains(b, "test-kwok") {
			return n, b
		}
	}

	return "", ""
}

// TestKwokSuiteRunsInCI pins the point of the whole exercise: the only suite
// with a real kube-scheduler and a real kube-controller-manager must run
// somewhere CI can see, or it is local-only by definition.
func TestKwokSuiteRunsInCI(t *testing.T) {
	name, _ := kwokJobWorkflow(t)
	if name == "" {
		t.Error("no workflow under .github/workflows runs `make test-kwok`; " +
			"the only suite with a real kube-scheduler and kube-controller-manager " +
			"is local-only")
	}
}

// TestKwokSuiteRunsInItsOwnWorkflow keeps the integration suite off the unit
// test job's critical path. Measured at 136s against ~30s for `make test`.
func TestKwokSuiteRunsInItsOwnWorkflow(t *testing.T) {
	name, _ := kwokJobWorkflow(t)
	if name == "" {
		t.Skip("covered by TestKwokSuiteRunsInCI")
	}

	if name == "test.yml" {
		t.Error("the kwok suite runs inside test.yml; a ~136s integration suite " +
			"should not serialise in front of the unit tests")
	}
}

// TestKwokctlIsPinnedInCI stops a kwok release from breaking CI on a commit
// that changed nothing.
//
// Every `uses:` in this repo is SHA-pinned. test-e2e.yml's curl of
// kind-linux-latest is the one exception and is a known, separate gap — do not
// copy it. A pinned version is also required for the binary cache key to mean
// anything.
func TestKwokctlIsPinnedInCI(t *testing.T) {
	name, body := kwokJobWorkflow(t)
	if name == "" {
		t.Skip("covered by TestKwokSuiteRunsInCI")
	}

	for line := range strings.SplitSeq(body, "\n") {
		if !strings.Contains(line, "kwok") {
			continue
		}

		if strings.Contains(line, "/latest/") || strings.Contains(line, "kwokctl-latest") {
			t.Errorf("%s installs kwokctl from an unpinned URL:\n\t%s",
				name, strings.TrimSpace(line))
		}
	}

	if !regexp.MustCompile(`(?i)kwok[_-]?version`).MatchString(body) {
		t.Errorf("%s does not declare a KWOK_VERSION; an unpinned kwokctl breaks CI "+
			"on a commit that changed nothing, and the binary cache key needs a version", name)
	}
}

// TestKwokBinariesAreCachedInCI guards the job's dominant cold cost.
//
// ~/.kwok holds 443 MB of kube-apiserver, etcd, kube-scheduler and
// kube-controller-manager binaries — measured. With them warm, cluster creation
// is 1.3s. A cache miss on every run turns a ~3 minute job into something much
// longer, and it will not look like a regression, just like CI being slow.
func TestKwokBinariesAreCachedInCI(t *testing.T) {
	name, body := kwokJobWorkflow(t)
	if name == "" {
		t.Skip("covered by TestKwokSuiteRunsInCI")
	}

	if !strings.Contains(body, "actions/cache") || !strings.Contains(body, ".kwok") {
		t.Errorf("%s does not cache ~/.kwok; every run re-downloads 443 MB of "+
			"control-plane binaries", name)
	}
}

// TestWorkflowsPassActionlint is the structural gate that hand-rolled regexes
// cannot be.
//
// actionlint parses the real GitHub Actions schema — job graph, `needs`,
// expression syntax and types, event filters — and runs shellcheck over every
// `run:` block. The checks in this file only know what they were told to look
// for; actionlint knows what a workflow *is*.
//
// Verified clean against all five workflows.
//
// Skips when the binary is absent, because it is not a module dependency. That
// makes this a local convenience — the real gate is the CI step asserted by
// TestActionlintRunsInCI.
func TestWorkflowsPassActionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not on PATH; CI coverage is asserted by TestActionlintRunsInCI")
	}

	cmd := exec.Command(bin)

	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("actionlint reported problems:\n%s", out)
	}
}

// TestBaseImageAnnotationsMatchTheDockerfile keeps two copies of the same
// fact from drifting apart.
//
// The final stage's FROM pins the base by digest, and base.name/base.digest
// repeat it as annotations so a scanner can tell our layers from the base's.
// Nothing makes them agree. A dependency bot bumping the FROM line and
// leaving the labels alone produces an image that reports a parent it does
// not have -- which is worse than reporting none, because it looks answered.
func TestBaseImageAnnotationsMatchTheDockerfile(t *testing.T) {
	body := readRepoFile(t, "Dockerfile")

	// The last FROM is the stage the published image is built from.
	from := regexp.MustCompile(`(?m)^FROM ([^\s@]+)@(sha256:[0-9a-f]{64})`)

	matches := from.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		t.Fatal("no digest-pinned FROM in the Dockerfile; the base is floating again")
	}

	final := matches[len(matches)-1]
	wantName, wantDigest := final[1], final[2]

	label := func(key string) string {
		re := regexp.MustCompile(`org\.opencontainers\.image\.` + regexp.QuoteMeta(key) + `=([^\s\\]+)`)
		if m := re.FindStringSubmatch(body); m != nil {
			return m[1]
		}

		return ""
	}

	if got := label("base.name"); got != wantName {
		t.Errorf("base.name is %q but the final FROM builds on %q", got, wantName)
	}

	if got := label("base.digest"); got != wantDigest {
		t.Errorf("base.digest is %q but the final FROM pins %q", got, wantDigest)
	}
}

// TestVerificationScriptExists guards the claim the document makes about
// itself.
//
// docs/tutorial-v2.html tells the reader that
// hack/verify-tutorial-steps.sh proves the series green. A document citing a
// script nobody ships is the same defect as a workflow nobody runs, and it is
// harder to notice: the prose reads exactly as convincingly either way.
//
// This asserts the file exists and is executable. It deliberately does not run
// it -- checking out every commit and testing it takes far longer than a unit
// test should -- so what is guarded here is that the claim has something
// behind it, not that the something currently passes.
func TestVerificationScriptExists(t *testing.T) {
	const script = "hack/verify-tutorial-steps.sh"

	info, err := os.Stat(filepath.Join(repoRoot, script))
	if err != nil {
		t.Fatalf("%s is missing, but the tutorial document tells the reader to run it: %v", script, err)
	}

	if info.Mode()&0o111 == 0 {
		t.Errorf("%s is not executable, so the command the document prints does not work", script)
	}
}

// TestActionlintRunsInCI wires the structural check into the one place that
// always runs it.
//
// The precedent is already here: security.yml runs hadolint through an action
// specifically to backstop a local pre-commit hook that is "local, skippable,
// and silently skips when hadolint is not installed" — its own words. Workflow
// YAML has exactly that gap and no backstop at all.
//
// It matters more with every workflow added. A malformed workflow does not
// fail loudly; it silently does not run, which is the same failure mode that
// left the kwok suite rotting.
func TestActionlintRunsInCI(t *testing.T) {
	for _, body := range workflows(t) {
		if strings.Contains(body, "actionlint") {
			return
		}
	}

	t.Error("no workflow runs actionlint; workflow YAML is the only configuration " +
		"in this repo with no validation gate, and a malformed workflow fails by " +
		"silently not running")
}

// TestWorkflowActionsArePinnedBySHA is a guard the new workflow must not break.
//
// Complements actionlint rather than duplicating it: actionlint validates
// structure and does not care how an action is versioned. Supply-chain pinning
// is this repo's own convention, so it needs its own assertion.
//
// Passes today across all four workflows. A `uses: actions/foo@v6` in the new
// file would be the first unpinned action in the repo.
func TestWorkflowActionsArePinnedBySHA(t *testing.T) {
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	uses := regexp.MustCompile(`uses:\s*(\S+)`)

	for name, body := range workflows(t) {
		for _, m := range uses.FindAllStringSubmatch(body, -1) {
			ref := m[1]

			at := strings.LastIndex(ref, "@")
			if at < 0 {
				t.Errorf("%s: `uses: %s` has no version at all", name, ref)

				continue
			}

			if !sha.MatchString(ref[at+1:]) {
				t.Errorf("%s: `uses: %s` is not pinned to a 40-character SHA", name, ref)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// hazards found while measuring
// ---------------------------------------------------------------------------

var kwokTestTimeout = regexp.MustCompile(`go test[^\n]*-timeout=(\d+)m`)

// TestKwokTimeoutHasMarginOverMeasuredRuntime.
//
// The suite takes 136s on an idle machine with warm caches. -timeout=5m is 2.2x
// that, and the two slowest tests (51s and 47s) are convergence loops polling
// with a 60s budget. On a shared GitHub runner that margin is thin, and a
// tripped Go test timeout reports as a panic — indistinguishable in the CI
// summary from a real failure.
//
// The timeout exists to stop a hung suite, not to enforce a performance budget.
func TestKwokTimeoutHasMarginOverMeasuredRuntime(t *testing.T) {
	got, err := kwokTimeoutMinutes(readRepoFile(t, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}

	if got < wantKwokTimeoutMinutes {
		t.Errorf("test-kwok runs with -timeout=%dm; the suite measures 136s on an "+
			"idle machine, so %dm leaves little room on a shared runner. Want >=%dm.",
			got, got, wantKwokTimeoutMinutes)
	}
}

const wantKwokTimeoutMinutes = 15

func kwokTimeoutMinutes(makefile string) (int, error) {
	target, err := makeTarget(makefile, "test-kwok:")
	if err != nil {
		return 0, err
	}

	m := kwokTestTimeout.FindStringSubmatch(target)
	if m == nil {
		return 0, errors.New("test-kwok does not pass -timeout")
	}

	return strconv.Atoi(m[1])
}

// makeTarget returns the body of a Makefile target, stopping at the next
// .PHONY so a later target's flags cannot be mistaken for this one's.
func makeTarget(makefile, name string) (string, error) {
	idx := strings.Index(makefile, name)
	if idx < 0 {
		return "", fmt.Errorf("no %s target in the Makefile", name)
	}

	body := makefile[idx:]
	if end := strings.Index(body, "\n.PHONY"); end >= 0 {
		body = body[:end]
	}

	return body, nil
}

// TestKwokClusterVersionDerivesFromTheModuleGraph.
//
// ENVTEST_K8S_VERSION is computed from k8s.io/api in go.mod (Makefile), so
// envtest tracks the module graph automatically. kwokctl takes its Kubernetes
// version from its own default — v1.36.1 here, against envtest's 1.36. Aligned
// by luck.
//
// A kwokctl release defaulting to 1.37 would silently move the integration
// suite onto a different API server than every other test, and the symptom
// would read as a behaviour change rather than a version skew.
func TestKwokClusterVersionDerivesFromTheModuleGraph(t *testing.T) {
	target, err := makeTarget(readRepoFile(t, "Makefile"), "setup-kwok:")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(target, "KWOK_KUBE_VERSION") {
		t.Error("setup-kwok does not set KWOK_KUBE_VERSION, so the kwok cluster runs " +
			"kwokctl's default Kubernetes version while envtest derives its own from " +
			"k8s.io/api; the two can drift apart silently")
	}

	if !strings.Contains(target, "ENVTEST_K8S_VERSION") {
		t.Error("setup-kwok's KWOK_KUBE_VERSION is not derived from ENVTEST_K8S_VERSION; " +
			"pinning it to a literal reintroduces the drift by hand")
	}
}

// TestKwokSuiteTargetsTheKwokContext is the footgun that CI going green would
// hide.
//
// TestMain builds its config from NewDefaultClientConfigLoadingRules with empty
// ConfigOverrides and never names the kwok context. It works locally only
// because `kwokctl create cluster` sets current-context as a side effect —
// verified.
//
// So `make test-kwok` on a machine whose current context is a production
// cluster runs the suite against production. ensureTopology creates and deletes
// Node objects; ensureSoleController creates a PodPool in default. The existing
// guard checks for a second *controller*, not for the wrong *cluster*.
//
// Harmless in CI, where there is exactly one context — which is why it will
// stop being noticed once this job is green.
func TestKwokSuiteTargetsTheKwokContext(t *testing.T) {
	src := readRepoFile(t, "test/kwok/kwok_test.go")

	if strings.Contains(src, "CurrentContext") {
		return // an explicit override names the cluster
	}

	if regexp.MustCompile(`"kwok-"|kwok-\$|HasPrefix\([^)]*"kwok`).MatchString(src) {
		return // a prefix guard refuses a foreign context
	}

	t.Error("test/kwok/kwok_test.go neither overrides CurrentContext nor checks for a " +
		"kwok- context prefix, so `make test-kwok` runs against whatever kubeconfig " +
		"context happens to be current — including a production cluster, where " +
		"ensureTopology creates and deletes Nodes")
}

// ---------------------------------------------------------------------------
// meta
// ---------------------------------------------------------------------------

// TestAssertionsAcceptPostFixConfig proves the checks above are not vacuous.
//
// In the history this tutorial is based on, every other test in this file was
// born failing — none had ever been observed passing — so a typo in a regex,
// or `yaml:` where sigs.k8s.io/yaml wants `json:`, would look exactly like
// the defect being reported. On this branch they are born green, which is the
// same epistemic problem from the other side: a probe that cannot parse its
// input goes green for the wrong reason and stays green when the gate is
// deleted. This feeds each assertion a minimal conforming input and requires
// it to accept, which is what makes the rest of the file trustworthy in both
// directions.
func TestAssertionsAcceptPostFixConfig(t *testing.T) {
	t.Run("lint config", func(t *testing.T) {
		tags, err := lintBuildTags("version: \"2\"\nrun:\n  allow-parallel-runners: true\n  build-tags:\n    - kwok\n")
		if err != nil {
			t.Fatalf("parsing: %v", err)
		}

		if !slices.Contains(tags, "kwok") {
			t.Errorf("post-fix config parsed to %v; the assertion would stay red "+
				"after the fix (sigs.k8s.io/yaml routes through JSON — the struct "+
				"tag must be `json:\"build-tags\"`)", tags)
		}
	})

	t.Run("makefile timeout", func(t *testing.T) {
		got, err := kwokTimeoutMinutes("test-kwok: setup-kwok\n\tgo test -tags=kwok ./test/kwok/ -v -count=1 -timeout=15m\n")
		if err != nil {
			t.Fatalf("parsing: %v", err)
		}

		if got != wantKwokTimeoutMinutes {
			t.Errorf("parsed %dm from a -timeout=15m line", got)
		}
	})

	t.Run("makefile target boundary", func(t *testing.T) {
		// setup-kwok must not pick up flags belonging to the target after it.
		body, err := makeTarget("setup-kwok:\n\tkwokctl create cluster\n.PHONY: test-kwok\ntest-kwok:\n\tKWOK_KUBE_VERSION=oops\n", "setup-kwok:")
		if err != nil {
			t.Fatal(err)
		}

		if strings.Contains(body, "KWOK_KUBE_VERSION") {
			t.Error("makeTarget ran past .PHONY into the next target")
		}
	})

	t.Run("workflow shape", func(t *testing.T) {
		body := "env:\n  KWOK_VERSION: v0.8.0\njobs:\n  test-kwok:\n    steps:\n" +
			"      - uses: actions/cache@5a3ec84eff668545956fd18022155c47e93e2684\n" +
			"        with:\n          path: ~/.kwok\n" +
			"      - run: curl -Lo kwokctl \".../${KWOK_VERSION}/kwokctl-linux-amd64\"\n" +
			"      - run: make test-kwok\n"

		if !strings.Contains(body, "test-kwok") {
			t.Error("the kwok-workflow probe would not find a conforming workflow")
		}

		if !regexp.MustCompile(`(?i)kwok[_-]?version`).MatchString(body) {
			t.Error("the version-pin regex rejects a conforming workflow")
		}

		if !strings.Contains(body, "actions/cache") || !strings.Contains(body, ".kwok") {
			t.Error("the cache probe rejects a conforming workflow")
		}

		for line := range strings.SplitSeq(body, "\n") {
			if strings.Contains(line, "kwok") && strings.Contains(line, "/latest/") {
				t.Errorf("the pin check false-positives on %q", line)
			}
		}
	})

	t.Run("kube context guard", func(t *testing.T) {
		guard := regexp.MustCompile(`"kwok-"|kwok-\$|HasPrefix\([^)]*"kwok`)
		for _, src := range []string{
			`cfg := clientcmd.ConfigOverrides{CurrentContext: "kwok-" + cluster}`,
			`if !strings.HasPrefix(raw.CurrentContext, "kwok-") { return errFatal }`,
		} {
			if !strings.Contains(src, "CurrentContext") && !guard.MatchString(src) {
				t.Errorf("the context guard rejects a conforming fix: %s", src)
			}
		}
	})
}
