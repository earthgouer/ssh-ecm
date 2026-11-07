#!/usr/bin/env python3
"""本地假 ECM API：模拟 GET /api/v1/servers/{目标}。

查询键使用真实 IP（与生产用法一致），返回 ip / ssh_port / root_password。
"""
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import unquote, urlparse

TARGET_IP = os.environ.get("TARGET_IP", "172.28.0.10")
TARGET_PORT = int(os.environ.get("TARGET_PORT", "22"))
TARGET_PASSWORD = os.environ.get("TARGET_PASSWORD", "Root@Test123")
TOKEN = os.environ.get("ECM_TOKEN", "")
HOST = os.environ.get("MOCK_HOST", "127.0.0.1")
PORT = int(os.environ.get("MOCK_PORT", "18080"))

# ECM 只按真实 IP 查询，不识别别名或主机名。
SERVERS = {TARGET_IP: "target-01"}


class Handler(BaseHTTPRequestHandler):
    server_version = "mock-ecm/1.0"

    def log_message(self, fmt, *args):
        sys.stderr.write("[mock-ecm] %s %s\n" % (self.address_string(), fmt % args))

    def reply(self, status, obj):
        data = json.dumps(obj, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if TOKEN and self.headers.get("Authorization", "") != "Bearer " + TOKEN:
            self.reply(401, {"code": 401, "msg": "invalid token"})
            return

        path = urlparse(self.path).path
        for prefix in ("/api/v1/servers/", "/servers/"):
            if path.startswith(prefix):
                target = unquote(path[len(prefix):])
                break
        else:
            self.reply(404, {"code": 404, "msg": "not found"})
            return

        server_id = SERVERS.get(target)
        if server_id is None:
            self.reply(404, {"code": 404, "msg": "server %s not found" % target})
            return

        self.reply(200, {
            "code": 0,
            "msg": "ok",
            "data": {
                "id": server_id,
                "ip": TARGET_IP,
                "ssh_port": TARGET_PORT,
                "root_password": TARGET_PASSWORD,
                "idc": "本地 Docker 测试机房",
            },
        })


if __name__ == "__main__":
    print("[mock-ecm] listening on http://%s:%d" % (HOST, PORT), file=sys.stderr)
    ThreadingHTTPServer((HOST, PORT), Handler).serve_forever()
