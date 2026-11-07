package config

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// ServerEntry 是 servers.csv 的一行解析结果。
//
// 表头列名（顺序任意、大小写不敏感，中英文皆可）：
//
//	alias,target,su,jump,port,note,tags
type ServerEntry struct {
	Alias  string
	Target string // 交给 ECM API 的查询键：ECM ID 或 IP
	SuUser string // 空 = 未配置（不切换）；"-" 时 NoSu=true
	NoSu   bool   // su 列显式写 "-"
	Jump   string // 空/- = 直连；非空 = 跳板别名
	NoJump bool   // 跳板列显式写 "-"，强制直连
	Port   int    // 0 = 用 ECM/defaults；显式写了则优先于 ECM 返回值
	Note   string
	Tags   []string
	Line   int // 源文件行号（1 起）
}

// TagsText 返回用于展示和过滤的标签文本。
func (e ServerEntry) TagsText() string { return strings.Join(e.Tags, ";") }

// LoadServers 解析 servers.csv。文件不存在时返回空列表（不报错）。
func LoadServers(path string) ([]ServerEntry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败：%w", path, err)
	}
	return parseServers(data, path)
}

type csvColumns struct {
	alias  int
	target int
	su     int
	jump   int
	port   int
	note   int
	tags   int
}

func parseServers(data []byte, path string) ([]ServerEntry, error) {
	// Excel / Windows 记事本经常带 UTF-8 BOM。
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}

	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = detectDelimiter(data)
	r.Comment = '#'
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1 // 行尾少列按空值处理，多列单独校验

	header, err := r.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s 表头解析失败：%w", path, err)
	}
	cols, err := parseHeader(header, path)
	if err != nil {
		return nil, err
	}

	var (
		entries []ServerEntry
		byAlias = map[string]int{}
	)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s：%w", path, err)
		}
		if rowEmpty(rec) {
			continue
		}

		line := 0
		if len(rec) > 0 {
			line, _ = r.FieldPos(0)
		}

		// Excel 经常在行尾留空列，允许纯空的多余列；有内容则说明表头漏了列。
		if len(rec) > len(header) {
			for _, extra := range rec[len(header):] {
				if strings.TrimSpace(extra) != "" {
					return nil, fmt.Errorf("%s 第 %d 行：列数多于表头（%d > %d）",
						path, line, len(rec), len(header))
				}
			}
		}

		cell := func(idx int) string {
			if idx < 0 || idx >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[idx])
		}

		alias := cell(cols.alias)
		target := cell(cols.target)
		if alias == "" {
			return nil, fmt.Errorf("%s 第 %d 行：alias 为空（每行必须有别名和目标）", path, line)
		}
		if target == "" {
			return nil, fmt.Errorf("%s 第 %d 行：target 为空（别名 %q 后面要有 ECM ID 或 IP）", path, line, alias)
		}

		e := ServerEntry{
			Alias:  alias,
			Target: target,
			Line:   line,
			Note:   cell(cols.note),
			Tags:   splitTags(cell(cols.tags)),
		}
		switch v := cell(cols.su); v {
		case "":
		case "-":
			e.NoSu = true
		default:
			e.SuUser = v
		}
		switch v := cell(cols.jump); v {
		case "":
		case "-":
			e.NoJump = true
		default:
			e.Jump = v
		}
		if v := cell(cols.port); v != "" {
			p, err := strconv.Atoi(v)
			if err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("%s 第 %d 行：port 非法 %q（应为 1-65535）", path, line, v)
			}
			e.Port = p
		}

		if idx, dup := byAlias[alias]; dup {
			fmt.Fprintf(os.Stderr, "警告：%s 第 %d 行的别名 %q 与第 %d 行重复，以后者为准\n",
				path, line, alias, entries[idx].Line)
			entries[idx] = e
			continue
		}
		byAlias[alias] = len(entries)
		entries = append(entries, e)
	}
	return entries, nil
}

// parseHeader 把表头映射到列下标；列顺序任意，未识别的列直接报错，
// 避免把 prot 之类的拼写错误静默忽略。
func parseHeader(header []string, path string) (csvColumns, error) {
	cols := csvColumns{alias: -1, target: -1, su: -1, jump: -1, port: -1, note: -1, tags: -1}
	seen := map[string]string{}

	for i, h := range header {
		name := strings.TrimSpace(h)
		if name == "" {
			continue // 允许 Excel 的空表头尾列
		}
		field := canonicalColumn(name)
		if field == "" {
			return cols, fmt.Errorf("%s 表头第 %d 列：未知列 %q（可用：alias,target,su,jump,port,note,tags）",
				path, i+1, name)
		}
		if prev, ok := seen[field]; ok {
			return cols, fmt.Errorf("%s 表头：列 %q 与 %q 重复", path, prev, name)
		}
		seen[field] = name

		switch field {
		case "alias":
			cols.alias = i
		case "target":
			cols.target = i
		case "su":
			cols.su = i
		case "jump":
			cols.jump = i
		case "port":
			cols.port = i
		case "note":
			cols.note = i
		case "tags":
			cols.tags = i
		}
	}

	if cols.alias < 0 {
		return cols, fmt.Errorf("%s 表头缺少 alias 列（可用：alias,target,su,jump,port,note,tags）", path)
	}
	if cols.target < 0 {
		return cols, fmt.Errorf("%s 表头缺少 target 列（可用：alias,target,su,jump,port,note,tags）", path)
	}
	return cols, nil
}

// canonicalColumn 归一化列名：去空格/下划线/连字符，大小写不敏感，兼容中文表头。
func canonicalColumn(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "", "\t", "", "_", "", "-", "").Replace(s)
	switch s {
	case "alias", "name", "别名", "名称":
		return "alias"
	case "target", "ip", "ecmid", "host", "目标", "地址", "服务器":
		return "target"
	case "su", "suuser", "su用户", "切换用户":
		return "su"
	case "jump", "bastion", "跳板", "跳板机":
		return "jump"
	case "port", "端口":
		return "port"
	case "note", "desc", "description", "备注", "说明":
		return "note"
	case "tag", "tags", "标签":
		return "tags"
	}
	return ""
}

// detectDelimiter 识别表头分隔符：逗号 / 分号 / Tab。
// Excel 在部分区域设置下会导出分号 CSV，这里一并兼容。
func detectDelimiter(data []byte) rune {
	for _, raw := range bytes.Split(data, []byte("\n")) {
		line := strings.TrimSpace(strings.TrimSuffix(string(raw), "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var comma, semi, tab int
		inQuotes := false
		for _, r := range line {
			if r == '"' {
				inQuotes = !inQuotes
				continue
			}
			if inQuotes {
				continue
			}
			switch r {
			case ',':
				comma++
			case ';':
				semi++
			case '\t':
				tab++
			}
		}
		switch {
		case tab > comma && tab > semi:
			return '\t'
		case semi > comma:
			return ';'
		default:
			return ','
		}
	}
	return ','
}

// splitTags 支持用 ; | ， 、 分隔多个标签。
func splitTags(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ';', '|', '，', '、':
			return true
		}
		return false
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func rowEmpty(rec []string) bool {
	for _, v := range rec {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}
