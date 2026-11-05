// Package expr 是 ssh-ecm 用来适配 ECM 接口的极简表达式语言。
//
// 设计目标：让"取 JSON 字段 / 截取 / 拼接"这类适配逻辑写在配置里，而不是改代码。
// 语法刻意做得很小，没有变量赋值、循环、条件分支，配置里写不出任何可执行逻辑：
//
//	expr   := term ('+' term)*
//	term   := STRING | NUMBER | path | func '(' args ')'
//	path   := root ('.' 字段名 | '.' 数字 | '[' 数字 ']')*
//	root   := data | meta | env | 裸字段名（data. 的简写）
//
// 路径语义：
//
//	data.*   ECM 响应 JSON（对象/数组都能走，缺字段返回空，不报错）
//	meta.*   本地解析结果（alias/target/ip/port/jump/su/note/password）
//	env.*    环境变量，只在 api.request 的 URL/头/body 里允许（AllowEnv）
//	code     不带根时等价于 data.code（写配置时更省事）
//
// 函数：substr / trim / upper / lower / replace / default / urlencode / urlescape
//
// 例：
//
//	data.ip
//	substr(data.root_password, 0, 8) + "@2026"
//	default(data.ssh_port, "22")
package expr

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Env 是一次求值的上下文。
type Env struct {
	Data     any            // ECM 响应 JSON
	Meta     map[string]any // 本地解析结果
	AllowEnv bool           // 是否允许读取 env.*（只有请求渲染才打开）
}

// Stringify 把取值转成字符串（拼接与打印共用）。
func Stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case bool:
		if t {
			return 1
		}
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
	}
	return 0
}

// Expr 是编译后的表达式，可重复求值。
type Expr struct {
	src  string
	root node
}

// Source 返回原始表达式文本。
func (e *Expr) Source() string { return e.src }

// Compile 解析表达式，语法错误会指出列号。
func Compile(src string) (*Expr, error) {
	p := &parser{src: src}
	n, err := p.parseExpr()
	if err != nil {
		return nil, fmt.Errorf("表达式 %q 非法：%w", src, err)
	}
	p.skipSpace()
	if !p.eof() {
		return nil, fmt.Errorf("表达式 %q 非法：第 %d 列有多余内容", src, p.pos+1)
	}
	return &Expr{src: src, root: n}, nil
}

// Eval 求值，返回原始类型（可能是 nil / string / float64 / []any / map）。
func (e *Expr) Eval(env Env) (any, error) {
	if e == nil || e.root == nil {
		return nil, nil
	}
	return e.root.eval(env)
}

// EvalString 求值并转成字符串。
func (e *Expr) EvalString(env Env) (string, error) {
	v, err := e.Eval(env)
	if err != nil {
		return "", err
	}
	return Stringify(v), nil
}

// EvalInt 求值并转成整数，空值/非数字返回 0。
func (e *Expr) EvalInt(env Env) (int, error) {
	v, err := e.Eval(env)
	if err != nil {
		return 0, err
	}
	return toInt(v), nil
}

// RefsEnv 报告表达式是否用到了 env.*（用于禁止 extract / info 读取环境变量）。
func (e *Expr) RefsEnv() bool {
	if e == nil || e.root == nil {
		return false
	}
	return refsEnv(e.root)
}

// Eval 是便捷函数：编译并求值（适合配置里点名的临时表达式）。
func Eval(src string, env Env) (any, error) {
	e, err := Compile(src)
	if err != nil {
		return nil, err
	}
	return e.Eval(env)
}

// EvalString 是便捷函数：编译并求值为字符串。
func EvalString(src string, env Env) (string, error) {
	e, err := Compile(src)
	if err != nil {
		return "", err
	}
	return e.EvalString(env)
}

// ---------------- 模板（${...} 插值） ----------------

// Template 是带 ${表达式} 占位符的字符串。
type Template struct {
	src   string
	parts []tplPart
}

type tplPart struct {
	lit string
	ex  *Expr
}

