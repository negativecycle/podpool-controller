//go:build kwok

package kwok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/controller"
)

const (
	testNamespace = "default"
	pollInterval  = 500 * time.Millisecond
	pollTimeout   = 60 * time.Second
)

var (
	k8sClient      client.Client
	topologyClient client.Client // uncached — for node create/delete where cache lag breaks Get-after-Create
	mgrCancel      context.CancelFunc
)

// pollFor polls cond every pollInterval until it reports done, and returns
// the poll error on timeout so the caller owns the failure message. It is
// deliberately t-free and background-rooted: these helpers also serve
// t.Cleanup-time callers, where t.Context() is already canceled (see the
// usetesting exclusion in .golangci.yml). Transient API errors belong inside
// cond as (false, nil) — a returned error aborts the poll immediately.
func pollFor(timeout time.Duration, cond wait.ConditionWithContextFunc) error {
	return wait.PollUntilContextTimeout(context.Background(), pollInterval, timeout, true, cond)
}

func pctTarget(pct int) *intstr.IntOrString {
	v := intstr.FromString(fmt.Sprintf("%d%%", pct))

	return &v
}

func schedulingOverride(nodeSelector map[string]string, priorityClassName string) *runtime.RawExtension {
	podSpec := map[string]any{}

	if len(nodeSelector) > 0 {
		nsAny := make(map[string]any, len(nodeSelector))
		for k, v := range nodeSelector {
			nsAny[k] = v
		}

		podSpec["nodeSelector"] = nsAny
	}

	if priorityClassName != "" {
		podSpec["priorityClassName"] = priorityClassName
	}

	if len(podSpec) == 0 {
		return nil
	}

	override := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": podSpec,
			},
		},
	}
	raw, _ := json.Marshal(override)

	return &runtime.RawExtension{Raw: raw}
}

func makeWorkloadTemplate(image string) runtime.RawExtension {
	tmpl := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{
							"name":  "app",
							"image": image,
						},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(tmpl)

	return runtime.RawExtension{Raw: raw}
}

// ensureSoleController proves nothing else is reconciling this cluster before
// our manager starts, by planting a canary PodPool and watching it.
//
// This exists because it happened: three controller binaries — two of them
// stale survivors of `pkill go run`, one running pre-fix code — were once
// reconciling the same kwok cluster at once, and every failure they produced
// pointed somewhere else. A process check would only see local strays; the
// canary catches anything with a watch on the CRD, wherever it runs, because
// the invariant that matters is behavioural: nobody but the suite's manager
// may act on PodPools here.
func ensureSoleController(cfg *rest.Config, scheme *runtime.Scheme) error {
	const canaryName = "controller-exclusivity-canary"

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("building canary client: %w", err)
	}

	ctx := context.Background()

	canary := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: canaryName, Namespace: testNamespace},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         1,
			WorkloadTemplate: makeWorkloadTemplate("nginx"),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{Name: "canary", Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](1)}},
			},
		},
	}
	canaryGone := func(ctx context.Context) (bool, error) { //nolint:unparam // signature fixed by wait.ConditionWithContextFunc
		err := c.Get(ctx, types.NamespacedName{Name: canaryName, Namespace: testNamespace}, &podpoolsv1alpha1.PodPool{})

		return err != nil, nil
	}

	// A leftover canary from an aborted run is stale evidence, not a verdict.
	// A canary that will not die is not fatal either: Create fails loudly on
	// AlreadyExists, so the timeout error is deliberately dropped.
	_ = c.Delete(ctx, canary.DeepCopy())
	_ = wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 15*time.Second, true, canaryGone)

	if err := c.Create(ctx, canary); err != nil {
		return fmt.Errorf("creating canary: %w", err)
	}
	defer func() {
		_ = c.Delete(ctx, canary)
		_ = wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 15*time.Second, true, canaryGone)
	}()

	// Our manager is not running yet, so any reaction at all is a foreign
	// controller. Watch both the signals a reconcile produces: status on the
	// pool, and a child workload.
	//
	// Deliberate quiet-window loop, not a poll: success here is running out
	// the clock with nothing observed, which is the inverse of what
	// wait.PollUntilContextTimeout expresses.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := &podpoolsv1alpha1.PodPool{}
		if err := c.Get(ctx, types.NamespacedName{Name: canaryName, Namespace: testNamespace}, got); err == nil {
			if len(got.Status.Conditions) > 0 || got.Status.GroupCount != 0 {
				return errors.New("the canary's status was written before this suite's manager started: " +
					"another controller is reconciling this cluster (check `pgrep -af exe/main` for stray " +
					"`go run` children, and other terminals running `make run`)")
			}
		}

		child := &appsv1.Deployment{}
		if err := c.Get(ctx, types.NamespacedName{Name: canaryName + "-canary", Namespace: testNamespace}, child); err == nil {
			return errors.New("the canary's child was created before this suite's manager started: " +
				"another controller is reconciling this cluster (check `pgrep -af exe/main`)")
		}

		time.Sleep(250 * time.Millisecond)
	}

	return nil
}

