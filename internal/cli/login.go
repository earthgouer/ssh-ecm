package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/ecm/ssh-ecm/internal/config"
	"github.com/ecm/ssh-ecm/internal/ecm"
	"github.com/ecm/ssh-ecm/internal/expr"
	"github.com/ecm/ssh-ecm/internal/sshx"
	sshterm "github.com/ecm/ssh-ecm/internal/term"
)

func runLogin(cmd *cobra.Command, args []string) error {
	var target, remoteCmd string
	if len(args) > 0 {
		target = strings.TrimSpace(args[0])
		remoteCmd = strings.TrimSpace(strings.Join(args[1:], " "))
	}

	if flagInfo && remoteCmd != "" {
		return fmt.Errorf("-i/--info 是查询模式，不能与远程命令同时使用（想执行命令请去掉 -i）")
	}
	if target == "" {
		if flagInfo {
			return fmt.Errorf("查询模式需要指定目标，例如：ssh-ecm web1 -i")
		}
		return selectAndLogin()
	}
	return login(target, remoteCmd)
}

// login 完成一次完整流程：解析 → 调 ECM → (查询 / 登录)。
func login(targetName, remoteCmd string) error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	if err := cfg.LoadServersInto(); err != nil {
		return err
	}

	tg, jump, err := cfg.ResolveTarget(targetName)
	if err != nil {
		return err
	}
	if err := applyFlags(cfg, tg, &jump); err != nil {
		return err
	}

	client, err := ecm.NewClient(cfg.API)
	if err != nil {
		return err
	}
	if flagVerbose {
		fmt.Fprintf(os.Stderr, "[verbose] 目标：%s（%s）\n", tg.Alias, tg.IDOrIP)
	}
	info, err := queryServer(client, tg)
	if err != nil {
		return err
	}

	ip := strings.TrimSpace(info.IP)
	port := resolvePort(cfg, tg, info)
	suUser := tg.SuUser
	if tg.NoSu {
		suUser = ""
	}
	jumpDesc := describeJump(jump)

	if flagInfo {
		if err := printInfo(cfg.API, tg, info, ip, port, suUser, jumpDesc); err != nil {
			return err
		}
		return nil
	}
	if flagVerbose {
		fmt.Fprintf(os.Stderr, "[verbose] IP：%s  端口：%d  用户：root\n", ip, port)
		fmt.Fprintf(os.Stderr, "[verbose] 跳板：%s\n", jumpDesc)
		if suUser != "" {
			fmt.Fprintf(os.Stderr, "[verbose] 登录后切换：su - %s\n", suUser)
		}
		fmt.Fprintf(os.Stderr, "[verbose] 认证：密码（来自 ECM，不显示）\n")
	}

	sshClient, err := sshx.Dial(&sshx.Target{
		User:     "root",
		IP:       ip,
		Port:     port,
		Password: info.Password,
	}, toSSHJump(jump), flagTimeout)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	if remoteCmd != "" {
		code, err := sshterm.RunCommand(sshClient, remoteCmd)
		if err != nil {
			return err
		}
		if code != 0 {
			return &ExitCodeError{Code: code}
		}
		return nil
	}

	initCmds := ""
	if suUser != "" {
		initCmds = "su - " + suUser + "\n"
	}
	if err := sshterm.RunInteractive(sshClient, initCmds); err != nil {
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			if code := exitErr.ExitStatus(); code != 0 {
				return &ExitCodeError{Code: code}
			}
			return nil
		}
		return err
	}
	return nil
}

// queryServer 调 ECM API 拿 IP / 端口 / root 密码。
func queryServer(client ecm.Client, tg *config.Target) (*ecm.ServerInfo, error) {
	info, err := client.GetServer(tg.IDOrIP)
	if err != nil {
		if isIPv4(tg.IDOrIP) {
			return nil, fmt.Errorf("未在 ECM 找到该机器（%s）：%w", tg.IDOrIP, err)
		}
		return nil, fmt.Errorf("从 ECM 查询 %q 失败：%w", tg.IDOrIP, err)
	}
	if strings.TrimSpace(info.IP) == "" && isIPv4(tg.IDOrIP) {
		info.IP = tg.IDOrIP
	}
	return info, nil
}

// applyFlags 用命令行 flag 覆盖 servers.txt / config.yaml 的解析结果。
func applyFlags(cfg *config.Config, tg *config.Target, jump **config.JumpConfig) error {
	if flagPort > 0 {
		tg.Port = flagPort
	}
	if flagSuUser != "" {
		tg.SuUser = flagSuUser
		tg.NoSu = false
	}

	switch {
	case flagJumpHost != "":
		j, err := parseJumpHost(flagJumpHost)
		if err != nil {
			return err
		}
		*jump = j
		tg.NoJump = false
	case flagJump != "":
		alias := flagJump
		if alias == jumpBare {
			alias = strings.TrimSpace(cfg.Defaults.Jump)
			if alias == "" {
				return fmt.Errorf("-j 需要用默认跳板机，但 %s 里没有配置 defaults.jump", config.ConfigPath())
			}
		}
		j, err := cfg.ResolveJump(alias)
		if err != nil {
			return err
		}
		*jump = j
		tg.NoJump = false
	case flagNoJump:
		*jump = nil
		tg.NoJump = true
	}
	return nil
}

