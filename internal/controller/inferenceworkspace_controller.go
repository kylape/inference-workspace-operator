package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	workspacev1alpha1 "github.com/kylape/inference-workspace-operator/api/v1alpha1"
	workspacevcluster "github.com/kylape/inference-workspace-operator/internal/vcluster"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const (
	FinalizerName           = "inference.redhat.com/workspace-cleanup"
	NamespacePrefix         = "workspace-"
	AccessBindingName       = "workspace-access"
	VClusterAccessRoleName  = "workspace-kubeconfig-reader"
	WorkspaceRoleName       = "inference-workspace-user"
	VClusterRoleName        = "inference-workspace-vcluster"
	LocalQueueName          = "default"
	DefaultClusterQueueName = "inference-workspaces"
	KueueManagedLabel       = "kueue.openshift.io/managed"
	VClusterKubeconfigKey   = "config"
)

var ErrNamespaceCollision = errors.New("workspace namespace already exists and is not controlled by this workspace")
var ErrAccessSubjectNotFound = errors.New("workspace access subject does not exist")
var ErrRouteCollision = errors.New("vCluster Route already exists and is not controlled by this workspace")
var ErrKubeconfigSecretCollision = errors.New("public vCluster kubeconfig Secret already exists and is not controlled by this workspace")

var openShiftRouteGVK = schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"}
var openShiftIngressGVK = schema.GroupVersionKind{Group: "config.openshift.io", Version: "v1", Kind: "Ingress"}

type InferenceWorkspaceReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	VClusterInstaller workspacevcluster.Installer
	OpenShift         bool
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
		if errors.Is(err, ErrAccessSubjectNotFound) {
			return r.notReady(ctx, workspace, namespaceName, "AccessSubjectNotFound", err.Error(), 30*time.Second, nil)
		}
		return r.notReady(ctx, workspace, namespaceName, "ReconciliationFailed", err.Error(), 0, err)
	}

	if err := r.ensureClusterQueueExists(ctx, workspace); err != nil {
		if apierrors.IsNotFound(err) {
			return r.notReady(ctx, workspace, namespaceName, "ClusterQueueNotFound", err.Error(), 30*time.Second, nil)
		}
		return r.notReady(ctx, workspace, namespaceName, "ReconciliationFailed", err.Error(), 0, err)
	}

	queue, err := r.ensureLocalQueue(ctx, workspace, namespaceName)
	if err != nil {
		return r.notReady(ctx, workspace, namespaceName, "ReconciliationFailed", err.Error(), 0, err)
	}
	if !localQueueActive(queue) {
		return r.notReady(ctx, workspace, namespaceName, "QueueNotReady", "Default LocalQueue is waiting for its ClusterQueue", 5*time.Second, nil)
	}

	var kubeconfigSecretRef *corev1.SecretReference
	if workspaceMode(workspace) == workspacev1alpha1.WorkspaceModeVCluster {
		secretRef, ready, err := r.ensureVCluster(ctx, workspace, namespaceName)
		if err != nil {
			return r.notReady(ctx, workspace, namespaceName, "ReconciliationFailed", err.Error(), 0, err)
		}
		if !ready {
			return r.notReady(ctx, workspace, namespaceName, "VClusterNotReady", "vCluster control plane is starting", 5*time.Second, nil)
		}
		kubeconfigSecretRef = secretRef
	}

	if err := r.setReady(ctx, workspace, namespaceName, kubeconfigSecretRef, metav1.ConditionTrue, "Ready", "Workspace is ready"); err != nil {
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
				Name:   name,
				Labels: workspaceNamespaceLabels(workspace),
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
	if namespace.Labels == nil {
		namespace.Labels = make(map[string]string)
	}
	changed := false
	for key, value := range workspaceNamespaceLabels(workspace) {
		if namespace.Labels[key] != value {
			namespace.Labels[key] = value
			changed = true
		}
	}
	if changed {
		return r.Update(ctx, namespace)
	}
	return nil
}

