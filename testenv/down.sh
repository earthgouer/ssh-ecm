#!/usr/bin/env bash
# 停止本地测试环境。加 --clean 一并删除网络、编译产物与生成的 known_hosts。
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN="$DIR/run"

docker rm -f ssh-ecm-target ssh-ecm-jump >/dev/null 2>&1 || true

if [ -f "$RUN/mock.pid" ]; then
  kill "$(cat "$RUN/mock.pid")" 2>/dev/null || true
  rm -f "$RUN/mock.pid"
fi

if [ "${1:-}" = "--clean" ]; then
  docker network rm ssh-ecm-test >/dev/null 2>&1 || true
  rm -rf "$RUN" "$DIR/env.sh" "$DIR/config/known_hosts"
  echo "已清理容器、网络、二进制与生成文件（镜像 ssh-ecm-test-sshd:local 保留）"
else
  echo "已停止容器与 mock ECM（保留网络与编译产物；彻底清理用 $0 --clean）"
fi
