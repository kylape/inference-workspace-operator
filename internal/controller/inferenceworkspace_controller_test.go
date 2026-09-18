package controller

import (
	"context"
	"testing"

	workspacev1alpha1 "github.com/kylape/inference-workspace-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestReconcileNamespaceWorkspace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := testScheme(t)
	workspace := &workspacev1alpha1.InferenceWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "example", UID: types.UID("workspace-uid")},
		Spec: workspacev1alpha1.InferenceWorkspaceSpec{Subjects: []workspacev1alpha1.WorkspaceSubject{
			{Kind: "User", Name: "alice"},
			{Kind: "ServiceAccount", Name: "runner", Namespace: "ci"},
		}},
	}
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workspacev1alpha1.InferenceWorkspace{}).
		WithObjects(workspace).
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
	binding := &rbacv1.RoleBinding{}
	if err := client.Get(ctx, types.NamespacedName{Name: AccessBindingName, Namespace: namespace.Name}, binding); err != nil {
		t.Fatalf("workspace RoleBinding: %v", err)
	}
	if len(binding.Subjects) != 2 || binding.Subjects[0].APIGroup != rbacv1.GroupName || binding.Subjects[1].Namespace != "ci" {
		t.Fatalf("unexpected subjects: %#v", binding.Subjects)
	}

	queue := &kueuev1beta2.LocalQueue{}
	if err := client.Get(ctx, types.NamespacedName{Name: LocalQueueName, Namespace: namespace.Name}, queue); err != nil {
		t.Fatalf("default LocalQueue: %v", err)
	}
	if queue.Spec.ClusterQueue != kueuev1beta2.ClusterQueueReference(ClusterQueueName) {
		t.Fatalf("LocalQueue references %q, want %q", queue.Spec.ClusterQueue, ClusterQueueName)
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
		Spec: workspacev1alpha1.InferenceWorkspaceSpec{Subjects: []workspacev1alpha1.WorkspaceSubject{
			{Kind: "User", Name: "alice"},
		}},
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

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
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