// ParseTemplate 解析 "${表达式}" 占位符，其余部分原样保留。
func ParseTemplate(src string) (*Template, error) {
	t := &Template{src: src}
	rest := src
	for {
		i := strings.Index(rest, "${")
		if i < 0 {
			if rest != "" {
				t.parts = append(t.parts, tplPart{lit: rest})
			}
			return t, nil
		}
		if i > 0 {
			t.parts = append(t.parts, tplPart{lit: rest[:i]})
		}
		rest = rest[i+2:]
		j := strings.IndexByte(rest, '}')
		if j < 0 {
			return nil, fmt.Errorf("模板 %q 非法：${ 缺少对应的 }", src)
		}
		inner := strings.TrimSpace(rest[:j])
		if inner == "" {
			return nil, fmt.Errorf("模板 %q 非法：${} 里不能为空", src)
		}
		ex, err := Compile(inner)
		if err != nil {
			return nil, err
		}
		t.parts = append(t.parts, tplPart{ex: ex})
		rest = rest[j+1:]
	}
}

// Render 求值并拼接。
func (t *Template) Render(env Env) (string, error) {
	if t == nil {
		return "", nil
	}
	var b strings.Builder
	for _, p := range t.parts {
		if p.ex == nil {
			b.WriteString(p.lit)
			continue
		}
		v, err := p.ex.Eval(env)
		if err != nil {
			return "", err
		}
		b.WriteString(Stringify(v))
	}
	return b.String(), nil
}

// IsEmpty 报告模板是否为空（只含空字面量也算空）。
func (t *Template) IsEmpty() bool {
	if t == nil {
		return true
	}
	for _, p := range t.parts {
		if p.ex != nil || p.lit != "" {
			return false
		}
	}
	return true
}

// RefsEnv 报告模板里是否有 env.* 引用。
func (t *Template) RefsEnv() bool {
	if t == nil {
		return false
	}
	for _, p := range t.parts {
		if p.ex != nil && p.ex.RefsEnv() {
			return true
		}
	}
	return false
}

// ---------------- 语法树 ----------------

type node interface {
	eval(Env) (any, error)
}

type litNode struct{ v any }

func (n *litNode) eval(Env) (any, error) { return n.v, nil }

type segment struct {
	name    string
	index   int
	isIndex bool
}

type pathNode struct {
	root string
	segs []segment
}

type envOnly struct{}

func (n *pathNode) eval(env Env) (any, error) {
	var cur any
	switch n.root {
	case "data":
		cur = env.Data
	case "meta":
		cur = env.Meta
	case "env":
		if !env.AllowEnv {
			return nil, errors.New("当前配置不允许读取环境变量（只有 api.request 的 URL/query/headers/body 可以使用 env.*）")
		}
		cur = envOnly{}
	default:
		return nil, fmt.Errorf("未知的根变量 %q", n.root)
	}

	for _, s := range n.segs {
		switch c := cur.(type) {
		case envOnly:
			if s.isIndex {
				return nil, nil
			}
			cur = os.Getenv(s.name)
		case map[string]any:
			if s.isIndex {
				cur = c[strconv.Itoa(s.index)]
				continue
			}
			cur = c[s.name]
		case []any:
			if !s.isIndex || s.index < 0 || s.index >= len(c) {
				return nil, nil
			}
			cur = c[s.index]
		default:
			return nil, nil
		}
	}
	// envOnly 没有后续字段时（写成 "env"）视为空。
	if _, ok := cur.(envOnly); ok {
		return nil, nil
	}
	return cur, nil
}

type concatNode struct{ parts []node }

func (n *concatNode) eval(env Env) (any, error) {
	var b strings.Builder
	for _, p := range n.parts {
		v, err := p.eval(env)
		if err != nil {
			return nil, err
		}
		b.WriteString(Stringify(v))
	}
	return b.String(), nil
}

type callNode struct {
	name string
	args []node
}

