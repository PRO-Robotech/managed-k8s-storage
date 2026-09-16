# Storage API v1 wire contract

Prototype uses JSON RPC over HTTPS: `POST /v1/<Operation>`. This API is independent
of CSI and Kubernetes; adding a VM adapter does not change Node CSI protocol.
JSON field names below are case sensitive as emitted by the Go model. No cloud token,
tenant selector or instance identity assertion is accepted as an authentication method.

mTLS TLS 1.3, one URI SAN, exact match in a **platform-owned** principal registry.
The server derives tenant/cluster/instance from that registry. Node callers cannot
invoke controller operations. `InstanceID` in a NodeSelf request is ignored.

Every response is `{"Volume": {...}}`, `{"Attachment": {...}}`,
`{"Instance": {...}}`, or `{}`. Unknown fields and bodies >32 KiB are rejected.
Errors: `{"Code":"PermissionDenied","Message":"..."}`. HTTP 401 unauthenticated,
403 denied, 404 absent, 409 conflict/precondition, 400 invalid, 500 internal.
Responses are observations, not guarantees of immediate convergence. Poll/retry the
same operation; CSI adapter returns Aborted while lifecycle operation is pending.

| Operation | Principal | Request fields | Result / semantics |
|---|---|---|---|
| CreateVolume | controller | Name, SizeBytes, Type, Region, Zone, Mode, FSType | idempotency tuple: authenticated cluster + tenant + Name; incompatible original spec conflicts |
| GetVolume | controller | ID | own-cluster volume |
| DeleteVolume | controller | ID | reject active attachment; asynchronous delete; missing succeeds |
| ResizeVolume | controller | ID, SizeBytes | grow only; repeat same size succeeds |
| CreateAttachment | controller | VolumeID, InstanceID | both same tenant+cluster, zone; only one live attachment |
| GetAttachment | controller | ID or VolumeID+InstanceID | latest matching attachment |
| DeleteAttachment | controller | ID or VolumeID+InstanceID | close authorization gate, wait uses, then provider detach |
| GetInstance | controller | InstanceID | validate platform instance registry scope |
| NodeSelf | node | empty | instance derived from certificate identity |
| AcquireUse | node | VolumeID, AttachmentID, Generation, UseID, Mode, FSType | validate proof atomically with persistent use insertion |
| CheckUse | node | VolumeID, AttachmentID, Generation, UseID | require matching healthy active attachment and existing use |
| MarkInitialized | node | same as CheckUse | revoke future blank-device format grant after successful first stage |
| RecordPublish | node | same + PodUID, PodName, Namespace, ServiceAccount | check active use, audit supplied Pod context |
| ReleaseUse | node | VolumeID, AttachmentID, Generation, UseID | valid ownership/generation; allowed while DETACHING, repeat succeeds |

`DeleteAttachment` may omit InstanceID when selecting by VolumeID, because MVP has
one live attachment; the latest incarnation is selected. A multi-attach extension
must replace this with an explicit transaction over all relevant attachments.

`UseID = SHA256(volumeID + NUL + stagingTargetPath + NUL + generation)` is a durable
node-side cleanup barrier, not a credential. Its ownership comes from mTLS and
attachment identity. It does not expire during partitions. Node releases only after
all tracked target mounts and staging mount were removed. Missing local journal
requires recovery and cannot be repaired by trusting a tenant VolumeAttachment.

```json
{
  "Name": "pvc-7c185fee-2a99-45b6-a0c1-9ba171366fab",
  "SizeBytes": 1073741824,
  "Type": "standard",
  "Region": "ru-1",
  "Zone": "ru-1a",
  "Mode": "mount",
  "FSType": "ext4"
}
```

Volume modes are `mount` / `block`. Block FSType must be empty. Type is `standard`
in the prototype. Scope comes solely from the platform-issued controller identity.
The registry is loaded once at startup; hot reload, certificate renewal/revocation,
quotas, pagination and administrative fenced recovery are production extensions.

Provider interface: EnsureVolume, GetVolume, DeleteVolume, EnsureAttached,
EnsureDetached. EnsureVolume handles create/grow; platform never recreates a previously
observed disk after disappearance. Provider implementations must persist idempotency
keys, return stable device identity and distinguish unknown outcome from proven absence.

State model in Go: [model.go](../internal/model/model.go). The prototype keeps
operational state and separate audit records in bbolt buckets; both are durable,
but the local audit store is not an immutable external audit archive.
