#!/usr/bin/env bash
set -euo pipefail
umask 077
out=${1:?output directory required}
mkdir -p "$out"
if [[ -e "$out/ca.key" ]]; then echo 'Refusing to replace existing CA' >&2; exit 1; fi
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$out/ca.key" -out "$out/ca.pem" -days 2 -subj /CN=prototype-dev-ca >/dev/null 2>&1
issue() {
  local name=$1 san=$2 usage=$3
  openssl req -newkey rsa:2048 -nodes -keyout "$out/$name.key" -out "$out/$name.csr" -subj "/CN=$name" >/dev/null 2>&1
  printf 'subjectAltName=%s\nextendedKeyUsage=%s\n' "$san" "$usage" > "$out/$name.ext"
  openssl x509 -req -in "$out/$name.csr" -CA "$out/ca.pem" -CAkey "$out/ca.key" -CAcreateserial -out "$out/$name.pem" -days 1 -extfile "$out/$name.ext" >/dev/null 2>&1
}
issue server 'DNS:localhost,IP:127.0.0.1' serverAuth
issue controller-a URI:spiffe://storage.example.cloud/controller/cluster-a clientAuth
issue controller-b URI:spiffe://storage.example.cloud/controller/cluster-b clientAuth
issue node-a URI:spiffe://storage.example.cloud/node/vm-a clientAuth
issue node-b URI:spiffe://storage.example.cloud/node/vm-b clientAuth
