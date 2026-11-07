// Package sshx 负责建立到目标机的 SSH 连接（直连 / 经跳板机），
// 以及 TOFU（首次信任）式的主机指纹校验。
package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/ecm/ssh-ecm/internal/config"
)

// Target 是最终要连的机器。
type Target struct {
	User     string // 通常 "root"
	IP       string
	Port     int
	Password string // 只走内存
}

// Jump 是解析后的跳板机。
type Jump struct {
	Name string
	Host string
	Port int
	User string
	Key  string
	// PasswordEnv 是跳板机密码所在的环境变量名；为空时走终端提示。
	PasswordEnv string
}

// Dial 建立 SSH 连接。jump 为 nil 时直连。
func Dial(target *Target, jump *Jump, timeout time.Duration) (*ssh.Client, error) {
	if target == nil || strings.TrimSpace(target.IP) == "" {
		return nil, fmt.Errorf("目标 IP 为空")
	}
	if target.Port <= 0 {
		target.Port = 22
	}
	if strings.TrimSpace(target.User) == "" {
		target.User = "root"
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	hostKeyCB, err := tofuCallback(config.KnownHostsPath())
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(target.IP, strconv.Itoa(target.Port))
	targetCfg := &ssh.ClientConfig{
		User:            target.User,
		Auth:            []ssh.AuthMethod{ssh.Password(target.Password)},
		HostKeyCallback: hostKeyCB,
		Timeout:         timeout,
	}

	if jump == nil {
		client, err := ssh.Dial("tcp", addr, targetCfg)
		if err != nil {
			return nil, explainDialError(addr, err)
		}
		return client, nil
	}

	jumpAddr := net.JoinHostPort(jump.Host, strconv.Itoa(jump.Port))
	auths, err := jumpAuth(jump)
	if err != nil {
		return nil, err
	}
	jumpCfg := &ssh.ClientConfig{
		User:            jump.User,
		Auth:            auths,
		HostKeyCallback: hostKeyCB,
		Timeout:         timeout,
	}
	jclient, err := ssh.Dial("tcp", jumpAddr, jumpCfg)
	if err != nil {
		return nil, explainJumpDialError(jumpAddr, err)
	}

	type dialResult struct {
		conn net.Conn
		err  error
	}
	ch := make(chan dialResult, 1)
	go func() {
		conn, err := jclient.Dial("tcp", addr)
		ch <- dialResult{conn: conn, err: err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			jclient.Close()
			return nil, fmt.Errorf("经跳板机 %s 连接目标 %s 失败：%w", jumpAddr, addr, explainDialError(addr, r.err))
		}
		cc, chans, reqs, err := ssh.NewClientConn(r.conn, addr, targetCfg)
		if err != nil {
			r.conn.Close()
			jclient.Close()
			return nil, explainDialError(addr, err)
		}
		return ssh.NewClient(cc, chans, reqs), nil
	case <-time.After(timeout):
		jclient.Close()
		return nil, fmt.Errorf("经跳板机 %s 连接目标 %s 超时（%s）", jumpAddr, addr, timeout)
	}
}

// jumpAuth 组装跳板机认证方式：公钥优先（ssh-agent + 私钥文件），
// 公钥用不上时回退到密码（password_env 或终端提示），并兼容 keyboard-interactive。
func jumpAuth(jump *Jump) ([]ssh.AuthMethod, error) {
	methods, loadErrs := jumpKeyMethods(jump)

	pw := &jumpPassword{jump: jump}
	if len(methods) == 0 && !pw.available() {
		if len(loadErrs) > 0 {
			return nil, fmt.Errorf("加载跳板机私钥失败：%s", strings.Join(loadErrs, "；"))
		}
		return nil, fmt.Errorf("没有可用于登录跳板机 %s 的 SSH 密钥，也无法交互输入密码："+
			"请把公钥加入跳板机 authorized_keys，或配置 jumps.<别名>.password_env / 在终端下重试", jump.Host)
	}

	// 这两个方法只在服务器要求密码认证时才会被调用，公钥能过就不会打扰用户。
	methods = append(methods,
		ssh.KeyboardInteractive(pw.answer),
		ssh.PasswordCallback(pw.get),
	)
	return methods, nil
}

// jumpKeyMethods 收集公钥认证方式；没有任何可用方式时返回空切片与加载错误。
func jumpKeyMethods(jump *Jump) ([]ssh.AuthMethod, []string) {
	var methods []ssh.AuthMethod

	if sock := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK")); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			ag := agent.NewClient(conn)
			methods = append(methods, ssh.PublicKeysCallback(ag.Signers))
		}
	}

	var candidates []string
	explicit := strings.TrimSpace(jump.Key)
	if explicit != "" {
		candidates = append(candidates, expandHome(explicit))
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".ssh", "id_rsa"),
			filepath.Join(home, ".ssh", "id_ed25519"),
			filepath.Join(home, ".ssh", "id_ecdsa"),
		)
	}

	explicitPath := expandHome(explicit)
	seen := map[string]bool{}
	var loadErrs []string
	for _, p := range candidates {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		data, err := os.ReadFile(p)
		if err != nil {
			if p == explicitPath {
				loadErrs = append(loadErrs, fmt.Sprintf("%s（%v）", p, err))
			}
			continue
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			// 带口令的私钥无法在这里解密，交给 ssh-agent。
			if p == explicitPath {
				loadErrs = append(loadErrs, fmt.Sprintf("%s（%v）", p, err))
			}
			continue
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	return methods, loadErrs
}

// tofuCallback 返回 TOFU 校验函数：未知主机记录到专用 known_hosts，指纹变更直接拒绝。
func tofuCallback(path string) (ssh.HostKeyCallback, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建配置目录失败：%w", err)
		}
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("创建 %s 失败：%w", path, err)
		}
		f.Close()
	} else if err != nil {
		return nil, fmt.Errorf("检查 %s 失败：%w", path, err)
	}

	var mu sync.Mutex
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		mu.Lock()
		defer mu.Unlock()

		cb, err := knownhosts.New(path)
		if err != nil {
			return fmt.Errorf("读取 %s 失败：%w", path, err)
		}
		if err := cb(hostname, remote, key); err == nil {
			return nil
		} else {
			var kerr *knownhosts.KeyError
			if !errors.As(err, &kerr) {
				return err
			}
			if len(kerr.Want) > 0 {
				return fmt.Errorf("主机 %s 的密钥与 %s 记录不一致（可能是中间人攻击或机器重装）：\n"+
					"  当前密钥指纹：%s\n"+
					"  如确认无误，删除 %s 中对应行后重试",
					hostname, path, ssh.FingerprintSHA256(key), path)
			}
			// 首次连接：记录（TOFU）
			line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
			f, ferr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if ferr != nil {
				return fmt.Errorf("写入 %s 失败：%w", path, ferr)
			}
			defer f.Close()
			if _, ferr := f.WriteString(line + "\n"); ferr != nil {
				return fmt.Errorf("写入 %s 失败：%w", path, ferr)
			}
			return nil
		}
	}, nil
}