func (n *callNode) eval(env Env) (any, error) {
	args := make([]any, 0, len(n.args))
	for _, a := range n.args {
		v, err := a.eval(env)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	return callFunc(n.name, args)
}

func refsEnv(n node) bool {
	switch t := n.(type) {
	case *pathNode:
		return t.root == "env"
	case *concatNode:
		for _, p := range t.parts {
			if refsEnv(p) {
				return true
			}
		}
	case *callNode:
		for _, a := range t.args {
			if refsEnv(a) {
				return true
			}
		}
	}
	return false
}

// ---------------- 函数表 ----------------

var funcArity = map[string]int{
	"substr":    3,
	"trim":      1,
	"upper":     1,
	"lower":     1,
	"replace":   3,
	"default":   2,
	"urlencode": 1,
	"urlescape": 1,
}

func callFunc(name string, args []any) (any, error) {
	switch name {
	case "substr":
		return substr(Stringify(args[0]), toInt(args[1]), toInt(args[2])), nil
	case "trim":
		return strings.TrimSpace(Stringify(args[0])), nil
	case "upper":
		return strings.ToUpper(Stringify(args[0])), nil
	case "lower":
		return strings.ToLower(Stringify(args[0])), nil
	case "replace":
		return strings.ReplaceAll(Stringify(args[0]), Stringify(args[1]), Stringify(args[2])), nil
	case "default":
		if Stringify(args[0]) == "" {
			return args[1], nil
		}
		return args[0], nil
	case "urlencode":
		return url.QueryEscape(Stringify(args[0])), nil
	case "urlescape":
		return url.PathEscape(Stringify(args[0])), nil
	}
	return nil, fmt.Errorf("未知的函数 %q", name)
}

// substr 按 rune 截取：start 为负表示从末尾算，length 为负表示取到末尾。
func substr(s string, start, length int) string {
	r := []rune(s)
	n := len(r)
	if start < 0 {
		start = n + start
	}
	if start < 0 {
		start = 0
	}
	if start > n {
		start = n
	}
	end := n
	if length >= 0 {
		end = start + length
	}
	if end > n {
		end = n
	}
	if end < start {
		end = start
	}
	return string(r[start:end])
}

// ---------------- 解析器 ----------------

type parser struct {
	src string
	pos int
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) peek() byte { return p.src[p.pos] }

func (p *parser) skipSpace() {
	for !p.eof() {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) errf(format string, args ...any) error {
	return fmt.Errorf("第 %d 列：%s", p.pos+1, fmt.Sprintf(format, args...))
}

func (p *parser) parseExpr() (node, error) {
	first, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	parts := []node{first}
	for {
		p.skipSpace()
		if p.eof() || p.peek() != '+' {
			break
		}
		p.pos++
		next, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		parts = append(parts, next)
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return &concatNode{parts: parts}, nil
}

func (p *parser) parseTerm() (node, error) {
	p.skipSpace()
	if p.eof() {
		return nil, p.errf("缺少操作数")
	}
	c := p.peek()
	switch {
	case c == '"' || c == '\'':
		return p.parseString()
	case c == '-' || (c >= '0' && c <= '9'):
		return p.parseNumber()
	case isIdentStart(c):
		ident := p.parseIdent()
		p.skipSpace()
		if !p.eof() && p.peek() == '(' {
			return p.parseCall(ident)
		}
		return p.parsePath(ident)
	}
	return nil, p.errf("无法识别的字符 %q", string(c))
}

func (p *parser) parseString() (node, error) {
	quote := p.peek()
	p.pos++
	var b strings.Builder
	for {
		if p.eof() {
			return nil, p.errf("字符串缺少结束引号 %c", quote)
		}
		c := p.src[p.pos]
		p.pos++
		switch c {
		case quote:
			return &litNode{v: b.String()}, nil
		case '\\':
			if p.eof() {
				return nil, p.errf("字符串转义不完整")
			}
			e := p.src[p.pos]
			p.pos++
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
		}
	}
}

func (p *parser) parseNumber() (node, error) {
	start := p.pos
	if p.peek() == '-' {
		p.pos++
	}
	for !p.eof() && p.peek() >= '0' && p.peek() <= '9' {
		p.pos++
	}
	if start == p.pos || (p.pos == start+1 && p.src[start] == '-') {
		return nil, p.errf("数字格式错误")
	}
	n, err := strconv.Atoi(p.src[start:p.pos])
	if err != nil {
		return nil, p.errf("数字格式错误：%s", p.src[start:p.pos])
	}
	return &litNode{v: n}, nil
}

func (p *parser) parseIdent() string {
	start := p.pos
	for !p.eof() {
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			break
		}
		p.pos += size
	}
	return p.src[start:p.pos]
}

func (p *parser) parseCall(name string) (node, error) {
	want, ok := funcArity[name]
	if !ok {
		return nil, p.errf("未知的函数 %q（可用：substr/trim/upper/lower/replace/default/urlencode/urlescape）", name)
	}
	p.pos++ // '('
	var args []node
	p.skipSpace()
	if !p.eof() && p.peek() == ')' {
		p.pos++
		if want != 0 {
			return nil, p.errf("函数 %s 需要 %d 个参数，实际 0 个", name, want)
		}
		return &callNode{name: name}, nil
	}
	for {
		arg, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		p.skipSpace()
		if p.eof() {
			return nil, p.errf("函数 %s 缺少 )", name)
		}
		switch p.peek() {
		case ',':
			p.pos++
		case ')':
			p.pos++
			if len(args) != want {
				return nil, p.errf("函数 %s 需要 %d 个参数，实际 %d 个", name, want, len(args))
			}
			return &callNode{name: name, args: args}, nil
		default:
			return nil, p.errf("函数 %s 的参数之间需要逗号", name)
		}
	}
}

func (p *parser) parsePath(root string) (node, error) {
	n := &pathNode{root: root}
	switch root {
	case "data", "meta", "env":
		// 完整写法
	default:
		// 简写：裸标识符视为 data 下的字段，例如 success.path 写 "code" 等价于 "data.code"
		n = &pathNode{root: "data", segs: []segment{{name: root}}}
	}
	for {
		if p.eof() {
			return n, nil
		}
		p.skipSpace()
		if p.eof() {
			return n, nil
		}
		switch p.peek() {
		case '.':
			p.pos++
			p.skipSpace()
			if p.eof() {
				return nil, p.errf("路径 %s. 缺少字段名", root)
			}
			if c := p.peek(); c >= '0' && c <= '9' {
				start := p.pos
				for !p.eof() && p.peek() >= '0' && p.peek() <= '9' {
					p.pos++
				}
				idx, err := strconv.Atoi(p.src[start:p.pos])
				if err != nil {
					return nil, p.errf("下标格式错误")
				}
				n.segs = append(n.segs, segment{index: idx, isIndex: true})
				continue
			}
			if !isIdentStart(p.peek()) {
				return nil, p.errf("路径 %s. 后需要字段名", root)
			}
			n.segs = append(n.segs, segment{name: p.parseIdent()})
		case '[':
			p.pos++
			p.skipSpace()
			start := p.pos
			for !p.eof() && p.peek() >= '0' && p.peek() <= '9' {
				p.pos++
			}
			if start == p.pos {
				return nil, p.errf("[] 里需要数字下标")
			}
			idx, err := strconv.Atoi(p.src[start:p.pos])
			if err != nil {
				return nil, p.errf("下标格式错误")
			}
			p.skipSpace()
			if p.eof() || p.peek() != ']' {
				return nil, p.errf("[] 缺少 ]")
			}
			p.pos++
			n.segs = append(n.segs, segment{index: idx, isIndex: true})
		default:
			return n, nil
		}
	}
}

func isIdentStart(c byte) bool {
	if c >= 0x80 { // 允许中文等 UTF-8 字段名
		return true
	}
	r := rune(c)
	return unicode.IsLetter(r) || c == '_'
}
