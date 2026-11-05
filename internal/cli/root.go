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

// jumpBare 是 `-j` 不带值时的哨兵值：表示使用配置的 defaults.jump。
const jumpBare = "@default"

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

  目标       servers.txt 里的别名、ECM 服务器 ID，或 IP 地址
  命令       给了命令就不进交互 shell，执行后按远端退出码退出

示例:
  ssh-ecm web1                 登录 web1（交互 shell）
  ssh-ecm web1 -i              只查询并打印服务器信息（含 root 密码），不登录
  ssh-ecm web1 -j              强制走默认跳板机
  ssh-ecm web1 -j bastion      走指定跳板机
  ssh-ecm web1 -J 10.1.1.1:22  临时指定跳板机地址
  ssh-ecm web1 -u app          登录后自动 su - app
  ssh-ecm web1 -p 2222         覆盖端口
  ssh-ecm web1 --no-jump       强制直连
  ssh-ecm web1 "hostname"      非交互执行命令
  ssh-ecm                      无参数：上下键选服务器，/ 过滤，回车连接
  ssh-ecm list                 列出全部服务器
  ssh-ecm edit                 编辑 servers.txt
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

	pf := root.PersistentFlags()
	pf.StringVarP(&flagJump, "jump", "j", "", "走跳板机：不带值=默认跳板；带值=指定跳板别名")
	pf.Lookup("jump").NoOptDefVal = jumpBare
	pf.StringVarP(&flagJumpHost, "jump-host", "J", "", "临时指定跳板机地址（user@host:port），优先于 -j")
	pf.StringVarP(&flagSuUser, "su-user", "u", "", "登录后 su - <user>，覆盖别名里配置的 su 用户")
	pf.BoolVar(&flagNoJump, "no-jump", false, "强制直连，不走默认跳板")
	pf.IntVarP(&flagPort, "port", "p", 0, "覆盖目标端口（默认用 API 返回或配置端口）")
	pf.DurationVarP(&flagTimeout, "timeout", "t", 10*time.Second, "SSH 连接超时")
	pf.BoolVarP(&flagVerbose, "verbose", "v", false, "打印解析后的连接信息（绝不打印密码）")
	pf.BoolVarP(&flagInfo, "info", "i", false, "查询模式：打印服务器信息（含 root 密码）后退出，不登录")

	root.AddCommand(
		newListCmd(),
		newEditCmd(),
		newCompletionCmd(),
		newVersionCmd(),
	)
	return root
}

// Execute 是入口，负责参数预处理、错误格式与退出码。
func Execute() {
	root := NewRootCmd()
	root.SetArgs(normalizeJumpArgs(os.Args)[1:])
	if err := root.Execute(); err != nil {
		var codeErr *ExitCodeError
		if errors.As(err, &codeErr) {
			os.Exit(codeErr.Code)
		}
		fmt.Fprintf(os.Stderr, "ssh-ecm: %v\n", err)
		os.Exit(1)
	}
}

// normalizeJumpArgs 预处理 -j 的两种语义。
//
// `-j` 支持"不带值 = 默认跳板"和"带值 = 指定跳板别名"两种写法，但 pflag 对
// 设置了 NoOptDefVal 的 flag 不会把下一个参数当作值（`-j bastion` 里的 bastion
// 会变成位置参数，被当成远程命令）。这里在解析前做一次确定性的改写：
//
//	-j <非 - 开头的值>  → --jump=<值>（值不是已配置的跳板别名时由后续流程明确报错）
//	-j / --jump（末尾） → --jump=@default
//	-j -x              → --jump=@default -x
//
// 因此"不带值的 -j"请放在最后（如 `ssh-ecm web1 -j`）；`-j` 后面紧跟的非 flag
// 参数一律视为跳板别名，避免歧义。
func normalizeJumpArgs(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	out := make([]string, 0, len(argv))
	out = append(out, argv[0])
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg != "-j" && arg != "--jump" {
			out = append(out, arg)
			continue
		}
		if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
			out = append(out, "--jump="+argv[i+1])
			i++
			continue
		}
		out = append(out, "--jump="+jumpBare)
	}
	return out
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

// completeTargets 为 `ssh-ecm <TAB>` 动态补全 servers.txt 里的别名（每次重新读文件）。
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
