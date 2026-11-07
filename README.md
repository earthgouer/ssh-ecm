# ssh-ecm

在麒麟 V10 桌面终端一键 root 登录内网服务器：IP 与 root 密码实时取自 ECM 平台 HTTP API，
密码不落盘；需要时可经跳板机（本机到跳板机已配置密钥免密）登录。
支持交互 shell、非交互命令和 SFTP 文件传输（ls/get/put/rm），跳板模式下同样可用。

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
├── config.yaml             0600  全局配置（API、默认端口、跳板机定义）
├── servers.csv             服务器清单（CSV 带表头，Excel 友好）
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
  port: 22

jumps:
  bastion:
    host: 10.10.0.1
    port: 22
    user: myname
    key: ~/.ssh/id_rsa                  # 缺省用 ~/.ssh/id_rsa / id_ed25519 + ssh-agent
    password_env: BASTION_PASSWORD      # 跳板机只允许密码登录时用；不配则在终端提示输入
```

跳板机认证顺序：ssh-agent / `key` / `~/.ssh/id_{rsa,ed25519,ecdsa}` → 密码。密码只在公钥被拒、
服务器要求密码（含 keyboard-interactive）时才提示，不回显、不落盘；非交互场景请用 `password_env`。

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

## servers.csv（服务器清单）

CSV 带表头，列顺序任意，也支持中文表头：

```csv
alias,target,su,jump,port,note,tags
web1,10.20.30.40,app,bastion,22,生产 web 前端,prod;web
db1,10.20.30.40,,,22,生产数据库,prod;db
```

| 列 | 含义 | 必填 |
|----|------|------|
| alias | 别名（命令行/补全用的名字） | 是 |
| target | 目标：ECM 中的服务器 ID/名称，或 IPv4 | 是 |
| su | su 用户（登录后 `su - <user>`） | 否 |
| jump | 跳板：跳板别名 / `-` / 空 | 否 |
| port | 端口；留空用 ECM 返回或默认值，写了优先于 ECM | 否 |
| note | 备注；含逗号时要加双引号 | 否 |
| tags | 标签，多个用 `;` 或 `|` 分隔 | 否 |

规则：

- 第一行必须是表头；列顺序任意，列名大小写不敏感，支持中文（别名/目标/su用户/跳板/端口/备注/标签）。
- 不会自动走跳板：jump 列留空或写 `-` 都表示直连；要用跳板必须在 jump 列写 `jumps` 里的别名，或命令行 `-j 别名` / `-J host:port`。
- 其他空字段 = 用默认（su 不切换、端口用 ECM/默认）；su 列写 `-` 也是不切换。
- `#` 开头的整行是注释。
- 别名重复时后者覆盖前者并在 stderr 警告；缺列、未知列、端口非法都会报错并带行号。
- 支持 CRLF、UTF-8 BOM，以及逗号/分号/Tab 分隔（Excel 不同区域设置导出的 CSV 都能读）。
- 备注里有逗号时用双引号：`web1,10.0.0.1,,,22,"生产,web",prod;web`。

## Excel 使用流程

1. 用 Excel 打开 `~/.config/ssh-ecm/servers.csv`（或执行 `ssh-ecm edit`）。
2. 保留第一行表头，按列填 alias、target、su、jump、port、note、tags。
3. 另存为 **CSV UTF-8（逗号分隔）**。
4. 执行 `ssh-ecm list` 校验解析结果。

jump 空单元格表示直连；要用跳板就填 `jumps` 里的别名。su 空单元格表示不切换用户。

## 常用命令

