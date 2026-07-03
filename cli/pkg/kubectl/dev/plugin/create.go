/*
Copyright 2025 The Kube Bind Authors.

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

package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/component-base/logs"
	logsv1 "k8s.io/component-base/logs/api/v1"
	"sigs.k8s.io/kind/pkg/cluster"

	"github.com/kube-bind/kube-bind/cli/pkg/kubectl/base"
)

// DevOptions contains the options for the dev command
type DevOptions struct {
	*base.Options
	Logs    *logs.Options
	Streams genericclioptions.IOStreams

	BackendImage        string
	BackendTag          string
	ProviderClusterName string
	ConsumerClusterName string
	WaitForReadyTimeout time.Duration
	ChartPath           string
	ChartVersion        string
	KindNetwork         string
}

// fallbackAssetVersion is used when unable to fetch the latest version
const fallbackAssetVersion = "0.7.1"

// gitHubRelease represents a GitHub release response
type gitHubRelease struct {
	TagName string `json:"tag_name"`
}

// NewDevOptions creates a new DevOptions
func NewDevOptions(streams genericclioptions.IOStreams) *DevOptions {
	opts := base.NewOptions(streams)
	return &DevOptions{
		Options:             opts,
		Logs:                logs.NewOptions(),
		Streams:             streams,
		ProviderClusterName: "kind-provider",
		ConsumerClusterName: "kind-consumer",
		ChartPath:           "oci://ghcr.io/kbind-dev/charts/backend",
	}
}

// AddCmdFlags adds command line flags
func (o *DevOptions) AddCmdFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)
	logsv1.AddFlags(o.Logs, cmd.Flags())

	cmd.Flags().StringVar(&o.ProviderClusterName, "provider-cluster-name", "kind-provider", "Name of the provider cluster in dev mode")
	cmd.Flags().StringVar(&o.ConsumerClusterName, "consumer-cluster-name", "kind-consumer", "Name of the consumer cluster in dev mode")
	cmd.Flags().DurationVar(&o.WaitForReadyTimeout, "wait-for-ready-timeout", 2*time.Minute, "Timeout for waiting for the cluster to be ready")
	cmd.Flags().StringVar(&o.ChartPath, "chart-path", o.ChartPath, "Helm chart path or OCI registry URL")
	cmd.Flags().StringVar(&o.ChartVersion, "chart-version", "", "The version of the Helm chart to use")
	cmd.Flags().StringVar(&o.BackendImage, "backend-image", "ghcr.io/kbind-dev/backend", "The name of kube-bind backend to use in dev mode")
	cmd.Flags().StringVar(&o.BackendTag, "backend-tag", "", "The tag of the kube-bind backend image to use in dev mode")
	cmd.Flags().StringVar(&o.KindNetwork, "kind-network", "kube-bind-dev", "The name of the kind network to use in dev mode")
}

// Complete completes the options
func (o *DevOptions) Complete(args []string) error {
	assetVersion := fallbackAssetVersion
	version, err := fetchLatestRelease()
	if err != nil {
		fmt.Fprintf(o.Streams.ErrOut, "Warning: Failed to fetch latest release version: %v. Using fallback version %s\n", err, fallbackAssetVersion)
	} else {
		assetVersion = version
	}

	if o.ChartVersion == "" {
		o.ChartVersion = assetVersion
	}

	if o.BackendTag == "" {
		o.BackendTag = "v" + assetVersion
	}

	return nil
}

// fetchLatestRelease fetches the latest release version from GitHub
func fetchLatestRelease() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/kube-bind/kube-bind/releases/latest", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var release gitHubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return "", fmt.Errorf("failed to parse release data: %w", err)
	}

	if release.TagName == "" {
		return "", fmt.Errorf("no tag name in release data")
	}

	version := strings.TrimPrefix(release.TagName, "v")
	return version, nil
}

// Validate validates the options
func (o *DevOptions) Validate() error {
	return o.Options.Validate()
}

var providerClusterConfig = `apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
networking:
  apiServerAddress: "0.0.0.0"
  apiServerPort: 6443
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 31000
    hostPort: 8080
    protocol: TCP
    listenAddress: "127.0.0.1"
`

var consumerClusterConfig = `apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
nodes:
- role: control-plane
`

// Color helper functions
func blueCommand(text string) string {
	return "\033[38;5;67m" + text + "\033[0m"
}

func redText(text string) string {
	return "\033[31m" + text + "\033[0m"
}

func (o *DevOptions) runWithColors(ctx context.Context) error {
	_ = o.Options.GetConfig()

	// Display experimental warning header with red "EXPERIMENTAL"
	fmt.Fprintf(o.Streams.ErrOut, "kube-bind Development Environment Setup\n\n")
	fmt.Fprintf(o.Streams.ErrOut, "%s kube-bind dev command is in preview\n", redText("EXPERIMENTAL:"))
	fmt.Fprintf(o.Streams.ErrOut, "Requirements: Docker must be installed and running\n\n")

	hostEntryExists := o.setupHostEntries()

	if err := o.checkFileLimits(); err != nil {
		fmt.Fprintf(o.Streams.ErrOut, "⚠️  File limit check warning: %v\n", err)
	}

	if err := o.createCluster(ctx, o.ProviderClusterName, providerClusterConfig, true); err != nil {
		return err
	}

	providerIP, err := o.getClusterIPAddress(ctx, o.ProviderClusterName, o.KindNetwork)
	if err != nil {
		fmt.Fprintf(o.Streams.ErrOut, "⚠️  Failed to get provider cluster IP address: %v\n", err)
		providerIP = ""
	}

	if err := o.createCluster(ctx, o.ConsumerClusterName, consumerClusterConfig, false); err != nil {
		return err
	}

	// Success message
	fmt.Fprint(o.Streams.ErrOut, "kube-bind dev environment is ready!\n\n")

	// Configuration
	fmt.Fprint(o.Streams.ErrOut, "Configuration:\n")
	fmt.Fprintf(o.Streams.ErrOut, "• Provider cluster kubeconfig: %s.kubeconfig\n", o.ProviderClusterName)
	fmt.Fprintf(o.Streams.ErrOut, "• Consumer cluster kubeconfig: %s.kubeconfig\n", o.ConsumerClusterName)
	fmt.Fprint(o.Streams.ErrOut, "• kube-bind server URL: http://kube-bind.dev.local:8080\n\n")

	// Next steps with colored commands
	fmt.Fprint(o.Streams.ErrOut, "Next Steps:\n\n")

	stepNum := 1

	// Only show /etc/hosts step if entry didn't already exist
	if !hostEntryExists {
		fmt.Fprintf(o.Streams.ErrOut, "%d. Add to /etc/hosts (if not already done):\n", stepNum)
		fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand("echo '127.0.0.1 kube-bind.dev.local' | sudo tee -a /etc/hosts"))
		stepNum++
	}

	fmt.Fprintf(o.Streams.ErrOut, "%d. Login to authenticate to the provider cluster:\n", stepNum)
	fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand("kubectl bind login http://kube-bind.dev.local:8080"))
	stepNum++

	fmt.Fprintf(o.Streams.ErrOut, "%d. Bind an API service from provider to consumer:\n", stepNum)
	if providerIP != "" {
		fmt.Fprintf(o.Streams.ErrOut, "%s\n", blueCommand(fmt.Sprintf("KUBECONFIG=%s.kubeconfig kubectl bind --konnector-host-alias %s:kube-bind.dev.local", o.ConsumerClusterName, providerIP)))
	} else {
		fmt.Fprintf(o.Streams.ErrOut, "%s\n", blueCommand(fmt.Sprintf("PROVIDER_IP=$(docker inspect %s-control-plane | jq -r '.[0].NetworkSettings.Networks[\"%s\"].IPAddress') && KUBECONFIG=%s.kubeconfig kubectl bind --konnector-host-alias ${PROVIDER_IP}:kube-bind.dev.local", o.ProviderClusterName, o.KindNetwork, o.ConsumerClusterName)))
	}

	return nil
}

// Run runs the dev command
func (o *DevOptions) Run(ctx context.Context) error {
	return o.runWithColors(ctx)
}

func (o *DevOptions) setupHostEntries() bool {
	if err := addHostEntry("kube-bind.dev.local"); err != nil {
		fmt.Fprintf(o.Streams.ErrOut, "Warning: Could not automatically add host entry. Please run:\n")
		if runtime.GOOS == "windows" {
			fmt.Fprintf(o.Streams.ErrOut, "  echo 127.0.0.1 kube-bind.dev.local >> C:\\Windows\\System32\\drivers\\etc\\hosts\n")
		} else {
			fmt.Fprintf(o.Streams.ErrOut, "  echo '127.0.0.1 kube-bind.dev.local' | sudo tee -a /etc/hosts\n")
		}

		return false
	}

	fmt.Fprint(o.Streams.ErrOut, "Host entry exists for kube-bind.dev.local\n")
	return true
}

func (o *DevOptions) createCluster(ctx context.Context, clusterName, clusterConfig string, installKubeBind bool) error {
	// Set experimental Docker network for kind clusters to communicate
	os.Setenv("KIND_EXPERIMENTAL_DOCKER_NETWORK", o.KindNetwork)

	provider := cluster.NewProvider()

	clusters, err := provider.List()
	if err != nil {
		return err
	}

	kubeconfigPath := fmt.Sprintf("%s.kubeconfig", clusterName)

	if slices.Contains(clusters, clusterName) {
		fmt.Fprint(o.Streams.ErrOut, "Kind cluster "+clusterName+" already exists, skipping creation\n")

		// Export kubeconfig for existing cluster
		err := provider.ExportKubeConfig(clusterName, kubeconfigPath, false)
		if err != nil {
			return fmt.Errorf("failed to export kubeconfig for existing cluster %s: %w", clusterName, err)
		}
	} else {
		fmt.Fprintf(o.Streams.ErrOut, "Creating kind cluster %s with network %s\n", clusterName, o.KindNetwork)
		err := provider.Create(clusterName,
			cluster.CreateWithRawConfig([]byte(clusterConfig)),
			cluster.CreateWithWaitForReady(o.WaitForReadyTimeout),
			cluster.CreateWithDisplaySalutation(true),
			cluster.CreateWithKubeconfigPath(kubeconfigPath),
		)
		if err != nil {
			return err
		}
		fmt.Fprint(o.Streams.ErrOut, "Kind cluster "+clusterName+" created\n")
	}

	if installKubeBind {
		restConfig, err := base.LoadRestConfigFromFile(kubeconfigPath)
		if err != nil {
			return err
		}
		if err := o.installHelmChart(ctx, restConfig); err != nil {
			fmt.Fprint(o.Streams.ErrOut, "Failed to install Helm chart\n")
			return err
		}
		fmt.Fprint(o.Streams.ErrOut, "Helm chart installed successfully\n")
	}

	return nil
}

func (o *DevOptions) getClusterIPAddress(ctx context.Context, clusterName, networkName string) (string, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("failed to create docker client: %w", err)
	}
	defer dockerClient.Close()

	// Get the container name for the kind cluster control plane
	containerName := fmt.Sprintf("%s-control-plane", clusterName)

	containers, err := dockerClient.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list containers: %w", err)
	}

	for _, c := range containers {
		for _, name := range c.Names {
			if strings.Contains(name, containerName) {
				containerDetails, err := dockerClient.ContainerInspect(ctx, c.ID)
				if err != nil {
					return "", fmt.Errorf("failed to inspect container %s: %w", c.ID, err)
				}

				if networks := containerDetails.NetworkSettings.Networks; networks != nil {
					if network, exists := networks[networkName]; exists {
						if network.IPAddress != "" {
							return network.IPAddress, nil
						}
					}
				}
			}
		}
	}

	return "", fmt.Errorf("could not find IP address for cluster %s in network %s", clusterName, networkName)
}

func (o *DevOptions) installHelmChart(_ context.Context, restConfig *rest.Config) error {
	actionConfig := new(action.Configuration)

	if err := actionConfig.Init(&restConfigGetter{config: restConfig}, "kube-bind", "secret", func(format string, v ...any) {}); err != nil {
		return fmt.Errorf("failed to initialize helm action config: %w", err)
	}

	// Initialize registry client for OCI support
	registryClient, regErr := registry.NewClient()
	if regErr != nil {
		return fmt.Errorf("failed to create registry client: %w", regErr)
	}
	actionConfig.RegistryClient = registryClient

	values := map[string]any{
		"image": map[string]any{
			"repository": o.BackendImage,
			"tag":        o.BackendTag,
		},

		"examples": map[string]any{
			"enabled": true,
		},
		"backend": map[string]any{
			"image": map[string]any{
				"repository": o.BackendImage,
				"tag":        o.BackendTag,
			},
			"listenAddress":      "0.0.0.0:8080",
			"namespacePrefix":    "kube-bind-",
			"externalAddress":    "https://kube-bind.dev.local:6443",
			"externalServerName": "kind-provider-control-plane",
			"consumerScope":      "cluster",
			"oidc": map[string]any{
				"callbackUrl": "http://kube-bind.dev.local:8080/api/callback",
				"issuerUrl":   "http://kube-bind.dev.local:8080/oidc",
				"type":        "embedded",
			},
		},
		"service": map[string]any{
			"type":     "NodePort",
			"port":     8080,
			"nodePort": 31000,
		},
		"hostAliases": []map[string]any{
			{
				"ip":        "0.0.0.0",
				"hostnames": []string{"kube-bind.dev.local"},
			},
		},
	}

	var chart *chart.Chart
	var err error

	if strings.HasPrefix(o.ChartPath, "oci://") {
		tempInstallAction := action.NewInstall(actionConfig)
		tempInstallAction.ChartPathOptions.Version = o.ChartVersion
		chartPath, err := tempInstallAction.ChartPathOptions.LocateChart(o.ChartPath, cli.New())
		if err != nil {
			return fmt.Errorf("failed to locate OCI chart: %w", err)
		}
		chart, err = loader.Load(chartPath)
		if err != nil {
			return fmt.Errorf("failed to load OCI chart: %w", err)
		}
	} else {
		chart, err = loader.Load(o.ChartPath)
		if err != nil {
			return fmt.Errorf("failed to load local chart: %w", err)
		}
	}

	histClient := action.NewHistory(actionConfig)
	histClient.Max = 1
	if _, err := histClient.Run("kube-bind"); err == nil {
		upgradeAction := action.NewUpgrade(actionConfig)
		upgradeAction.Namespace = "kube-bind"
		_, err = upgradeAction.Run("kube-bind", chart, values)
		if err != nil {
			return fmt.Errorf("failed to upgrade chart: %w", err)
		}
	} else {
		installAction := action.NewInstall(actionConfig)
		installAction.ReleaseName = "kube-bind"
		installAction.Namespace = "kube-bind"
		installAction.CreateNamespace = true
		_, err = installAction.Run(chart, values)
		if err != nil {
			return fmt.Errorf("failed to install chart: %w", err)
		}
	}

	return nil
}

type restConfigGetter struct {
	config *rest.Config
}

func (r *restConfigGetter) ToRESTConfig() (*rest.Config, error) {
	return r.config, nil
}

func (r *restConfigGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(r.config)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(discoveryClient), nil
}

func (r *restConfigGetter) ToRESTMapper() (meta.RESTMapper, error) {
	discoveryClient, err := r.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient)
	return mapper, nil
}

func (r *restConfigGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return clientcmd.NewNonInteractiveClientConfig(clientcmdapi.Config{}, "", &clientcmd.ConfigOverrides{
		Context: clientcmdapi.Context{
			Namespace: "kube-bind",
		},
	}, nil)
}

func getHostsPath() string {
	if runtime.GOOS == "windows" {
		return `C:\Windows\System32\drivers\etc\hosts`
	}
	return "/etc/hosts"
}

func addHostEntry(hostname string) error {
	hostsPath := getHostsPath()
	entry := fmt.Sprintf("127.0.0.1 %s", hostname)

	exists, err := hostEntryExists(hostsPath, hostname)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	file, err := os.OpenFile(hostsPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open hosts file: %w", err)
	}
	defer file.Close()

	if _, err := fmt.Fprintf(file, "\n%s\n", entry); err != nil {
		return fmt.Errorf("failed to write to hosts file: %w", err)
	}

	return nil
}

func removeHostEntry(hostname string) error {
	hostsPath := getHostsPath()

	file, err := os.Open(hostsPath)
	if err != nil {
		return fmt.Errorf("failed to open hosts file: %w", err)
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, hostname) || !strings.Contains(line, "127.0.0.1") {
			lines = append(lines, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read hosts file: %w", err)
	}

	content := strings.Join(lines, "\n")
	if err := os.WriteFile(hostsPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write hosts file: %w", err)
	}

	return nil
}

func hostEntryExists(hostsPath, hostname string) (bool, error) {
	file, err := os.Open(hostsPath)
	if err != nil {
		return false, fmt.Errorf("failed to open hosts file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, hostname) && strings.Contains(line, "127.0.0.1") {
			return true, nil
		}
	}

	return false, scanner.Err()
}

// CheckFileLimits checks system file limits for the dev environment
func (o *DevOptions) CheckFileLimits() error {
	return o.checkFileLimits()
}

// GetClusterIPAddress gets the IP address of a cluster in the specified network
func (o *DevOptions) GetClusterIPAddress(ctx context.Context, clusterName, networkName string) (string, error) {
	return o.getClusterIPAddress(ctx, clusterName, networkName)
}

// CreateProviderCluster creates the provider cluster
func (o *DevOptions) CreateProviderCluster(ctx context.Context) error {
	return o.createCluster(ctx, o.ProviderClusterName, providerClusterConfig, true)
}

// CreateConsumerCluster creates the consumer cluster
func (o *DevOptions) CreateConsumerCluster(ctx context.Context) error {
	return o.createCluster(ctx, o.ConsumerClusterName, consumerClusterConfig, false)
}

// GetProviderClusterName returns the provider cluster name
func (o *DevOptions) GetProviderClusterName() string {
	return o.ProviderClusterName
}

// GetConsumerClusterName returns the consumer cluster name
func (o *DevOptions) GetConsumerClusterName() string {
	return o.ConsumerClusterName
}

func (o *DevOptions) checkFileLimits() error {
	// Only check on Linux systems
	if runtime.GOOS != "linux" {
		return nil
	}

	// Check fs.inotify.max_user_watches
	watchesCmd := exec.CommandContext(context.Background(), "sysctl", "-n", "fs.inotify.max_user_watches")
	watchesOutput, err := watchesCmd.Output()
	if err == nil {
		if watches, err := strconv.Atoi(strings.TrimSpace(string(watchesOutput))); err == nil && watches < 524288 {
			fmt.Fprintf(o.Streams.ErrOut, "⚠️  fs.inotify.max_user_watches is %d (recommended: 524288)\n", watches)
			fmt.Fprintf(o.Streams.ErrOut, "💡 To increase: sudo sysctl fs.inotify.max_user_watches=524288\n")
		}
	}

	// Check fs.inotify.max_user_instances
	instancesCmd := exec.CommandContext(context.Background(), "sysctl", "-n", "fs.inotify.max_user_instances")
	instancesOutput, err := instancesCmd.Output()
	if err == nil {
		if instances, err := strconv.Atoi(strings.TrimSpace(string(instancesOutput))); err == nil && instances < 512 {
			fmt.Fprintf(o.Streams.ErrOut, "⚠️  fs.inotify.max_user_instances is %d (recommended: 512)\n", instances)
			fmt.Fprintf(o.Streams.ErrOut, "💡 To increase: sudo sysctl fs.inotify.max_user_instances=512\n")
		}
	}

	return nil
}
