package sshx

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

// jumpPassword 惰性获取跳板机密码：环境变量优先，其次终端输入；整个进程只取一次。
type jumpPassword struct {
	jump *Jump

	once sync.Once
	pw   string
	err  error
}

// available 报告是否有密码来源（不实际输入）。
func (p *jumpPassword) available() bool {
	if env := strings.TrimSpace(p.jump.PasswordEnv); env != "" && strings.TrimSpace(os.Getenv(env)) != "" {
		return true
	}
	_, closeFn, ok := promptFile()
	if ok {
		closeFn()
	}
	return ok
}

// get 供 ssh.PasswordCallback 使用：只有服务器要求密码时才调用。
func (p *jumpPassword) get() (string, error) {
	p.once.Do(func() { p.pw, p.err = resolveJumpPassword(p.jump) })
	return p.pw, p.err
}

// answer 供 ssh.KeyboardInteractive 使用：PAM 常见形式是单个不回显的问题。
func (p *jumpPassword) answer(_, _ string, questions []string, echos []bool) ([]string, error) {
	pw, err := p.get()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(questions))
	for i := range questions {
		if i < len(echos) && !echos[i] {
			out[i] = pw
		}
	}
	return out, nil
}

func resolveJumpPassword(jump *Jump) (string, error) {
	env := strings.TrimSpace(jump.PasswordEnv)
	if env != "" {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, nil
		}
	}

	f, closeFn, ok := promptFile()
	if !ok {
		if env != "" {
			return "", fmt.Errorf("环境变量 %s 为空，且当前不是交互终端，无法输入跳板机密码", env)
		}
		return "", errors.New("当前不是交互终端，无法输入跳板机密码：请配置 jumps.<别名>.password_env，" +
			"或把公钥加入跳板机 authorized_keys")
	}
	defer closeFn()

	fmt.Fprintf(os.Stderr, "跳板机 %s 需要密码（%s）：", jump.Host, jump.User)
	b, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("读取跳板机密码失败：%w", err)
	}
	if len(b) == 0 {
		return "", errors.New("跳板机密码为空")
	}
	return string(b), nil
}

// promptFile 返回可读密码的终端：优先 stdin（若它是终端），否则打开 /dev/tty，
// 避免管道输入被密码提示吃掉。
var promptFile = func() (*os.File, func(), bool) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return os.Stdin, func() {}, true
	}
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, false
	}
	return f, func() { f.Close() }, true
}
