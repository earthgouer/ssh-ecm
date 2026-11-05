// Package config 负责 ssh-ecm 的配置加载：
//
//	~/.config/ssh-ecm/config.yaml    全局项（API、默认跳板、跳板机定义）
//	~/.config/ssh-ecm/servers.txt    服务器清单（行式，Excel 友好）
//	~/.config/ssh-ecm/known_hosts    TOFU 主机指纹
//
// 目录可用 SSH_ECM_CONFIG_DIR 覆盖，便于多套配置/测试切换。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ecm/ssh-ecm/internal/expr"
)

const (
	// EnvConfigDir 覆盖默认配置目录。
	EnvConfigDir = "SSH_ECM_CONFIG_DIR"

	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// Duration 让 YAML 可以直接写 "5s"、"500ms"（yaml.v3 原生不支持 time.Duration）。
type Duration time.Duration

// UnmarshalYAML 支持字符串与整数（整数按秒处理）。
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			*d = 0
			return nil
		}
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("非法的时间格式 %q（示例：5s、500ms）", s)
		}
		*d = Duration(v)
		return nil
	}
	var i int64
	if err := n.Decode(&i); err != nil {
		return fmt.Errorf("非法的时间格式（示例：5s、500ms）")
	}
	*d = Duration(time.Duration(i) * time.Second)
	return nil
}

// APIConfig 对应 config.yaml 的 api 段。
type APIConfig struct {
	URL      string   `yaml:"url"`
	TokenEnv string   `yaml:"token_env"`
	Timeout  Duration `yaml:"timeout"`

	// 以下均为可选：不写就使用内置的默认行为（GET {url}/servers/{目标} + 自动猜字段）。
	Request      RequestConfig `yaml:"request"`
	Success      SuccessConfig `yaml:"success"`
	ErrorMessage string        `yaml:"error_message"`
	Extract      ExtractConfig `yaml:"extract"`
	Info         InfoConfig    `yaml:"info"`
}

// RequestConfig 描述怎么调 ECM 接口。
type RequestConfig struct {
	Method  string            `yaml:"method"`  // 默认 GET
	Path    string            `yaml:"path"`    // 支持 ${表达式}；绝对 URL 则忽略 api.url
	Query   map[string]string `yaml:"query"`   // 值支持 ${表达式}
	Headers map[string]string `yaml:"headers"` // 值支持 ${表达式}
	Body    string            `yaml:"body"`    // 支持 ${表达式}；留空表示无 body
}

// SuccessConfig 判定业务成功：把 path 求值后与 equals 比较（equals 为空按 "0"）。
type SuccessConfig struct {
	Path   string `yaml:"path"`
	Equals string `yaml:"equals"`
}

// ExtractConfig 从响应 JSON 里取服务器信息，字段值都是表达式。
// 某个字段留空时，该字段退回内置的候选键自动识别。
type ExtractConfig struct {
	IP       string `yaml:"ip"`
	Port     string `yaml:"port"`
	Password string `yaml:"password"`
}

// InfoConfig 自定义 --info 打印的行；配置了就整体替换默认 8 行。
type InfoConfig struct {
	Rows []InfoRowConfig `yaml:"rows"`
}

// InfoRowConfig 是一行 "标签 + 表达式"。
type InfoRowConfig struct {
	Label string `yaml:"label"`
	Value string `yaml:"value"`
}

// TimeoutDuration 返回解析后的超时，未配置时 5s。
func (a APIConfig) TimeoutDuration() time.Duration {
	if a.Timeout <= 0 {
		return 5 * time.Second
	}
	return time.Duration(a.Timeout)
}

var allowedMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// validate 在加载配置时就把表达式/模板的语法错误指出来，避免登录到一半才炸。
func (a *APIConfig) validate() error {
	if m := strings.ToUpper(strings.TrimSpace(a.Request.Method)); m != "" {
		if !allowedMethods[m] {
			return fmt.Errorf("api.request.method 不支持 %q（可用：GET/POST/PUT/PATCH/DELETE）", a.Request.Method)
		}
		a.Request.Method = m
	}

	checkTemplate := func(key, src string) error {
		if strings.TrimSpace(src) == "" {
			return nil
		}
		if _, err := expr.ParseTemplate(src); err != nil {
			return fmt.Errorf("%s 配置有误：%w", key, err)
		}
		return nil
	}
	if err := checkTemplate("api.request.path", a.Request.Path); err != nil {
		return err
	}
	if err := checkTemplate("api.request.body", a.Request.Body); err != nil {
		return err
	}
	for k, v := range a.Request.Query {
		if err := checkTemplate("api.request.query."+k, v); err != nil {
			return err
		}
	}
	for k, v := range a.Request.Headers {
		if err := checkTemplate("api.request.headers."+k, v); err != nil {
			return err
		}
	}

	// extract / info 不允许读环境变量，避免把 token 拼到密码或打印出来。
	checkExpr := func(key, src string) error {
		if strings.TrimSpace(src) == "" {
			return nil
		}
		if strings.Contains(src, "${") {
			return fmt.Errorf("%s 直接写表达式即可（例如 data.ip），不需要 ${}；${} 只用于 api.request 的 path/query/headers/body", key)
		}
		e, err := expr.Compile(src)
		if err != nil {
			return fmt.Errorf("%s 配置有误：%w", key, err)
		}
		if e.RefsEnv() {
			return fmt.Errorf("%s 不允许使用 env.*（只有 api.request 里可以读取环境变量）", key)
		}
		return nil
	}
	if err := checkExpr("api.success.path", a.Success.Path); err != nil {
		return err
	}
	if err := checkExpr("api.error_message", a.ErrorMessage); err != nil {
		return err
	}
	for key, src := range map[string]string{
		"api.extract.ip":       a.Extract.IP,
		"api.extract.port":     a.Extract.Port,
		"api.extract.password": a.Extract.Password,
	} {
		if err := checkExpr(key, src); err != nil {
			return err
		}
	}

	for i, row := range a.Info.Rows {
		if strings.TrimSpace(row.Label) == "" {
			return fmt.Errorf("api.info.rows[%d] 缺少 label", i)
		}
		if strings.TrimSpace(row.Value) == "" {
			return fmt.Errorf("api.info.rows[%d]（%s）缺少 value 表达式", i, row.Label)
		}
		if err := checkExpr(fmt.Sprintf("api.info.rows[%d].value", i), row.Value); err != nil {
			return err
		}
	}
	return nil
}

// DefaultsConfig 对应 config.yaml 的 defaults 段。
type DefaultsConfig struct {
	Jump string `yaml:"jump"`
	Port int    `yaml:"port"`
}

// JumpConfig 是一台跳板机的定义。
type JumpConfig struct {
	Name string `yaml:"-"`
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	Key  string `yaml:"key"`
}

// AliasConfig 是 config.yaml 里兼容保留的 aliases 段条目。
type AliasConfig struct {
	Target string `yaml:"target"`
	Port   int    `yaml:"port"`
	SuUser string `yaml:"su_user"`
	Jump   string `yaml:"jump"`
	NoJump bool   `yaml:"no_jump"`
	Note   string `yaml:"note"`
}

// Config 是 config.yaml 的内存表示。
type Config struct {
	API      APIConfig              `yaml:"api"`
	Defaults DefaultsConfig         `yaml:"defaults"`
	Jumps    map[string]JumpConfig  `yaml:"jumps"`
	Aliases  map[string]AliasConfig `yaml:"aliases"`

	// Servers 由 LoadServers 填充（servers.txt 解析结果），不来自 YAML。
	Servers []ServerEntry `yaml:"-"`
}

// Target 是"别名/命令行目标"解析后的结果。
type Target struct {
	Alias     string // 命中的别名（未命中时等于用户输入）
	IDOrIP    string // 交给 ECM API 的查询键：ECM ID 或 IPv4
	Port      int    // 0 = 稍后按 API/配置/22 决定
	SuUser    string // 登录后 su - <user>；空表示不切换
	NoSu      bool   // servers.txt 里 su 列写了 "-"（显式不切换）
	JumpAlias string // 跳板别名（已合并 defaults.jump）
	NoJump    bool   // 强制直连（servers.txt 跳板列写了 "-"，或 --no-jump）
	Note      string
	Source    string // 该条目的来源描述，用于 -v / 报错
}

// ConfigDir 返回配置目录：$SSH_ECM_CONFIG_DIR 或 ~/.config/ssh-ecm。
func ConfigDir() string {
	if d := strings.TrimSpace(os.Getenv(EnvConfigDir)); d != "" {
		return expandHome(d)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".config", "ssh-ecm")
}

// ConfigPath 返回 config.yaml 绝对路径。
func ConfigPath() string { return filepath.Join(ConfigDir(), "config.yaml") }

// ServersPath 返回 servers.txt 绝对路径。
func ServersPath() string { return filepath.Join(ConfigDir(), "servers.txt") }

// KnownHostsPath 返回本工具专用的 known_hosts 路径。
func KnownHostsPath() string { return filepath.Join(ConfigDir(), "known_hosts") }