func workspaceNamespaceLabels(workspace *workspacev1alpha1.InferenceWorkspace) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by":   "inference-workspace-operator",
		"inference.redhat.com/workspace": workspace.Name,
		KueueManagedLabel:                "true",
	}
}

func (r *InferenceWorkspaceReconciler) ensureAccess(
	ctx context.Context,
	workspace *workspacev1alpha1.InferenceWorkspace,
	namespace string,
) error {
	desiredSubjects := make([]rbacv1.Subject, 0, len(workspace.Spec.Access.Subjects))
	var missingSubject error
	for _, subject := range workspace.Spec.Access.Subjects {
		serviceAccount := &corev1.ServiceAccount{}
		key := types.NamespacedName{Name: subject.Name, Namespace: subject.Namespace}
		if err := r.Get(ctx, key, serviceAccount); err != nil {
			if apierrors.IsNotFound(err) {
				if missingSubject == nil {
					missingSubject = fmt.Errorf("%w: ServiceAccount %s/%s", ErrAccessSubjectNotFound, subject.Namespace, subject.Name)
				}
				continue
			}
			return err
		}
		desiredSubjects = append(desiredSubjects, rbacv1.Subject{
			Kind: "ServiceAccount", Name: subject.Name, Namespace: subject.Namespace,
		})
	}

	roleRef := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: WorkspaceRoleName}
	if workspaceMode(workspace) == workspacev1alpha1.WorkspaceModeVCluster {
		role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: VClusterAccessRoleName, Namespace: namespace}}
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
			role.Rules = []rbacv1.PolicyRule{{
				APIGroups:     []string{""},
				Resources:     []string{"secrets"},
				ResourceNames: []string{r.kubeconfigSecretName(workspace)},
				Verbs:         []string{"get"},
			}}
			return controllerutil.SetControllerReference(workspace, role, r.Scheme)
		})
		if err != nil {
			return err
		}
		roleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: VClusterAccessRoleName}
	}

	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: AccessBindingName, Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, binding, func() error {
		binding.RoleRef = roleRef
		binding.Subjects = desiredSubjects
		return controllerutil.SetControllerReference(workspace, binding, r.Scheme)
	})
	if err != nil {
		return err
	}
	return missingSubject
}

func (r *InferenceWorkspaceReconciler) kubeconfigSecretName(workspace *workspacev1alpha1.InferenceWorkspace) string {
	if r.OpenShift && workspaceMode(workspace) == workspacev1alpha1.WorkspaceModeVCluster {
		return "vc-" + workspace.Name + "-external"
	}
	return "vc-" + workspace.Name
}

func (r *InferenceWorkspaceReconciler) ensureClusterQueueExists(ctx context.Context, workspace *workspacev1alpha1.InferenceWorkspace) error {
	queue := &kueuev1beta2.ClusterQueue{}
	return r.Get(ctx, types.NamespacedName{Name: string(requestedClusterQueue(workspace))}, queue)
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
		queue.Spec.ClusterQueue = requestedClusterQueue(workspace)
		return controllerutil.SetControllerReference(workspace, queue, r.Scheme)
	})
	return queue, err
}

func requestedClusterQueue(workspace *workspacev1alpha1.InferenceWorkspace) kueuev1beta2.ClusterQueueReference {
	if workspace.Spec.ClusterQueue == "" {
		return kueuev1beta2.ClusterQueueReference(DefaultClusterQueueName)
	}
	return kueuev1beta2.ClusterQueueReference(workspace.Spec.ClusterQueue)
}

func localQueueActive(queue *kueuev1beta2.LocalQueue) bool {
	return apimeta.IsStatusConditionTrue(queue.Status.Conditions, kueuev1beta2.LocalQueueActive)
}

func workspaceMode(workspace *workspacev1alpha1.InferenceWorkspace) workspacev1alpha1.WorkspaceMode {
	if workspace.Spec.Mode == "" {
		return workspacev1alpha1.WorkspaceModeNamespace
	}
	return workspace.Spec.Mode
}

