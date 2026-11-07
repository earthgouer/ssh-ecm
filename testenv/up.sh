#!/usr/bin/env bash
# 拉起本地测试环境：目标机 + 跳板机（Docker）+ 假 ECM API（宿主机 Python）。
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN="$DIR/run"
NET="ssh-ecm-test"
IMAGE="ssh-ecm-test-sshd:local"
TARGET_NAME="ssh-ecm-target"
JUMP_NAME="ssh-ecm-jump"
TARGET_IP="172.28.0.10"
JUMP_IP="172.28.0.11"
TARGET_PASSWORD="Root@Test123"
JUMP_PASSWORD="Jump@Test123"
ECM_TOKEN_VALUE="test-token"

for c in docker python3 go; do
  command -v "$c" >/dev/null || { echo "缺少 $c，无法创建测试环境" >&2; exit 1; }
done

mkdir -p "$RUN"

echo ">> 构建 sshd 测试镜像"
# 本机容器内 IPv6 不可达，而 dl-cdn 的 AAAA 会先被解析到；把 A 记录注入构建容器的
# /etc/hosts，强制 apk 走 IPv4，否则 apk add 会长时间卡住。
APK_HOST="mirrors.aliyun.com"
APK_IP4="$(getent ahostsv4 "$APK_HOST" 2>/dev/null | awk 'NR==1{print $1}')"
BUILD_ARGS=()
if [ -n "$APK_IP4" ]; then
  BUILD_ARGS+=(--add-host "$APK_HOST:$APK_IP4")
  echo "   apk 源使用 IPv4：$APK_IP4"
fi
# BuildKit 在本机的出网会卡住，改用传统构建器（行为与 docker run 一致）。
DOCKER_BUILDKIT=0 docker build "${BUILD_ARGS[@]}" -q -t "$IMAGE" "$DIR" >/dev/null

echo ">> 准备 Docker 网络 $NET"
if ! docker network inspect "$NET" >/dev/null 2>&1; then
  docker network create --subnet 172.28.0.0/24 "$NET" >/dev/null
fi

echo ">> 重建目标机与跳板机容器"
docker rm -f "$TARGET_NAME" "$JUMP_NAME" >/dev/null 2>&1 || true
docker run -d --name "$TARGET_NAME" --network "$NET" --ip "$TARGET_IP" \
  --hostname target -e ROOT_PASSWORD="$TARGET_PASSWORD" "$IMAGE" >/dev/null
docker run -d --name "$JUMP_NAME" --network "$NET" --ip "$JUMP_IP" \
  --hostname bastion -p 127.0.0.1:12223:22 -e ROOT_PASSWORD="$JUMP_PASSWORD" "$IMAGE" >/dev/null

echo ">> 启动 mock ECM API（127.0.0.1:18080）"
if [ -f "$RUN/mock.pid" ]; then
  kill "$(cat "$RUN/mock.pid")" 2>/dev/null || true
  rm -f "$RUN/mock.pid"
fi
TARGET_IP="$TARGET_IP" TARGET_PASSWORD="$TARGET_PASSWORD" ECM_TOKEN="$ECM_TOKEN_VALUE" \
  nohup python3 "$DIR/mock_ecm.py" >"$RUN/mock.log" 2>&1 &
echo $! >"$RUN/mock.pid"

wait_port() {
  local host="$1" port="$2" i
  for i in $(seq 1 60); do
    if (echo >"/dev/tcp/$host/$port") 2>/dev/null; then
      return 0
    fi
    sleep 0.2
  done
  return 1
}

api_ready() {
  python3 - "$ECM_TOKEN_VALUE" "$TARGET_IP" <<'PY'
import sys, urllib.request
req = urllib.request.Request(
    "http://127.0.0.1:18080/api/v1/servers/" + sys.argv[2],
    headers={"Authorization": "Bearer " + sys.argv[1]},
)
try:
    with urllib.request.urlopen(req, timeout=1) as resp:
        sys.exit(0 if resp.status == 200 else 1)
except Exception:
    sys.exit(1)
PY
}

echo ">> 等待服务就绪"
wait_port "$TARGET_IP" 22 || { echo "目标机 sshd 未就绪" >&2; exit 1; }
wait_port 127.0.0.1 12223 || { echo "跳板机 sshd 未就绪" >&2; exit 1; }
for _ in $(seq 1 50); do
  api_ready && break
  sleep 0.2
done
api_ready || { echo "mock ECM 未就绪，日志：" >&2; tail -20 "$RUN/mock.log" >&2; exit 1; }

echo ">> 编译当前源码"
(cd "$DIR/.." && CGO_ENABLED=0 go build -trimpath -o "$RUN/ssh-ecm" ./cmd/ssh-ecm)

# 每次重建容器都会换主机密钥，清掉旧的 TOFU 记录，避免第一次连接就报指纹不一致。
rm -f "$DIR/config/known_hosts"

cat >"$DIR/env.sh" <<EOF
# 由 testenv/up.sh 生成，source 后即可使用
export SSH_ECM_CONFIG_DIR="$DIR/config"
export ECM_TOKEN="$ECM_TOKEN_VALUE"
export BASTION_PASSWORD="$JUMP_PASSWORD"
export PATH="$RUN:\$PATH"
EOF
chmod 600 "$DIR/env.sh" "$DIR/config/config.yaml"

cat <<EOF

环境已就绪。使用方式：

  source testenv/env.sh
  ./testenv/verify.sh        # 非交互最小验证集
  ssh-ecm web1               # 交互式手工验证

测试资产：
  目标机    $TARGET_NAME  $TARGET_IP:22       root 密码 $TARGET_PASSWORD
  跳板机    $JUMP_NAME 127.0.0.1:12223     root 密码 $JUMP_PASSWORD
  mock ECM  http://127.0.0.1:18080/api/v1/servers/{web1|db1}
  token     $ECM_TOKEN_VALUE
  配置目录  $DIR/config
EOF
