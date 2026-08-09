/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	ctrl "sigs.k8s.io/controller-runtime"
)

func recordingLogger(verbosity int) (logr.Logger, *[]string) {
	var lines []string

	logger := funcr.New(func(prefix, args string) {
		lines = append(lines, prefix+" "+args)
	}, funcr.Options{Verbosity: verbosity})

	return logger, &lines
}

func containsMessage(lines []string, fragment string) bool {
	for _, l := range lines {
		if strings.Contains(l, fragment) {
			return true
		}
	}

	return false
}

func TestComputedTargetsIsNotLoggedAtDefaultLevel(t *testing.T) {
	logger, lines := recordingLogger(0)

	logger.Info("Something at V(0)")
	logger.V(4).Info("Computed group targets", "total", 10, "targets", []int32{5, 5})

	if containsMessage(*lines, "Computed group targets") {
		t.Error("V(4) message appeared at default level; production logs will be flooded with per-reconcile output")
	}
}

func TestComputedTargetsIsLoggedAtV4(t *testing.T) {
	logger, lines := recordingLogger(4)

	logger.V(4).Info("Computed group targets", "total", 10, "targets", []int32{5, 5})

	if !containsMessage(*lines, "Computed group targets") {
		t.Error("V(4) message did not appear at verbosity 4; the demotion may have been turned into a deletion")
	}
}

// The counterweight to the demotion. Level 0 is not "log nothing", it is "log
// state changes an operator acts on", and deleting a running workload is the
// most consequential thing this controller does. A sweep that ran silently
// would be indistinguishable from one that never ran.
func TestOrphanDeletionStaysAtDefaultLevel(t *testing.T) {
	logger, lines := recordingLogger(0)
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)

	ctx := logr.NewContext(t.Context(), logger)
	if _, err := r.Reconcile(ctx, reconcileRequestFor(pool)); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	live := getPool(t, cl, pool)
	live.Spec.Groups = live.Spec.Groups[:1]

	if err := cl.Update(t.Context(), live); err != nil {
		t.Fatalf("dropping a group: %v", err)
	}

	if _, err := r.Reconcile(ctx, reconcileRequestFor(pool)); err != nil {
		t.Fatalf("sweeping reconcile: %v", err)
	}

	if !containsMessage(*lines, "Deleting orphaned workload") {
		t.Errorf("orphan deletion is invisible at the default level; a destructive action "+
			"must be auditable without turning on debug logging. lines: %v", *lines)
	}
}

// The two tests above exercise logr, not this controller: they pin what V(4)
// means, and would stay green if Reconcile logged at level 0. These two drive a
// real reconcile with a recording logger installed on the context, which is
// where logf.FromContext gets it from.
func reconcileWithLogger(t *testing.T, verbosity int) []string {
	t.Helper()

	logger, lines := recordingLogger(verbosity)
	pool := fakeTestPool()
	r, _ := newFakeReconciler(t, nil, pool)

	ctx := logr.NewContext(t.Context(), logger)
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: reconcileRequestFor(pool).NamespacedName}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	return *lines
}

func TestReconcileIsQuietAtDefaultVerbosity(t *testing.T) {
	if containsMessage(reconcileWithLogger(t, 0), "Computed group targets") {
		t.Error("a healthy reconcile logged at the default level; one line per pass is " +
			"noise at 2 pools and an outage at 2000")
	}
}

func TestReconcileLogsTargetsAtV4(t *testing.T) {
	if !containsMessage(reconcileWithLogger(t, 4), "Computed group targets") {
		t.Error("the targets line is missing at verbosity 4; the demotion was a deletion")
	}
}
