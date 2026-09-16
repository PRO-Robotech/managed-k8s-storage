# Проверка прототипа — 2026-09-16

Проверялся исходный код этого репозитория в Linux с Go 1.26.8. Это отчёт о
прототипе, а не подтверждение production readiness или CSI certification.

| Проверка | Результат |
|---|---|
| `go test -count=1 ./...` | PASS |
| `go vet ./...` | PASS |
| `gofmt -l cmd internal` | Пустой вывод |
| Go race detector | PASS в `golang:1.26.8`, CGO_ENABLED=1 |
| `make demo` | PASS, отдельные API/controller/node процессы |
| YAML parsing всех deploy manifests | PASS; live Kubernetes validation не выполнялась |
| Реальные cloud resources | Не создавались |
| Реальные Linux mount/mkfs | Не выполнялись |
| Запуск upstream CSI sidecars в Kubernetes | Не выполнялся |
| Полный CSI sanity / Kubernetes e2e | Не выполнялся |

На локальном хосте отсутствует C compiler, поэтому race detector запускается в
уже доступном Go container. В CI используется runner с C compiler. Воспроизведение
в контейнере после загрузки module cache:

```bash
docker run --rm --network none -e CGO_ENABLED=1 \
  -v "$PWD:/workspace:ro" \
  -v "$(go env GOMODCACHE):/go/pkg/mod:ro" \
  -w /workspace golang:1.26.8 go test -race -count=1 ./...
```

## Проверенные сценарии

- Full CSI flow для **ext4, xfs, raw block**: CreateVolume → ControllerPublish →
  NodeStage → NodePublish → ControllerExpand → NodeExpand → Stats → cleanup → Delete.
- Повтор CreateVolume не создаёт второй ID; несовместимый запрос с тем же именем отклонён.
- Конкурирующий attach одного volume к двум VM: ровно один успешный intent.
- Foreign tenant не может read/delete/resize/attach/mount volume.
- Поддельный nodeID другой VM/tenant и старый generation не проходят authorization.
- Node identity не может вызывать cloud mutation и перечислять диски tenant.
- TLS без machine certificate отвергается; certificate CN не заменяет registered URI SAN.
- InstanceID в NodeSelf request не переопределяет identity сертификата.
- Detach до unstage блокируется durable use; новые publishes после DETACHING отклоняются.
- Ошибка unmount сохраняет barrier и запрещает provider detach.
- Unstage при существующем target отклоняется, повтор unpublish успешен.
- Lost response после cloud attach: повтор использует тот же attachment ID.
- Platform DB restart и Node journal restart сохраняют lifecycle state.
- Исчезнувший cloud disk не создаётся заново; drift при active use требует recovery.
- Несовместимая topology, inactive VM и shrink отклоняются.
- Signature policy запрещает format существующей/иной FS, GPT и initialized blank disk.
- Traversal и symlink в target path отклоняются до host operation.

## Process demo

Demo запускает настоящие Unix gRPC endpoints CSI и HTTPS Storage API с независимо
выданными dev certificates для controller/node. Cloud actual state сохраняется
отдельно от platform intent; local mount state также хранится отдельно от Node journal.

Ожидаемый вывод:

```json
{
  "result": "PASS",
  "transport": "real CSI gRPC Unix sockets + Storage API mTLS",
  "provider": "persistent emulator",
  "host": "simulated (no real mounts)"
}
```

Assertions проверяют state/ошибки и capacity, но не наличие Linux filesystem на
физическом диске. Unit tests Linux policy не заменяют mount tests на isolated VM.
Journal fsync/crash между каждой парой syscalls, аппаратные отказы, real cloud
API timeouts, kubelet reboot и cloud fencing требуют отдельной fault campaign.

CI workflow повторяет race tests, vet, форматирование и process demo. Remote CI
результат следует смотреть для конкретного commit в GitHub Actions; локальные
проверки сами по себе не означают успешный remote CI run.
