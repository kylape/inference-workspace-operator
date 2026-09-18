# Inference Workspace Operator Implementation Plan

## Purpose

The Inference Workspace Operator provides an Inference Engineering-owned API
for provisioning isolated development and test workspaces. Client tools such as
`infra` and a future UI create `InferenceWorkspace` resources; the operator
reconciles the cluster resources needed to make each workspace usable.

The operator owns workspace provisioning and access policy. It does not replace
Kueue, vCluster, Forge, Fournos, or higher-level lifecycle tools.

## API contract

The initial API is a cluster-scoped `InferenceWorkspace` resource in the
`inference.redhat.com/v1alpha1` API group.

```yaml
apiVersion: inference.redhat.com/v1alpha1
kind: InferenceWorkspace
metadata:
  name: alice-test
spec:
  subjects:
    - kind: User
      name: alice
    - kind: ServiceAccount
      name: ci-runner
      namespace: ci-system
```

Permission to create an `InferenceWorkspace` includes permission to delegate
access to the users and service accounts listed in `spec.subjects`. The operator
does not track or authorize the identity that submitted the request. RBAC
controls which identities may create workspace requests.

Groups are intentionally excluded from the initial API. User subjects use the
`rbac.authorization.k8s.io` API group. Service account subjects must include a
namespace and omit the API group.

The initial implementation supports host-cluster namespace workspaces only. A
future API revision will add an access mode that selects either direct namespace
access or vCluster access.

## Namespace reconciliation

For an `InferenceWorkspace` named `<name>`, the operator creates a namespace
named `workspace-<name>`.

The operator must not adopt an existing namespace. If the target namespace
already exists and is not controlled by the requesting `InferenceWorkspace`,
reconciliation fails and the collision is reported through the `Ready`
condition.

The operator creates a RoleBinding in the workspace namespace that grants each
subject the operator-owned `inference-workspace-user` ClusterRole. The role
allows normal application and inference workload management but excludes CRD,
RBAC, and Kueue queue mutation. It grants read-only access to the workspace's
LocalQueue and Workloads so users can inspect admission state. Users that
require their own CRDs will use the future vCluster access mode.

The workspace role is intentionally defined by this operator instead of using
the built-in `admin` or `edit` roles. Those roles are dynamically extended by
installed operators and cannot guarantee that LocalQueue mutation remains
reserved for the workspace operator.

## Kueue integration

Every workspace namespace receives a Kueue `LocalQueue` named `default`. It
references the platform-owned `ClusterQueue` named `inference-workspaces`.

Naming the LocalQueue `default` enables Kueue LocalQueue defaulting for enabled
workload integrations. The cluster's Kueue installation remains responsible
for enabling the required workload integrations and enforcing management of
workloads in workspace namespaces.

The operator does not create or manage the ClusterQueue, ResourceFlavors,
cohorts, or priority policy.

The hard-coded ClusterQueue reference is provisional. The public API will
eventually expose a logical workload class that the operator resolves to an
authorized ClusterQueue based on centrally managed user or team entitlements.
Workspace users must never receive permission to create, replace, or modify a
LocalQueue, because doing so could bypass that authorization boundary.

## Status

Status remains deliberately compact:

```yaml
status:
  namespaceRef:
    name: workspace-alice-test
  kubeconfigSecretRef:
    namespace: workspace-alice-test
    name: workspace-alice-test-kubeconfig
  conditions:
    - type: Ready
      status: "True"
      observedGeneration: 1
      reason: Ready
      message: Workspace is ready
```

`namespaceRef` identifies the backing host namespace. The optional
`kubeconfigSecretRef` is reserved for vCluster mode and is omitted for namespace
workspaces. `Ready=False` communicates failures through specific reasons such
as `NamespaceCollision`, `QueueNotReady`, or `ReconciliationFailed`.

Object deletion is represented by `metadata.deletionTimestamp`; no deletion or
expiration condition is added.

## Lifecycle and cleanup

The operator does not implement expiration. External tools may implement
lifespans by deleting the `InferenceWorkspace` resource.

The operator uses a finalizer to delete the backing namespace. It removes the
finalizer only after the namespace no longer exists. Deleting the namespace
also removes the RoleBinding, LocalQueue, and all workspace resources.

The operator continuously restores managed namespace labels, RoleBinding
subjects, and LocalQueue configuration while the workspace exists.

## Future vCluster mode

vCluster mode will retain the same `InferenceWorkspace` resource and use a
mutually exclusive access mode. Workspace subjects will not receive general
access to the backing host namespace. They will receive only `get` permission
for the specific Secret containing the vCluster kubeconfig, without Secret
`list` permission.

The kubeconfig grants access to the virtual cluster and must not contain host
cluster credentials. Its Secret reference will be published in
`status.kubeconfigSecretRef` only after the credential exists.

## Delivery

The repository contains a container image definition, deployment manifests,
unit tests, and Tekton resources. The Tekton pipeline clones a requested Git
revision, runs tests, builds the manager binary, and builds the operator image.
Image pushing remains an explicit pipeline parameter so validation builds do
not require publishing an image.
