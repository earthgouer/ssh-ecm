#!/usr/bin/env bash
# 非交互最小验证集。交互 shell / 选择器 / su 请按提示手工验证。
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$DIR/env.sh"

PASS=0
FAIL=0
OUT="$(mktemp)"
SF_LOCAL="$(mktemp)"
SF_DOWN="$(mktemp)"
SF_JUMP="$(mktemp)"
SF_DIR="$(mktemp -d)"
SF_OUT="$(mktemp -d)"
trap 'rm -f "$OUT" "$SF_LOCAL" "$SF_DOWN" "$SF_JUMP"; rm -rf "$SF_DIR" "$SF_OUT"' EXIT

pass() { PASS=$((PASS + 1)); printf '  PASS  %s\n' "$1"; }
fail() {
  FAIL=$((FAIL + 1))
  printf '  FAIL  %s\n' "$1"
  [ -s "$OUT" ] && sed 's/^/        /' "$OUT" | tail -8
}

expect_ok() {
  local desc="$1"; shift
  if "$@" >"$OUT" 2>&1; then pass "$desc"; else fail "$desc"; fi
}
expect_out() {
  local desc="$1" want="$2"; shift 2
  if "$@" >"$OUT" 2>&1 && grep -qF -- "$want" "$OUT"; then pass "$desc"; else fail "$desc"; fi
}
expect_not_out() {
  local desc="$1" bad="$2"; shift 2
  if "$@" >"$OUT" 2>&1 && ! grep -qF -- "$bad" "$OUT"; then pass "$desc"; else fail "$desc"; fi
}
expect_code() {
  local desc="$1" want="$2"; shift 2
  "$@" >"$OUT" 2>&1
  local got=$?
  if [ "$got" -eq "$want" ]; then
    pass "$desc"
  else
    printf '        期望退出码 %s，实际 %s\n' "$want" "$got" >>"$OUT"
    fail "$desc"
  fi
}
expect_fail() {
  local desc="$1"; shift
  if "$@" >"$OUT" 2>&1; then fail "$desc"; else pass "$desc"; fi
}

echo "== 基础 =="
expect_out  "version 输出版本"                  "ssh-ecm v"      ssh-ecm version
expect_ok   "list 解析 servers.csv"                              ssh-ecm list
expect_out  "list 显示 CSV 标签/端口"            "test;direct"    ssh-ecm list
expect_out  "web1 -i 从 mock ECM 取到 IP"        "172.28.0.10"    ssh-ecm web1 -i
expect_out  "web1 -i 打印 ECM 返回的 root 密码"  "Root@Test123" ssh-ecm web1 -i
expect_not_out "web1 -v 不打印密码"              "Root@Test123" ssh-ecm -v web1 "hostname"

echo "== 直连 =="
expect_out  "web1 执行 hostname"                 "target"         ssh-ecm web1 "hostname"
expect_out  "web1 --no-jump 以 root 执行 whoami" "root"           ssh-ecm web1 --no-jump "whoami"
expect_code "远端退出码透传（exit 7）"           7                ssh-ecm web1 "exit 7"

echo "== 跳板 =="
expect_out  "db1 -j bastion 经跳板执行"          "target"         ssh-ecm db1 -j bastion "hostname"
expect_out  "web2 显式跳板"                     "target"         ssh-ecm web2 "hostname"
expect_out  "web3 空跳板列直连"                   "跳板：直连"      ssh-ecm -v web3 "hostname"
expect_out  "裸 IP 默认直连"                     "跳板：直连"      ssh-ecm -v 172.28.0.10 "hostname"

echo "== SFTP =="
printf 'sftp verify\n' >"$SF_LOCAL"
printf 'nested\n' >"$SF_DIR/nested.txt"
ssh-ecm rm -rf web1 /tmp/ecm-verify.txt /tmp/ecm-verify-jump.txt /tmp/ecm-verify-dir >/dev/null 2>&1 || true

expect_ok   "put 上传文件"                        ssh-ecm put web1 "$SF_LOCAL" /tmp/ecm-verify.txt
expect_out  "ls 列出远端文件"                      "ecm-verify.txt" ssh-ecm ls web1 /tmp
expect_ok   "get 下载文件"                        ssh-ecm get web1 /tmp/ecm-verify.txt "$SF_DOWN"
if cmp -s "$SF_LOCAL" "$SF_DOWN"; then
  pass "get 内容一致"
else
  printf '下载内容与上传内容不一致\n' >"$OUT"
  fail "get 内容一致"
fi

expect_ok   "跳板模式 put"                        ssh-ecm put db1 "$SF_LOCAL" /tmp/ecm-verify-jump.txt
expect_ok   "跳板模式 get"                        ssh-ecm get db1 /tmp/ecm-verify-jump.txt "$SF_JUMP"
if cmp -s "$SF_LOCAL" "$SF_JUMP"; then
  pass "跳板模式 get 内容一致"
else
  printf '跳板下载内容与上传内容不一致\n' >"$OUT"
  fail "跳板模式 get 内容一致"
fi

expect_ok   "put -r 上传目录"                     ssh-ecm put -r web1 "$SF_DIR" /tmp/ecm-verify-dir
expect_out  "ls 列出远端子文件"                    "nested.txt" ssh-ecm ls web1 /tmp/ecm-verify-dir
expect_ok   "get -r 下载目录"                     ssh-ecm get -r web1 /tmp/ecm-verify-dir "$SF_OUT"
if cmp -s "$SF_DIR/nested.txt" "$SF_OUT/nested.txt"; then
  pass "递归下载内容一致"
else
  printf '递归下载内容不一致\n' >"$OUT"
  fail "递归下载内容一致"
fi

expect_fail "get 目录未加 -r 应报错"              ssh-ecm get web1 /tmp/ecm-verify-dir
expect_fail "SFTP 命令不支持 -i"                  ssh-ecm ls web1 /tmp -i
expect_fail "SFTP 命令不支持 -u"                  ssh-ecm ls web1 /tmp -u app

expect_ok   "rm 删除文件"                        ssh-ecm rm web1 /tmp/ecm-verify.txt /tmp/ecm-verify-jump.txt
expect_ok   "rm -r 删除目录"                     ssh-ecm rm -r web1 /tmp/ecm-verify-dir

echo "== 安全与错误路径 =="
expect_out  "TOFU 已记录目标机指纹"              "172.28.0.10"    grep -F "172.28.0.10" "$SSH_ECM_CONFIG_DIR/known_hosts"
expect_out  "TOFU 已记录跳板机指纹"              "12223"          grep -F "12223" "$SSH_ECM_CONFIG_DIR/known_hosts"
expect_fail "未知目标查询失败"                                    ssh-ecm nope -i
expect_fail "-j 未指定别名应报错"                                ssh-ecm web1 -j

echo
echo "结果：$PASS 通过，$FAIL 失败"
[ "$FAIL" -eq 0 ]
