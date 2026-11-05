package expr

import (
	"strings"
	"testing"
)

func TestEval(t *testing.T) {
	env := Env{
		Data: map[string]any{
			"ip":            "10.0.0.1",
			"root_password": "secret-xyz-2026",
			"num":           float64(22),
			"flag":          true,
			"ssh":           map[string]any{"port": float64(2222)},
			"list":          []any{map[string]any{"name": "n0"}, map[string]any{"name": "n1"}},
		},
		Meta: map[string]any{"target": "web-01", "port": 22},
	}

	tests := []struct {
		src     string
		want    string
		wantErr bool
	}{
		{src: "data.ip", want: "10.0.0.1"},
		{src: "ip", want: "10.0.0.1"},
		{src: "ssh.port", want: "2222"},
		{src: "data.ssh.port", want: "2222"},
		{src: "data.list[1].name", want: "n1"},
		{src: "data.list.0.name", want: "n0"},
		{src: `substr(data.root_password, 0, 6) + "@2026"`, want: "secret@2026"},
		{src: `substr(data.root_password, -4, 4)`, want: "2026"},
		{src: `substr("abcdef", 2, -1)`, want: "cdef"},
		{src: `default(data.missing, "fallback")`, want: "fallback"},
		{src: `upper(trim("  ab  "))`, want: "AB"},
		{src: `replace(data.ip, ".", "-")`, want: "10-0-0-1"},
		{src: `urlencode("a b")`, want: "a+b"},
		{src: `urlescape("a/b")`, want: "a%2Fb"},
		{src: "data.num", want: "22"},
		{src: "data.flag", want: "true"},
		{src: `meta.target + ":" + meta.port`, want: "web-01:22"},
		{src: "data.不存在", want: ""},
		{src: "env.HOME", wantErr: true}, // AllowEnv=false
		{src: "data.", wantErr: true},
		{src: `substr("x")`, wantErr: true},
		{src: "nope(1)", wantErr: true},
		{src: "data.ip +", wantErr: true},
	}

	for _, tc := range tests {
		got, err := EvalString(tc.src, env)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: 期望报错，实际得到 %q", tc.src, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: 意外报错：%v", tc.src, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: 得到 %q，期望 %q", tc.src, got, tc.want)
		}
	}
}

func TestEvalAllowEnv(t *testing.T) {
	t.Setenv("SSH_ECM_TEST_TOKEN", "tok-123")
	got, err := EvalString("env.SSH_ECM_TEST_TOKEN + \"-x\"", Env{AllowEnv: true})
	if err != nil {
		t.Fatalf("意外报错：%v", err)
	}
	if got != "tok-123-x" {
		t.Fatalf("得到 %q", got)
	}
}

func TestTemplate(t *testing.T) {
	tpl, err := ParseTemplate("/api/servers/${urlescape(meta.target)}?id=${meta.target}")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	got, err := tpl.Render(Env{Meta: map[string]any{"target": "web/01"}})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if want := "/api/servers/web%2F01?id=web/01"; got != want {
		t.Fatalf("得到 %q，期望 %q", got, want)
	}

	if _, err := ParseTemplate("${data.ip"); err == nil {
		t.Fatal("缺少 } 应该报错")
	}
	if _, err := ParseTemplate("${}"); err == nil {
		t.Fatal("空占位符应该报错")
	}

	envTpl, err := ParseTemplate("${env.TOKEN}")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if !envTpl.RefsEnv() {
		t.Fatal("RefsEnv 应该为 true")
	}
	if !strings.Contains(envTpl.src, "env.TOKEN") {
		t.Fatal("src 保留原文")
	}
}

func TestCompileErrorMentionsColumn(t *testing.T) {
	_, err := Compile("data.ip + + 1")
	if err == nil {
		t.Fatal("应该报错")
	}
	if !strings.Contains(err.Error(), "列") {
		t.Fatalf("错误信息应带列号：%v", err)
	}
}
