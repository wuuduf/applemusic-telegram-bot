package app

import (
	"context"
	"os"
	"strings"

	"github.com/wuuduf/applemusic-telegram-bot/utils/cmdrunner"
)

func runExternalCommand(ctx context.Context, name string, args ...string) (cmdrunner.Result, error) {
	result, err := cmdrunner.RunWithOptions(ctx, name, args, cmdrunner.RunOptions{})
	if cmdrunner.IsTimeout(err) {
		appRuntimeMetrics.recordExternalCommandTimeout()
	}
	return result, err
}

func runExternalCommandInDir(ctx context.Context, dir string, name string, args ...string) (cmdrunner.Result, error) {
	result, err := cmdrunner.RunWithOptions(ctx, name, args, cmdrunner.RunOptions{Dir: dir})
	if cmdrunner.IsTimeout(err) {
		appRuntimeMetrics.recordExternalCommandTimeout()
	}
	return result, err
}

func runMP4BoxWithTags(ctx context.Context, tags []string, args ...string) (cmdrunner.Result, error) {
	if len(tags) == 0 {
		return runExternalCommand(ctx, "MP4Box", args...)
	}
	tagFile, err := os.CreateTemp("", "mp4box-itags-*.txt")
	if err != nil {
		return cmdrunner.Result{}, err
	}
	tagPath := tagFile.Name()
	defer os.Remove(tagPath)
	if _, err := tagFile.WriteString(strings.Join(tags, "\n") + "\n"); err != nil {
		_ = tagFile.Close()
		return cmdrunner.Result{}, err
	}
	if err := tagFile.Close(); err != nil {
		return cmdrunner.Result{}, err
	}
	mp4Args := append([]string{"-itags", tagPath}, args...)
	return runExternalCommand(ctx, "MP4Box", mp4Args...)
}