// resolvePort 端口优先级：-p > ECM 返回 > 别名配置 > defaults.port > 22。
func resolvePort(cfg *config.Config, tg *config.Target, info *ecm.ServerInfo) int {
	if flagPort > 0 {
		return flagPort
	}
	port := tg.Port
	if info != nil && info.Port > 0 {
		port = info.Port
	}
	if port == 0 {
		port = cfg.Defaults.Port
	}
	if port == 0 {
		port = 22
	}
	return port
}

// parseJumpHost 解析 -J 的 user@host:port 形式（端口/用户可省略）。
func parseJumpHost(s string) (*config.JumpConfig, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return nil, fmt.Errorf("-J 的跳板机地址为空")
	}
	user := ""
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		user = raw[:i]
		raw = raw[i+1:]
	}

	host, portStr := raw, ""
	if h, p, err := net.SplitHostPort(raw); err == nil {
		host, portStr = h, p
	} else if strings.Contains(raw, ":") {
		// IPv6 或格式错误
		return nil, fmt.Errorf("-J 格式错误：%q，应为 user@host:port", s)
	}

	port := 22
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil || p <= 0 || p > 65535 {
			return nil, fmt.Errorf("-J 的端口非法：%q", portStr)
		}
		port = p
	}
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("-J 格式错误：%q，缺少主机地址", s)
	}
	if user == "" {
		user = config.CurrentUser()
	}
	return &config.JumpConfig{Host: host, Port: port, User: user}, nil
}

func toSSHJump(j *config.JumpConfig) *sshx.Jump {
	if j == nil {
		return nil
	}
	return &sshx.Jump{Name: j.Name, Host: j.Host, Port: j.Port, User: j.User, Key: j.Key}
}

func describeJump(j *config.JumpConfig) string {
	if j == nil {
		return "直连"
	}
	addr := net.JoinHostPort(j.Host, strconv.Itoa(j.Port))
	if j.Name == "" {
		return addr
	}
	return fmt.Sprintf("%s (%s)", j.Name, addr)
}

type infoRow struct{ label, value string }

// printInfo 输出查询模式信息（root 密码按需求原样打印）。
// 配了 api.info.rows 就整体替换默认行，行里的表达式可以取 meta.* 与 data.*。
func printInfo(apiCfg config.APIConfig, tg *config.Target, info *ecm.ServerInfo,
	ip string, port int, suUser, jumpDesc string) error {

	meta := map[string]any{
		"alias":    tg.Alias,
		"target":   tg.IDOrIP,
		"ip":       ip,
		"port":     port,
		"jump":     jumpDesc,
		"su":       suUser,
		"note":     tg.Note,
		"password": info.Password,
	}

	var rows []infoRow
	if len(apiCfg.Info.Rows) > 0 {
		for _, r := range apiCfg.Info.Rows {
			v, err := expr.EvalString(r.Value, expr.Env{Data: info.Data, Meta: meta})
			if err != nil {
				return fmt.Errorf("渲染 api.info.rows 的 %q 失败：%w", r.Label, err)
			}
			rows = append(rows, infoRow{r.Label, v})
		}
		if !infoRowsMentionPassword(apiCfg.Info.Rows) {
			fmt.Fprintln(os.Stderr, "提示：api.info.rows 未包含 meta.password，--info 不会打印 root 密码")
		}
	} else {
		rows = []infoRow{
			{"别名", tg.Alias},
			{"ECM ID", tg.IDOrIP},
			{"IP", ip},
			{"端口", strconv.Itoa(port)},
			{"跳板", jumpDesc},
		}
		if suUser != "" {
			rows = append(rows, infoRow{"su 用户", suUser})
		}
		if tg.Note != "" {
			rows = append(rows, infoRow{"备注", tg.Note})
		}
		rows = append(rows, infoRow{"root密码", info.Password})
	}

	width := 0
	for _, r := range rows {
		if w := displayWidth(r.label); w > width {
			width = w
		}
	}
	for _, r := range rows {
		fmt.Printf("%s%s%s\n", r.label, strings.Repeat(" ", width-displayWidth(r.label)+2), r.value)
	}
	return nil
}

func infoRowsMentionPassword(rows []config.InfoRowConfig) bool {
	for _, r := range rows {
		if strings.Contains(r.Value, "meta.password") {
			return true
		}
	}
	return false
}

// displayWidth 估算终端显示宽度（中文占 2 列），用于 --info 的键值对齐。
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func isIPv4(s string) bool {
	ip := net.ParseIP(strings.TrimSpace(s))
	return ip != nil && ip.To4() != nil && strings.Count(s, ".") == 3
}

// selectAndLogin 无参数时的交互式选单：上下键选择、回车连接。
func selectAndLogin() error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	if err := cfg.LoadServersInto(); err != nil {
		return err
	}
	if len(cfg.Servers) == 0 {
		return fmt.Errorf("%s 里还没有服务器，请执行 ssh-ecm edit 添加（格式：别名<Tab>目标[<Tab>su用户<Tab>跳板<Tab>备注]）",
			config.ServersPath())
	}

	alias, err := pickServer(cfg, cfg.Servers)
	if err != nil {
		return err
	}
	if alias == "" {
		return nil
	}
	return login(alias, "")
}
