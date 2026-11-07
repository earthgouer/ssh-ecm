package cli

import (
	"bufio"
	"strings"
	"testing"

	"github.com/ecm/ssh-ecm/internal/config"
)

func TestReadKey(t *testing.T) {
	tests := []struct {
		in   string
		kind keyKind
		r    rune
	}{
		{"\x1b[A", keyUp, 0},
		{"\x1b[B", keyDown, 0},
		{"\x1b[H", keyHome, 0},
		{"\x1b[F", keyEnd, 0},
		{"k", keyRune, 'k'},
		{"j", keyRune, 'j'},
		{"/", keyRune, '/'},
		{"中", keyRune, '中'},
		{"\r", keyEnter, 0},
		{"\n", keyEnter, 0},
		{"q", keyRune, 'q'},
		{"\x03", keyCancel, 0},
		{"\x1b", keyCancel, 0},
		{"\x7f", keyBackspace, 0},
		{"\x08", keyBackspace, 0},
	}
	for _, tc := range tests {
		got := readKey(bufio.NewReader(strings.NewReader(tc.in)))
		if got.kind != tc.kind || got.r != tc.r {
			t.Errorf("输入 %q：得到 {kind:%v r:%q}，期望 {kind:%v r:%q}",
				tc.in, got.kind, got.r, tc.kind, tc.r)
		}
	}
}

func TestFitWidth(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"abc", 5, "abc  "},
		{"abcdef", 4, "abc…"},
		{"中文", 4, "中文"},
		{"中文", 3, "中…"},
		{"生产 web 前端", 6, "生产 …"},
		{"", 3, "   "},
	}

	for _, tc := range tests {
		got := fitWidth(tc.in, tc.width)
		if got != tc.want {
			t.Errorf("fitWidth(%q, %d)：得到 %q，期望 %q", tc.in, tc.width, got, tc.want)
		}
		if w := displayWidth(got); w != tc.width && tc.width > 0 {
			t.Errorf("fitWidth(%q, %d)：结果宽度 %d，期望 %d", tc.in, tc.width, w, tc.width)
		}
	}
}

func TestTailFitWidth(t *testing.T) {
	if got := tailFitWidth("过滤: 生产 web", 4); got != " web" {
		t.Errorf("得到 %q", got)
	}
	if got := tailFitWidth("abc", 10); got != "abc" {
		t.Errorf("得到 %q", got)
	}
}

func TestServerPickerFilter(t *testing.T) {
	p := &serverPicker{
		all: []config.ServerEntry{
			{Alias: "web1", Target: "10.20.30.40", SuUser: "app", Note: "生产 web 前端"},
			{Alias: "db1", Target: "db-01", Note: "数据库", Tags: []string{"mysql", "prod"}},
			{Alias: "app01", Target: "app-01", Jump: "bastion", Note: "应用服务器 A"},
		},
	}

	p.applyFilter()
	if len(p.list) != 3 {
		t.Fatalf("空过滤应显示全部，实际 %d", len(p.list))
	}

	cases := []struct {
		filter string
		want   string
	}{
		{"WEB", "web1"},      // 大小写不敏感，命中别名
		{"库", "db1"},         // 中文命中备注
		{"bastion", "app01"}, // 命中跳板列
		{"mysql", "db1"},     // 命中标签
		{"10.20", "web1"},    // 命中目标
		{"zzz", ""},
	}
	for _, tc := range cases {
		p.filter = []rune(tc.filter)
		p.applyFilter()
		switch {
		case tc.want == "" && len(p.list) != 0:
			t.Errorf("过滤 %q：期望无匹配，实际 %d 条", tc.filter, len(p.list))
		case tc.want != "" && (len(p.list) != 1 || p.list[0].Alias != tc.want):
			t.Errorf("过滤 %q：期望 %s，实际 %+v", tc.filter, tc.want, p.list)
		}
		if p.cursor != 0 {
			t.Errorf("过滤 %q 后光标应复位到 0，实际 %d", tc.filter, p.cursor)
		}
	}
}
