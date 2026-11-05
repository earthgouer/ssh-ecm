package cli

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/ecm/ssh-ecm/internal/config"
)

// render 在非终端下会退化为 100x24 的默认尺寸，输出是确定的，可以直接断言。
func newTestPicker(buf *bytes.Buffer, entries ...config.ServerEntry) *serverPicker {
	p := &serverPicker{
		cfg: &config.Config{},
		all: entries,
		out: bufio.NewWriter(buf),
	}
	p.applyFilter()
	return p
}

func TestRenderNormal(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPicker(&buf,
		config.ServerEntry{Alias: "web1", Target: "10.20.30.40", SuUser: "app", Note: "生产 web 前端"},
		config.ServerEntry{Alias: "db1", Target: "db-01", Note: "数据库"},
	)
	p.width, p.height = 80, 12
	p.cursor = 1
	p.render()
	p.out.Flush()

	got := buf.String()
	for _, want := range []string{"选择服务器", "共 2 台", "> db1", "第 2/2 台", "/ 过滤"} {
		if !strings.Contains(got, want) {
			t.Errorf("输出缺少 %q：\n%s", want, got)
		}
	}
	if strings.Contains(got, "过滤: ") {
		t.Errorf("没有过滤词时不应显示过滤行：\n%s", got)
	}
}

func TestRenderFiltered(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPicker(&buf,
		config.ServerEntry{Alias: "web1", Target: "10.20.30.40", Note: "生产 web 前端"},
		config.ServerEntry{Alias: "db1", Target: "db-01", Note: "数据库"},
	)
	p.width, p.height = 80, 12
	p.filter = []rune("db")
	p.filtering = true
	p.applyFilter()
	p.render()
	p.out.Flush()

	got := buf.String()
	for _, want := range []string{"过滤: db", "匹配 1 台", "> db1", "输入关键字过滤"} {
		if !strings.Contains(got, want) {
			t.Errorf("输出缺少 %q：\n%s", want, got)
		}
	}
	if strings.Contains(got, "web1") {
		t.Errorf("过滤后不应显示 web1：\n%s", got)
	}
}

func TestRenderNoMatch(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPicker(&buf, config.ServerEntry{Alias: "web1", Target: "10.20.30.40"})
	p.width, p.height = 80, 12
	p.filter = []rune("zzz")
	p.filtering = true
	p.applyFilter()
	p.render()
	p.out.Flush()

	got := buf.String()
	for _, want := range []string{"匹配 0 台", "过滤: zzz", "（无匹配", "Esc 清除"} {
		if !strings.Contains(got, want) {
			t.Errorf("输出缺少 %q：\n%s", want, got)
		}
	}
}

// 每帧的输出行数不能超过终端高度，否则会在终端里滚动错位。
func TestRenderLineCountFitsHeight(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPicker(&buf,
		config.ServerEntry{Alias: "a1", Target: "1.1.1.1"},
		config.ServerEntry{Alias: "a2", Target: "1.1.1.2"},
	)
	p.width, p.height = 80, 12
	p.render()
	p.out.Flush()

	lines := strings.Count(buf.String(), "\r\n") + 1
	if lines > p.height {
		t.Fatalf("输出了 %d 行，超过终端高度 %d", lines, p.height)
	}
}
