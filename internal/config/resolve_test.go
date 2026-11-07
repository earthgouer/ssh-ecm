package config

import "testing"

func testConfigWithJump() *Config {
	return &Config{
		Jumps: map[string]JumpConfig{
			"bastion": {Host: "10.0.0.1", Port: 22, User: "root"},
		},
	}
}

func TestResolveTargetRawIPDefaultsDirect(t *testing.T) {
	c := testConfigWithJump()

	tg, jump, err := c.ResolveTarget("128.1.2.3")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if jump != nil {
		t.Fatalf("裸 IP 应直连，实际跳板：%+v", jump)
	}
	if !tg.NoJump {
		t.Fatalf("裸 IP 应标记为直连，实际 %+v", tg)
	}
	if tg.IDOrIP != "128.1.2.3" {
		t.Fatalf("查询键应为裸 IP，实际 %q", tg.IDOrIP)
	}
}

func TestResolveTargetCSVEmptyJumpIsDirect(t *testing.T) {
	c := testConfigWithJump()
	c.Servers = []ServerEntry{{Alias: "web1", Target: "10.0.0.2"}}

	_, jump, err := c.ResolveTarget("web1")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if jump != nil {
		t.Fatalf("清单条目 jump 留空应直连，实际 %+v", jump)
	}
}

func TestResolveTargetCSVDashForcesDirect(t *testing.T) {
	c := testConfigWithJump()
	c.Servers = []ServerEntry{{Alias: "web1", Target: "10.0.0.2", NoJump: true}}

	_, jump, err := c.ResolveTarget("web1")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if jump != nil {
		t.Fatalf("清单条目 jump 写 - 应强制直连，实际 %+v", jump)
	}
}
