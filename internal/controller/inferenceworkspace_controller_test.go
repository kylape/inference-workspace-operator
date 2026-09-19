package controller

import (
	"context"
	"os"
	"testing"

	workspacev1alpha1 "github.com/kylape/inference-workspace-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/yaml"
)

func TestReconcileNamespaceWorkspace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := testScheme(t)
	workspace := &workspacev1alpha1.InferenceWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "example", UID: types.UID("workspace-uid")},
		Spec:       workspacev1alpha1.InferenceWorkspaceSpec{ClusterQueue: "development"},
	}
	clusterQueue := &kueuev1beta2.ClusterQueue{ObjectMeta: metav1.ObjectMeta{Name: "development"}}
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workspacev1alpha1.InferenceWorkspace{}).
		WithObjects(workspace, clusterQueue).
		Build()
	reconciler := &InferenceWorkspaceReconciler{Client: client, Scheme: scheme}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: workspace.Name}}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("adding finalizer: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("creating workspace resources: %v", err)
	}

	namespace := &corev1.Namespace{}
	if err := client.Get(ctx, types.NamespacedName{Name: "workspace-example"}, namespace); err != nil {
		t.Fatalf("workspace namespace: %v", err)
	}
	if namespace.Labels[KueueManagedLabel] != "true" {
		t.Fatalf("workspace namespace is not labeled for Kueue management: %#v", namespace.Labels)
	}
	queue := &kueuev1beta2.LocalQueue{}
	if err := client.Get(ctx, types.NamespacedName{Name: LocalQueueName, Namespace: namespace.Name}, queue); err != nil {
		t.Fatalf("default LocalQueue: %v", err)
	}
	if queue.Spec.ClusterQueue != "development" {
		t.Fatalf("LocalQueue references %q, want %q", queue.Spec.ClusterQueue, "development")
	}

	current := &workspacev1alpha1.InferenceWorkspace{}
	if err := client.Get(ctx, request.NamespacedName, current); err != nil {
		t.Fatalf("workspace status: %v", err)
	}
	ready := apimeta.FindStatusCondition(current.Status.Conditions, workspacev1alpha1.ReadyCondition)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "QueueNotReady" {
		t.Fatalf("unexpected Ready condition: %#v", ready)
	}
}

func TestNamespaceCollision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := testScheme(t)
	workspace := &workspacev1alpha1.InferenceWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "collision",
			UID:        types.UID("workspace-uid"),
			Finalizers: []string{FinalizerName},
		},
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "workspace-collision"}}
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workspacev1alpha1.InferenceWorkspace{}).
		WithObjects(workspace, namespace).
		Build()
	reconciler := &InferenceWorkspaceReconciler{Client: client, Scheme: scheme}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: workspace.Name}}); err != nil {
		t.Fatalf("reconcile collision: %v", err)
	}
	current := &workspacev1alpha1.InferenceWorkspace{}
	if err := client.Get(ctx, types.NamespacedName{Name: workspace.Name}, current); err != nil {
		t.Fatalf("workspace status: %v", err)
	}
	ready := apimeta.FindStatusCondition(current.Status.Conditions, workspacev1alpha1.ReadyCondition)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "NamespaceCollision" {
		t.Fatalf("unexpected Ready condition: %#v", ready)
	}
}

func TestLocalQueueActive(t *testing.T) {
	t.Parallel()
	queue := &kueuev1beta2.LocalQueue{Status: kueuev1beta2.LocalQueueStatus{
		Conditions: []metav1.Condition{{Type: kueuev1beta2.LocalQueueActive, Status: metav1.ConditionTrue}},
	}}
	if !localQueueActive(queue) {
		t.Fatal("expected LocalQueue to be active")
	}
}

func TestClusterQueueDefaults(t *testing.T) {
	t.Parallel()
	workspace := &workspacev1alpha1.InferenceWorkspace{}
	if got := requestedClusterQueue(workspace); got != kueuev1beta2.ClusterQueueReference(DefaultClusterQueueName) {
		t.Fatalf("default ClusterQueue is %q, want %q", got, DefaultClusterQueueName)
	}
}

type recordingVClusterInstaller struct {
	openShift bool
	calls     int
}

