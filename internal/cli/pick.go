package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	xterm "golang.org/x/term"

	"github.com/ecm/ssh-ecm/internal/config"
)

// 交互式选单：
//
//	↑/↓ 或 j/k 移动，Home/End 跳首尾，Enter 连接；
//	/ 进入过滤输入（别名/目标/su/跳板/备注/标签 任意字段包含匹配，大小写不敏感）；
//	q / Esc / Ctrl-C 退出。
//
// 实现约定：
//   - 全程在备用屏幕（alternate screen）里画，退出时还原，不会污染滚动历史；
//   - 每帧重绘可见窗口，列表比屏幕长时自动滚动；
//   - raw mode 下本地输出必须写 \r\n（OPOST/ONLCR 已关闭），否则光标不回行首；
//   - stdin/stdout 不是终端（管道、重定向、脚本）时退化为编号输入，
//     保证 `printf '1\n' | ssh-ecm` 这类用法和自动化不受影响。

type keyKind int

const (
	keyNone keyKind = iota
	keyUp
	keyDown
	keyHome
	keyEnd
	keyEnter
	keyCancel
	keyBackspace
	keyRune // 可打印字符，见 keyEvent.r
)

type keyEvent struct {
	kind keyKind
	r    rune
}

const (
	pickerAliasW  = 14
	pickerTargetW = 18
	pickerSuW     = 6
	pickerJumpW   = 12
)

type serverPicker struct {
	all  []config.ServerEntry // 全量列表
	list []config.ServerEntry // 过滤后可见列表

	filter    []rune // 过滤关键字
	filtering bool   // 是否处于"输入过滤词"状态

	width  int
	height int
	cursor int
	offset int

	out *bufio.Writer
}

// pickServer 返回选中的别名；空字符串表示用户取消。
func pickServer(entries []config.ServerEntry) (string, error) {
	if !xterm.IsTerminal(int(os.Stdin.Fd())) || !xterm.IsTerminal(int(os.Stdout.Fd())) {
		return pickServerPlain(entries)
	}

	fd := int(os.Stdin.Fd())
	oldState, err := xterm.MakeRaw(fd)
	if err != nil {
		// 拿不到 raw mode 就退回编号输入，不打断使用。
		return pickServerPlain(entries)
	}
	defer func() { _ = xterm.Restore(fd, oldState) }()

	out := bufio.NewWriter(os.Stdout)
	p := &serverPicker{all: entries, out: out}
	p.applyFilter()

	out.WriteString("\033[?1049h\033[?25l") // 备用屏幕 + 隐藏光标
	defer func() {
		out.WriteString("\033[?25h\033[?1049l") // 恢复光标 + 退出备用屏幕
		out.Flush()
	}()

	in := bufio.NewReader(os.Stdin)
	for {
		p.render()
		if err := out.Flush(); err != nil {
			return "", nil
		}

		ev := readKey(in)
		switch ev.kind {
		case keyRune:
			switch {
			case p.filtering:
				p.filter = append(p.filter, ev.r)
				p.applyFilter()
			case ev.r == '/':
				p.filtering = true
				p.filter = nil
				p.applyFilter()
			case ev.r == 'q' || ev.r == 'Q':
				return "", nil
			case ev.r == 'k' || ev.r == 'K':
				p.move(-1)
			case ev.r == 'j' || ev.r == 'J':
				p.move(1)
			case ev.r == 'g':
				p.cursor = 0
			case ev.r == 'G':
				p.cursor = len(p.list) - 1
			}
		case keyUp:
			p.move(-1)
		case keyDown:
			p.move(1)
		case keyHome:
			p.cursor = 0
		case keyEnd:
			p.cursor = len(p.list) - 1
		case keyBackspace:
			switch {
			case p.filtering && len(p.filter) > 0:
				p.filter = p.filter[:len(p.filter)-1]
				p.applyFilter()
			case p.filtering:
				p.filtering = false // 过滤词已空，退格退出输入状态
			case len(p.filter) > 0:
				p.filter = p.filter[:len(p.filter)-1]
				p.applyFilter()
			}
		case keyEnter:
			if len(p.list) == 0 {
				continue
			}
			return p.list[p.cursor].Alias, nil
		case keyCancel:
			switch {
			case p.filtering:
				// 第一下 Esc：退出输入并清空过滤
				p.filtering = false
				p.filter = nil
				p.applyFilter()
			case len(p.filter) > 0:
				p.filter = nil
				p.applyFilter()
			default:
				return "", nil
			}
		}
	}
}

func (p *serverPicker) move(delta int) {
	if len(p.list) == 0 {
		return
	}
	next := p.cursor + delta
	if next < 0 {
		next = 0
	}
	if next > len(p.list)-1 {
		next = len(p.list) - 1
	}
	p.cursor = next
}

// applyFilter 按关键字重算可见列表，并把光标复位到第一项。
func (p *serverPicker) applyFilter() {
	p.list = make([]config.ServerEntry, 0, len(p.all))
	for _, e := range p.all {
		if p.match(e) {
			p.list = append(p.list, e)
		}
	}
	p.cursor = 0
	p.offset = 0
}

