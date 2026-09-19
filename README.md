# Inference Workspace Operator

Inference Workspace Operator provisions isolated workspaces for inference
development and testing. It provides an Inference Engineering-owned boundary
between clients such as `infra` or a future UI and cluster services such as
Kueue and vCluster.

The operator provisions either a host-cluster namespace or a vCluster for each
cluster-scoped `InferenceWorkspace` resource.

See the [implementation plan](docs/implementation-plan.md) for the API and
ownership decisions.

## Workspace request

```yaml
apiVersion: inference.redhat.com/v1alpha1
kind: InferenceWorkspace
metadata:
  name: alice-test
spec:
  mode: VCluster
  clusterQueue: inference-workspaces
```

Creating this resource provisions:

* namespace `workspace-alice-test`;
* a Kueue `LocalQueue` named `default` that references the platform-owned
  `inference-workspaces` ClusterQueue.

For `VCluster` mode, the operator also installs the pinned vCluster chart and
publishes its kubeconfig Secret reference in status. The vCluster can read
host-cluster `StorageClass` and `CSIStorageCapacity` objects through a dedicated
read-only role. The chart's own cluster-wide RBAC is disabled.

The reusable workspace role does not permit its subjects to mutate LocalQueues,
cluster RBAC, or CRDs. Kueue queue selection remains controlled by the operator.
`spec.clusterQueue` may reference any ClusterQueue in the initial API and
defaults to `inference-workspaces` when omitted.

The external `infra` service owns user and service-account access. It binds the
operator-provided workspace role for direct namespace access or grants access
to the vCluster kubeconfig Secret. Access identities are intentionally absent
from the `InferenceWorkspace` API.

## Development

```bash
make test
make build
```

The Tekton resources in `config/tekton` clone a requested Git revision, run the
tests, build the manager binary, and build the operator container image. Image
publishing is disabled by default and can be enabled when a registry credential
workspace is supplied.