func (r *InferenceWorkspaceReconciler) ensureVCluster(
	ctx context.Context,
	workspace *workspacev1alpha1.InferenceWorkspace,
	namespace string,
) (*corev1.SecretReference, bool, error) {
	if r.VClusterInstaller == nil {
		return nil, false, errors.New("vCluster installer is not configured")
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: workspace.Name + "-vcluster"},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, binding, func() error {
		binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: VClusterRoleName}
		binding.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: "vc-" + workspace.Name, Namespace: namespace}}
		return controllerutil.SetControllerReference(workspace, binding, r.Scheme)
	})
	if err != nil {
		return nil, false, fmt.Errorf("ensure vCluster CSI capacity binding: %w", err)
	}
	publicHost := ""
	if r.OpenShift {
		publicHost, err = r.openShiftVClusterHost(ctx, workspace)
		if err != nil {
			return nil, false, fmt.Errorf("discover OpenShift vCluster hostname: %w", err)
		}
	}
	if err := r.VClusterInstaller.Ensure(ctx, workspace.Name, namespace, r.OpenShift, publicHost); err != nil {
		return nil, false, err
	}

	statefulSet := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Name: workspace.Name, Namespace: namespace}, statefulSet); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if statefulSet.Status.ReadyReplicas < 1 {
		return nil, false, nil
	}
	secretName := "vc-" + workspace.Name
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}

	if !r.OpenShift {
		return &corev1.SecretReference{Name: secretName, Namespace: namespace}, true, nil
	}

	publicSecret, err := r.ensureOpenShiftVClusterEndpoint(ctx, workspace, namespace, secret)
	if err != nil {
		return nil, false, err
	}
	return publicSecret, true, nil
}

func (r *InferenceWorkspaceReconciler) ensureOpenShiftVClusterEndpoint(
	ctx context.Context,
	workspace *workspacev1alpha1.InferenceWorkspace,
	namespace string,
	internalSecret *corev1.Secret,
) (*corev1.SecretReference, error) {
	host, err := r.openShiftVClusterHost(ctx, workspace)
	if err != nil {
		return nil, err
	}

	if err := r.ensureOpenShiftRoute(ctx, workspace, namespace, host); err != nil {
		return nil, err
	}

	secretName := r.kubeconfigSecretName(workspace)
	publicSecret := &corev1.Secret{}
	secretKey := kubeconfigSecretKey(internalSecret)
	if secretKey == "" {
		return nil, fmt.Errorf("vCluster Secret %s/%s does not contain a kubeconfig in %q or %q", internalSecret.Namespace, internalSecret.Name, VClusterKubeconfigKey, "kubeconfig")
	}
	rewrittenKubeconfig, err := rewriteKubeconfigServer(internalSecret.Data[secretKey], "https://"+host)
	if err != nil {
		return nil, fmt.Errorf("rewrite vCluster kubeconfig for Route %s: %w", host, err)
	}

	publicSecret = &corev1.Secret{}
	secretKeyRef := types.NamespacedName{Name: secretName, Namespace: namespace}
	if err := r.Get(ctx, secretKeyRef, publicSecret); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("get public vCluster kubeconfig Secret: %w", err)
		}
		publicSecret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace}}
	} else if !metav1.IsControlledBy(publicSecret, workspace) {
		return nil, fmt.Errorf("%w: %s/%s", ErrKubeconfigSecretCollision, namespace, secretName)
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, publicSecret, func() error {
		publicSecret.Type = internalSecret.Type
		publicSecret.Data = copySecretData(internalSecret.Data)
		publicSecret.Data[secretKey] = rewrittenKubeconfig
		return controllerutil.SetControllerReference(workspace, publicSecret, r.Scheme)
	})
	if err != nil {
		return nil, fmt.Errorf("ensure public vCluster kubeconfig Secret: %w", err)
	}

	return &corev1.SecretReference{Name: secretName, Namespace: namespace}, nil
}

func (r *InferenceWorkspaceReconciler) openShiftVClusterHost(
	ctx context.Context,
	workspace *workspacev1alpha1.InferenceWorkspace,
) (string, error) {
	appsDomain, err := r.openShiftAppsDomain(ctx)
	if err != nil {
		return "", fmt.Errorf("discover OpenShift applications domain: %w", err)
	}
	return workspace.Name + "." + appsDomain, nil
}