// explainDialError 把目标机的底层错误翻译成人话。
func explainDialError(addr string, err error) error {
	return explainDialErrorAs(addr, err, false)
}

// explainJumpDialError 是跳板机专用版本：认证失败时提示检查 user/key/密码，而不是 ECM 密码。
func explainJumpDialError(addr string, err error) error {
	return explainDialErrorAs(addr, err, true)
}

func explainDialErrorAs(addr string, err error, jump bool) error {
	if err == nil {
		return nil
	}
	prefix := "连接 "
	if jump {
		prefix = "连接跳板机 "
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return fmt.Errorf("%s%s 超时（网络不通、目标机未开机或防火墙拦截）", prefix, addr)
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"),
		strings.Contains(msg, "no supported methods remain"),
		strings.Contains(msg, "permission denied"):
		if jump {
			return fmt.Errorf("%s%s 认证失败：请确认 jumps 里的 user 是否正确、ssh-agent / ~/.ssh 里有该跳板机认可的私钥；"+
				"若该账号只允许密码登录，可配置 password_env 或在终端下重试", prefix, addr)
		}
		return fmt.Errorf("%s%s 认证失败：请确认 ECM 返回的 root 密码正确，且目标机允许密码登录", prefix, addr)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("%s%s 被拒绝：端口不通或 sshd 未启动", prefix, addr)
	case strings.Contains(msg, "no route to host"):
		return fmt.Errorf("%s%s 失败：没有到主机的路由（检查网络或跳板机）", prefix, addr)
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "deadline exceeded"):
		return fmt.Errorf("%s%s 超时（网络不通、目标机未开机或防火墙拦截）", prefix, addr)
	case strings.Contains(msg, "known_hosts"), strings.Contains(msg, "密钥与"):
		return err // TOFU 回调已给出中文说明
	}
	return fmt.Errorf("%s%s 失败：%w", prefix, addr, err)
}

func expandHome(p string) string {
	if p == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}
