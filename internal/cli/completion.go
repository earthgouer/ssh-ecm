package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newCompletionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "completion [bash]",
		Short: "输出 shell 补全脚本（bash）",
		Long: `输出 bash 补全脚本。加入 ~/.bashrc 后，ssh-ecm <TAB> 会补全 servers.txt 里的别名：

  source <(ssh-ecm completion bash)`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "bash" {
				return fmt.Errorf("暂只支持 bash，收到 %q（用法：ssh-ecm completion bash）", args[0])
			}
			return cmd.Root().GenBashCompletion(os.Stdout)
		},
	}
}
