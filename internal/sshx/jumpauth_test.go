package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startPasswordServer 起一个只接受密码认证的临时 SSH 服务端。
func startPasswordServer(t *testing.T, user, password string) net.Listener {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成主机密钥失败：%v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("构造 signer 失败：%v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if c.User() == user && string(p) == password {
				return nil, nil
			}
			return nil, errors.New("bad password")
		},
	}
	cfg.AddHostKey(signer)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	t.Cleanup(func() { l.Close() })

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					ch.Reject(ssh.UnknownChannelType, "test server")
				}
			}()
		}
	}()
	return l
}

func TestJumpAuthPasswordFromEnv(t *testing.T) {
	l := startPasswordServer(t, "iomp_j", "s3cret")
	host, portStr, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(portStr)

	t.Setenv("HOME", t.TempDir()) // 避免读到本机 ~/.ssh 下的私钥
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("TEST_JUMP_PW", "s3cret")

	auths, err := jumpAuth(&Jump{Host: host, Port: port, User: "iomp_j", PasswordEnv: "TEST_JUMP_PW"})
	if err != nil {
		t.Fatalf("有 password_env 时不应报错：%v", err)
	}
	client, err := ssh.Dial("tcp", l.Addr().String(), &ssh.ClientConfig{
		User:            "iomp_j",
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatalf("密码认证应成功：%v", err)
	}
	client.Close()
}

func TestJumpAuthNoKeyNoPasswordSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	orig := promptFile
	promptFile = func() (*os.File, func(), bool) { return nil, nil, false }
	t.Cleanup(func() { promptFile = orig })

	_, err := jumpAuth(&Jump{Host: "127.0.0.1", Port: 22, User: "nobody"})
	if err == nil {
		t.Fatal("既没有公钥也没有密码来源时应当报错")
	}
	if !strings.Contains(err.Error(), "password_env") {
		t.Fatalf("错误信息应提示 password_env：%v", err)
	}
}

func TestExplainJumpAuthMessage(t *testing.T) {
	raw := errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain")
	msg := explainJumpDialError("128.194.223.126:22", raw).Error()
	if !strings.Contains(msg, "连接跳板机") {
		t.Fatalf("应说明是跳板机：%s", msg)
	}
	if strings.Contains(msg, "ECM 返回的 root 密码") {
		t.Fatalf("跳板机认证失败不应提 ECM 密码：%s", msg)
	}
	if strings.Count(msg, "128.194.223.126:22") != 1 {
		t.Fatalf("地址不应重复：%s", msg)
	}

	target := explainDialError("10.0.0.1:22", raw).Error()
	if !strings.Contains(target, "ECM 返回的 root 密码") {
		t.Fatalf("目标机认证失败仍应提示 ECM 密码：%s", target)
	}
}
