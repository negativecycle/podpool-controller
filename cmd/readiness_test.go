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

package main

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type stubCache struct {
	syncResult bool
}

func (s *stubCache) WaitForCacheSync(ctx context.Context) bool { return s.syncResult }

func TestCacheSyncLatchNotReadyBeforeSync(t *testing.T) {
	l := &cacheSyncLatch{
		cache:   &stubCache{syncResult: true},
		readyCh: make(chan struct{}),
	}

	if err := l.Checker(nil); err == nil {
		t.Fatal("Checker returned nil before Start; pod would report ready with an empty cache")
	}
}

func TestCacheSyncLatchReadyAfterSync(t *testing.T) {
	l := &cacheSyncLatch{
		cache:   &stubCache{syncResult: true},
		readyCh: make(chan struct{}),
	}

	if err := l.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := l.Checker(nil); err != nil {
		t.Errorf("Checker returned error after successful sync: %v", err)
	}
}

func TestCacheSyncLatchStaysReadyAfterLaterInformerAdded(t *testing.T) {
	sc := &stubCache{syncResult: true}
	l := &cacheSyncLatch{cache: sc, readyCh: make(chan struct{})}

	if err := l.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Simulate a later informer that never syncs. A live check calling
	// WaitForCacheSync would now block or return false, pulling the pod
	// out of the webhook Service because of a user's bad CR.
	sc.syncResult = false

	if err := l.Checker(nil); err != nil {
		t.Errorf("Checker failed after a later informer was added: %v; "+
			"a live check would let one bad PodPool take down admission cluster-wide", err)
	}
}

func TestCacheSyncLatchDoesNotNeedLeaderElection(t *testing.T) {
	l := &cacheSyncLatch{readyCh: make(chan struct{})}

	if l.NeedLeaderElection() {
		t.Fatal("NeedLeaderElection() = true; standby replicas would never become ready " +
			"and never join the webhook Service")
	}
}

func TestCacheSyncLatchImplementsLeaderElectionRunnable(t *testing.T) {
	var _ manager.LeaderElectionRunnable = (*cacheSyncLatch)(nil)
}

func TestLatchSurfacesSyncFailure(t *testing.T) {
	l := &cacheSyncLatch{
		cache:   &stubCache{syncResult: false},
		readyCh: make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := l.Start(ctx); err == nil {
		t.Fatal("Start returned nil with a cancelled context and a failing cache")
	}

	if err := l.Checker(nil); err == nil {
		t.Error("Checker returned nil after a failed sync; the channel should not have been closed")
	}
}
