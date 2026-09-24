package controller

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	workspacev1alpha1 "github.com/kylape/inference-workspace-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "requests", UID: types.UID("workspace-uid")},
		Spec: workspacev1alpha1.InferenceWorkspaceSpec{
			Access:       testAccess("access", "alice"),
			ClusterQueue: "development",
		},
	}
	clusterQueue := &kueuev1beta2.ClusterQueue{ObjectMeta: metav1.ObjectMeta{Name: "development"}}
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "access"}}
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workspacev1alpha1.InferenceWorkspace{}).
		WithObjects(workspace, clusterQueue, serviceAccount).
		Build()
	reconciler := &InferenceWorkspaceReconciler{Client: client, Scheme: scheme}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: workspace.Name, Namespace: workspace.Namespace}}

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
	if !workspaceOwns(namespace, workspace) || len(namespace.OwnerReferences) != 0 {
		t.Fatalf("workspace namespace has unexpected ownership metadata: annotations=%#v owners=%#v", namespace.Annotations, namespace.OwnerReferences)
	}
	binding := &rbacv1.RoleBinding{}
	if err := client.Get(ctx, types.NamespacedName{Name: AccessBindingName, Namespace: namespace.Name}, binding); err != nil {
		t.Fatalf("workspace access RoleBinding: %v", err)
	}
	if binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != WorkspaceRoleName ||
		len(binding.Subjects) != 2 || binding.Subjects[1].Name != "alice" || binding.Subjects[1].Namespace != "access" ||
		binding.Subjects[0].Name != NamespaceKubeconfigServiceAccountName || binding.Subjects[0].Namespace != namespace.Name {
		t.Fatalf("unexpected workspace access binding: %#v", binding)
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
			Namespace:  "requests",
			UID:        types.UID("workspace-uid"),
			Finalizers: []string{FinalizerName},
		},
		Spec: workspacev1alpha1.InferenceWorkspaceSpec{Access: testAccess("access", "alice")},
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "workspace-collision"}}
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workspacev1alpha1.InferenceWorkspace{}).
		WithObjects(workspace, namespace).
		Build()
	reconciler := &InferenceWorkspaceReconciler{Client: client, Scheme: scheme}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: workspace.Name, Namespace: workspace.Namespace}}); err != nil {
		t.Fatalf("reconcile collision: %v", err)
	}
	current := &workspacev1alpha1.InferenceWorkspace{}
	if err := client.Get(ctx, types.NamespacedName{Name: workspace.Name, Namespace: workspace.Namespace}, current); err != nil {
		t.Fatalf("workspace status: %v", err)
	}
	ready := apimeta.FindStatusCondition(current.Status.Conditions, workspacev1alpha1.ReadyCondition)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "NamespaceCollision" {
		t.Fatalf("unexpected Ready condition: %#v", ready)
	}
}

func TestMissingAccessSubjectIsReported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := testScheme(t)
	workspace := &workspacev1alpha1.InferenceWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "requests", UID: types.UID("workspace-uid"), Finalizers: []string{FinalizerName}},
		Spec:       workspacev1alpha1.InferenceWorkspaceSpec{Access: testAccess("access", "missing")},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&workspacev1alpha1.InferenceWorkspace{}).
		WithObjects(workspace).Build()
	reconciler := &InferenceWorkspaceReconciler{Client: client, Scheme: scheme}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: workspace.Name, Namespace: workspace.Namespace}}); err != nil {
		t.Fatal(err)
	}
	current := &workspacev1alpha1.InferenceWorkspace{}
	if err := client.Get(ctx, types.NamespacedName{Name: workspace.Name, Namespace: workspace.Namespace}, current); err != nil {
		t.Fatal(err)
	}
	ready := apimeta.FindStatusCondition(current.Status.Conditions, workspacev1alpha1.ReadyCondition)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "AccessSubjectNotFound" {
		t.Fatalf("unexpected Ready condition: %#v", ready)
	}
}

