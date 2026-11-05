// Package term 负责交互式终端：远端 PTY + 本地 raw mode + 窗口尺寸同步 + su 注入。
package term

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// terminalModes 与原生 ssh 的 PTY 模式保持一致，保证 vim/htop 等全屏程序正常。
func terminalModes() ssh.TerminalModes {
	return ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.ISIG:          1,
		ssh.ICANON:        1,
		ssh.IEXTEN:        1,
		ssh.IXON:          1,
		ssh.ICRNL:         1,
		ssh.OPOST:         1,
		ssh.ONLCR:         1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
}

// RunInteractive 在远端建立 shell 并接管本机终端，直到会话结束。
//
// initCmds 在 shell 建立后立即写入（例如 "su - app\n"）；不等待提示符，
// 直接写入即可，pty 输入有缓冲，远端 shell 读到后会执行。
func RunInteractive(client *ssh.Client, initCmds string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("创建 SSH 会话失败：%w", err)
	}
	defer session.Close()

	inFd := int(os.Stdin.Fd())
	outFd := int(os.Stdout.Fd())

	width, height, err := term.GetSize(outFd)
	if err != nil || width <= 0 || height <= 0 {
		width, height = 80, 24
	}
	if err := session.RequestPty("xterm-256color", height, width, terminalModes()); err != nil {
		return fmt.Errorf("申请远端 PTY 失败：%w", err)
	}

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	// 用管道把「initCmds + 本机键盘输入」串起来，保证 su 命令只写一次且先于用户输入。
	pr, pw := io.Pipe()
	session.Stdin = pr

	oldState, err := term.MakeRaw(inFd)
	if err != nil {
		return fmt.Errorf("切换终端 raw 模式失败（请确认在真实终端里运行）：%w", err)
	}
	// 无论怎么退出都必须恢复终端属性，否则终端会"变哑"。
	defer func() { _ = term.Restore(inFd, oldState) }()

	// resize 同步：窗口拖拽时把新尺寸发给远端。
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if w, h, err := term.GetSize(outFd); err == nil && w > 0 && h > 0 {
				_ = session.WindowChange(h, w)
			}
		}
	}()

	if err := session.Shell(); err != nil {
		return fmt.Errorf("启动远端 shell 失败：%w", err)
	}

	if initCmds != "" {
		if _, err := io.WriteString(pw, initCmds); err != nil {
			return fmt.Errorf("写入初始命令失败：%w", err)
		}
	}
	go func() {
		_, _ = io.Copy(pw, os.Stdin)
	}()

	if err := session.Wait(); err != nil {
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			return err // 交给调用方取退出码
		}
		var missing *ssh.ExitMissingError
		if errors.As(err, &missing) {
			return fmt.Errorf("远端会话异常结束（连接可能被中断）")
		}
		return fmt.Errorf("远端会话异常结束：%w", err)
	}
	return nil
}

// RunCommand 非交互执行一条远端命令，stdout/stderr 分流，返回远端退出码。
func RunCommand(client *ssh.Client, command string) (int, error) {
	session, err := client.NewSession()
	if err != nil {
		return 1, fmt.Errorf("创建 SSH 会话失败：%w", err)
	}
	defer session.Close()

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	if err := session.Run(command); err != nil {
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitStatus(), nil
		}
		var missing *ssh.ExitMissingError
		if errors.As(err, &missing) {
			return 255, nil
		}
		return 1, fmt.Errorf("执行远端命令失败：%w", err)
	}
	return 0, nil
}
