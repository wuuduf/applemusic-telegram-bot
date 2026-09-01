//go:build windows

package app

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// diskFreeBytes 返回路径所在文件系统的可用字节数（Windows 实现）。
func diskFreeBytes(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	target := path
	if !info.IsDir() {
		target = filepath.Dir(path)
	}
	ptr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return 0, err
	}
	var freeBytesAvailableToCaller uint64
	var totalNumberOfBytes uint64
	var totalNumberOfFreeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &freeBytesAvailableToCaller, &totalNumberOfBytes, &totalNumberOfFreeBytes); err != nil {
		return 0, err
	}
	return int64(freeBytesAvailableToCaller), nil
}
