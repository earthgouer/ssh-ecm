// Package ecm 是 ECM 平台 HTTP API 的客户端。
//
// 请求地址、鉴权头、JSON 字段映射、密码生成规则全部来自 config.yaml 的 api 段
// （见 config.example.yaml），这里只负责"按配置执行"。不写任何可选项时的内置默认：
//
//	GET {api.url}/servers/{目标}
//	Header: Authorization: Bearer <token_env 指向的环境变量>
//	响应: JSON；字段名在下面的一组候选键里自动匹配，也兼容
//	      {"code":0,"data":{...}} / {"data":[...]} 之类的包装层。
package ecm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/ecm/ssh-ecm/internal/config"
	"github.com/ecm/ssh-ecm/internal/expr"
)

// defaultRequestPath 是未配置 api.request.path 时的请求路径模板。
const defaultRequestPath = "/servers/${urlescape(meta.target)}"

// ServerInfo 是登录一台服务器所需的全部信息。
type ServerInfo struct {
	IP       string
	Port     int    // 0 = 用配置默认
	Password string // root 密码，只在内存中流转，禁止落盘/打印（-i 模式除外）

	// Data 是 ECM 响应的完整 JSON（仅在内存中），供 --info 的自定义行取字段。
	// 注意：这是响应原文，带包装层时需要写 data.data.ip 这种路径。
	Data any
}

// Client 便于测试时注入假实现。
type Client interface {
	GetServer(idOrIP string) (*ServerInfo, error)
}

// DefaultClient 是配置驱动的 HTTP 实现。
type DefaultClient struct {
	base    string
	method  string
	pathTpl *expr.Template
	query   map[string]*expr.Template
	headers map[string]*expr.Template
	bodyTpl *expr.Template
	token   string
	http    *http.Client

	success   *expr.Expr // nil = 走内置的 code/success 判定
	successEq string
	errMsg    *expr.Expr

	exIP   *expr.Expr
	exPort *expr.Expr
	exPwd  *expr.Expr
}

var _ Client = (*DefaultClient)(nil)

// NewClient 根据 config.yaml 的 api 段创建客户端，并在创建时暴露配置错误。
func NewClient(cfg config.APIConfig) (*DefaultClient, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	pathSrc := strings.TrimSpace(cfg.Request.Path)
	if pathSrc == "" {
		pathSrc = defaultRequestPath
	}
	pathIsAbs := strings.HasPrefix(pathSrc, "http://") || strings.HasPrefix(pathSrc, "https://")

	if base == "" && !pathIsAbs {
		return nil, fmt.Errorf("尚未配置 ECM API 地址：请编辑 %s，填写真实的 api.url", config.ConfigPath())
	}
	if base != "" {
		if strings.Contains(base, "ecm.example.com") && !pathIsAbs {
			return nil, fmt.Errorf("尚未配置 ECM API 地址：请编辑 %s，把 api.url 改成真实地址", config.ConfigPath())
		}
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			return nil, fmt.Errorf("api.url 必须以 http:// 或 https:// 开头，当前为 %q", cfg.URL)
		}
	}

	hasAuthHeader := false
	for k := range cfg.Request.Headers {
		if strings.EqualFold(k, "Authorization") {
			hasAuthHeader = true
			break
		}
	}

	var token string
	if env := strings.TrimSpace(cfg.TokenEnv); env != "" {
		token = strings.TrimSpace(os.Getenv(env))
		if token == "" && !hasAuthHeader {
			fmt.Fprintf(os.Stderr, "警告：环境变量 %s 未设置（ECM API 可能需要 token）\n", env)
		}
	}

	c := &DefaultClient{
		base:    base,
		method:  "GET",
		token:   token,
		http:    &http.Client{Timeout: cfg.TimeoutDuration()},
		query:   map[string]*expr.Template{},
		headers: map[string]*expr.Template{},
	}
	if m := strings.ToUpper(strings.TrimSpace(cfg.Request.Method)); m != "" {
		c.method = m
	}

	var err error
	c.pathTpl, err = expr.ParseTemplate(pathSrc)
	if err != nil {
		return nil, fmt.Errorf("api.request.path 配置有误：%w", err)
	}
	for k, v := range cfg.Request.Query {
		tpl, err := expr.ParseTemplate(v)
		if err != nil {
			return nil, fmt.Errorf("api.request.query.%s 配置有误：%w", k, err)
		}
		c.query[k] = tpl
	}
	for k, v := range cfg.Request.Headers {
		tpl, err := expr.ParseTemplate(v)
		if err != nil {
			return nil, fmt.Errorf("api.request.headers.%s 配置有误：%w", k, err)
		}
		c.headers[k] = tpl
	}
	if strings.TrimSpace(cfg.Request.Body) != "" {
		c.bodyTpl, err = expr.ParseTemplate(cfg.Request.Body)
		if err != nil {
			return nil, fmt.Errorf("api.request.body 配置有误：%w", err)
		}
	}

	if src := strings.TrimSpace(cfg.Success.Path); src != "" {
		c.success, err = expr.Compile(src)
		if err != nil {
			return nil, fmt.Errorf("api.success.path 配置有误：%w", err)
		}
		c.successEq = strings.TrimSpace(cfg.Success.Equals)
	}
	if src := strings.TrimSpace(cfg.ErrorMessage); src != "" {
		c.errMsg, err = expr.Compile(src)
		if err != nil {
			return nil, fmt.Errorf("api.error_message 配置有误：%w", err)
		}
	}
	if c.exIP, err = compileOptional("api.extract.ip", cfg.Extract.IP); err != nil {
		return nil, err
	}
	if c.exPort, err = compileOptional("api.extract.port", cfg.Extract.Port); err != nil {
		return nil, err
	}
	if c.exPwd, err = compileOptional("api.extract.password", cfg.Extract.Password); err != nil {
		return nil, err
	}
	return c, nil
}

