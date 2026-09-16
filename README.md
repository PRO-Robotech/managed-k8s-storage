# Managed Kubernetes Storage — прототип

Storage-подсистема с внешним CSI Controller, authoritative Storage API и минимальным
CSI Node Agent на worker VM. Kubernetes задаёт intent и targetPath; платформа
проверяет tenant/cluster/VM ownership и управляет cloud lifecycle.

**Рабочий прототип контрактов и reconciliation, не production storage driver.**
Проверяемый сценарий использует persistent cloud emulator и simulated host.
Настоящие CSI gRPC sockets и mTLS API работают; отдельный Linux mount backend
предоставлен для дальнейших испытаний на disposable VM.

```text
Tenant Kubernetes: PVC / PV / VolumeAttachment
      │ standard external-provisioner / attacher / resizer (outside tenant)
      ▼
External CSI Controller ──mTLS──► Storage API + persistent reconciler
                                      │ Provider interface
                                      ▼
                                 Cloud backend
                                      │ stable device identity
kubelet ──CSI Unix socket──► CSI Node ──┘
                               │ own-VM attachment authorization via mTLS
                               └─ stage / publish to kubelet-supplied paths
```

## Быстрый запуск

Нужны Linux, Go **1.26.8**, Make и OpenSSL. Demo не требует root, cloud account или
действующего Kubernetes cluster. Он не выполняет mount/mkfs на машине запуска.

```bash
git clone https://github.com/PRO-Robotech/managed-k8s-storage.git
cd managed-k8s-storage
make demo
```

Результат: JSON `result: PASS` для ext4/xfs/block. Dev CA, private keys, DB и логи
создаются в отдельном `run/demo.*` и исключены из Git/Docker build context.
Dev PKI предназначена только для локального примера и имеет короткий срок действия.
Все процессы завершаются после demo, evidence остаётся локально.

```bash
go test ./...        # не требует C compiler
make test           # race detector, требует C compiler
make vet
```

## Что реализовано

- CSI Identity; Controller Create/Delete/Publish/Unpublish/Validate/Expand/Capabilities.
- CSI Node GetInfo/Stage/Unstage/Publish/Unpublish/Expand/Stats/Capabilities.
- Persistent Volume и Attachment, monotonic generations, desired/actual reconciliation.
- Идемпотентные create/attach/delete; запрет второго writer до provider API.
- mTLS machine identity; controller scope задаётся платформой, Node API ограничен своей VM.
- Attachment proof через стандартный `publish_context`; повторная online validation перед publish.
- Durable node-use barrier: detach не проходит, пока node-side cleanup не завершён.
- Persistent node journal, повтор mount после restart, безопасный отказ при config mismatch.
- Filesystem / block capability, ext4/xfs policy, topology, controller/node resize, stats.
- Тесты foreign tenant, forged node ID, generation, single writer, early detach,
  lost cloud response, restart, cloud drift, topology, filesystem signatures и mTLS.
- Отдельный audit bucket с principal, operation, resource identity и Pod context.
- Linux backend с stable by-id discovery, проверкой signatures, bind mount, resize и stats.

## Документы

- [Архитектура: 20 результатов проектирования, диаграммы, state machines и threat model](docs/architecture.ru.md).
- [Storage API: wire contract и authorization](docs/api.md).
- [Размещение внешних sidecars, Node service и Kubernetes resources](docs/deployment.ru.md).
- [Проверки и их границы](docs/verification.ru.md).

## Границы текущей версии

| Область | Статус |
|---|---|
| CSI RPC и API через реальные транспорта | Проверено локальным process demo |
| Cloud lifecycle / mounts в demo | Persistent cloud emulator / simulated host |
| Linux disk operations | Код и unit policy tests; реальные mounts не проверены |
| Dynamic Provisioning в Kubernetes | CSI adapter + upstream sidecar templates; live cluster E2E не выполнен |
| Node identity | mTLS + статический platform registry; issuer/renewal вне прототипа |
| HA | Архитектура описана; API single writer bbolt, replicas=1 |
| Recovery | Идемпотентные повторы, restart и безопасные блокировки; fencing не реализован |
| Observability | Durable audit; production metrics/traces пока не реализованы |
| CSI conformance | Полный csi-sanity / Kubernetes storage e2e ещё не выполнен |
| Cloud providers | Реальные Beget/Yandex/Ceph/NVMe-oF/SAN adapters ещё не реализованы |

Tenant `cluster-admin` может подделать Kubernetes storage objects, но они не дают
полномочий платформенному API. Если он получает root на своей VM, он способен
обойти локальный Node Agent; граница защиты чужих tenant проходит через platform
ownership и hypervisor/cloud attachment. Pod UID используется как immutable audit
identifier, а не как самостоятельное доказательство авторизации.

## Структура

```text
cmd/storage-api       HTTPS mTLS + persistent reconciler + fake provider
cmd/csi-controller    external CSI Controller server
cmd/csi-node          CSI Node server (simulated / opt-in Linux host)
cmd/demo              CSI client, проверяющий полный lifecycle
internal/platform     ownership, state machines, use barrier
internal/provider     provider abstraction + persistent cloud emulator
internal/driver       CSI adapters и node journal
internal/host         simulated and Linux local operations
internal/transport    machine mTLS API client/server
deploy/               platform / tenant / worker templates
scripts/              disposable local dev PKI and process demo
```
