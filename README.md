# Inference Workspace Operator

Inference Workspace Operator provisions isolated workspaces for inference
development and testing. It provides an Inference Engineering-owned boundary
between clients such as `infra` or a future UI and cluster services such as
Kueue and vCluster.

The initial implementation provisions a host-cluster namespace for each
cluster-scoped `InferenceWorkspace` resource. vCluster-backed workspaces are a
planned extension of the same API.

See the [implementation plan](docs/implementation-plan.md) for the API and
ownership decisions.

## Workspace request

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

Creating this resource provisions:

* namespace `workspace-alice-test`;
* namespaced application access through the operator-owned
  `inference-workspace-user` ClusterRole; and
* a Kueue `LocalQueue` named `default` that references the platform-owned
  `inference-workspaces` ClusterQueue.

The workspace role does not permit subjects to mutate LocalQueues, cluster
RBAC, or CRDs. Kueue queue selection remains controlled by the operator.

Permission to create an `InferenceWorkspace` includes permission to delegate
workspace access to other users and service accounts.

## Development

```bash
make test
make build
```

The Tekton resources in `config/tekton` clone a requested Git revision, run the
tests, build the manager binary, and build the operator container image. Image
publishing is disabled by default and can be enabled when a registry credential
workspace is supplied.
