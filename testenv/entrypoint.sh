#!/bin/sh
set -eu

: "${ROOT_PASSWORD:?必须通过 -e ROOT_PASSWORD=... 指定 root 密码}"
echo "root:${ROOT_PASSWORD}" | chpasswd

# su 测试用的普通用户
if ! id app >/dev/null 2>&1; then
  adduser -D -s /bin/sh app
fi

# 每次启动重新生成主机密钥，保证两个容器指纹不同，也方便验证 TOFU
rm -f /etc/ssh/ssh_host_*
ssh-keygen -A >/dev/null
mkdir -p /run/sshd

exec /usr/sbin/sshd -D -e