func TestMain(m *testing.M) {
	logf.SetLogger(zap.New(zap.UseDevMode(true)))

	clusterName := os.Getenv("KWOK_CLUSTER")
	if clusterName == "" {
		clusterName = "podpool-sim"
	}

	wantCtx := "kwok-" + clusterName
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{CurrentContext: wantCtx},
	)

	cfg, err := kubeConfig.ClientConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading kubeconfig: %v\n", err)
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = podpoolsv1alpha1.AddToScheme(scheme)

	// Create the node topology before starting the manager. The kwok
	// cluster's scheduler needs nodes to place pods on, and the tests own
	// their node shapes — no external YAML to apply.
	directClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "building direct client: %v\n", err)
		os.Exit(1)
	}

	topologyClient = directClient
	if err := ensureTopology(context.Background(), topologyClient, defaultTopology); err != nil {
		fmt.Fprintf(os.Stderr, "setting up node topology: %v\n", err)
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating manager: %v\n", err)
		os.Exit(1)
	}

	if err := ensureSoleController(cfg, scheme); err != nil {
		fmt.Fprintf(os.Stderr, "controller exclusivity check failed: %v\n", err)
		os.Exit(1)
	}

	reconciler := &controller.PodPoolReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		APIReader: mgr.GetAPIReader(),
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "setting up controller: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	mgrCancel = cancel

	go func() {
		if err := mgr.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "manager error: %v\n", err)
		}
	}()

	syncCtx, syncCancel := context.WithTimeout(ctx, 10*time.Second)
	defer syncCancel()

	if !mgr.GetCache().WaitForCacheSync(syncCtx) {
		cancel()
		fmt.Fprintln(os.Stderr, "cache sync failed")
		os.Exit(1)
	}

	k8sClient = mgr.GetClient()
	code := m.Run()

	cancel()
	os.Exit(code)
}

func cleanupPodPool(t *testing.T, name string) {
	t.Helper()

	ctx := context.Background()

	pool := &podpoolsv1alpha1.PodPool{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: testNamespace}, pool); err == nil {
		_ = k8sClient.Delete(ctx, pool)

		// Best effort, as before: a pool that outlives the wait surfaces in
		// the next test's Create, not here.
		_ = pollFor(30*time.Second, func(ctx context.Context) (bool, error) {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: testNamespace}, pool)

			return err != nil, nil
		})
	}
}

