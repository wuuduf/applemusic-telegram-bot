package cmdrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunnerCapturesStderrOnFailure(t *testing.T) {
	t.Parallel()
	runner := New(5 * time.Second)
	args := helperProcessArgs("stderr-exit")
	result, err := runner.RunWithOptions(context.Background(), os.Args[0], args, RunOptions{
		Env: []string{
			"GO_WANT_HELPER_PROCESS=1",
			"HELPER_MODE=stderr-exit",
			"HELPER_STDERR=boom-from-helper",
		},
		Timeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatalf("expected command error")
	}
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected CommandError, got %T", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code")
	}
	if !strings.Contains(result.Stderr, "boom-from-helper") {
		t.Fatalf("expected stderr in result, got: %q", result.Stderr)
	}
	if !strings.Contains(cmdErr.Error(), "boom-from-helper") {
		t.Fatalf("expected stderr in error message, got: %q", cmdErr.Error())
	}
}

func TestRunnerTimeoutKillsProcess(t *testing.T) {
	t.Parallel()
	runner := New(150 * time.Millisecond)
	args := helperProcessArgs("sleep")
	start := time.Now()
	_, err := runner.RunWithOptions(context.Background(), os.Args[0], args, RunOptions{
		Env: []string{
			"GO_WANT_HELPER_PROCESS=1",
			"HELPER_MODE=sleep",
		},
		Timeout: 150 * time.Millisecond,
	})
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if !IsTimeout(err) {
		t.Fatalf("expected timeout error, got: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout kill took too long: %s", time.Since(start))
	}
}

func helperProcessArgs(mode string) []string {
	return []string{"-test.run=TestHelperProcess", "--", mode}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	mode := os.Getenv("HELPER_MODE")
	switch mode {
	case "stderr-exit":
		fmt.Fprintln(os.Stderr, os.Getenv("HELPER_STDERR"))
		os.Exit(7)
	case "sleep":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode: %s\n", mode)
		os.Exit(2)
	}
}
