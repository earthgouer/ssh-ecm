package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ServerEntry 是 servers.txt 的一行解析结果。
//
// 列：别名 | 目标(ECM ID 或 IP) | su用户 | 跳板 | 备注(第 5 列起全部内容)
type ServerEntry struct {
	Alias  string
	Target string
	SuUser string // 空 = 未配置（不切换）；"-" 时 NoSu=true
	NoSu   bool   // su 列显式写 "-"
	Jump   string // 空 = 用 defaults.jump；"-" 时 NoJump=true
	NoJump bool   // 跳板列显式写 "-"，强制直连
	Note   string
	Line   int // 源文件行号（1 起）
}

// 2 个及以上连续空格作为列分隔符（防止备注里的单个空格被切开）。
var multiSpaceSep = regexp.MustCompile(` {2,}`)

// LoadServers 解析 servers.txt。文件不存在时返回空列表（不报错）。
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

func parseServers(data []byte, path string) ([]ServerEntry, error) {
	// Excel / Windows 记事本经常带 UTF-8 BOM。
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 单行上限 1MB

	var (
		entries []ServerEntry
		byAlias = map[string]int{} // 别名 → entries 下标
		lineNo  int
	)
	for sc.Scan() {
		lineNo++
		line := strings.TrimSuffix(sc.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		spans := splitSpans(line)
		col := func(i int) string {
			if i >= len(spans) {
				return ""
			}
			return strings.TrimSpace(line[spans[i].start:spans[i].end])
		}

		alias := col(0)
		target := col(1)
		if alias == "" {
			return nil, fmt.Errorf("%s 第 %d 行：别名为空（每行至少两列：别名<Tab>目标）", path, lineNo)
		}
		if target == "" {
			return nil, fmt.Errorf("%s 第 %d 行：缺少目标（别名 %q 后面要有 ECM ID 或 IP）", path, lineNo, alias)
		}

		e := ServerEntry{Alias: alias, Target: target, Line: lineNo}
		if v := col(2); v != "" {
			if v == "-" {
				e.NoSu = true
			} else {
				e.SuUser = v
			}
		}
		if v := col(3); v != "" {
			if v == "-" {
				e.NoJump = true
			} else {
				e.Jump = v
			}
		}
		if len(spans) > 4 {
			// 第 5 列起整体作备注：从第 5 列起点到行尾，保留内部空格。
			noteStart := spans[4].start
			if noteStart > len(line) {
				noteStart = len(line)
			}
			e.Note = strings.TrimSpace(line[noteStart:])
		}

		if idx, dup := byAlias[alias]; dup {
			fmt.Fprintf(os.Stderr, "警告：%s 第 %d 行的别名 %q 与第 %d 行重复，以后者为准\n",
				path, lineNo, alias, entries[idx].Line)
			entries[idx] = e
			continue
		}
		byAlias[alias] = len(entries)
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取 %s 失败：%w", path, err)
	}
	return entries, nil
}

type span struct{ start, end int }

// splitSpans 按 §4.3 的分隔符优先级切列：
//
//	Tab > 2 个及以上连续空格 > 单个空格
//
// 返回各列的字节区间，便于把第 5 列起按原文回拼成备注（保留内部空格）。
func splitSpans(line string) []span {
	if strings.IndexByte(line, '\t') >= 0 {
		spans := make([]span, 0, 8)
		start := 0
		for i := 0; i < len(line); i++ {
			if line[i] == '\t' {
				spans = append(spans, span{start, i})
				start = i + 1
			}
		}
		return append(spans, span{start, len(line)})
	}

	if locs := multiSpaceSep.FindAllStringIndex(line, -1); len(locs) > 0 {
		spans := make([]span, 0, len(locs)+1)
		start := 0
		for _, loc := range locs {
			spans = append(spans, span{start, loc[0]})
			start = loc[1]
		}
		return append(spans, span{start, len(line)})
	}

	spans := make([]span, 0, 8)
	start := 0
	for i := 0; i < len(line); i++ {
		if line[i] == ' ' {
			spans = append(spans, span{start, i})
			start = i + 1
		}
	}
	return append(spans, span{start, len(line)})
}