func waitForCondition(t *testing.T, name, condType string, status metav1.ConditionStatus) {
	t.Helper()

	last := "condition absent"

	err := pollFor(pollTimeout, func(ctx context.Context) (bool, error) {
		pool := &podpoolsv1alpha1.PodPool{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: testNamespace}, pool); err == nil {
			for _, cond := range pool.Status.Conditions {
				if cond.Type == condType {
					last = fmt.Sprintf("%s: %s", cond.Status, cond.Message)
				}

				if cond.Type == condType && cond.Status == status {
					t.Logf("condition %s=%s: %s", condType, status, cond.Message)

					return true, nil
				}
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("timed out waiting for condition %s=%s on %s, last saw %s", condType, status, name, last)
	}
}

func TestPodPoolFullLifecycle(t *testing.T) {
	poolName := "kwok-lifecycle"

	cleanupPodPool(t, poolName)
	defer cleanupPodPool(t, poolName)

	ctx := context.Background()

	pool := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      poolName,
			Namespace: testNamespace,
		},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         6,
			WorkloadTemplate: makeWorkloadTemplate("nginx:latest"),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:      "on-demand",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "on-demand"}, ""),
				},
				{
					Name:      "spot",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(70)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "spot"}, ""),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pool); err != nil {
		t.Fatalf("creating PodPool: %v", err)
	}

	t.Log("waiting for child Deployments...")

	var baseDep, spotDep appsv1.Deployment

	err := pollFor(pollTimeout, func(ctx context.Context) (bool, error) {
		errBase := k8sClient.Get(ctx, types.NamespacedName{Name: poolName + "-on-demand", Namespace: testNamespace}, &baseDep)
		errSpot := k8sClient.Get(ctx, types.NamespacedName{Name: poolName + "-spot", Namespace: testNamespace}, &spotDep)

		return errBase == nil && errSpot == nil, nil
	})
	if err != nil {
		t.Fatal("child Deployments not created in time")
	}

	if *baseDep.Spec.Replicas != 3 {
		t.Errorf("on-demand replicas: got %d, want 3", *baseDep.Spec.Replicas)
	}

	if *spotDep.Spec.Replicas != 3 {
		t.Errorf("spot replicas: got %d, want 3", *spotDep.Spec.Replicas)
	}

	if baseDep.Spec.Template.Spec.NodeSelector["capacity-type"] != "on-demand" {
		t.Errorf("on-demand Deployment nodeSelector: %v", baseDep.Spec.Template.Spec.NodeSelector)
	}

	if spotDep.Spec.Template.Spec.NodeSelector["capacity-type"] != "spot" {
		t.Errorf("spot Deployment nodeSelector: %v", spotDep.Spec.Template.Spec.NodeSelector)
	}

	t.Log("waiting for pods to become ready via KWOK stages...")
	waitForCondition(t, poolName, "Available", metav1.ConditionTrue)
	waitForCondition(t, poolName, "Progressing", metav1.ConditionFalse)

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName, Namespace: testNamespace}, pool); err != nil {
		t.Fatalf("getting PodPool status: %v", err)
	}

	if pool.Status.ReadyReplicas != 6 {
		t.Errorf("status.readyReplicas: got %d, want 6", pool.Status.ReadyReplicas)
	}

	if pool.Status.Replicas != 6 {
		t.Errorf("status.replicas: got %d, want 6", pool.Status.Replicas)
	}

	t.Log("verifying pod placement on nodes...")

	podList := &corev1.PodList{}
	if err := k8sClient.List(ctx, podList, client.InNamespace(testNamespace), client.MatchingLabels{
		"podpools.dev/pool":  poolName,
		"podpools.dev/group": "on-demand",
	}); err != nil {
		t.Fatalf("listing on-demand pods: %v", err)
	}

	for _, pod := range podList.Items {
		if pod.Spec.NodeSelector["capacity-type"] != "on-demand" {
			t.Errorf("on-demand pod %s has wrong nodeSelector: %v", pod.Name, pod.Spec.NodeSelector)
		}
	}

	spotPods := &corev1.PodList{}
	if err := k8sClient.List(ctx, spotPods, client.InNamespace(testNamespace), client.MatchingLabels{
		"podpools.dev/pool":  poolName,
		"podpools.dev/group": "spot",
	}); err != nil {
		t.Fatalf("listing spot pods: %v", err)
	}

	for _, pod := range spotPods.Items {
		if pod.Spec.NodeSelector["capacity-type"] != "spot" {
			t.Errorf("spot pod %s has wrong nodeSelector: %v", pod.Name, pod.Spec.NodeSelector)
		}
	}

	t.Logf("on-demand pods: %d, spot pods: %d", len(podList.Items), len(spotPods.Items))
}

func TestPodPoolScaling(t *testing.T) {
	poolName := "kwok-scale"

	cleanupPodPool(t, poolName)
	defer cleanupPodPool(t, poolName)

	ctx := context.Background()

	pool := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      poolName,
			Namespace: testNamespace,
		},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         4,
			WorkloadTemplate: makeWorkloadTemplate("nginx:latest"),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:      "on-demand",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](2)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "on-demand"}, ""),
				},
				{
					Name:      "spot",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(70)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "spot"}, ""),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pool); err != nil {
		t.Fatalf("creating PodPool: %v", err)
	}

	t.Log("waiting for initial Available=True...")
	waitForCondition(t, poolName, "Available", metav1.ConditionTrue)

	// Scale up. The controller patches this pool's status concurrently, which
	// bumps resourceVersion, so a bare Get/Update races it — scalePool retries
	// on conflict.
	scalePool(t, poolName, 10)

	t.Log("waiting for scale-up to complete...")

	err := pollFor(pollTimeout, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName, Namespace: testNamespace}, pool); err == nil {
			return pool.Status.ReadyReplicas == 10, nil
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("scale-up: readyReplicas=%d, want 10", pool.Status.ReadyReplicas)
	}

	var baseDep, spotDep appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName + "-on-demand", Namespace: testNamespace}, &baseDep); err != nil {
		t.Fatalf("getting on-demand deployment: %v", err)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName + "-spot", Namespace: testNamespace}, &spotDep); err != nil {
		t.Fatalf("getting spot deployment: %v", err)
	}

	t.Logf("after scale-up: on-demand=%d, spot=%d", *baseDep.Spec.Replicas, *spotDep.Spec.Replicas)

	total := *baseDep.Spec.Replicas + *spotDep.Spec.Replicas
	if total != 10 {
		t.Errorf("total replicas: got %d, want 10", total)
	}

	// Scale down
	scalePool(t, poolName, 2)

	t.Log("waiting for scale-down...")

	err = pollFor(pollTimeout, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName, Namespace: testNamespace}, pool); err == nil {
			return pool.Status.Replicas == 2, nil
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("scale-down: status.replicas=%d, want 2", pool.Status.Replicas)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName + "-on-demand", Namespace: testNamespace}, &baseDep); err != nil {
		t.Fatalf("getting on-demand after scale-down: %v", err)
	}

	if *baseDep.Spec.Replicas != 2 {
		t.Errorf("on-demand after scale-down: got %d, want 2", *baseDep.Spec.Replicas)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName + "-spot", Namespace: testNamespace}, &spotDep); err != nil {
		t.Fatalf("getting spot after scale-down: %v", err)
	}

	if *spotDep.Spec.Replicas != 0 {
		t.Errorf("spot after scale-down: got %d, want 0", *spotDep.Spec.Replicas)
	}
}

