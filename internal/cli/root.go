package cli

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ecm/ssh-ecm/internal/config"
)

// Version 由 build.sh 通过 -ldflags -X 注入。
var Version = "1.4.0"

var (
	flagJump     string
	flagJumpHost string
	flagSuUser   string
	flagNoJump   bool
	flagPort     int
	flagTimeout  time.Duration
	flagVerbose  bool
	flagInfo     bool
)

// ExitCodeError 让 main 能够原样透传远端退出码。
type ExitCodeError struct{ Code int }

func (e *ExitCodeError) Error() string { return fmt.Sprintf("远端命令退出码 %d", e.Code) }

// NewRootCmd 构建 CLI 命令树。
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ssh-ecm [目标] [命令...]",
		Short: "一键 root 登录内网服务器（IP 与密码实时取自 ECM 平台）",
		Long: `ssh-ecm 用原生 SSH 协议直连或经跳板机登录内网服务器。

  目标       servers.csv 里的别名、ECM 服务器 ID，或 IP 地址
  命令       给了命令就不进交互 shell，执行后按远端退出码退出
  裸 IP      不在 servers.csv 里的目标默认直连；要走跳板请加 -j 别名 / -J

示例:
  ssh-ecm web1                 登录 web1（交互 shell）
  ssh-ecm web1 -i              只查询并打印服务器信息（含 root 密码），不登录
  ssh-ecm web1 -j bastion      走指定跳板机（必须给别名）
  ssh-ecm web1 -J 10.1.1.1:22  临时指定跳板机地址
  ssh-ecm web1 -u app          登录后自动 su - app
  ssh-ecm web1 -p 2222         覆盖端口
  ssh-ecm web1 --no-jump       显式直连（默认行为）
  ssh-ecm web1 "hostname"      非交互执行命令
  ssh-ecm                      无参数：上下键选服务器，/ 过滤，回车连接
  ssh-ecm list                 列出全部服务器
  ssh-ecm ls web1 /tmp         通过 SFTP 列出远端目录
  ssh-ecm get web1 /tmp/a ./a  通过 SFTP 下载文件
  ssh-ecm put web1 ./a /tmp/a  通过 SFTP 上传文件
  ssh-ecm rm web1 /tmp/a       通过 SFTP 删除远端文件
  ssh-ecm edit                 编辑 servers.csv
  ssh-ecm completion bash      输出 bash 补全脚本
  ssh-ecm version              打印版本信息`,
		Args:               cobra.ArbitraryArgs,
		SilenceUsage:       true,
		SilenceErrors:      true,
		ValidArgsFunction:  completeTargets,
		DisableSuggestions: false,
		RunE:               runLogin,
		CompletionOptions:  cobra.CompletionOptions{DisableDefaultCmd: true},
	}

	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		msg := err.Error()
		if strings.Contains(msg, "flag needs an argument") &&
			(strings.Contains(msg, "'j'") || strings.Contains(msg, "--jump")) {
			return fmt.Errorf("-j 需要指定跳板别名（例如 -j bastion）")
		}
		return err
	})

	pf := root.PersistentFlags()
	pf.StringVarP(&flagJump, "jump", "j", "", "走跳板机，必须指定 jumps 里的别名（如 -j bastion）")
	pf.StringVarP(&flagJumpHost, "jump-host", "J", "", "临时指定跳板机地址（user@host:port），优先于 -j")
	pf.StringVarP(&flagSuUser, "su-user", "u", "", "登录后 su - <user>，覆盖别名里配置的 su 用户")
	pf.BoolVar(&flagNoJump, "no-jump", false, "显式直连（默认就是直连）")
	pf.IntVarP(&flagPort, "port", "p", 0, "覆盖目标端口（默认用 API 返回或配置端口）")
	pf.DurationVarP(&flagTimeout, "timeout", "t", 10*time.Second, "SSH 连接超时")
	pf.BoolVarP(&flagVerbose, "verbose", "v", false, "打印解析后的连接信息（绝不打印密码）")
	pf.BoolVarP(&flagInfo, "info", "i", false, "查询模式：打印服务器信息（含 root 密码）后退出，不登录")

	root.AddCommand(
		newListCmd(),
		newEditCmd(),
		newCompletionCmd(),
		newVersionCmd(),
		newLsCmd(),
		newGetCmd(),
		newPutCmd(),
		newRmCmd(),
	)
	return root
}

// Execute 是入口，负责参数预处理、错误格式与退出码。
func Execute() {
	root := NewRootCmd()
	root.SetArgs(os.Args[1:])
	if err := root.Execute(); err != nil {
		var codeErr *ExitCodeError
		if errors.As(err, &codeErr) {
			os.Exit(codeErr.Code)
		}
		fmt.Fprintf(os.Stderr, "ssh-ecm: %v\n", err)
		os.Exit(1)
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "打印版本与编译信息",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("ssh-ecm v%s\n", Version)
			fmt.Printf("go: %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
			fmt.Printf("配置目录: %s\n", config.ConfigDir())
			return nil
		},
	}
}

// completeTargets 为 `ssh-ecm <TAB>` 动态补全 servers.csv 里的别名（每次重新读文件）。
func completeTargets(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	entries, err := config.LoadServers(config.ServersPath())
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	comps := make([]string, 0, len(entries))
	for _, e := range entries {
		desc := e.Note
		if desc == "" {
			desc = e.Target
		}
		comps = append(comps, e.Alias+"\t"+desc)
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}
