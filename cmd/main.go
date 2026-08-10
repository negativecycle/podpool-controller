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
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/controller"
	webhookv1alpha1 "github.com/negativecycle/podpool-controller/internal/webhook/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(podpoolsv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// ----------------------------------------------------------------------------
// cacheSyncLatch
// ----------------------------------------------------------------------------

type cacheSyncer interface {
	WaitForCacheSync(ctx context.Context) bool
}

// cacheSyncLatch closes readyCh once the manager's caches have completed their
// initial sync, and never re-opens it.
//
// One-shot on purpose. Cache.WaitForCacheSync waits on every registered
// informer, and ensureWatch registers one per workloadTemplate GVK at runtime,
// so a live check would let a PodPool naming an uninstalled GVK fail readiness
// on every replica and pull the whole fleet out of the webhook Service.
type cacheSyncLatch struct {
	cache   cacheSyncer
	readyCh chan struct{}
}

var _ manager.LeaderElectionRunnable = (*cacheSyncLatch)(nil)

func (l *cacheSyncLatch) Start(ctx context.Context) error {
	if !l.cache.WaitForCacheSync(ctx) {
		return errors.New("failed to wait for caches to sync")
	}

	close(l.readyCh)

	return nil
}

// NeedLeaderElection returns false because the webhook server serves on every
// replica regardless of leadership (config/webhook/service.yaml selects all
// pods with control-plane=controller-manager). A standby that never becomes
// ready is a standby the Service never routes to.
func (l *cacheSyncLatch) NeedLeaderElection() bool { return false }

func (l *cacheSyncLatch) Checker(_ *http.Request) error {
	select {
	case <-l.readyCh:
		return nil
	default:
		return errors.New("informer caches have not completed their initial sync")
	}
}

// ----------------------------------------------------------------------------
// options
// ----------------------------------------------------------------------------

type options struct {
	metricsAddr    string
	probeAddr      string
	secureMetrics  bool
	enableHTTP2    bool
	enableWebhooks bool

	webhookCertPath string
	webhookCertName string
	webhookCertKey  string
	metricsCertPath string
	metricsCertName string
	metricsCertKey  string

	enableLeaderElection    bool
	maxConcurrentReconciles int
	rateLimiterBaseDelay    time.Duration
	rateLimiterMaxDelay     time.Duration
	kubeAPIQPS              float64
	kubeAPIBurst            int

	zapOpts zap.Options
}

