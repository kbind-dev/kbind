/*
Copyright 2022 The Kube Bind Authors.

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

package options

import (
	"fmt"

	"github.com/kcp-dev/sdk/apis/core"
	"github.com/spf13/pflag"
	"k8s.io/component-base/logs"
	logsv1 "k8s.io/component-base/logs/api/v1"
)

type Options struct {
	Logs *logs.Options

	ExtraOptions
}

type ExtraOptions struct {
	KCPKubeConfig string
	KCPContext    string

	RootWorkspace  string
	KBindWorkspace string
}

type completedOptions struct {
	Logs *logs.Options

	ExtraOptions
}

type CompletedOptions struct {
	*completedOptions
}

func NewOptions() *Options {
	// Default to -v=2
	logs := logs.NewOptions()
	logs.Verbosity = logsv1.VerbosityLevel(2)

	return &Options{
		Logs: logs,
		ExtraOptions: ExtraOptions{
			RootWorkspace:  core.RootCluster.Path().String(),
			KBindWorkspace: "root:kbind",
		},
	}
}

func (options *Options) AddFlags(fs *pflag.FlagSet) {
	logsv1.AddFlags(options.Logs, fs)

	fs.StringVar(&options.KCPKubeConfig, "kcp-kubeconfig", options.KCPKubeConfig, "path to a kcp kubeconfig. Required to bootstrap the server.")
	fs.StringVar(&options.KCPContext, "context", options.KCPContext, "Name of the context in the kcp kubeconfig file to use")
	fs.StringVar(&options.RootWorkspace, "root-workspace", options.RootWorkspace, "Workspace path to use as the root (must exist already)")
	fs.StringVar(&options.KBindWorkspace, "kbind-workspace", options.KBindWorkspace, "kbind workspace to use (must exist already)")
}

func (options *Options) Complete() (*CompletedOptions, error) {
	if options.KCPKubeConfig == "" {
		return nil, fmt.Errorf("kcp kubeconfig must be specified")
	}

	return &CompletedOptions{
		completedOptions: &completedOptions{
			Logs:         options.Logs,
			ExtraOptions: options.ExtraOptions,
		},
	}, nil
}

func (options *CompletedOptions) Validate() error {
	return nil
}
