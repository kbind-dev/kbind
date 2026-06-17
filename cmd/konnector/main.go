/*
Copyright 2026 The Kube Bind Authors.

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

// Command konnector is the v2 slim-core sync engine. It runs in (or against) a
// consumer cluster, watches Connection/ClusterBinding/Binding objects, and
// bridges to provider clusters discovered from Connections.
package main

import (
	"context"
	"errors"
	"flag"
	"os"

	"golang.org/x/sync/errgroup"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/kbind/kbind/engine/binding"
	"github.com/kbind/kbind/engine/connection"
	"github.com/kbind/kbind/engine/provider"
	syncengine "github.com/kbind/kbind/engine/sync"
	corev1alpha1 "github.com/kbind/kbind/sdk/apis/core/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilRuntimeMust(clientgoscheme.AddToScheme(scheme))
	utilRuntimeMust(apiextensionsv1.AddToScheme(scheme))
	utilRuntimeMust(corev1alpha1.AddToScheme(scheme))
}

// options holds the konnector's runtime flags.
type options struct {
	metricsAddr      string
	probeAddr        string
	leaderElect      bool
	leaderElectionID string
}

func main() {
	var o options
	flag.StringVar(&o.metricsAddr, "metrics-bind-address", ":8085", "address the metric endpoint binds to")
	flag.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "address the health/readiness probe endpoint binds to")
	flag.BoolVar(&o.leaderElect, "leader-elect", false,
		"enable leader election, ensuring only one active konnector replica (required for HA / multiple replicas)")
	flag.StringVar(&o.leaderElectionID, "leader-election-id", "konnector.kbind.io",
		"name of the Lease used for leader election")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if err := run(o); err != nil {
		ctrl.Log.Error(err, "konnector exited with error")
		os.Exit(1)
	}
}

func run(o options) error {
	ctx := ctrl.SetupSignalHandler()
	log := ctrl.Log.WithName("konnector")

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}

	// Local (consumer) manager: owns the core CRDs and the consumer-side caches.
	// Leader election gates all consumer-side controllers, so standby replicas
	// engage no provider clusters and run no syncers until they win the lease.
	localMgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                        scheme,
		Metrics:                       metricsserver.Options{BindAddress: o.metricsAddr},
		HealthProbeBindAddress:        o.probeAddr,
		LeaderElection:                o.leaderElect,
		LeaderElectionID:              o.leaderElectionID,
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return err
	}
	if err := localMgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := localMgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}

	// Connection provider: each ready Connection becomes an engaged provider cluster.
	connProvider, err := provider.New(localMgr, provider.Options{})
	if err != nil {
		return err
	}

	// Multicluster manager bridging consumer <-> provider clusters.
	mcMgr, err := mcmanager.New(cfg, connProvider, mcmanager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return err
	}

	// Consumer-side reconcilers (run on the local manager).
	if err := (&connection.Reconciler{}).SetupWithManager(localMgr); err != nil {
		return err
	}
	if err := (&binding.ClusterReconciler{}).SetupWithManager(localMgr); err != nil {
		return err
	}
	if err := (&binding.NamespacedReconciler{}).SetupWithManager(localMgr); err != nil {
		return err
	}
	if err := syncengine.SetupWithManager(localMgr, connProvider); err != nil {
		return err
	}

	log.Info("starting konnector")
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return ignoreCanceled(localMgr.Start(ctx)) })
	g.Go(func() error { return ignoreCanceled(connProvider.Run(ctx, mcMgr)) })
	g.Go(func() error { return ignoreCanceled(mcMgr.Start(ctx)) })
	return g.Wait()
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func utilRuntimeMust(err error) {
	if err != nil {
		panic(err)
	}
}