func bindFlags(fs *flag.FlagSet) *options {
	o := &options{}
	fs.StringVar(&o.metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	fs.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	fs.BoolVar(&o.enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	fs.BoolVar(&o.secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	fs.StringVar(&o.webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	fs.StringVar(&o.webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	fs.StringVar(&o.webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	fs.StringVar(&o.metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	fs.StringVar(&o.metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	fs.StringVar(&o.metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	fs.BoolVar(&o.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")

	fs.IntVar(&o.maxConcurrentReconciles, "max-concurrent-reconciles", 5,
		"Maximum number of concurrent reconciles for the PodPool controller.")
	fs.DurationVar(&o.rateLimiterBaseDelay, "rate-limiter-base-delay", 1*time.Second,
		"Base delay for the per-item exponential failure rate limiter.")
	fs.DurationVar(&o.rateLimiterMaxDelay, "rate-limiter-max-delay", 5*time.Minute,
		"Maximum delay for the per-item exponential failure rate limiter.")

	// Default to -1 (unlimited): controller-runtime disables client-side
	// throttling by default in favour of server-side API Priority and
	// Fairness. A positive value here re-introduces a client-side bottleneck;
	// use only on clusters running without APF.
	fs.Float64Var(&o.kubeAPIQPS, "kube-api-qps", -1,
		"Client-side QPS limit for the Kubernetes API server. "+
			"-1 disables client-side throttling (default; relies on server-side API Priority and Fairness). "+
			"Raising --max-concurrent-reconciles with a positive QPS may starve workers.")
	fs.IntVar(&o.kubeAPIBurst, "kube-api-burst", 0,
		"Client-side burst limit for the Kubernetes API server. "+
			"Only meaningful with a positive --kube-api-qps.")

	o.zapOpts = zap.Options{Development: false}
	o.zapOpts.BindFlags(fs)

	return o
}

func (o *options) validate() error {
	if o.maxConcurrentReconciles < 1 {
		return fmt.Errorf("--max-concurrent-reconciles must be >= 1, got %d", o.maxConcurrentReconciles)
	}

	if o.rateLimiterBaseDelay > o.rateLimiterMaxDelay {
		return fmt.Errorf("--rate-limiter-base-delay (%s) must not exceed --rate-limiter-max-delay (%s)",
			o.rateLimiterBaseDelay, o.rateLimiterMaxDelay)
	}

	return nil
}

func main() {
	o := bindFlags(flag.CommandLine)

	flag.Parse()

	// The scaffold's convention, kept deliberately: envtest suites and local
	// runs disable the webhook with an env var, and the deployed manifests
	// never set it. It lives on options so every consumer reads the same
	// answer main() acted on.
	o.enableWebhooks = os.Getenv("ENABLE_WEBHOOKS") != "false"

	if err := o.validate(); err != nil {
		setupLog.Error(err, "Invalid flags")
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&o.zapOpts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	var tlsOpts []func(*tls.Config)
	if !o.enableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			setupLog.Info("Disabling HTTP/2")

			c.NextProtos = []string{"http/1.1"}
		})
	}

	webhookServerOptions := webhook.Options{
		TLSOpts: tlsOpts,
	}

	if len(o.webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", o.webhookCertPath, "webhook-cert-name", o.webhookCertName, "webhook-cert-key", o.webhookCertKey)

		webhookServerOptions.CertDir = o.webhookCertPath
		webhookServerOptions.CertName = o.webhookCertName
		webhookServerOptions.KeyName = o.webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   o.metricsAddr,
		SecureServing: o.secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if o.secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// Metrics TLS certs can be provisioned by cert-manager via the
	// [METRICS-WITH-CERTS] kustomize component in config/default/kustomization.yaml.
	if len(o.metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", o.metricsCertPath, "metrics-cert-name", o.metricsCertName, "metrics-cert-key", o.metricsCertKey)

		metricsServerOptions.CertDir = o.metricsCertPath
		metricsServerOptions.CertName = o.metricsCertName
		metricsServerOptions.KeyName = o.metricsCertKey
	}

	cfg := ctrl.GetConfigOrDie()
	cfg.QPS = float32(o.kubeAPIQPS)
	cfg.Burst = o.kubeAPIBurst

	// GracefulShutdownTimeout must be shorter than the pod's
	// terminationGracePeriodSeconds (60s in config/manager/manager.yaml)
	// so the manager finishes draining before the kubelet sends SIGKILL.
	// LeaderElectionReleaseOnCancel releases the lease at the end of
	// shutdown; if SIGKILL arrives first the lease is not released and
	// failover waits out the full LeaseDuration.
	gracefulShutdown := 30 * time.Second

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                        scheme,
		Metrics:                       metricsServerOptions,
		WebhookServer:                 webhookServer,
		HealthProbeBindAddress:        o.probeAddr,
		LeaderElection:                o.enableLeaderElection,
		LeaderElectionID:              "podpool-controller.podpools.dev",
		LeaderElectionReleaseOnCancel: true,
		GracefulShutdownTimeout:       &gracefulShutdown,
		Cache: cache.Options{
			// Strip fields the controller never reads before they enter the
			// cache. This applies to every informer, including the ones
			// ensureWatch creates at runtime for workload GVKs.
			DefaultTransform: controller.TransformStripCacheWeight(),
			// Cache only objects this controller manages. Every child carries
			// the managed-by label, and the informers ensureWatch creates for
			// workload GVKs inherit this selector, so unmanaged Deployments in
			// the cluster never enter the cache at all.
			DefaultLabelSelector: labels.SelectorFromSet(labels.Set{
				workload.LabelManagedBy: workload.ManagerName,
			}),
			ByObject: map[client.Object]cache.ByObject{
				// The PodPool itself carries no managed-by label -- users
				// create pools -- so it must opt OUT of the default selector.
				// The trap: an empty ByObject{} entry does not opt out. A nil
				// Label cascades to DefaultLabelSelector, the cache then
				// filters every PodPool away, and the controller goes silently
				// deaf: no reconciles, no errors, nothing. Only a non-nil
				// selector stops the cascade.
				&podpoolsv1alpha1.PodPool{}: {Label: labels.Everything()},
			},
		},
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	if err := (&controller.PodPoolReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("podpool-controller"),
		// Uncached: opportunistic sizing reads a few pods occasionally rather
		// than caching every pod in the cluster.
		APIReader:               mgr.GetAPIReader(),
		MaxConcurrentReconciles: o.maxConcurrentReconciles,
		RateLimiterBaseDelay:    o.rateLimiterBaseDelay,
		RateLimiterMaxDelay:     o.rateLimiterMaxDelay,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "podpool")
		os.Exit(1)
	}

	if o.enableWebhooks {
		if err := webhookv1alpha1.SetupPodPoolWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "PodPool")
			os.Exit(1)
		}
	}
	// +kubebuilder:scaffold:builder

	// Liveness: always healthy once the process is running. Must NOT gate on
	// cache sync or the API server: an apiserver blip would restart every
	// operator pod simultaneously.
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}

	latch := &cacheSyncLatch{cache: mgr.GetCache(), readyCh: make(chan struct{})}
	if err := mgr.Add(latch); err != nil {
		setupLog.Error(err, "Failed to add cache sync latch")
		os.Exit(1)
	}

	if err := mgr.AddReadyzCheck("cache-sync", latch.Checker); err != nil {
		setupLog.Error(err, "Failed to set up cache sync readiness check")
		os.Exit(1)
	}

	if o.enableWebhooks {
		if err := mgr.AddReadyzCheck("webhook", webhookServer.StartedChecker()); err != nil {
			setupLog.Error(err, "Failed to set up webhook readiness check")
			os.Exit(1)
		}
	}

	setupLog.Info("Starting manager")

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