func compileOptional(key, src string) (*expr.Expr, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, nil
	}
	e, err := expr.Compile(src)
	if err != nil {
		return nil, fmt.Errorf("%s 配置有误：%w", key, err)
	}
	return e, nil
}

// GetServer 查询一台服务器的 IP / 端口 / root 密码。
func (c *DefaultClient) GetServer(idOrIP string) (*ServerInfo, error) {
	idOrIP = strings.TrimSpace(idOrIP)
	if idOrIP == "" {
		return nil, fmt.Errorf("查询键为空")
	}

	// 请求渲染阶段：可以读 env.*（token/租户等），但只有 meta.target 可用。
	reqEnv := expr.Env{Meta: map[string]any{"target": idOrIP}, AllowEnv: true}

	endpoint, err := c.buildURL(reqEnv)
	if err != nil {
		return nil, err
	}

	var body io.Reader
	if !c.bodyTpl.IsEmpty() {
		s, err := c.bodyTpl.Render(reqEnv)
		if err != nil {
			return nil, fmt.Errorf("渲染 api.request.body 失败：%w", err)
		}
		body = strings.NewReader(s)
	}

	req, err := http.NewRequest(c.method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, tpl := range c.headers {
		v, err := tpl.Render(reqEnv)
		if err != nil {
			return nil, fmt.Errorf("渲染请求头 %s 失败：%w", name, err)
		}
		req.Header.Set(name, v)
	}
	if c.token != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 ECM 失败（%s %s）：%w", c.method, endpoint, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 ECM 响应失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("ECM 接口返回 HTTP %d：%s", resp.StatusCode, snippet(bodyBytes))
	}

	var payload any
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return nil, fmt.Errorf("ECM 响应不是合法 JSON：%v（响应前 200 字：%s）", err, snippet(bodyBytes))
	}

	// 响应阶段用 data.* + meta.*；读取环境变量已关闭，避免敏感值被拼进密码/打印。
	dataEnv := expr.Env{Data: payload, Meta: reqEnv.Meta}

	if c.success != nil {
		v, err := c.success.Eval(dataEnv)
		if err != nil {
			return nil, fmt.Errorf("判定 api.success.path 失败：%w", err)
		}
		got := expr.Stringify(v)
		want := c.successEq
		if want == "" {
			want = "0"
		}
		if got != want {
			return nil, fmt.Errorf("ECM 返回失败（%s=%s）：%s", c.success.Source(), got, c.errorMessage(dataEnv, payload))
		}
	} else if err := legacySuccessCheck(payload); err != nil {
		return nil, err
	}

	info := &ServerInfo{Data: payload}
	if c.exIP != nil {
		s, err := c.exIP.EvalString(dataEnv)
		if err != nil {
			return nil, fmt.Errorf("求值 api.extract.ip 失败：%w", err)
		}
		info.IP = strings.TrimSpace(s)
	} else {
		info.IP = legacyPickIP(payload)
	}
	if info.IP == "" {
		return nil, fmt.Errorf("未能从 ECM 响应提取 IP：%s", extractionHint(c.exIP, "ip", payload))
	}

	if c.exPort != nil {
		p, err := c.exPort.EvalInt(dataEnv)
		if err != nil {
			return nil, fmt.Errorf("求值 api.extract.port 失败：%w", err)
		}
		if p > 0 && p <= 65535 {
			info.Port = p
		}
	} else {
		info.Port = legacyPickPort(payload)
	}

	if c.exPwd != nil {
		s, err := c.exPwd.EvalString(dataEnv)
		if err != nil {
			return nil, fmt.Errorf("求值 api.extract.password 失败：%w", err)
		}
		info.Password = s
		if s == "" {
			return nil, fmt.Errorf("未能从 ECM 响应提取 root 密码：%s", extractionHint(c.exPwd, "password", payload))
		}
	} else {
		info.Password = legacyPickPassword(payload)
	}
	return info, nil
}

