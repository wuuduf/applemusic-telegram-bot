//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"syscall"
)

// diskFreeBytes 返回路径所在文件系统的可用字节数（类 Unix 实现）。
func diskFreeBytes(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	target := path
	if !info.IsDir() {
		target = filepath.Dir(path)
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(target, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
