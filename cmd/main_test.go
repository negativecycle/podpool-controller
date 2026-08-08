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
	"flag"
	"strings"
	"testing"
	"time"
)

func TestFlagDefaults(t *testing.T) {
	o := bindFlags(flag.NewFlagSet("test", flag.ContinueOnError))

	if o.maxConcurrentReconciles != 5 {
		t.Errorf("maxConcurrentReconciles = %d, want 5", o.maxConcurrentReconciles)
	}

	if o.rateLimiterBaseDelay != 1*time.Second {
		t.Errorf("rateLimiterBaseDelay = %s, want 1s", o.rateLimiterBaseDelay)
	}

	if o.rateLimiterMaxDelay != 5*time.Minute {
		t.Errorf("rateLimiterMaxDelay = %s, want 5m", o.rateLimiterMaxDelay)
	}
}

func TestQPSDefaultPreservesUnlimited(t *testing.T) {
	o := bindFlags(flag.NewFlagSet("test", flag.ContinueOnError))

	if o.kubeAPIQPS >= 0 {
		t.Errorf("kubeAPIQPS = %f, want negative (unlimited); a positive default re-introduces "+
			"client-side throttling that controller-runtime deliberately disables", o.kubeAPIQPS)
	}
}

func TestValidateRejectsZeroConcurrency(t *testing.T) {
	o := bindFlags(flag.NewFlagSet("test", flag.ContinueOnError))
	o.maxConcurrentReconciles = 0

	err := o.validate()
	if err == nil {
		t.Fatal("validate() accepted maxConcurrentReconciles=0; controller-runtime silently substitutes 1")
	}

	if !strings.Contains(err.Error(), "max-concurrent-reconciles") {
		t.Errorf("error does not name the flag: %v", err)
	}
}

func TestValidateRejectsInvertedRateLimiterBounds(t *testing.T) {
	o := bindFlags(flag.NewFlagSet("test", flag.ContinueOnError))
	o.rateLimiterBaseDelay = 10 * time.Minute
	o.rateLimiterMaxDelay = 1 * time.Second

	err := o.validate()
	if err == nil {
		t.Fatal("validate() accepted base > max; the workqueue rate limiter behaves unexpectedly")
	}

	if !strings.Contains(err.Error(), "rate-limiter") {
		t.Errorf("error does not name the flag: %v", err)
	}
}

func TestBindFlagsUsesProvidedFlagSet(t *testing.T) {
	fs1 := flag.NewFlagSet("a", flag.ContinueOnError)
	fs2 := flag.NewFlagSet("b", flag.ContinueOnError)

	o1 := bindFlags(fs1)
	o2 := bindFlags(fs2)

	if err := fs1.Parse([]string{"--max-concurrent-reconciles=10"}); err != nil {
		t.Fatal(err)
	}

	if o1.maxConcurrentReconciles != 10 {
		t.Errorf("fs1 parse: got %d, want 10", o1.maxConcurrentReconciles)
	}

	if o2.maxConcurrentReconciles != 5 {
		t.Errorf("fs2 should still be default: got %d, want 5", o2.maxConcurrentReconciles)
	}
}
