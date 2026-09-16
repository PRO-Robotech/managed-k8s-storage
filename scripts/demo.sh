#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p run
work=$(mktemp -d "$PWD/run/demo.XXXXXX")
# The configurable port allows concurrent independent demos without changing global state.
port=${STORAGE_DEMO_PORT:-19443}
pids=()
cleanup() { for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done; for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done; }
trap cleanup EXIT
bash scripts/dev-pki.sh "$work/pki"
./bin/storage-api --listen "127.0.0.1:$port" --db "$work/platform.db" --cloud-db "$work/cloud.db" --ca "$work/pki/ca.pem" --cert "$work/pki/server.pem" --key "$work/pki/server.key" >"$work/api.log" 2>&1 &
pids+=("$!")
./bin/csi-controller --socket "$work/controller.sock" --api "https://localhost:$port" --ca "$work/pki/ca.pem" --cert "$work/pki/controller-a.pem" --key "$work/pki/controller-a.key" >"$work/controller.log" 2>&1 &
pids+=("$!")
./bin/csi-node --socket "$work/node.sock" --api "https://localhost:$port" --ca "$work/pki/ca.pem" --cert "$work/pki/node-a.pem" --key "$work/pki/node-a.key" --kubelet-root "$work/kubelet" --journal "$work/node.db" --host-db "$work/host.db" >"$work/node.log" 2>&1 &
pids+=("$!")
for attempt in $(seq 1 100); do
  for pid in "${pids[@]}"; do if ! kill -0 "$pid" 2>/dev/null; then cat "$work/"*.log >&2; exit 1; fi; done
  if [[ -S "$work/controller.sock" && -S "$work/node.sock" ]]; then break; fi
  sleep 0.1
done
./bin/demo --controller "$work/controller.sock" --node "$work/node.sock" --root "$work/kubelet" | tee "$work/result.json"
printf 'Local evidence (includes dev private keys; do not publish): %s\n' "$work"