// EnsureDir 创建配置目录（0700）。
func EnsureDir() error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("创建配置目录 %s 失败：%w", dir, err)
	}
	// MkdirAll 不会修正已存在目录的权限，这里尽量收敛。
	if err := os.Chmod(dir, dirPerm); err != nil {
		return nil // 只读挂载等场景下不阻断
	}
	return nil
}

const configTemplate = `# ssh-ecm 全局配置（服务器清单请编辑同目录下的 servers.txt）
api:
  url: https://ecm.example.com/api/v1   # 改成真实的 ECM API 地址
  token_env: ECM_TOKEN                  # token 从该环境变量读取（export ECM_TOKEN=...）
  timeout: 5s

  # ↓↓↓ 以下是"对接 ECM 接口"的可选项，全部注释掉时走内置默认：
  #     GET {url}/servers/{目标}，自动从常见字段名里取 ip / 端口 / root 密码
  #
  # request:                            # 怎么调接口
  #   method: GET                       # GET/POST/PUT/PATCH/DELETE
  #   path: /servers/${urlescape(meta.target)}   # 支持 ${表达式}；写绝对 URL 则忽略上面的 url
  #   query:                            # 值支持 ${表达式}
  #     id: ${meta.target}
  #   headers:
  #     Content-Type: application/json
  #     X-Tenant: ${env.ECM_TENANT}     # 只有这里能读 env.*
  #   body: '{"server_id":"${meta.target}"}'     # POST 时的请求体
  #
  # success:                            # 业务成功判定（不写就只看 HTTP 状态码 + code/success）
  #   path: code
  #   equals: "0"
  # error_message: msg                  # 失败时取这句话
  #
  # extract:                            # 从响应 JSON 取服务器信息（表达式，留空的字段回退到自动识别）
  #   ip: data.ip
  #   port: data.ssh_port
  #   password: data.root_password      # 也支持截取/拼接，见下方 info 示例
  #
  # info:                               # 自定义 --info 打印的行（写了就整体替换默认 8 行）
  #   rows:
  #     - {label: 别名,     value: meta.alias}
  #     - {label: ECM ID,   value: meta.target}
  #     - {label: IP,       value: meta.ip}
  #     - {label: 端口,     value: meta.port}
  #     - {label: 跳板,     value: meta.jump}
  #     - {label: 机房,     value: data.idc}          # 直接取响应 JSON 字段（裸写 idc 也行）
  #     - {label: 备注,     value: meta.note}
  #     - {label: root密码, value: meta.password}
  #
  # extract/info 里直接写表达式（不要加 ${}，${} 只用于上面 request 的 path/query/headers/body）。
  # 表达式可用：data.*（响应 JSON，可简写成裸字段名）、
  #            meta.*（alias/target/ip/port/jump/su/note/password）
  # 函数：substr/trim/upper/lower/replace/default/urlencode/urlescape
  # 例：password: substr(data.root_password, 0, 8) + "@2026"

defaults:
  # jump: bastion                       # 默认跳板机别名；不需要默认跳板就保持注释
  port: 22

jumps:
  # bastion:
  #   host: 10.10.0.1
  #   port: 22
  #   user: myname
  #   key: ~/.ssh/id_rsa                # 缺省用 ~/.ssh/id_rsa / id_ed25519 + ssh-agent
`

const serversTemplate = `# 别名	目标(ECM ID 或 IP)	su用户	跳板	备注
# 一行一台，Tab 分隔（可直接从 Excel 复制粘贴）；# 开头的整行是注释
# 第 3 列留空 = 不切换用户，写 - = 显式不切换
# 第 4 列留空 = 用 config.yaml 的 defaults.jump，写 - = 强制直连
# 示例：
# web1	10.20.30.40	app	bastion	生产 web 前端
`

// EnsureServersFile 首次运行创建 servers.txt（已存在则不动）。
func EnsureServersFile() error {
	if err := EnsureDir(); err != nil {
		return err
	}
	return ensureFile(ServersPath(), serversTemplate)
}

func ensureFile(path, tpl string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查文件 %s 失败：%w", path, err)
	}
	if err := os.WriteFile(path, []byte(tpl), filePerm); err != nil {
		return fmt.Errorf("创建文件 %s 失败：%w", path, err)
	}
	return nil
}

