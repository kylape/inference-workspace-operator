package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	workspacev1alpha1 "github.com/kylape/inference-workspace-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const (
	FinalizerName     = "inference.redhat.com/workspace-cleanup"
	NamespacePrefix   = "workspace-"
	AccessBindingName = "workspace-access"
	WorkspaceRoleName = "inference-workspace-user"
	LocalQueueName    = "default"
	ClusterQueueName  = "inference-workspaces"
)

var ErrNamespaceCollision = errors.New("workspace namespace already exists and is not controlled by this workspace")

type InferenceWorkspaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *InferenceWorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	workspace := &workspacev1alpha1.InferenceWorkspace{}
	if err := r.Get(ctx, req.NamespacedName, workspace); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	namespaceName := NamespacePrefix + workspace.Name
	if workspace.DeletionTimestamp != nil {
		return r.finalize(ctx, workspace, namespaceName)
	}

	if !controllerutil.ContainsFinalizer(workspace, FinalizerName) {
		controllerutil.AddFinalizer(workspace, FinalizerName)
		if err := r.Update(ctx, workspace); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if err := r.ensureNamespace(ctx, workspace, namespaceName); err != nil {
		if errors.Is(err, ErrNamespaceCollision) {
			return r.notReady(ctx, workspace, namespaceName, "NamespaceCollision", err.Error(), 30*time.Second, nil)
		}
		return r.notReady(ctx, workspace, namespaceName, "ReconciliationFailed", err.Error(), 0, err)
	}

	if err := r.ensureAccess(ctx, workspace, namespaceName); err != nil {
		return r.notReady(ctx, workspace, namespaceName, "ReconciliationFailed", err.Error(), 0, err)
	}

	queue, err := r.ensureLocalQueue(ctx, workspace, namespaceName)
	if err != nil {
		return r.notReady(ctx, workspace, namespaceName, "ReconciliationFailed", err.Error(), 0, err)
	}
	if !localQueueActive(queue) {
		return r.notReady(ctx, workspace, namespaceName, "QueueNotReady", "Default LocalQueue is waiting for its ClusterQueue", 5*time.Second, nil)
	}

	if err := r.setReady(ctx, workspace, namespaceName, metav1.ConditionTrue, "Ready", "Workspace is ready"); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *InferenceWorkspaceReconciler) ensureNamespace(
	ctx context.Context,
	workspace *workspacev1alpha1.InferenceWorkspace,
	name string,
) error {
	namespace := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: name}, namespace)
	if apierrors.IsNotFound(err) {
		namespace = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by":   "inference-workspace-operator",
					"inference.redhat.com/workspace": workspace.Name,
				},
			},
		}
		if err := controllerutil.SetControllerReference(workspace, namespace, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, namespace)
	}
	if err != nil {
		return err
	}
	if !metav1.IsControlledBy(namespace, workspace) {
		return fmt.Errorf("%w: %s", ErrNamespaceCollision, name)
	}
	return nil
}

func (r *InferenceWorkspaceReconciler) ensureAccess(
	ctx context.Context,
	workspace *workspacev1alpha1.InferenceWorkspace,
	namespace string,
) error {
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: AccessBindingName, Namespace: namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, binding, func() error {
		binding.RoleRef = rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     WorkspaceRoleName,
		}
		binding.Subjects = make([]rbacv1.Subject, 0, len(workspace.Spec.Subjects))
		for _, subject := range workspace.Spec.Subjects {
			rbacSubject := rbacv1.Subject{Kind: subject.Kind, Name: subject.Name}
			if subject.Kind == "User" {
				rbacSubject.APIGroup = rbacv1.GroupName
			} else {
				rbacSubject.Namespace = subject.Namespace
			}
			binding.Subjects = append(binding.Subjects, rbacSubject)
		}
		return controllerutil.SetControllerReference(workspace, binding, r.Scheme)
	})
	return err
}

func (r *InferenceWorkspaceReconciler) ensureLocalQueue(
	ctx context.Context,
	workspace *workspacev1alpha1.InferenceWorkspace,
	namespace string,
) (*kueuev1beta2.LocalQueue, error) {
	queue := &kueuev1beta2.LocalQueue{
		ObjectMeta: metav1.ObjectMeta{Name: LocalQueueName, Namespace: namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, queue, func() error {
		queue.Spec.ClusterQueue = kueuev1beta2.ClusterQueueReference(ClusterQueueName)
		return controllerutil.SetControllerReference(workspace, queue, r.Scheme)
	})
	return queue, err
}

func localQueueActive(queue *kueuev1beta2.LocalQueue) bool {
	return apimeta.IsStatusConditionTrue(queue.Status.Conditions, kueuev1beta2.LocalQueueActive)
}

func (r *InferenceWorkspaceReconciler) notReady(
	ctx context.Context,
	workspace *workspacev1alpha1.InferenceWorkspace,
	namespaceName string,
	reason string,
	message string,
	requeueAfter time.Duration,
	reconcileErr error,
) (ctrl.Result, error) {
	if err := r.setReady(ctx, workspace, namespaceName, metav1.ConditionFalse, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, reconcileErr
}

func (r *InferenceWorkspaceReconciler) setReady(
	ctx context.Context,
	workspace *workspacev1alpha1.InferenceWorkspace,
	namespaceName string,
	status metav1.ConditionStatus,
	reason string,
	message string,
) error {
	before := workspace.DeepCopy()
	workspace.Status.NamespaceRef = &corev1.LocalObjectReference{Name: namespaceName}
	apimeta.SetStatusCondition(&workspace.Status.Conditions, metav1.Condition{
		Type:               workspacev1alpha1.ReadyCondition,
		Status:             status,
		ObservedGeneration: workspace.Generation,
		Reason:             reason,
		Message:            message,
	})
	return r.Status().Patch(ctx, workspace, client.MergeFrom(before))
}

func (r *InferenceWorkspaceReconciler) finalize(
	ctx context.Context,
	workspace *workspacev1alpha1.InferenceWorkspace,
	namespaceName string,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(workspace, FinalizerName) {
		return ctrl.Result{}, nil
	}

	namespace := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace)
	if err == nil && metav1.IsControlledBy(namespace, workspace) {
		if namespace.DeletionTimestamp == nil {
			if err := r.Delete(ctx, namespace); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(workspace, FinalizerName)
	return ctrl.Result{}, r.Update(ctx, workspace)
}

func (r *InferenceWorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&workspacev1alpha1.InferenceWorkspace{}).
		Owns(&corev1.Namespace{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&kueuev1beta2.LocalQueue{}).
		Complete(r)
}
