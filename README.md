# ssh-ecm

在麒麟 V10 桌面终端一键 root 登录内网服务器：IP 与 root 密码实时取自 ECM 平台 HTTP API，
密码不落盘；需要时可经跳板机（本机到跳板机已配置密钥免密）登录。

完全自己实现 SSH 协议 + 真 PTY，不依赖 expect / sshpass / 系统 ssh 命令。
产物是单个静态二进制，无运行时依赖。

## 构建

```bash
./scripts/build.sh          # 产出 dist/ssh-ecm-linux-amd64 与 dist/ssh-ecm-linux-arm64
```

要求 Go ≥ 1.21（`CGO_ENABLED=0` 静态编译，麒麟 glibc 2.28 级别即可运行）。

## 首次运行

任意命令（如 `ssh-ecm list`）会自动创建：

```
~/.config/ssh-ecm/          0700
├── config.yaml             0600  全局配置（API、默认跳板、跳板机定义）
├── servers.txt             服务器清单（一行一台，Excel 友好）
└── known_hosts             TOFU 主机指纹（自动维护）
```

- 目录可用环境变量 `SSH_ECM_CONFIG_DIR` 覆盖，便于多套配置/测试切换。
- 任何文件都不保存密码、token。token 从 `api.token_env` 指定的环境变量读取。
- 本工具只使用自己的 `known_hosts`，不会污染 `~/.ssh/known_hosts`。

## config.yaml

```yaml
api:
  url: https://ecm.example.com/api/v1   # 改成真实地址
  token_env: ECM_TOKEN                  # export ECM_TOKEN=...
  timeout: 5s

defaults:
  jump: bastion                         # 默认跳板别名；不需要就删掉这行
  port: 22

jumps:
  bastion:
    host: 10.10.0.1
    port: 22
    user: myname
    key: ~/.ssh/id_rsa                  # 缺省用 ~/.ssh/id_rsa / id_ed25519 + ssh-agent
```

### 对接 ECM 接口（全部可选）

`api` 下的 `request / success / error_message / extract / info` 都不写时，走内置默认：
`GET {url}/servers/{目标}`，再从响应里自动识别 `ip / 端口 / root 密码`。
真实接口不一样时按下面配置，完整样例见仓库根目录的 `config.example.yaml`。

```yaml
api:
  url: https://ecm.example.com/api/v1
  token_env: ECM_TOKEN
  timeout: 5s

  request:                                  # 怎么调接口
    method: POST
    path: /server/detail
    query:
      from: ssh-ecm
    headers:
      X-Tenant: ${env.ECM_TENANT}           # 只有这里能读 env.*
    body: '{"server_id":"${meta.target}"}'

  success:                                  # 业务成功判定
    path: code
    equals: "0"
  error_message: msg

  extract:                                  # 从响应 JSON 取服务器信息
    ip: data.data.ip
    port: data.data.ssh_port
    password: 'substr(data.data.root_password, 0, 8) + "@2026"'

  info:                                     # 自定义 --info 打印的行
    rows:
      - {label: 机房, value: data.data.idc}
      - {label: root密码, value: meta.password}
```

表达式规则（mini DSL，无赋值/循环/分支）：

| 写法 | 含义 |
|---|---|
| `data.a.b` / `a.b` | 响应 JSON 字段（裸字段名等价于 `data.` 前缀），取不到为空不报错 |
| `data.items[0].ip` 或 `data.items.0.ip` | 数组下标 |
| `meta.alias/target/ip/port/jump/su/note/password` | 本地解析结果 |
| `env.NAME` | 环境变量，**仅** `api.request` 的 path/query/headers/body 可用 |
| `substr(v, start, len)` | 按字符截取（中文安全）；start 为负从末尾算，len 为负取到末尾 |
| `trim/upper/lower(v)`、`replace(v, old, new)` | 常规字符串处理 |
| `default(v, fallback)` | `v` 为空时用兜底值 |
| `urlencode(v)` / `urlescape(v)` | URL 编码 / 路径段编码 |
| `a + b` | 字符串拼接（数字会自动转文本） |

要点：

- `extract / info / success / error_message` 里**直接写表达式**，不要加 `${}`；
  `${}` 只用于 `request` 的 path/query/headers/body 插值。
- `info.rows` 写了就**整体替换**默认 8 行；如果没包含 `meta.password`，`--info` 会提示不会打印密码。
- 配置在加载时就会校验：表达式语法错误、`extract` 里用 `env.*`、表格行缺 label 等都会直接报错并指明是哪个键。