// Load 读取配置文件；首次运行会自动创建目录与模板文件。
func Load(cfgPath string) (*Config, error) {
	if strings.TrimSpace(cfgPath) == "" {
		cfgPath = ConfigPath()
	}
	if err := EnsureDir(); err != nil {
		return nil, err
	}
	if err := ensureFile(cfgPath, configTemplate); err != nil {
		return nil, err
	}
	if err := EnsureServersFile(); err != nil {
		return nil, err
	}
	warnLoosePerm(cfgPath)

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s 失败：%w", cfgPath, err)
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s 失败：%w", cfgPath, err)
	}
	if cfg.Jumps == nil {
		cfg.Jumps = map[string]JumpConfig{}
	}
	if cfg.Aliases == nil {
		cfg.Aliases = map[string]AliasConfig{}
	}
	for name, j := range cfg.Jumps {
		if strings.TrimSpace(j.Host) == "" {
			return nil, fmt.Errorf("配置 %s 的 jumps.%s 缺少 host", cfgPath, name)
		}
		if j.Port < 0 || j.Port > 65535 {
			return nil, fmt.Errorf("配置 %s 的 jumps.%s 端口非法：%d", cfgPath, name, j.Port)
		}
	}
	for name, a := range cfg.Aliases {
		if strings.TrimSpace(a.Target) == "" {
			return nil, fmt.Errorf("配置 %s 的 aliases.%s 缺少 target（ECM ID 或 IP）", cfgPath, name)
		}
	}
	if err := cfg.API.validate(); err != nil {
		return nil, fmt.Errorf("配置 %s：%w", cfgPath, err)
	}
	return cfg, nil
}

// LoadServersInto 解析 servers.txt 并填充到 c.Servers。
func (c *Config) LoadServersInto() error {
	entries, err := LoadServers(ServersPath())
	if err != nil {
		return err
	}
	c.Servers = entries
	return nil
}

// ResolveTarget 解析目标（别名 → 目标字段），并按 servers.txt 单行 + config defaults 合并。
//
// 合并顺序：调用方后续再叠加命令行 flag（flag > 单行 > defaults）。
func (c *Config) ResolveTarget(name string) (*Target, *JumpConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil, fmt.Errorf("目标不能为空")
	}
	tg := &Target{Alias: name}

	if e := c.findServer(name); e != nil {
		tg.IDOrIP = e.Target
		tg.SuUser = e.SuUser
		tg.NoSu = e.NoSu
		tg.Note = e.Note
		tg.JumpAlias = e.Jump
		tg.NoJump = e.NoJump
		tg.Source = fmt.Sprintf("servers.txt 第 %d 行", e.Line)
	} else if c.hasAlias(name) {
		a := c.Aliases[name]
		tg.IDOrIP = a.Target
		tg.Port = a.Port
		tg.SuUser = normalizeDash(a.SuUser)
		tg.JumpAlias = normalizeDash(a.Jump)
		tg.NoJump = a.NoJump
		tg.Note = a.Note
		tg.Source = "config.yaml 的 aliases 段"
	} else {
		tg.IDOrIP = name
		tg.Source = "命令行参数"
	}

	// 跳板列留空 = 用 defaults.jump；跳板列写 "-" = 强制直连（NoJump）。
	if !tg.NoJump && tg.JumpAlias == "" {
		tg.JumpAlias = strings.TrimSpace(c.Defaults.Jump)
	}

	jump, err := c.ResolveJump(tg.JumpAlias)
	if err != nil {
		return nil, nil, err
	}
	return tg, jump, nil
}

func (c *Config) findServer(alias string) *ServerEntry {
	for i := range c.Servers {
		if c.Servers[i].Alias == alias {
			return &c.Servers[i]
		}
	}
	return nil
}

func (c *Config) hasAlias(alias string) bool {
	_, ok := c.Aliases[alias]
	return ok
}

// ResolveJump 按别名取跳板机定义；空别名表示直连。
func (c *Config) ResolveJump(alias string) (*JumpConfig, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return nil, nil
	}
	j, ok := c.Jumps[alias]
	if !ok {
		return nil, fmt.Errorf("找不到跳板机 %q：请先在 %s 的 jumps 段定义该别名", alias, ConfigPath())
	}
	res := j
	res.Name = alias
	if res.Port == 0 {
		res.Port = 22
	}
	if strings.TrimSpace(res.User) == "" {
		res.User = CurrentUser()
	}
	res.Key = expandHome(res.Key)
	return &res, nil
}

// CurrentUser 返回本机当前用户名（跳板机默认用户）。
func CurrentUser() string {
	for _, k := range []string{"USER", "LOGNAME"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return filepath.Base(home)
	}
	return "root"
}

func normalizeDash(s string) string {
	s = strings.TrimSpace(s)
	if s == "-" {
		return ""
	}
	return s
}

func warnLoosePerm(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if info.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "警告：%s 权限为 %04o，建议 chmod 600（文件里可能含敏感配置）\n", path, info.Mode().Perm())
	}
}

// expandHome 把开头的 ~ 替换为用户主目录（配置里的路径不依赖 shell）。
func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}
