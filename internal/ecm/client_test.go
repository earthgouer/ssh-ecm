package ecm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ecm/ssh-ecm/internal/config"
)

// 不写 request/extract 时，走内置默认（GET {url}/servers/{目标} + 剥包装层猜字段）。
func TestGetServerLegacy(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":0,"data":{"ip":"10.1.2.3","ssh_port":2201,"root_password":"legacy-pass"}}`)
	}))
	defer srv.Close()

	c, err := NewClient(config.APIConfig{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient 失败：%v", err)
	}
	info, err := c.GetServer("web-01")
	if err != nil {
		t.Fatalf("GetServer 失败：%v", err)
	}
	if gotPath != "/servers/web-01" {
		t.Fatalf("请求路径为 %q", gotPath)
	}
	if info.IP != "10.1.2.3" || info.Port != 2201 || info.Password != "legacy-pass" {
		t.Fatalf("解析结果不对：%+v", info)
	}
}

// 配置驱动：POST + query + header + body + success 判定 + 嵌套提取 + 密码拼接。
func TestGetServerConfigured(t *testing.T) {
	var (
		gotMethod, gotPath, gotQuery, gotTenant, gotBody string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("sid")
		gotTenant = r.Header.Get("X-Tenant")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":0,"data":{"server":{"ip":"192.168.1.9","ssh":{"port":"2200"},"pwd":"secret-2026"}}}`)
	}))
	defer srv.Close()

	c, err := NewClient(config.APIConfig{
		URL: srv.URL,
		Request: config.RequestConfig{
			Method:  "POST",
			Path:    "/api/v1/detail",
			Query:   map[string]string{"sid": "${meta.target}"},
			Headers: map[string]string{"X-Tenant": "t-1"},
			Body:    `{"id":"${meta.target}"}`,
		},
		Success: config.SuccessConfig{Path: "code", Equals: "0"},
		Extract: config.ExtractConfig{
			IP:       "data.data.server.ip",
			Port:     "data.data.server.ssh.port",
			Password: `substr(data.data.server.pwd, 0, 6) + "@2026"`,
		},
	})
	if err != nil {
		t.Fatalf("NewClient 失败：%v", err)
	}
	info, err := c.GetServer("web-01")
	if err != nil {
		t.Fatalf("GetServer 失败：%v", err)
	}

	if gotMethod != "POST" {
		t.Fatalf("method 为 %q", gotMethod)
	}
	if gotPath != "/api/v1/detail" {
		t.Fatalf("path 为 %q", gotPath)
	}
	if gotQuery != "web-01" {
		t.Fatalf("query sid 为 %q", gotQuery)
	}
	if gotTenant != "t-1" {
		t.Fatalf("header X-Tenant 为 %q", gotTenant)
	}
	if gotBody != `{"id":"web-01"}` {
		t.Fatalf("body 为 %q", gotBody)
	}
	if info.IP != "192.168.1.9" || info.Port != 2200 || info.Password != "secret@2026" {
		t.Fatalf("解析结果不对：%+v", info)
	}
}

func TestGetServerBusinessFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":7,"msg":"机器不存在"}`)
	}))
	defer srv.Close()

	c, err := NewClient(config.APIConfig{
		URL:          srv.URL,
		Success:      config.SuccessConfig{Path: "code", Equals: "0"},
		ErrorMessage: "msg",
	})
	if err != nil {
		t.Fatalf("NewClient 失败：%v", err)
	}
	if _, err := c.GetServer("nope"); err == nil {
		t.Fatal("应该报错")
	} else if !strings.Contains(err.Error(), "机器不存在") || !strings.Contains(err.Error(), "code=7") {
		t.Fatalf("错误信息不完整：%v", err)
	}
}

func TestGetServerPasswordEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ip":"10.0.0.1"}`)
	}))
	defer srv.Close()

	c, err := NewClient(config.APIConfig{
		URL:     srv.URL,
		Extract: config.ExtractConfig{Password: "data.root_password"},
	})
	if err != nil {
		t.Fatalf("NewClient 失败：%v", err)
	}
	if _, err := c.GetServer("x"); err == nil {
		t.Fatal("密码取不到时应该报错")
	}
}

func TestNewClientRejectsPlaceholderURL(t *testing.T) {
	if _, err := NewClient(config.APIConfig{URL: "https://ecm.example.com/api/v1"}); err == nil {
		t.Fatal("占位地址应该报错")
	}
}
