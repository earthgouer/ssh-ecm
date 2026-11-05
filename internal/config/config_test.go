package config

import (
	"strings"
	"testing"
)

func TestAPIValidateOK(t *testing.T) {
	api := APIConfig{
		URL: "http://127.0.0.1:8080/api",
		Request: RequestConfig{
			Method: "post",
			Path:   "/servers/${urlescape(meta.target)}",
			Query:  map[string]string{"id": "${meta.target}"},
			Headers: map[string]string{
				"Authorization": "Bearer ${env.ECM_TOKEN}",
			},
			Body: `{"id":"${meta.target}"}`,
		},
		Success:      SuccessConfig{Path: "code", Equals: "0"},
		ErrorMessage: "msg",
		Extract:      ExtractConfig{IP: "data.ip", Password: `substr(data.pwd, 0, 4) + "@1"`},
		Info: InfoConfig{Rows: []InfoRowConfig{
			{Label: "别名", Value: "meta.alias"},
			{Label: "root密码", Value: "meta.password"},
		}},
	}
	if err := api.validate(); err != nil {
		t.Fatalf("应当通过：%v", err)
	}
	if api.Request.Method != "POST" {
		t.Fatalf("method 应被规范化为 POST，实际 %q", api.Request.Method)
	}
}

func TestAPIValidateErrors(t *testing.T) {
	tests := []struct {
		name string
		api  APIConfig
		want string
	}{
		{
			name: "method 非法",
			api:  APIConfig{Request: RequestConfig{Method: "FETCH"}},
			want: "api.request.method",
		},
		{
			name: "path 模板缺 }",
			api:  APIConfig{Request: RequestConfig{Path: "/s/${meta.target"}},
			want: "api.request.path",
		},
		{
			name: "extract 表达式语法错",
			api:  APIConfig{Extract: ExtractConfig{IP: "data."}},
			want: "api.extract.ip",
		},
		{
			name: "extract 不许读 env",
			api:  APIConfig{Extract: ExtractConfig{Password: "env.ECM_TOKEN"}},
			want: "不允许使用 env",
		},
		{
			name: "extract 误用 ${}",
			api:  APIConfig{Extract: ExtractConfig{IP: "${data.ip}"}},
			want: "不需要 ${}",
		},
		{
			name: "info 行缺 label",
			api:  APIConfig{Info: InfoConfig{Rows: []InfoRowConfig{{Value: "data.x"}}}},
			want: "缺少 label",
		},
		{
			name: "info 行表达式非法",
			api:  APIConfig{Info: InfoConfig{Rows: []InfoRowConfig{{Label: "X", Value: "data.x +"}}}},
			want: "api.info.rows[0].value",
		},
	}

	for _, tc := range tests {
		err := tc.api.validate()
		if err == nil {
			t.Errorf("%s：应当报错", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：错误信息应含 %q，实际 %v", tc.name, tc.want, err)
		}
	}
}
