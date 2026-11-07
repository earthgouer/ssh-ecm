FROM alpine:3.20

# dl-cdn.alpinelinux.org 在本机经常卡住，换阿里云镜像
RUN sed -i 's|https://dl-cdn.alpinelinux.org|https://mirrors.aliyun.com|' /etc/apk/repositories \
 && apk add --no-cache openssh-server shadow bash

COPY sshd_config /etc/ssh/sshd_config
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

EXPOSE 22
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