func (r *InferenceWorkspaceReconciler) openShiftAppsDomain(ctx context.Context) (string, error) {
	ingress := &unstructured.Unstructured{}
	ingress.SetGroupVersionKind(openShiftIngressGVK)
	if err := r.Get(ctx, types.NamespacedName{Name: "cluster"}, ingress); err != nil {
		return "", err
	}
	if domain, found, err := unstructured.NestedString(ingress.Object, "spec", "domain"); err != nil {
		return "", err
	} else if found && domain != "" {
		return domain, nil
	}
	if domain, found, err := unstructured.NestedString(ingress.Object, "status", "domain"); err != nil {
		return "", err
	} else if found && domain != "" {
		return domain, nil
	}
	return "", errors.New("OpenShift Ingress object does not publish an applications domain")
}

func (r *InferenceWorkspaceReconciler) ensureOpenShiftRoute(
	ctx context.Context,
	workspace *workspacev1alpha1.InferenceWorkspace,
	namespace string,
	host string,
) error {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(openShiftRouteGVK)
	routeKey := types.NamespacedName{Name: workspace.Name, Namespace: namespace}
	if err := r.Get(ctx, routeKey, route); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get vCluster Route: %w", err)
		}
		route = desiredOpenShiftRoute(workspace.Name, namespace, host)
		if err := controllerutil.SetControllerReference(workspace, route, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, route); err != nil {
			return fmt.Errorf("create vCluster Route: %w", err)
		}
		return nil
	}
	if !metav1.IsControlledBy(route, workspace) {
		return fmt.Errorf("%w: %s/%s", ErrRouteCollision, namespace, workspace.Name)
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, route, func() error {
		route.Object["spec"] = desiredOpenShiftRoute(workspace.Name, namespace, host).Object["spec"]
		return controllerutil.SetControllerReference(workspace, route, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("update vCluster Route: %w", err)
	}
	return nil
}

func desiredOpenShiftRoute(name, namespace, host string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": openShiftRouteGVK.Group + "/" + openShiftRouteGVK.Version,
		"kind":       openShiftRouteGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"host": host,
			"to": map[string]interface{}{
				"kind":   "Service",
				"name":   name,
				"weight": int64(100),
			},
			"port": map[string]interface{}{
				"targetPort": "https",
			},
			"tls": map[string]interface{}{
				"termination": "passthrough",
			},
		},
	}}
}

func kubeconfigSecretKey(secret *corev1.Secret) string {
	if _, found := secret.Data[VClusterKubeconfigKey]; found {
		return VClusterKubeconfigKey
	}
	if _, found := secret.Data["kubeconfig"]; found {
		return "kubeconfig"
	}
	return ""
}

func copySecretData(data map[string][]byte) map[string][]byte {
	copy := make(map[string][]byte, len(data))
	for key, value := range data {
		copy[key] = append([]byte(nil), value...)
	}
	return copy
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
	if err := r.setReady(ctx, workspace, namespaceName, nil, metav1.ConditionFalse, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, reconcileErr
}

func (r *InferenceWorkspaceReconciler) setReady(
	ctx context.Context,
	workspace *workspacev1alpha1.InferenceWorkspace,
	namespaceName string,
	kubeconfigSecretRef *corev1.SecretReference,
	status metav1.ConditionStatus,
	reason string,
	message string,
) error {
	before := workspace.DeepCopy()
	workspace.Status.NamespaceRef = &corev1.LocalObjectReference{Name: namespaceName}
	workspace.Status.KubeconfigSecretRef = kubeconfigSecretRef
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
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&rbacv1.ClusterRoleBinding{}).
		Owns(&kueuev1beta2.LocalQueue{}).
		Owns(&corev1.Secret{}).
		Owns(func() client.Object {
			route := &unstructured.Unstructured{}
			route.SetGroupVersionKind(openShiftRouteGVK)
			return route
		}()).
		Complete(r)
}
