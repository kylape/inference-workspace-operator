# Inference Workspace Manager

Inference Workspace Manager provisions short-lived workspaces for inference
development and testing. It provides a platform-owned boundary between client
tools such as `infra`, application orchestrators such as Forge and Fournos, and
cluster services such as Kueue.

## Initial scope

The first implementation will define an `InferenceWorkspace` resource and use
a Kubernetes Secret containing YAML to map users and teams to platform policy.
The initial access mode is a host-cluster namespace. vCluster-backed workspaces
will use the same API later, but are intentionally outside the first slice.

The manager will eventually reconcile:

* a workspace namespace;
* team- and user-scoped RBAC;
* a default Kueue `LocalQueue` binding; and
* workspace expiry metadata.

Forge and Fournos remain responsible for application lifecycle and topology.
The manager owns platform policy and access provisioning.

## Team mapping

The mapping Secret contains YAML in a `mapping.yaml` data entry. The Secret is
platform-owned and should not be writable by workspace users.

```yaml
teams:
  inference-engineering:
    members:
      - alice
    queue: inference-engineering-dev
    priorityClass: inference-development
  psap:
    members:
      - bob
    queue: psap-ci
    priorityClass: ci
```

The exact schema is part of the API design and will be validated by the
controller. A workspace request may reference a team, but the requester must
be authenticated and authorized for that team; the reference must not grant
membership by itself.