func TestPodPoolOrphanCleanup(t *testing.T) {
	poolName := "kwok-orphan"

	cleanupPodPool(t, poolName)
	defer cleanupPodPool(t, poolName)

	ctx := context.Background()

	pool := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      poolName,
			Namespace: testNamespace,
		},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         4,
			WorkloadTemplate: makeWorkloadTemplate("nginx:latest"),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:      "on-demand",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](2)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "on-demand"}, ""),
				},
				{
					Name:      "spot",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](1)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "spot"}, ""),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pool); err != nil {
		t.Fatalf("creating PodPool: %v", err)
	}

	t.Log("waiting for both groups to become available...")
	waitForCondition(t, poolName, "Available", metav1.ConditionTrue)

	var spotDep appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName + "-spot", Namespace: testNamespace}, &spotDep); err != nil {
		t.Fatalf("spot Deployment should exist: %v", err)
	}

	// Same conflict race as scaling: re-read and retry until the write lands.
	err := pollFor(30*time.Second, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName, Namespace: testNamespace}, pool); err != nil {
			return false, fmt.Errorf("getting pool: %w", err)
		}

		pool.Spec.Groups = pool.Spec.Groups[:1]
		pool.Spec.Groups[0].Scaling.Min = ptr.To[int32](4)

		return k8sClient.Update(ctx, pool) == nil, nil
	})
	if err != nil {
		t.Fatalf("removing spot group: %v", err)
	}

	t.Log("waiting for orphan cleanup...")

	err = pollFor(pollTimeout, func(ctx context.Context) (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName + "-spot", Namespace: testNamespace}, &spotDep)

		return err != nil, nil
	})
	if err != nil {
		t.Fatal("orphaned spot Deployment was not cleaned up")
	}

	var baseDep appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName + "-on-demand", Namespace: testNamespace}, &baseDep); err != nil {
		t.Fatalf("on-demand Deployment should still exist: %v", err)
	}

	if *baseDep.Spec.Replicas != 4 {
		t.Errorf("on-demand replicas after orphan cleanup: got %d, want 4", *baseDep.Spec.Replicas)
	}
}

// TestAsymmetricTopology swaps the cluster to non-uniform nodes and runs a
// basic lifecycle, proving that the controller handles heterogeneous capacity.
//
// Topology: 3 on-demand (4+4+2 CPU) + 1 spot (8 CPU).
// The 2-CPU node can fit fewer pods than the others, so the scheduler's
// bin-packing decisions differ from the uniform default.
func TestAsymmetricTopology(t *testing.T) {
	waitForNoPods(t)
	withTopology(t, asymmetricTopology)

	poolName := "kwok-asymmetric"

	cleanupPodPool(t, poolName)
	defer cleanupPodPool(t, poolName)

	ctx := context.Background()

	pool := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: poolName, Namespace: testNamespace},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         8,
			WorkloadTemplate: makeWorkloadTemplate("nginx:latest"),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:      "on-demand",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "on-demand"}, ""),
				},
				{
					Name:      "spot",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(70)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "spot"}, ""),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pool); err != nil {
		t.Fatalf("creating PodPool: %v", err)
	}

	waitForCondition(t, poolName, "Available", metav1.ConditionTrue)

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName, Namespace: testNamespace}, pool); err != nil {
		t.Fatalf("getting pool: %v", err)
	}

	if pool.Status.ReadyReplicas != 8 {
		t.Errorf("readyReplicas: got %d, want 8", pool.Status.ReadyReplicas)
	}

	t.Logf("asymmetric topology: %d ready across %d groups", pool.Status.ReadyReplicas, len(pool.Status.Groups))

	for _, g := range pool.Status.Groups {
		t.Logf("  %s: %d replicas, %d ready", g.Name, g.Replicas, g.ReadyReplicas)
	}
}

