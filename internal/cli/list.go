package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ecm/ssh-ecm/internal/config"
)

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出 servers.txt 中的全部服务器",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load("")
			if err != nil {
				return err
			}
			entries, err := config.LoadServers(config.ServersPath())
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Fprintf(os.Stderr, "提示：%s 里还没有服务器，执行 ssh-ecm edit 添加\n", config.ServersPath())
				return nil
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(tw, "别名\t目标\tsu 用户\t跳板\t备注")
			for _, e := range entries {
				su := "-"
				if e.SuUser != "" && !e.NoSu {
					su = e.SuUser
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					e.Alias, e.Target, su, jumpColumn(cfg, e), noteOrDash(e.Note))
			}
			return tw.Flush()
		},
	}
}

func jumpColumn(cfg *config.Config, e config.ServerEntry) string {
	switch {
	case e.NoJump:
		return "直连(-)"
	case e.Jump != "":
		return e.Jump
	case cfg.Defaults.Jump != "":
		return "(默认) " + cfg.Defaults.Jump
	default:
		return "直连"
	}
}

func noteOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func newEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "用 $EDITOR（默认 vi）编辑 servers.txt",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureServersFile(); err != nil {
				return err
			}
			path := config.ServersPath()

			editor := strings.TrimSpace(os.Getenv("EDITOR"))
			if editor == "" {
				editor = "vi"
			}
			fields := strings.Fields(editor)
			if len(fields) == 0 {
				fields = []string{"vi"}
			}
			c := exec.Command(fields[0], append(fields[1:], path)...)
			c.Stdin = os.Stdin
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			if err := c.Run(); err != nil {
				return fmt.Errorf("编辑器 %s 异常退出：%w", fields[0], err)
			}

			// 改完立即校验，但不阻断（允许用户再修）。
			if _, err := config.LoadServers(path); err != nil {
				fmt.Fprintf(os.Stderr, "警告：%v\n", err)
			}
			return nil
		},
	}
}