// buildURL 渲染 path 模板并拼上 query。
func (c *DefaultClient) buildURL(env expr.Env) (string, error) {
	p, err := c.pathTpl.Render(env)
	if err != nil {
		return "", fmt.Errorf("渲染 api.request.path 失败：%w", err)
	}
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("api.request.path 渲染结果为空（模板里检查 ${...} 是否正确）")
	}

	full := p
	if !strings.HasPrefix(p, "http://") && !strings.HasPrefix(p, "https://") {
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		full = c.base + p
	}
	u, err := url.Parse(full)
	if err != nil {
		return "", fmt.Errorf("请求地址非法（%s）：%w", full, err)
	}
	if len(c.query) > 0 {
		q := u.Query()
		for k, tpl := range c.query {
			v, err := tpl.Render(env)
			if err != nil {
				return "", fmt.Errorf("渲染 api.request.query.%s 失败：%w", k, err)
			}
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// errorMessage 取失败原因：配置了 error_message 就用它，否则走内置候选键。
func (c *DefaultClient) errorMessage(env expr.Env, payload any) string {
	if c.errMsg != nil {
		if s, err := c.errMsg.EvalString(env); err == nil && s != "" {
			return s
		}
		return "（api.error_message 未取到内容）"
	}
	if m, ok := payload.(map[string]any); ok {
		if s := firstMessage(m); s != "" {
			return s
		}
	}
	return "（响应未提供错误信息）"
}

func extractionHint(e *expr.Expr, field string, payload any) string {
	if e != nil {
		return fmt.Sprintf("表达式 %s 求值为空（响应顶层键：%s）", e.Source(), topKeys(payload))
	}
	return fmt.Sprintf("内置候选键都没找到 %s 字段（响应顶层键：%s），请在 config.yaml 配置 api.extract.%s",
		field, topKeys(payload), field)
}

func topKeys(payload any) string {
	m, ok := payload.(map[string]any)
	if !ok {
		if _, isArr := payload.([]any); isArr {
			return "（响应是数组）"
		}
		return "（响应不是对象）"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return "（空对象）"
	}
	return strings.Join(keys, ", ")
}

// ---------------- 内置默认：业务成功判定与字段自动识别 ----------------

func legacySuccessCheck(payload any) error {
	m, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	if code, ok := asInt(m["code"]); ok && code != 0 {
		return fmt.Errorf("ECM 返回错误码 %d：%s", code, messageOrFallback(m))
	}
	if okFlag, ok := m["success"].(bool); ok && !okFlag {
		return fmt.Errorf("ECM 返回失败：%s", messageOrFallback(m))
	}
	return nil
}

// candidateObjs 返回可以找字段的对象：响应本体 + 常见包装层 + ssh 子对象。
func candidateObjs(payload any) []map[string]any {
	obj := unwrap(payload)
	if obj == nil {
		return nil
	}
	candidates := []map[string]any{obj}
	for _, k := range []string{"ssh", "server", "host", "instance"} {
		if child, ok := obj[k].(map[string]any); ok {
			candidates = append(candidates, child)
		}
	}
	return candidates
}

func legacyPickIP(payload any) string {
	return pickString(candidateObjs(payload),
		"ip", "ip_addr", "ipAddress", "private_ip", "inner_ip", "internal_ip", "host", "hostname", "address")
}

func legacyPickPort(payload any) int {
	p := pickInt(candidateObjs(payload), "port", "ssh_port", "sshPort", "sshport")
	if p > 0 && p <= 65535 {
		return p
	}
	return 0
}

func legacyPickPassword(payload any) string {
	return pickString(candidateObjs(payload),
		"password", "passwd", "pwd", "root_password", "rootPassword", "root_pass", "rootPwd", "root_pwd")
}

// unwrap 逐层剥掉 {"data":...} / {"result":...} 之类的包装，返回第一个对象。
func unwrap(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range []string{"data", "result", "server", "item", "records"} {
			if child, ok := t[k]; ok {
				if m := unwrap(child); m != nil {
					return m
				}
			}
		}
		return t
	case []any:
		for _, item := range t {
			if m := unwrap(item); m != nil {
				return m
			}
		}
	}
	return nil
}

func pickString(objs []map[string]any, keys ...string) string {
	for _, o := range objs {
		for _, k := range keys {
			v, ok := o[k]
			if !ok || v == nil {
				continue
			}
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func pickInt(objs []map[string]any, keys ...string) int {
	for _, o := range objs {
		for _, k := range keys {
			if p, ok := asInt(o[k]); ok && p > 0 && p < 65536 {
				return p
			}
		}
	}
	return 0
}

// asInt 兼容 JSON 数字、数字字符串。
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case string:
		if p, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return p, true
		}
	}
	return 0, false
}

// firstMessage 从响应里找出最能说明问题的一句话。
func firstMessage(m map[string]any) string {
	for _, k := range []string{"msg", "message", "error", "err", "detail"} {
		if s := pickString([]map[string]any{m}, k); s != "" {
			return s
		}
	}
	return ""
}

func messageOrFallback(m map[string]any) string {
	if s := firstMessage(m); s != "" {
		return s
	}
	return "（响应未提供错误信息）"
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	if s == "" {
		return "(空)"
	}
	return s
}
