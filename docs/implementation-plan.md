# Inference Workspace Operator Implementation Plan

## Purpose

The Inference Workspace Operator provides an Inference Engineering-owned API
for provisioning isolated development and test workspaces. Client tools such as
`infra` and a future UI create `InferenceWorkspace` resources; the operator
reconciles the cluster resources needed to make each workspace usable.

The operator owns cluster-local workspace provisioning. The `infra` service
owns identity, entitlement, access, and expiration policy. The operator does
not replace Kueue, Forge, Fournos, or higher-level lifecycle tools.

## API contract

The API is a cluster-scoped `InferenceWorkspace` resource in the
`inference.redhat.com/v1alpha1` API group.

```yaml
apiVersion: inference.redhat.com/v1alpha1
kind: InferenceWorkspace
metadata:
  name: alice-test
spec:
  mode: VCluster
  clusterQueue: inference-workspaces
```

`spec.mode` is immutable and selects `Namespace` or `VCluster`; it defaults to
`Namespace`. Access subjects are intentionally not part of the API. Only the
`infra` service account should receive CRUD access to workspace resources, and
`infra` is responsible for granting users or service accounts the resulting
workspace access.

## Namespace reconciliation

For an `InferenceWorkspace` named `<name>`, the operator creates a namespace
named `workspace-<name>`. It labels the namespace with
`kueue.openshift.io/managed: "true"` so the cluster Kueue controller manages
workloads submitted through the workspace's LocalQueue.

The operator must not adopt an existing namespace. If the target namespace
already exists and is not controlled by the requesting `InferenceWorkspace`,
reconciliation fails and the collision is reported through the `Ready`
condition.

The operator supplies an `inference-workspace-user` ClusterRole that allows
normal application and inference workload management but excludes CRD, RBAC,
and Kueue queue mutation. It grants read-only access to the workspace's
LocalQueue and Workloads so users can inspect admission state. The `infra`
service, rather than the operator, decides which identities receive this role.
Users that require their own CRDs use vCluster mode.

The workspace role is intentionally defined by this operator instead of using
the built-in `admin` or `edit` roles. Those roles are dynamically extended by
installed operators and cannot guarantee that LocalQueue mutation remains
reserved for the workspace operator.

## Kueue integration

Every workspace namespace receives a Kueue `LocalQueue` named `default`. It
references the `ClusterQueue` selected through `spec.clusterQueue`, which
defaults to `inference-workspaces` when omitted. Queue selection is immutable
because Kueue does not allow an existing LocalQueue to change its ClusterQueue.

Naming the LocalQueue `default` enables Kueue LocalQueue defaulting for enabled
workload integrations. The cluster's Kueue installation remains responsible
for enabling the required workload integrations and enforcing management of
workloads in workspace namespaces.

The operator verifies that the requested ClusterQueue exists but does not
create or manage ClusterQueues, ResourceFlavors, cohorts, or priority policy.
The `infra` service maps users and teams to allowed ClusterQueues and refuses
unauthorized requests before creating a workspace. Workspace users must never
receive permission to mutate a LocalQueue because doing so could bypass that
authorization boundary.

Because the vCluster control plane is itself scheduled in the managed workspace
namespace, the selected ClusterQueue must cover all of its requested resources,
including `ephemeral-storage` in addition to CPU and memory.

## vCluster mode

The operator installs vCluster from the pinned
`quay.io/klape/charts/vcluster:0.0.1-combined.4` chart with the
`quay.io/klape/vcluster:csi-capacity-debug-v26` control-plane image. The chart
is downloaded and checksum-verified while building the operator image, then
installed from that bundled artifact without runtime registry access.

The chart's cluster role and cluster role binding are disabled. A shared
operator-owned ClusterRole permits only `get`, `list`, and `watch` of
`StorageClass` and `CSIStorageCapacity`, and each vCluster workspace receives a
binding from its control-plane service account to that role. `CSINode` and
`CSIDriver` resources are not synchronized.

At startup the operator discovers whether `security.openshift.io` is served by
the cluster. On OpenShift it selects the chart's `restricted` security profile;
on Kubernetes it leaves the profile at the chart default. Discovery errors are
fatal so an incompatible profile is never selected silently.

The kubeconfig grants access to the virtual cluster and must not contain host
cluster credentials. Its Secret reference is published in
`status.kubeconfigSecretRef` only after both the control plane and credential
are ready. The `infra` service grants access to that specific Secret.

## Status

Status remains deliberately compact:

```yaml
status:
  namespaceRef:
    name: workspace-alice-test
  kubeconfigSecretRef:
    namespace: workspace-alice-test
    name: vc-alice-test
  conditions:
    - type: Ready
      status: "True"
      observedGeneration: 1
      reason: Ready
      message: Workspace is ready
```

`kubeconfigSecretRef` is populated only for a ready vCluster and is omitted for
namespace workspaces. `Ready=False` communicates failures through specific
reasons such as `NamespaceCollision`, `ClusterQueueNotFound`, `QueueNotReady`,
`VClusterNotReady`, or `ReconciliationFailed`.

Object deletion is represented by `metadata.deletionTimestamp`; no deletion or
expiration condition is added.

## Lifecycle and cleanup

The operator does not implement expiration. External tools may implement
lifespans by deleting the `InferenceWorkspace` resource.

The operator uses a finalizer to delete the backing namespace. It removes the
finalizer only after the namespace no longer exists. Deleting the namespace
also removes the LocalQueue, vCluster, and all namespaced workspace resources.
Kubernetes garbage collection removes the workspace-owned vCluster
ClusterRoleBinding.

## Delivery

The repository contains a container image definition, deployment manifests,
unit tests, and Tekton resources. The Tekton pipeline clones a requested Git
revision, runs tests, builds the manager binary, and builds the operator image.
Image pushing remains an explicit pipeline parameter so validation builds do
not require publishing an image.
