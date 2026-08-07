//go:build kwok

package kwok

import (
	"context"
	"fmt"
	"maps"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type kwokNode struct {
	Name   string
	Labels map[string]string
	CPU    string
	Memory string
}

// Topologies are Go-defined so the tests own their node shapes. Adding a
// topology here and calling withTopology(t, ...) in a test is all it takes;
// nothing external (YAML files, manual kubectl) is needed.
//
// If a test's arithmetic depends on node capacity, pin the topology in that
// test's header comment so a reader can verify the numbers without
// cross-referencing.

var (
	// 2 on-demand + 2 spot, 4 CPU / 16Gi each. Used by the lifecycle,
	// scaling, orphan, scavenger, and HPA tests.
	defaultTopology = []kwokNode{
		{Name: "on-demand-1", Labels: map[string]string{"capacity-type": "on-demand"}, CPU: "4", Memory: "16Gi"},
		{Name: "on-demand-2", Labels: map[string]string{"capacity-type": "on-demand"}, CPU: "4", Memory: "16Gi"},
		{Name: "spot-1", Labels: map[string]string{"capacity-type": "spot"}, CPU: "4", Memory: "16Gi"},
		{Name: "spot-2", Labels: map[string]string{"capacity-type": "spot"}, CPU: "4", Memory: "16Gi"},
	}

	// 3 on-demand (one smaller) + 1 large spot. Proves the controller
	// handles heterogeneous capacity without assuming uniform nodes.
	asymmetricTopology = []kwokNode{
		{Name: "on-demand-1", Labels: map[string]string{"capacity-type": "on-demand"}, CPU: "4", Memory: "16Gi"},
		{Name: "on-demand-2", Labels: map[string]string{"capacity-type": "on-demand"}, CPU: "4", Memory: "16Gi"},
		{Name: "on-demand-3", Labels: map[string]string{"capacity-type": "on-demand"}, CPU: "2", Memory: "8Gi"},
		{Name: "spot-1", Labels: map[string]string{"capacity-type": "spot"}, CPU: "8", Memory: "32Gi"},
	}
)

// ensureTopology replaces all kwok-managed nodes with the requested set. Safe
// to call before the controller's manager starts (TestMain) or mid-suite
// (per-test overrides via withTopology).
func ensureTopology(ctx context.Context, c client.Client, nodes []kwokNode) error {
	var nodeList corev1.NodeList
	if err := c.List(ctx, &nodeList); err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}

	for i := range nodeList.Items {
		if nodeList.Items[i].Annotations["kwok.x-k8s.io/node"] == "fake" {
			if err := c.Delete(ctx, &nodeList.Items[i]); err != nil {
				return fmt.Errorf("deleting node %s: %w", nodeList.Items[i].Name, err)
			}
		}
	}

	// Best effort, as before: a fake node that outlives the wait surfaces as
	// an AlreadyExists on the create below.
	_ = wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 15*time.Second, true,
		func(ctx context.Context) (bool, error) {
			if err := c.List(ctx, &nodeList); err != nil {
				return false, nil //nolint:nilerr // transient read failure: keep polling
			}

			for i := range nodeList.Items {
				if nodeList.Items[i].Annotations["kwok.x-k8s.io/node"] == "fake" {
					return false, nil
				}
			}

			return true, nil
		})

	for _, n := range nodes {
		if err := createKwokNode(ctx, c, n); err != nil {
			return err
		}
	}

	lastReady := 0

	err := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 30*time.Second, true,
		func(ctx context.Context) (bool, error) {
			if err := c.List(ctx, &nodeList); err != nil {
				return false, nil //nolint:nilerr // transient read failure: keep polling
			}

			ready := 0

			for i := range nodeList.Items {
				if nodeList.Items[i].Annotations["kwok.x-k8s.io/node"] != "fake" {
					continue
				}

				for _, cond := range nodeList.Items[i].Status.Conditions {
					if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
						ready++
					}
				}
			}

			lastReady = ready

			return ready >= len(nodes), nil
		})
	if err != nil {
		return fmt.Errorf("timed out waiting for %d nodes to become Ready, last saw %d: %w", len(nodes), lastReady, err)
	}

	return nil
}

func createKwokNode(ctx context.Context, c client.Client, n kwokNode) error {
	labels := map[string]string{"kubernetes.io/os": "linux"}
	maps.Copy(labels, n.Labels)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        n.Name,
			Labels:      labels,
			Annotations: map[string]string{"kwok.x-k8s.io/node": "fake"},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(n.CPU),
				corev1.ResourceMemory: resource.MustParse(n.Memory),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(n.CPU),
				corev1.ResourceMemory: resource.MustParse(n.Memory),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
	if err := c.Create(ctx, node); err != nil {
		return fmt.Errorf("creating node %s: %w", n.Name, err)
	}

	// The API server may strip status on create for some resource types.
	// Verify and patch via the status subresource if needed.
	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(node), got); err != nil {
		return fmt.Errorf("verifying node %s: %w", n.Name, err)
	}

	if got.Status.Allocatable.Cpu().IsZero() {
		got.Status = node.Status
		if err := c.Status().Update(ctx, got); err != nil {
			return fmt.Errorf("patching status for node %s: %w", n.Name, err)
		}
	}

	return nil
}

// withTopology swaps the cluster's nodes for the given set and restores the
// default topology when the test finishes. The cleanup drains all pods before
// deleting nodes — without this, pods whose node vanishes become zombies
// (no deletionTimestamp, so kwok's pod-delete Stage never fires).
func withTopology(t *testing.T, nodes []kwokNode) {
	t.Helper()

	ctx := context.Background()
	if err := ensureTopology(ctx, topologyClient, nodes); err != nil {
		t.Fatalf("setting up topology: %v", err)
	}

	t.Cleanup(func() {
		waitForNoPods(t)

		if err := ensureTopology(context.Background(), topologyClient, defaultTopology); err != nil {
			t.Errorf("restoring default topology: %v", err)
		}
	})
}

// waitForNoPods blocks until the test namespace has no pods at all, so a
// test whose arithmetic depends on node headroom cannot inherit terminating
// pods from an earlier test.
func waitForNoPods(t *testing.T) {
	t.Helper()

	remaining := -1

	err := pollFor(30*time.Second, func(ctx context.Context) (bool, error) {
		pods := &corev1.PodList{}
		if err := k8sClient.List(ctx, pods); err == nil {
			remaining = len(pods.Items)
		}

		return remaining == 0, nil
	})
	if err != nil {
		t.Fatalf("namespace not quiescent: %d pods still present at test start", remaining)
	}
}
