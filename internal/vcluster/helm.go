package vcluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

const (
	DefaultHelmPath  = "/helm"
	DefaultChartPath = "/charts/vcluster-0.0.1-combined.4.tgz"
)

type Installer interface {
	Ensure(context.Context, string, string, bool) error
}

type HelmInstaller struct {
	HelmPath  string
	ChartPath string
}

type helmRelease struct {
	Name string `json:"name"`
}

func (h HelmInstaller) Ensure(ctx context.Context, name, namespace string, openShift bool) error {
	helmPath := h.HelmPath
	if helmPath == "" {
		helmPath = DefaultHelmPath
	}
	chartPath := h.ChartPath
	if chartPath == "" {
		chartPath = DefaultChartPath
	}

	list, err := h.run(ctx, helmPath, "list", "--namespace", namespace, "--filter", "^"+name+"$", "--output", "json")
	if err != nil {
		return fmt.Errorf("list vCluster Helm releases: %w", err)
	}
	var releases []helmRelease
	if err := json.Unmarshal(list, &releases); err != nil {
		return fmt.Errorf("decode Helm release list: %w", err)
	}
	if len(releases) > 0 {
		return nil
	}

	if _, err := h.run(ctx, helmPath, installArgs(name, namespace, chartPath, openShift)...); err != nil {
		return fmt.Errorf("install vCluster Helm release: %w", err)
	}
	return nil
}

func installArgs(name, namespace, chartPath string, openShift bool) []string {
	args := []string{
		"upgrade", "--install", name, chartPath,
		"--namespace", namespace,
		"--set-string", "controlPlane.statefulSet.image.registry=quay.io",
		"--set-string", "controlPlane.statefulSet.image.repository=klape/vcluster",
		"--set-string", "controlPlane.statefulSet.image.tag=csi-capacity-debug-v26",
		"--set", "rbac.clusterRole.enabled=false",
		"--set", "sync.fromHost.csiStorageCapacities.enabled=true",
		"--set", "sync.fromHost.storageClasses.enabled=true",
		"--set", "sync.fromHost.csiNodes.enabled=false",
		"--set", "sync.fromHost.csiDrivers.enabled=false",
	}
	if openShift {
		args = append(args, "--set-string", "controlPlane.statefulSet.security.profile=restricted")
	}
	return args
}

func (h HelmInstaller) run(ctx context.Context, helmPath string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, helmPath, args...)
	command.Env = append(os.Environ(),
		"HELM_CACHE_HOME=/tmp/helm/cache",
		"HELM_CONFIG_HOME=/tmp/helm/config",
		"HELM_DATA_HOME=/tmp/helm/data",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}
