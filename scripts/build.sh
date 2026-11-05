#!/usr/bin/env bash
# 交叉编译 ssh-ecm：麒麟 V10（linux/amd64、linux/arm64），CGO 关闭保证静态无依赖。
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${VERSION:-v1.4.0}"
OUT_DIR="${OUT_DIR:-dist}"
MODULE="github.com/ecm/ssh-ecm"

mkdir -p "$OUT_DIR"

LDFLAGS="-s -w -X ${MODULE}/internal/cli.Version=${VERSION#v}"

for arch in amd64 arm64; do
	echo ">> building linux/${arch} ..."
	CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" \
		go build -trimpath -ldflags "${LDFLAGS}" \
		-o "${OUT_DIR}/ssh-ecm-linux-${arch}" ./cmd/ssh-ecm
done

echo ">> done:"
ls -lh "${OUT_DIR}"/ssh-ecm-linux-*