func TestMissingReplacementSubjectRevokesPreviousAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := testScheme(t)
	workspace := &workspacev1alpha1.InferenceWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "revoke", Namespace: "requests", UID: types.UID("workspace-uid")},
		Spec:       workspacev1alpha1.InferenceWorkspaceSpec{Access: testAccess("access", "alice")},
	}
	alice := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "access"}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workspace, alice).Build()
	reconciler := &InferenceWorkspaceReconciler{Client: client, Scheme: scheme}
	const namespace = "workspace-revoke"

	if err := reconciler.ensureAccess(ctx, workspace, namespace); err != nil {
		t.Fatal(err)
	}
	workspace.Spec.Access = testAccess("access", "missing")
	err := reconciler.ensureAccess(ctx, workspace, namespace)
	if !errors.Is(err, ErrAccessSubjectNotFound) {
		t.Fatalf("ensureAccess() error = %v, want ErrAccessSubjectNotFound", err)
	}
	binding := &rbacv1.RoleBinding{}
	if err := client.Get(ctx, types.NamespacedName{Name: AccessBindingName, Namespace: namespace}, binding); err != nil {
		t.Fatal(err)
	}
	if len(binding.Subjects) != 1 || binding.Subjects[0].Name != NamespaceKubeconfigServiceAccountName {
		t.Fatalf("removed subject retained access: %#v", binding.Subjects)
	}
}

func TestEnsureNamespaceKubeconfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := testScheme(t)
	workspace := &workspacev1alpha1.InferenceWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "requests", UID: types.UID("workspace-uid")},
	}
	namespace := "workspace-example"
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        NamespaceKubeconfigTokenSecretName,
			Namespace:   namespace,
			Annotations: workspaceOwnershipAnnotations(workspace),
		},
		Data: map[string][]byte{
			corev1.ServiceAccountTokenKey:  []byte("token-value"),
			corev1.ServiceAccountRootCAKey: []byte("ca-value"),
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workspace, tokenSecret).Build()
	reconciler := &InferenceWorkspaceReconciler{Client: client, Scheme: scheme, APIServerURL: "https://api.example.test/"}

	secretRef, ready, err := reconciler.ensureNamespaceKubeconfig(ctx, workspace, namespace)
	if err != nil {
		t.Fatal(err)
	}
	if !ready || secretRef == nil || secretRef.Name != NamespaceKubeconfigSecretName || secretRef.Namespace != namespace {
		t.Fatalf("unexpected namespace kubeconfig result: ready=%v ref=%#v", ready, secretRef)
	}
	kubeconfig := &corev1.Secret{}
	if err := client.Get(ctx, types.NamespacedName{Name: NamespaceKubeconfigSecretName, Namespace: namespace}, kubeconfig); err != nil {
		t.Fatal(err)
	}
	contents := string(kubeconfig.Data["config"])
	for _, expected := range []string{"https://api.example.test", "token-value", "Y2EtdmFsdWU="} {
		if !strings.Contains(contents, expected) {
			t.Fatalf("kubeconfig does not contain %q: %s", expected, contents)
		}
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
	openShift  bool
	publicHost string
	calls      int
}

func (i *recordingVClusterInstaller) Ensure(_ context.Context, _, _ string, openShift bool, publicHost string) error {
	i.openShift = openShift
	i.publicHost = publicHost
	i.calls++
	return nil
}

