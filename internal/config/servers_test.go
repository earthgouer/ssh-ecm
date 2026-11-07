package config

import (
	"strings"
	"testing"
)

func TestParseServersBasic(t *testing.T) {
	data := []byte("alias,target,su,jump,port,note,tags\n" +
		"web1,10.0.0.1,app,bastion,2222,\"生产,web\",prod;web\n" +
		"db1,10.0.0.2,-,-,,\"数据库\",\n")

	entries, err := parseServers(data, "servers.csv")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(entries))
	}

	web := entries[0]
	if web.Alias != "web1" || web.Target != "10.0.0.1" {
		t.Errorf("别名/目标错误：%+v", web)
	}
	if web.SuUser != "app" || web.Jump != "bastion" || web.Port != 2222 {
		t.Errorf("su/jump/port 错误：%+v", web)
	}
	if web.Note != "生产,web" {
		t.Errorf("带逗号的备注解析错误：%q", web.Note)
	}
	if strings.Join(web.Tags, "|") != "prod|web" {
		t.Errorf("标签解析错误：%v", web.Tags)
	}

	db := entries[1]
	if !db.NoSu || !db.NoJump {
		t.Errorf("- 语义错误：%+v", db)
	}
	if db.Port != 0 || len(db.Tags) != 0 {
		t.Errorf("空值应保持零值：%+v", db)
	}
}

func TestParseServersChineseReorderedHeader(t *testing.T) {
	data := []byte("\xef\xbb\xbf标签,端口,目标,别名,备注\r\n" +
		"test;web,22,10.0.0.1,web1,前端\r\n")

	entries, err := parseServers(data, "servers.csv")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("期望 1 条，实际 %d", len(entries))
	}
	e := entries[0]
	if e.Alias != "web1" || e.Target != "10.0.0.1" || e.Port != 22 || e.Note != "前端" {
		t.Errorf("中文表头/顺序解析错误：%+v", e)
	}
	if strings.Join(e.Tags, "|") != "test|web" {
		t.Errorf("标签解析错误：%v", e.Tags)
	}
}

func TestParseServersDelimiters(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"semicolon", "# 注释\nalias;target;su;note\nweb1;10.0.0.1;;hi\n"},
		{"tab", "alias\ttarget\tport\nweb1\t10.0.0.1\t22\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := parseServers([]byte(tc.data), "servers.csv")
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if len(entries) != 1 || entries[0].Alias != "web1" {
				t.Fatalf("解析结果错误：%+v", entries)
			}
		})
	}
}

func TestParseServersAllowsTrailingEmptyColumns(t *testing.T) {
	data := []byte("alias,target,note\nweb1,10.0.0.1,hi,,\n")
	entries, err := parseServers(data, "servers.csv")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(entries) != 1 || entries[0].Note != "hi" {
		t.Fatalf("解析结果错误：%+v", entries)
	}
}

func TestParseServersEmpty(t *testing.T) {
	entries, err := parseServers(nil, "servers.csv")
	if err != nil || len(entries) != 0 {
		t.Fatalf("空文件应返回空列表，实际 %+v, %v", entries, err)
	}
}

func TestParseServersErrors(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"缺少 alias", "target,port\n10.0.0.1,22\n", "缺少 alias"},
		{"缺少 target", "alias,port\nweb1,22\n", "缺少 target"},
		{"alias 为空", "alias,target\n,10.0.0.1\n", "alias 为空"},
		{"target 为空", "alias,target\nweb1,\n", "target 为空"},
		{"端口非法", "alias,target,port\nweb1,10.0.0.1,abc\n", "port 非法"},
		{"端口越界", "alias,target,port\nweb1,10.0.0.1,70000\n", "port 非法"},
		{"未知列", "alias,target,prot\nweb1,10.0.0.1,22\n", "未知列"},
		{"重复列", "alias,target,port,端口\nweb1,10.0.0.1,22,22\n", "重复"},
		{"多出的列有内容", "alias,target\nweb1,10.0.0.1,extra\n", "列数多于表头"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseServers([]byte(tc.data), "servers.csv"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望错误包含 %q，实际 %v", tc.want, err)
			}
		})
	}
}
