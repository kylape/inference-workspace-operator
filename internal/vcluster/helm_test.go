package vcluster

import "testing"

func TestInstallArgsSelectRestrictedProfileOnlyForOpenShift(t *testing.T) {
	t.Parallel()
	const profile = "controlPlane.statefulSet.security.profile=restricted"

	openShiftArgs := installArgs("test", "workspace-test", "chart.tgz", true)
	if !contains(openShiftArgs, profile) {
		t.Fatalf("OpenShift install args do not select restricted profile: %v", openShiftArgs)
	}

	kubernetesArgs := installArgs("test", "workspace-test", "chart.tgz", false)
	if contains(kubernetesArgs, profile) {
		t.Fatalf("Kubernetes install args unexpectedly select restricted profile: %v", kubernetesArgs)
	}
}

func TestInstallArgsLimitHostStorageSync(t *testing.T) {
	t.Parallel()
	args := installArgs("test", "workspace-test", "chart.tgz", false)
	for _, value := range []string{
		"rbac.clusterRole.enabled=false",
		"sync.fromHost.csiStorageCapacities.enabled=true",
		"sync.fromHost.storageClasses.enabled=true",
		"sync.fromHost.csiNodes.enabled=false",
		"sync.fromHost.csiDrivers.enabled=false",
	} {
		if !contains(args, value) {
			t.Fatalf("install args do not contain %q: %v", value, args)
		}
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
