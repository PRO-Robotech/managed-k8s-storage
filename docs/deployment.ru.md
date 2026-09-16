# Размещение и запуск

Локально воспроизводимый запуск — `make demo`: четыре процесса, настоящие gRPC Unix
sockets, HTTPS mTLS и persistent state; cloud и mount эмулируются. Никакой текущий
kube-context не используется. Для выбора свободного порта:
`STORAGE_DEMO_PORT=20443 make demo`.

## Platform / infra environment

1. Storage API: отдельный процесс с `--db`, `--cloud-db`, `--config`, `--ca`, `--cert`,
   `--key`. Только **fake provider** входит в этот репозиторий. Реальный provider
   должен выдавать stable deviceKey, поддерживать идемпотентные операции и fencing.
2. На каждый tenant cluster — CSI Controller и штатные provisioner/attacher/resizer
   с kubeconfig этого tenant API. Manifest `controller-external.yaml` применяется
   в infra cluster. Cloud/machine credentials и tenant kubeconfig хранятся там.
3. Выдать этому kubeconfig RBAC из release-matched upstream manifests
   [provisioner](https://github.com/kubernetes-csi/external-provisioner/tree/v6.3.0/deploy/kubernetes),
   [attacher](https://github.com/kubernetes-csi/external-attacher/tree/v4.13.0/deploy/kubernetes),
   [resizer](https://github.com/kubernetes-csi/external-resizer/tree/v2.2.1/deploy/kubernetes).
   Сверить permissions с watches в architecture.ru.md, namespace Leases — kube-system.
   Не использовать cluster-admin kubeconfig как постоянный deployment credential.
4. Пример явно оставляет `replicas: 1`: API использует bbolt single writer.
   Для production HA заменить storage на transactional shared DB и worker fencing.

Файлы в deploy — **шаблоны**, не проверенная end-to-end установка в Kubernetes.
Образ `managed-k8s-storage:prototype` нужно собрать/доставить самостоятельно.
Sidecar версии проверены по upstream releases 2026-09-16, совместимость выбранной
версии Kubernetes должна быть проверена отдельно. OCI image здесь не публикуется.

## Worker VM

`csi-node.service` запускает платформенный Linux Node Agent вне управления tenant
Deployment/DaemonSet. Machine key выдаётся при bootstrap VM; CA private key на VM
не копируется. `node-registrar.yaml` только регистрирует его socket в kubelet.
Node Agent не нуждается в Kubernetes token; registrar отключает automount token.
Kubelet должен видеть тот же mount namespace. Для containerized Node Agent нужны
явные mount propagation и host mounts; в прототипе такой DaemonSet не поставляется.

Node startup получает immutable VM/tenant/topology через NodeSelf по mTLS.
Платформа выставляет `Node.spec.providerID=cloud://ru-1/vm-a`; NodeGetInfo возвращает
vm-a. Tenant labels/providerID не изменяют server-side ownership.

`--host-mode=linux` требует root и утилиты `findmnt`, `wipefs`, `mount`, `umount`,
`blockdev`, `mkfs.ext4`, `mkfs.xfs`, `resize2fs`, `xfs_growfs`. Он **экспериментальный**:
реальные block-device mount, udev delay/reboot и kernel/filesystem recovery в текущем
отчёте не проверены. Использовать только в отдельной disposable worker VM.
Read-only target поддерживается bind remount; fsGroup и пользовательские mount flags
не поддерживаются и явно отклоняются. Нет автоматического fsck/repair.

Node socket не удаляется автоматически при запуске. После аварийного завершения
убедиться, что старый процесс отсутствует, и удалить только его stale Unix socket.
Journal/DB не удалять: это может потерять сведения, необходимые для безопасного detach.

## Tenant cluster

После готовности platform интеграции применяются `tenant-resources.yaml`, registrar
и пример PVC/Pod. Наличие одного YAML с PV не разрешает attach/mount чужого диска.

Standard sidecars создают PV и обслуживают finalizers/VA/resize. В custom коде нет
watches Pods, PV/PVC resolution или вычисления targetPath. `podInfoOnMount` требует
Pod UID при NodePublish; NodeStage не требует Pod identity.

## Recovery и ограничения

Нет auto force-detach, очистки use по TTL и удаления финализаторов по таймеру.
Потеря Node journal или мёртвая VM требуют проверенного fencing и операторской
процедуры. Prototype API не содержит force endpoint. Cloud DB loss и platform DB loss
не считаются обычным restart: восстановить backup, сверить actual state и audit.

Для production дополнительно нужны quota/rate limiting, external audit sink,
metrics/tracing, identity lifecycle, tenant-to-cluster provisioning controller,
trusted Node registration checks, provider inventory reconciliation и orphan GC.