```bash
ssh-ecm web1                  # 登录（交互 shell，root）
ssh-ecm web1 -i               # 只查询并打印服务器信息（含 root 密码），不登录
ssh-ecm web1 -j bastion       # 走指定跳板机（必须给别名）
ssh-ecm web1 -J 10.1.1.1:22   # 临时指定跳板机地址
ssh-ecm web1 -u app           # 登录后自动 su - app
ssh-ecm web1 -p 2222          # 覆盖端口
ssh-ecm web1 --no-jump        # 显式直连（默认行为）
ssh-ecm web1 "hostname"       # 非交互执行命令，退出码 = 远端退出码
ssh-ecm -v web1               # 打印解析后的连接信息（不打印密码）
ssh-ecm                       # 无参数：上下键选服务器，回车连接（↑/↓ 或 j/k，q 退出）
ssh-ecm list                  # 列出全部服务器
ssh-ecm ls web1 /tmp          # SFTP：列出远端目录
ssh-ecm get web1 /tmp/a ./a   # SFTP：下载文件
ssh-ecm put web1 ./a /tmp/a   # SFTP：上传文件
ssh-ecm rm web1 /tmp/a        # SFTP：删除远端文件
ssh-ecm edit                  # 编辑 servers.csv（$EDITOR，默认 vi）
ssh-ecm completion bash       # 输出 bash 补全脚本
ssh-ecm version               # 版本与编译信息
```

`-j` 必须跟跳板别名（例如 `ssh-ecm web1 -j bastion`）；没有默认跳板机，jump 留空的清单条目和裸 IP 都直连。

无参数启动是全屏选择器：`↑/↓`（或 `j/k`）移动、`Home/End` 跳首尾、`Enter` 连接、
`/` 进入关键字过滤、`q`/`Esc`/`Ctrl-C` 退出；列表比屏幕长时自动滚动，中文按 2 列宽度对齐截断。

过滤规则：对**别名、目标、su 用户、跳板、备注、标签**做大小写不敏感的包含匹配（支持中文），
输入时用 `Backspace` 删字符，`Esc` 清除过滤词并回到完整列表，`Enter` 直接连接当前选中项。
stdin/stdout 不是终端时（管道、脚本、CI）自动退化为编号输入，原有用法不受影响。

## SFTP 文件传输

`ls / get / put / rm` 复用同一套目标解析、ECM 查询和跳板链路；跳板模式不需要额外配置，`Dial` 返回的已经是到目标机的 SSH 连接。

```bash
ssh-ecm ls web1 /var/log                 # 列目录
ssh-ecm ls -l web1 /var/log              # 长格式（权限/大小/时间）
ssh-ecm get web1 /var/log/app.log ./     # 下载文件
ssh-ecm get -r web1 /var/log/app ./logs  # 递归下载目录
ssh-ecm put web1 ./app.conf /etc/app.conf
ssh-ecm put -r web1 ./dist /opt/app
ssh-ecm rm web1 /tmp/a /tmp/b
ssh-ecm rm -r web1 /tmp/dir
```

说明：

- 目标机需要启用 SFTP subsystem（常见配置是 `Subsystem sftp internal-sftp`）；跳板机只需要允许 TCP 转发。
- SFTP 会话始终以 root 身份运行，`-u/--su-user` 不适用（会直接报错）。
- `get` 不写本地路径时使用远端文件名；`put` 不写远端路径时使用本地文件名。
- `-r` 用于目录；`rm -f` 忽略不存在的路径。
- SFTP 命令不支持 `-i/--info`。

## 补全

```bash
source <(ssh-ecm completion bash)   # 加到 ~/.bashrc
```

`ssh-ecm <TAB>` 会列出 `servers.csv` 的全部别名（备注作为提示），每次按需重新读文件，
新增机器即时生效。

## 安全约定

- 密码只在内存中流转，不写入命令行参数、日志、任何文件。
- 唯一例外：`-i/--info` 查询模式按设计把 root 密码打印到 stdout（用于快速查密码）。
- `-v` 只打印 IP/端口/跳板链路，不打印密码。
- 主机指纹 TOFU：首次连接自动记录到 `~/.config/ssh-ecm/known_hosts`，指纹变化直接拒绝并
  提示当前指纹，防止中间人攻击。