func (i *recordingVClusterInstaller) Ensure(_ context.Context, _, _ string, openShift bool) error {
	i.openShift = openShift
	i.calls++
	return nil
}

func TestEnsureVClusterUsesDetectedPlatformAndScopedBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := testScheme(t)
	workspace := &workspacev1alpha1.InferenceWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "example", UID: types.UID("workspace-uid")},
		Spec:       workspacev1alpha1.InferenceWorkspaceSpec{Mode: workspacev1alpha1.WorkspaceModeVCluster},
	}
	namespace := "workspace-example"
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: workspace.Name, Namespace: namespace},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "vc-example", Namespace: namespace}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workspace, statefulSet, secret).Build()
	installer := &recordingVClusterInstaller{}
	reconciler := &InferenceWorkspaceReconciler{
		Client:            client,
		Scheme:            scheme,
		VClusterInstaller: installer,
		OpenShift:         true,
	}

	secretRef, ready, err := reconciler.ensureVCluster(ctx, workspace, namespace)
	if err != nil {
		t.Fatal(err)
	}
	if !ready || secretRef == nil || secretRef.Name != "vc-example" {
		t.Fatalf("unexpected vCluster readiness: ready=%v secretRef=%#v", ready, secretRef)
	}
	if installer.calls != 1 || !installer.openShift {
		t.Fatalf("installer did not receive OpenShift detection: %#v", installer)
	}
	binding := &rbacv1.ClusterRoleBinding{}
	if err := client.Get(ctx, types.NamespacedName{Name: "example-vcluster"}, binding); err != nil {
		t.Fatal(err)
	}
	if binding.RoleRef.Name != VClusterRoleName || len(binding.Subjects) != 1 ||
		binding.Subjects[0].Name != "vc-example" || binding.Subjects[0].Namespace != namespace {
		t.Fatalf("unexpected vCluster binding: %#v", binding)
	}
}

func TestWorkspaceRoleCannotMutateLocalQueues(t *testing.T) {
	t.Parallel()
	manifest, err := os.ReadFile("../../config/rbac/workspace_role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	role := &rbacv1.ClusterRole{}
	if err := yaml.Unmarshal(manifest, role); err != nil {
		t.Fatal(err)
	}
	if role.Name != WorkspaceRoleName {
		t.Fatalf("workspace role is named %q, want %q", role.Name, WorkspaceRoleName)
	}

	foundLocalQueueRule := false
	for _, rule := range role.Rules {
		matchesAPIGroup := contains(rule.APIGroups, kueuev1beta2.GroupVersion.Group) || contains(rule.APIGroups, "*")
		matchesResource := contains(rule.Resources, "localqueues") || contains(rule.Resources, "*")
		if !matchesAPIGroup || !matchesResource {
			continue
		}
		foundLocalQueueRule = true
		for _, verb := range rule.Verbs {
			if verb != "get" && verb != "list" && verb != "watch" {
				t.Fatalf("workspace role grants LocalQueue mutation through verb %q", verb)
			}
		}
	}
	if !foundLocalQueueRule {
		t.Fatal("workspace role should grant read-only LocalQueue visibility")
	}
}

func TestVClusterRoleOnlyReadsRequiredStorageDiscovery(t *testing.T) {
	t.Parallel()
	manifest, err := os.ReadFile("../../config/rbac/vcluster_role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	role := &rbacv1.ClusterRole{}
	if err := yaml.Unmarshal(manifest, role); err != nil {
		t.Fatal(err)
	}
	if role.Name != VClusterRoleName || len(role.Rules) != 1 {
		t.Fatalf("unexpected vCluster role: %#v", role)
	}
	rule := role.Rules[0]
	if len(rule.APIGroups) != 1 || rule.APIGroups[0] != "storage.k8s.io" ||
		len(rule.Resources) != 2 || !contains(rule.Resources, "csistoragecapacities") ||
		!contains(rule.Resources, "storageclasses") {
		t.Fatalf("unexpected vCluster role resources: %#v", rule)
	}
	for _, verb := range rule.Verbs {
		if verb != "get" && verb != "list" && verb != "watch" {
			t.Fatalf("vCluster role grants mutating verb %q", verb)
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

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := workspacev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kueuev1beta2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}