## servers.txt（服务器清单）

一行一台，列顺序固定：

| 列 | 含义 | 必填 |
|----|------|------|
| 1 | 别名（命令行/补全用的名字） | 是 |
| 2 | 目标：ECM 中的服务器 ID/名称，或 IPv4 | 是 |
| 3 | su 用户（登录后 `su - <user>`） | 否 |
| 4 | 跳板：跳板别名 / `-` / 空 | 否 |
| 5 及以后 | 备注（剩余全部内容，允许含空格） | 否 |

规则：

- 列分隔符优先级：Tab > 2 个及以上连续空格 > 单个空格（Excel 粘贴是 Tab，天然适配）。
- 空字段 = 用默认（su 不切换、跳板用 `defaults.jump`）；`-` = 显式没有
  （su 列 `-` 不切换，跳板列 `-` 强制直连）。
- `#` 开头的整行是注释，行尾 `#` 不算注释。
- 别名重复时后者覆盖前者并在 stderr 警告；列数不足会报错并带行号。
- 支持 CRLF 与 UTF-8 BOM（Windows/Excel 编辑过的文件不会踩坑）。

## Excel 使用流程

1. 在 Excel 里整理成 5 列：A 别名、B IP 或 ECM ID、C su 用户、D 跳板、E 备注。
2. 全选复制。
3. 粘贴到 `~/.config/ssh-ecm/servers.txt` 末尾（`ssh-ecm edit` 打开后粘贴保存）。
4. 执行 `ssh-ecm list` 校验解析结果。

Excel 空单元格粘贴过来是连续 Tab，即"空字段 = 用默认"，无需手工改 `-`。

## 常用命令

```bash
ssh-ecm web1                  # 登录（交互 shell，root）
ssh-ecm web1 -i               # 只查询并打印服务器信息（含 root 密码），不登录
ssh-ecm web1 -j               # 走默认跳板机
ssh-ecm web1 -j bastion       # 走指定跳板机
ssh-ecm web1 -J 10.1.1.1:22   # 临时指定跳板机地址
ssh-ecm web1 -u app           # 登录后自动 su - app
ssh-ecm web1 -p 2222          # 覆盖端口
ssh-ecm web1 --no-jump        # 强制直连
ssh-ecm web1 "hostname"       # 非交互执行命令，退出码 = 远端退出码
ssh-ecm -v web1               # 打印解析后的连接信息（不打印密码）
ssh-ecm                       # 无参数：上下键选服务器，回车连接（↑/↓ 或 j/k，q 退出）
ssh-ecm list                  # 列出全部服务器
ssh-ecm edit                  # 编辑 servers.txt（$EDITOR，默认 vi）
ssh-ecm completion bash       # 输出 bash 补全脚本
ssh-ecm version               # 版本与编译信息
```

注意：不带值的 `-j` 要放在命令末尾（`ssh-ecm web1 -j`）。`-j` 后面紧跟的非 flag 参数
一律视为跳板别名，避免"别名还是远端命令"的歧义。

无参数启动是全屏选择器：`↑/↓`（或 `j/k`）移动、`Home/End` 跳首尾、`Enter` 连接、
`/` 进入关键字过滤、`q`/`Esc`/`Ctrl-C` 退出；列表比屏幕长时自动滚动，中文按 2 列宽度对齐截断。

过滤规则：对**别名、目标、su 用户、跳板、备注**做大小写不敏感的包含匹配（支持中文），
输入时用 `Backspace` 删字符，`Esc` 清除过滤词并回到完整列表，`Enter` 直接连接当前选中项。
stdin/stdout 不是终端时（管道、脚本、CI）自动退化为编号输入，原有用法不受影响。

## 补全

```bash
source <(ssh-ecm completion bash)   # 加到 ~/.bashrc
```

`ssh-ecm <TAB>` 会列出 `servers.txt` 的全部别名（备注作为提示），每次按需重新读文件，
新增机器即时生效。

## 安全约定

- 密码只在内存中流转，不写入命令行参数、日志、任何文件。
- 唯一例外：`-i/--info` 查询模式按设计把 root 密码打印到 stdout（用于快速查密码）。
- `-v` 只打印 IP/端口/跳板链路，不打印密码。
- 主机指纹 TOFU：首次连接自动记录到 `~/.config/ssh-ecm/known_hosts`，指纹变化直接拒绝并
  提示当前指纹，防止中间人攻击。
