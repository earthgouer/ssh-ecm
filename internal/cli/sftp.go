package cli

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pkg/sftp"
	"github.com/spf13/cobra"
)

func newLsCmd() *cobra.Command {
	var long bool
	cmd := &cobra.Command{
		Use:               "ls <目标> [远端路径]",
		Short:             "列出远端目录（SFTP）",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeTargets,
		RunE: func(cmd *cobra.Command, args []string) error {
			remote := "."
			if len(args) == 2 {
				remote = args[1]
			}
			return withSFTP(args[0], func(c *sftp.Client) error {
				return sftpLs(c, remote, long)
			})
		},
	}
	cmd.Flags().BoolVarP(&long, "long", "l", false, "长格式显示")
	return cmd
}

func newGetCmd() *cobra.Command {
	var recursive bool
	cmd := &cobra.Command{
		Use:               "get <目标> <远端路径> [本地路径]",
		Short:             "从远端下载文件（-r 递归目录）",
		Args:              cobra.RangeArgs(2, 3),
		ValidArgsFunction: completeTargets,
		RunE: func(cmd *cobra.Command, args []string) error {
			remote := args[1]
			local := ""
			if len(args) == 3 {
				local = args[2]
			}
			return withSFTP(args[0], func(c *sftp.Client) error {
				return sftpGet(c, remote, local, recursive)
			})
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "递归下载目录")
	return cmd
}

func newPutCmd() *cobra.Command {
	var recursive bool
	cmd := &cobra.Command{
		Use:               "put <目标> <本地路径> [远端路径]",
		Short:             "上传文件到远端（-r 递归目录）",
		Args:              cobra.RangeArgs(2, 3),
		ValidArgsFunction: completeTargets,
		RunE: func(cmd *cobra.Command, args []string) error {
			local := args[1]
			remote := ""
			if len(args) == 3 {
				remote = args[2]
			}
			return withSFTP(args[0], func(c *sftp.Client) error {
				return sftpPut(c, local, remote, recursive)
			})
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "递归上传目录")
	return cmd
}

func newRmCmd() *cobra.Command {
	var recursive, force bool
	cmd := &cobra.Command{
		Use:               "rm <目标> <远端路径>...",
		Short:             "删除远端文件（-r 递归目录）",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeTargets,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSFTP(args[0], func(c *sftp.Client) error {
				for _, remote := range args[1:] {
					if err := sftpRemove(c, remote, recursive, force); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "递归删除目录")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "忽略不存在的路径")
	return cmd
}

// withSFTP 复用 shell 的解析/ECM 查询/拨号流程，再在目标 SSH 连接上开 SFTP 子系统。
// 跳板机不需要额外处理：Dial 返回的已经是到目标机的 ssh.Client。
func withSFTP(target string, fn func(*sftp.Client) error) error {
	if flagInfo {
		return fmt.Errorf("SFTP 命令不支持 -i/--info")
	}
	if strings.TrimSpace(flagSuUser) != "" {
		return fmt.Errorf("SFTP 命令不支持 -u/--su-user：SFTP 会话始终以 root 身份运行")
	}

	ci, err := resolveConn(target)
	if err != nil {
		return err
	}
	sshClient, err := ci.dial()
	if err != nil {
		return err
	}
	defer sshClient.Close()

	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return fmt.Errorf("建立 SFTP 会话失败（目标机可能未启用 sftp subsystem）：%w", err)
	}
	defer client.Close()
	return fn(client)
}

func sftpLs(c *sftp.Client, remote string, long bool) error {
	info, err := c.Stat(remote)
	if err != nil {
		return fmt.Errorf("读取远端 %s 失败：%w", remote, err)
	}
	if !info.IsDir() {
		printFileInfo(os.Stdout, info, long)
		return nil
	}

	entries, err := c.ReadDir(remote)
	if err != nil {
		return fmt.Errorf("读取远端目录 %s 失败：%w", remote, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		printFileInfo(os.Stdout, e, long)
	}
	return nil
}

func printFileInfo(w io.Writer, info os.FileInfo, long bool) {
	name := info.Name()
	if !long {
		if info.IsDir() {
			name += "/"
		}
		fmt.Fprintln(w, name)
		return
	}
	fmt.Fprintf(w, "%s %10d %s %s\n",
		info.Mode().String(), info.Size(), info.ModTime().Format("2006-01-02 15:04"), name)
}

func sftpGet(c *sftp.Client, remote, local string, recursive bool) error {
	info, err := c.Stat(remote)
	if err != nil {
		return fmt.Errorf("读取远端 %s 失败：%w", remote, err)
	}

	cleanRemote := strings.TrimSuffix(remote, "/")
	if cleanRemote == "" {
		cleanRemote = "/"
	}
	if local == "" {
		local = filepath.Base(filepath.FromSlash(cleanRemote))
		if local == "." || local == string(filepath.Separator) || local == "" {
			return fmt.Errorf("无法从远端路径 %q 推断本地文件名", remote)
		}
	}

	if info.IsDir() {
		if !recursive {
			return fmt.Errorf("%s 是目录，下载目录请加 -r", remote)
		}
		return getDir(c, cleanRemote, local)
	}

	if st, err := os.Stat(local); err == nil && st.IsDir() {
		local = filepath.Join(local, filepath.Base(filepath.FromSlash(cleanRemote)))
	}
	return getFile(c, remote, local, info.Mode().Perm())
}

func getDir(c *sftp.Client, remote, local string) error {
	return walkRemote(c, remote, func(p string, info os.FileInfo) error {
		rel := strings.TrimPrefix(p, remote)
		rel = strings.TrimPrefix(rel, "/")

		dst := local
		if rel != "" {
			dst = filepath.Join(local, filepath.FromSlash(rel))
		}
		if info.IsDir() {
			mode := info.Mode().Perm()
			if mode == 0 {
				mode = 0o755
			}
			return os.MkdirAll(dst, mode)
		}
		return getFile(c, p, dst, info.Mode().Perm())
	})
}

func getFile(c *sftp.Client, remote, local string, mode os.FileMode) error {
	r, err := c.Open(remote)
	if err != nil {
		return fmt.Errorf("打开远端 %s 失败：%w", remote, err)
	}
	defer r.Close()

	if dir := filepath.Dir(local); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建本地目录 %s 失败：%w", dir, err)
		}
	}
	if mode == 0 {
		mode = 0o644
	}
	w, err := os.OpenFile(local, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("创建本地文件 %s 失败：%w", local, err)
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return fmt.Errorf("写入本地文件 %s 失败：%w", local, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("关闭本地文件 %s 失败：%w", local, err)
	}
	_ = os.Chmod(local, mode)
	return nil
}

func sftpPut(c *sftp.Client, local, remote string, recursive bool) error {
	info, err := os.Stat(local)
	if err != nil {
		return fmt.Errorf("读取本地 %s 失败：%w", local, err)
	}

	cleanLocal := filepath.Clean(local)
	if remote == "" {
		remote = filepath.Base(cleanLocal)
		if remote == "." || remote == string(filepath.Separator) || remote == "" {
			return fmt.Errorf("无法从本地路径 %q 推断远端文件名", local)
		}
	}
	remote = path.Clean(remote)

	if info.IsDir() {
		if !recursive {
			return fmt.Errorf("%s 是目录，上传目录请加 -r", local)
		}
		return putDir(c, cleanLocal, remote)
	}

	if st, err := c.Stat(remote); err == nil && st.IsDir() {
		remote = path.Join(remote, filepath.Base(cleanLocal))
	}
	return putFile(c, local, remote, info.Mode().Perm())
}

func putDir(c *sftp.Client, local, remote string) error {
	return filepath.Walk(local, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(local, p)
		if err != nil {
			return err
		}

		dst := remote
		if rel != "." {
			dst = path.Join(remote, filepath.ToSlash(rel))
		}
		if info.IsDir() {
			if err := c.MkdirAll(dst); err != nil {
				return fmt.Errorf("创建远端目录 %s 失败：%w", dst, err)
			}
			_ = c.Chmod(dst, info.Mode().Perm())
			return nil
		}
		return putFile(c, p, dst, info.Mode().Perm())
	})
}

func putFile(c *sftp.Client, local, remote string, mode os.FileMode) error {
	r, err := os.Open(local)
	if err != nil {
		return fmt.Errorf("打开本地 %s 失败：%w", local, err)
	}
	defer r.Close()

	if dir := path.Dir(remote); dir != "." && dir != "/" {
		if err := c.MkdirAll(dir); err != nil {
			return fmt.Errorf("创建远端目录 %s 失败：%w", dir, err)
		}
	}

	w, err := c.Create(remote)
	if err != nil {
		return fmt.Errorf("创建远端文件 %s 失败：%w", remote, err)
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return fmt.Errorf("上传 %s 失败：%w", remote, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("关闭远端文件 %s 失败：%w", remote, err)
	}
	if mode != 0 {
		_ = c.Chmod(remote, mode)
	}
	return nil
}

func sftpRemove(c *sftp.Client, remote string, recursive, force bool) error {
	info, err := c.Lstat(remote)
	if err != nil {
		if force {
			return nil
		}
		return fmt.Errorf("读取远端 %s 失败：%w", remote, err)
	}
	if info.IsDir() {
		if !recursive {
			return fmt.Errorf("%s 是目录，删除目录请加 -r", remote)
		}
		if path.Clean(remote) == "/" {
			return fmt.Errorf("拒绝递归删除根目录 %q", remote)
		}
		return removeTree(c, remote)
	}
	if err := c.Remove(remote); err != nil {
		return fmt.Errorf("删除 %s 失败：%w", remote, err)
	}
	return nil
}

func removeTree(c *sftp.Client, root string) error {
	var paths []string
	w := c.Walk(root)
	for w.Step() {
		if err := w.Err(); err != nil {
			return fmt.Errorf("遍历远端 %s 失败：%w", root, err)
		}
		paths = append(paths, w.Path())
	}
	for i := len(paths) - 1; i >= 0; i-- {
		p := paths[i]
		info, err := c.Lstat(p)
		if err != nil {
			return fmt.Errorf("读取远端 %s 失败：%w", p, err)
		}
		if info.IsDir() {
			if err := c.RemoveDirectory(p); err != nil {
				return fmt.Errorf("删除目录 %s 失败：%w", p, err)
			}
			continue
		}
		if err := c.Remove(p); err != nil {
			return fmt.Errorf("删除 %s 失败：%w", p, err)
		}
	}
	return nil
}

func walkRemote(c *sftp.Client, root string, fn func(string, os.FileInfo) error) error {
	w := c.Walk(root)
	for w.Step() {
		if err := w.Err(); err != nil {
			return fmt.Errorf("遍历远端 %s 失败：%w", root, err)
		}
		if err := fn(w.Path(), w.Stat()); err != nil {
			return err
		}
	}
	return nil
}