// match 在多列内容里做大小写不敏感的包含匹配。
func (p *serverPicker) match(e config.ServerEntry) bool {
	if len(p.filter) == 0 {
		return true
	}
	needle := strings.ToLower(string(p.filter))
	haystack := strings.ToLower(strings.Join([]string{
		e.Alias, e.Target, e.SuUser, jumpColumn(e), e.Note, e.TagsText(),
	}, " "))
	return strings.Contains(haystack, needle)
}

// readKey 解析一次按键。可打印字符（含中文等多字节字符）以 keyRune 返回。
func readKey(r *bufio.Reader) keyEvent {
	b, err := r.ReadByte()
	if err != nil {
		return keyEvent{kind: keyCancel}
	}

	switch {
	case b == 3 || b == 4: // Ctrl-C / Ctrl-D
		return keyEvent{kind: keyCancel}
	case b == 13 || b == 10: // Enter
		return keyEvent{kind: keyEnter}
	case b == 127 || b == 8: // Backspace / Ctrl-H
		return keyEvent{kind: keyBackspace}
	case b == 27: // ESC 或方向键序列
		return readEscape(r)
	case b >= 32 && b < 127:
		return keyEvent{kind: keyRune, r: rune(b)}
	case b >= 0x80: // UTF-8 多字节字符（例如中文过滤词）
		buf := []byte{b}
		for !utf8.FullRune(buf) && r.Buffered() > 0 {
			next, err := r.ReadByte()
			if err != nil {
				break
			}
			buf = append(buf, next)
		}
		ch, _ := utf8.DecodeRune(buf)
		if ch == utf8.RuneError && len(buf) == 1 {
			return keyEvent{kind: keyNone}
		}
		return keyEvent{kind: keyRune, r: ch}
	}
	return keyEvent{kind: keyNone}
}

func readEscape(r *bufio.Reader) keyEvent {
	if r.Buffered() < 2 {
		return keyEvent{kind: keyCancel}
	}
	b1, err := r.ReadByte()
	if err != nil || (b1 != '[' && b1 != 'O') {
		return keyEvent{kind: keyNone}
	}
	b2, err := r.ReadByte()
	if err != nil {
		return keyEvent{kind: keyNone}
	}
	switch b2 {
	case 'A':
		return keyEvent{kind: keyUp}
	case 'B':
		return keyEvent{kind: keyDown}
	case 'H':
		return keyEvent{kind: keyHome}
	case 'F':
		return keyEvent{kind: keyEnd}
	}
	return keyEvent{kind: keyNone}
}

