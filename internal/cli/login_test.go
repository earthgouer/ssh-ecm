package cli

import (
	"testing"

	"github.com/ecm/ssh-ecm/internal/config"
	"github.com/ecm/ssh-ecm/internal/ecm"
)

func TestResolvePortCSVOverride(t *testing.T) {
	cfg := &config.Config{Defaults: config.DefaultsConfig{Port: 22}}

	// servers.csv 显式写了 port：优先于 ECM 返回值
	tg := &config.Target{Port: 2222, PortExplicit: true}
	if got := resolvePort(cfg, tg, &ecm.ServerInfo{Port: 22}); got != 2222 {
		t.Fatalf("CSV 端口应优先，期望 2222，实际 %d", got)
	}

	// 非显式端口（如 config.yaml aliases）：保持 ECM 优先
	tg = &config.Target{Port: 2222}
	if got := resolvePort(cfg, tg, &ecm.ServerInfo{Port: 22}); got != 22 {
		t.Fatalf("ECM 端口应优先，期望 22，实际 %d", got)
	}
}