func TestScavengerPattern(t *testing.T) {
	poolName := "kwok-scavenger"

	cleanupPodPool(t, poolName)
	defer cleanupPodPool(t, poolName)

	ctx := context.Background()

	pool := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      poolName,
			Namespace: testNamespace,
		},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         10,
			WorkloadTemplate: makeWorkloadTemplate("api-server:v2.1.0"),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:      "base",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "on-demand"}, ""),
				},
				{
					Name:      "scavenger",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(30)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "on-demand"}, ""),
				},
				{
					Name:      "burst",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(50)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "spot"}, ""),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pool); err != nil {
		t.Fatalf("creating PodPool: %v", err)
	}

	t.Log("waiting for scavenger pattern to stabilize...")
	waitForCondition(t, poolName, "Available", metav1.ConditionTrue)

	var baseDep, scavengerDep, burstDep appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName + "-base", Namespace: testNamespace}, &baseDep); err != nil {
		t.Fatalf("base: %v", err)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName + "-scavenger", Namespace: testNamespace}, &scavengerDep); err != nil {
		t.Fatalf("scavenger: %v", err)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName + "-burst", Namespace: testNamespace}, &burstDep); err != nil {
		t.Fatalf("burst: %v", err)
	}

	baseR := *baseDep.Spec.Replicas
	scavR := *scavengerDep.Spec.Replicas
	burstR := *burstDep.Spec.Replicas
	total := baseR + scavR + burstR

	t.Logf("scavenger pattern: base=%d, scavenger=%d, burst=%d (total=%d)", baseR, scavR, burstR, total)

	if total != 10 {
		t.Errorf("total replicas: got %d, want 10", total)
	}

	if baseR < 3 {
		t.Errorf("base should have at least 3 (min): got %d", baseR)
	}

	for _, tc := range []struct {
		group        string
		capacityType string
	}{
		{"base", "on-demand"},
		{"scavenger", "on-demand"},
		{"burst", "spot"},
	} {
		pods := &corev1.PodList{}
		if err := k8sClient.List(ctx, pods, client.InNamespace(testNamespace), client.MatchingLabels{
			"podpools.dev/pool":  poolName,
			"podpools.dev/group": tc.group,
		}); err != nil {
			t.Errorf("listing %s pods: %v", tc.group, err)

			continue
		}

		for _, pod := range pods.Items {
			if pod.Spec.NodeSelector["capacity-type"] != tc.capacityType {
				t.Errorf("%s pod %s nodeSelector: got %v, want capacity-type=%s",
					tc.group, pod.Name, pod.Spec.NodeSelector, tc.capacityType)
			}
		}
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: poolName, Namespace: testNamespace}, pool); err != nil {
		t.Fatalf("getting pool status: %v", err)
	}

	if len(pool.Status.Groups) != 3 {
		t.Errorf("expected 3 group statuses, got %d", len(pool.Status.Groups))
	}

	for _, gs := range pool.Status.Groups {
		t.Logf("  group %s: replicas=%d, ready=%d, workload=%s/%s",
			gs.Name, gs.Replicas, gs.ReadyReplicas, gs.WorkloadRef.Kind, gs.WorkloadRef.Name)

		if gs.WorkloadRef.Kind != "Deployment" {
			t.Errorf("group %s workloadRef.kind: got %s, want Deployment", gs.Name, gs.WorkloadRef.Kind)
		}
	}

	var totalReady int32
	for _, gs := range pool.Status.Groups {
		totalReady += gs.ReadyReplicas
	}

	if pool.Status.ReadyReplicas != totalReady {
		t.Errorf("status.readyReplicas=%d != sum of group readyReplicas=%d", pool.Status.ReadyReplicas, totalReady)
	}

	fmt.Printf("\n=== Scavenger Pattern Summary ===\n")
	fmt.Printf("Total: %d replicas across 3 groups\n", total)
	fmt.Printf("  base (on-demand):      %d pods\n", baseR)
	fmt.Printf("  scavenger (on-demand): %d pods (max 30%%)\n", scavR)
	fmt.Printf("  burst (spot):          %d pods (max 50%%)\n", burstR)
	fmt.Printf("All pods ready: %d/%d\n", pool.Status.ReadyReplicas, pool.Status.Replicas)
}