func (p *serverPicker) render() {
	p.width, p.height = terminalSize()
	if p.width > 1 {
		// 行宽留 1 列，避免写满整行触发终端"待换行"状态导致多出空行。
		p.width--
	}
	rows := p.height - 5 // 标题 + 表头 + 过滤行 + 空行 + 底部提示
	if rows < 3 {
		rows = 3
	}
	p.clampWindow(rows)

	// raw mode 下 OPOST/ONLCR 被关闭，本地输出必须显式 \r\n，否则换行后光标留在原列（界面一行比一行右缩）。
	var b strings.Builder
	b.WriteString("\033[H\033[2J") // 光标归位 + 清屏

	b.WriteString("\033[1m选择服务器\033[0m  共 ")
	b.WriteString(strconv.Itoa(len(p.all)))
	if len(p.filter) > 0 {
		b.WriteString(" 台，匹配 ")
		b.WriteString(strconv.Itoa(len(p.list)))
	}
	b.WriteString(" 台\r\n")

	// 列头：让用户知道每一列代表什么。
	b.WriteString("\033[2m")
	b.WriteString(p.formatHeader())
	b.WriteString("\033[0m\r\n")

	// 过滤行：处于输入状态或有过滤词时显示，占掉原来那一行空行。
	if p.filtering || len(p.filter) > 0 {
		line := "过滤: " + string(p.filter)
		if p.filtering {
			line += "▏"
		}
		b.WriteString("\033[36m")
		b.WriteString(tailFitWidth(line, p.width))
		b.WriteString("\033[0m\r\n")
	} else {
		b.WriteString("\r\n")
	}

	for i := p.offset; i < len(p.list) && i < p.offset+rows; i++ {
		line := p.formatRow(i)
		if i == p.cursor {
			b.WriteString("\033[7m")
			b.WriteString(line)
			b.WriteString("\033[0m\r\n")
			continue
		}
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	if len(p.list) == 0 {
		b.WriteString("\033[2m（无匹配，退格或 Esc 清除过滤词）\033[0m\r\n")
	} else {
		b.WriteString("\r\n")
	}
	if p.filtering {
		b.WriteString("\033[2m输入关键字过滤（别名/目标/su/跳板/备注/标签）   Enter 连接   Esc 清除   ⌫ 删除\033[0m")
	} else {
		b.WriteString("\033[2m↑/↓ 或 j/k 移动   / 过滤   Enter 连接   第 ")
		b.WriteString(strconv.Itoa(p.cursor + 1))
		b.WriteString("/")
		b.WriteString(strconv.Itoa(len(p.list)))
		b.WriteString(" 台   q 退出\033[0m")
	}

	p.out.WriteString(b.String())
}

func (p *serverPicker) clampWindow(rows int) {
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(p.list) {
		p.cursor = len(p.list) - 1
	}
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+rows {
		p.offset = p.cursor - rows + 1
	}
	if maxOffset := len(p.list) - rows; p.offset > maxOffset {
		p.offset = maxOffset
	}
	if p.offset < 0 {
		p.offset = 0
	}
}

func (p *serverPicker) formatHeader() string {
	parts := []string{
		"  ",
		fitWidth("别名", pickerAliasW), " ",
		fitWidth("目标", pickerTargetW), " ",
		fitWidth("su", pickerSuW), " ",
		fitWidth("跳板", pickerJumpW), " ",
	}
	used := 2 + pickerAliasW + 1 + pickerTargetW + 1 + pickerSuW + 1 + pickerJumpW + 1

	if noteWidth := p.width - used; noteWidth >= 8 {
		parts = append(parts, fitWidth("备注", noteWidth))
	}
	return fitWidth(strings.Join(parts, ""), p.width)
}

func (p *serverPicker) formatRow(i int) string {
	e := p.list[i]
	prefix := "  "
	if i == p.cursor {
		prefix = "> "
	}
	su := "-"
	if e.SuUser != "" && !e.NoSu {
		su = e.SuUser
	}

	parts := []string{
		prefix,
		fitWidth(e.Alias, pickerAliasW), " ",
		fitWidth(e.Target, pickerTargetW), " ",
		fitWidth(su, pickerSuW), " ",
		fitWidth(jumpColumn(e), pickerJumpW), " ",
	}
	used := 2 + pickerAliasW + 1 + pickerTargetW + 1 + pickerSuW + 1 + pickerJumpW + 1

	if noteWidth := p.width - used; noteWidth >= 8 {
		parts = append(parts, fitWidth(entryNote(e), noteWidth))
	}
	return fitWidth(strings.Join(parts, ""), p.width)
}

// terminalSize 取终端尺寸，失败时给一个安全默认值。
func terminalSize() (int, int) {
	if w, h, err := xterm.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
		return w, h
	}
	return 100, 24
}

// pickServerPlain 是非终端环境的退化形态：打印编号，读一行输入。
func pickServerPlain(entries []config.ServerEntry) (string, error) {
	for i, e := range entries {
		extra := entryNote(e)
		if extra == "" {
			extra = "-"
		}
		fmt.Printf("%2d) %-12s %-18s %s\n", i+1, e.Alias, e.Target, extra)
	}
	fmt.Print("请选择服务器编号（回车/q 退出）：")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", nil
	}
	line = strings.TrimSpace(line)
	if line == "" || strings.EqualFold(line, "q") {
		return "", nil
	}
	idx, err := strconv.Atoi(line)
	if err != nil || idx < 1 || idx > len(entries) {
		return "", fmt.Errorf("无效的选择：%q", line)
	}
	return entries[idx-1].Alias, nil
}

// entryNote 把备注和标签合成一列展示，标签放在方括号里。
func entryNote(e config.ServerEntry) string {
	note := e.Note
	if tags := e.TagsText(); tags != "" {
		if note != "" {
			note += " "
		}
		note += "[" + tags + "]"
	}
	return note
}

// fitWidth 把字符串按显示宽度裁剪/补齐（中文占 2 列），用于表格对齐。
func fitWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	used := 0
	cut := len(runes)
	for i, r := range runes {
		w := runeWidth(r)
		if used+w > width {
			cut = i
			break
		}
		used += w
	}

	out := string(runes[:cut])
	if cut < len(runes) {
		// 被截断时留一列放省略号，避免半个汉字。
		trimmed := []rune(out)
		for len(trimmed) > 0 && used+1 > width {
			used -= runeWidth(trimmed[len(trimmed)-1])
			trimmed = trimmed[:len(trimmed)-1]
		}
		out = string(trimmed) + "…"
		used++
	}
	if used < width {
		out += strings.Repeat(" ", width-used)
	}
	return out
}

// tailFitWidth 从尾部截取（过滤词很长时保证看得见刚输入的部分）。
func tailFitWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	used := 0
	start := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		w := runeWidth(runes[i])
		if used+w > width {
			break
		}
		used += w
		start = i
	}
	return string(runes[start:])
}

// runeWidth 估算一个字符占几列（CJK 与常见全角符号按 2 列）。
func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r >= 0x1100 && (r <= 0x115F ||
		(r >= 0x2E80 && r <= 0xA4CF) ||
		(r >= 0xAC00 && r <= 0xD7A3) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE30 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x1F300 && r <= 0x1FAFF)):
		return 2
	default:
		return 1
	}
}