func TestEnsureVClusterUsesDetectedPlatformAndScopedBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := testScheme(t)
	workspace := &workspacev1alpha1.InferenceWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "requests", UID: types.UID("workspace-uid")},
		Spec:       workspacev1alpha1.InferenceWorkspaceSpec{Mode: workspacev1alpha1.WorkspaceModeVCluster},
	}
	namespace := "workspace-example"
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: workspace.Name, Namespace: namespace},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vc-example", Namespace: namespace},
		Data:       map[string][]byte{"config": []byte("apiVersion: v1\nclusters:\n- cluster:\n    certificate-authority-data: Y2E=\n    server: https://vc-example.workspace-example.svc\n  name: vcluster\n")},
	}
	ingress := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "config.openshift.io/v1",
		"kind":       "Ingress",
		"metadata":   map[string]interface{}{"name": "cluster"},
		"spec":       map[string]interface{}{"domain": "apps.example.test"},
	}}
	ingress.SetGroupVersionKind(schema.GroupVersionKind{Group: "config.openshift.io", Version: "v1", Kind: "Ingress"})
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workspace, statefulSet, secret, ingress).Build()
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
	if !ready || secretRef == nil || secretRef.Name != "vc-example-external" {
		t.Fatalf("unexpected vCluster readiness: ready=%v secretRef=%#v", ready, secretRef)
	}
	if installer.calls != 1 || !installer.openShift || installer.publicHost != "example.apps.example.test" {
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
	if !workspaceOwns(binding, workspace) || len(binding.OwnerReferences) != 0 {
		t.Fatalf("vCluster binding has unexpected ownership metadata: annotations=%#v owners=%#v", binding.Annotations, binding.OwnerReferences)
	}
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(openShiftRouteGVK)
	if err := client.Get(ctx, types.NamespacedName{Name: "example", Namespace: namespace}, route); err != nil {
		t.Fatalf("vCluster Route: %v", err)
	}
	if host, _, _ := unstructured.NestedString(route.Object, "spec", "host"); host != "example.apps.example.test" {
		t.Fatalf("Route host = %q, want %q", host, "example.apps.example.test")
	}
	if termination, _, _ := unstructured.NestedString(route.Object, "spec", "tls", "termination"); termination != "passthrough" {
		t.Fatalf("Route TLS termination = %q, want passthrough", termination)
	}
	publicSecret := &corev1.Secret{}
	if err := client.Get(ctx, types.NamespacedName{Name: "vc-example-external", Namespace: namespace}, publicSecret); err != nil {
		t.Fatalf("public kubeconfig Secret: %v", err)
	}
	if !containsString(string(publicSecret.Data["config"]), "https://example.apps.example.test") {
		t.Fatalf("public kubeconfig server was not rewritten: %s", publicSecret.Data["config"])
	}
}

func TestEnsureVClusterAccessOnlyReadsKubeconfigSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := testScheme(t)
	workspace := &workspacev1alpha1.InferenceWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "requests", UID: types.UID("workspace-uid")},
		Spec: workspacev1alpha1.InferenceWorkspaceSpec{
			Access: testAccess("access", "alice"),
			Mode:   workspacev1alpha1.WorkspaceModeVCluster,
		},
	}
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "access"}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workspace, serviceAccount).Build()
	reconciler := &InferenceWorkspaceReconciler{Client: client, Scheme: scheme}
	namespace := "workspace-example"

	if err := reconciler.ensureAccess(ctx, workspace, namespace); err != nil {
		t.Fatal(err)
	}
	role := &rbacv1.Role{}
	if err := client.Get(ctx, types.NamespacedName{Name: VClusterAccessRoleName, Namespace: namespace}, role); err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) != 1 || len(role.Rules[0].Resources) != 1 || role.Rules[0].Resources[0] != "secrets" ||
		len(role.Rules[0].ResourceNames) != 1 || role.Rules[0].ResourceNames[0] != "vc-example" ||
		len(role.Rules[0].Verbs) != 1 || role.Rules[0].Verbs[0] != "get" {
		t.Fatalf("unexpected vCluster access role: %#v", role.Rules)
	}
	binding := &rbacv1.RoleBinding{}
	if err := client.Get(ctx, types.NamespacedName{Name: AccessBindingName, Namespace: namespace}, binding); err != nil {
		t.Fatal(err)
	}
	if binding.RoleRef.Kind != "Role" || binding.RoleRef.Name != VClusterAccessRoleName {
		t.Fatalf("unexpected vCluster access binding: %#v", binding.RoleRef)
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

func containsString(value, target string) bool {
	return strings.Contains(value, target)
}

func testAccess(namespace, name string) workspacev1alpha1.WorkspaceAccess {
	return workspacev1alpha1.WorkspaceAccess{Subjects: []workspacev1alpha1.WorkspaceSubject{{
		Kind: "ServiceAccount", Namespace: namespace, Name: name,
	}}}
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
