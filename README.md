# Inference Workspace Operator

Inference Workspace Operator provisions isolated workspaces for inference
development and testing. It provides an Inference Engineering-owned boundary
between clients such as `infra` or a future UI and cluster services such as
Kueue and vCluster.

The operator provisions either a host-cluster namespace or a vCluster for each
namespaced `InferenceWorkspace` resource. The resource namespace is the control
boundary: the infra service can manage workspaces across namespaces, while
direct users can be limited to a namespace by ordinary Kubernetes RBAC.

See the [implementation plan](docs/implementation-plan.md) for the API and
ownership decisions.

## Workspace request

```yaml
apiVersion: inference.redhat.com/v1alpha1
kind: InferenceWorkspace
metadata:
  name: alice-test
  namespace: alice-dev
spec:
  mode: VCluster
  clusterQueue: inference-workspaces
  access:
    subjects:
      - kind: ServiceAccount
        namespace: infra-users
        name: alice
```

Creating this resource provisions:

* namespace `workspace-alice-test`;
* a Kueue `LocalQueue` named `default` that references the platform-owned
  `inference-workspaces` ClusterQueue.

In `Namespace` mode, the operator also creates a dedicated ServiceAccount and
publishes a host-cluster kubeconfig in the workspace namespace. The kubeconfig
Secret reference is available in status after Kubernetes has populated the
ServiceAccount token.

For `VCluster` mode, the operator also installs the pinned vCluster chart and
publishes its kubeconfig Secret reference in status. On OpenShift, it also
creates a TLS-passthrough Route at
`<workspace-name>.<cluster-applications-domain>` and publishes an
operator-managed copy of the kubeconfig with that public server URL. The copy
retains the CA and certificate data provided by vCluster; certificate SAN
handling for the public hostname is intentionally left for a later iteration.
The vCluster can read host-cluster `StorageClass` and `CSIStorageCapacity`
objects through a dedicated read-only role. The chart's own cluster-wide RBAC
is disabled.

The reusable workspace role does not permit its subjects to mutate LocalQueues,
cluster RBAC, or CRDs. Kueue queue selection remains controlled by the operator.
`spec.clusterQueue` may reference any ClusterQueue in the initial API and
defaults to `inference-workspaces` when omitted.

The external `infra` service selects entitled service accounts through
`spec.access.subjects`, but it receives no permission to create RBAC. The
operator enforces fixed access: direct workspaces bind the
`inference-workspace-user` ClusterRole, while vCluster workspaces grant only
`get` on that workspace's kubeconfig Secret.

## Development

```bash
make test
make build
```

The Tekton resources in `config/tekton` clone a requested Git revision, run the
tests, build the manager binary, and build the operator container image. Image
publishing is disabled by default and can be enabled when a registry credential
workspace is supplied.
