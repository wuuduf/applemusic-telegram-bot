package cmdrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

func countAttemptsWithTimeout(t *testing.T, stdin io.Reader) int {
	t.Helper()
	countFile := filepath.Join(t.TempDir(), "attempts")
	runner := New(5 * time.Second)
	runner.SetRetryPolicy(2, 10*time.Millisecond)
	_, err := runner.RunWithOptions(context.Background(), os.Args[0], helperProcessArgs("count-sleep"), RunOptions{
		Env: []string{
			"GO_WANT_HELPER_PROCESS=1",
			"HELPER_MODE=count-sleep",
			"HELPER_COUNT_FILE=" + countFile,
		},
		Timeout: 300 * time.Millisecond,
		Stdin:   stdin,
	})
	if !IsTimeout(err) {
		t.Fatalf("expected timeout error, got: %v", err)
	}
	data, readErr := os.ReadFile(countFile)
	if readErr != nil {
		t.Fatalf("read attempt count: %v", readErr)
	}
	return strings.Count(string(data), "attempt")
}

func TestRunnerRetriesTimeout(t *testing.T) {
	t.Parallel()
	if got := countAttemptsWithTimeout(t, nil); got != 3 {
		t.Fatalf("expected 3 attempts (1 + 2 retries), got %d", got)
	}
}

func TestRunnerDoesNotRetryWithStdin(t *testing.T) {
	t.Parallel()
	if got := countAttemptsWithTimeout(t, strings.NewReader("payload")); got != 1 {
		t.Fatalf("expected a single attempt when stdin is set, got %d", got)
	}
}

func TestRunnerDoesNotRetryMissingBinary(t *testing.T) {
	t.Parallel()
	runner := New(5 * time.Second)
	runner.SetRetryPolicy(3, 2*time.Second)
	start := time.Now()
	_, err := runner.RunWithOptions(context.Background(), filepath.Join(t.TempDir(), "no-such-binary"), nil, RunOptions{})
	if err == nil {
		t.Fatalf("expected spawn error")
	}
	if isRetryable(err) {
		t.Fatalf("missing binary must not be retryable: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("missing binary was retried (took %s)", elapsed)
	}
}

func TestIsRetryableSpawnFailure(t *testing.T) {
	transient := &CommandError{Name: "x", ExitCode: -1, Cause: errors.New("resource temporarily unavailable"), spawnFailed: true}
	if !isRetryable(transient) {
		t.Fatalf("expected transient spawn failure to be retryable")
	}
	signaled := &CommandError{Name: "x", ExitCode: -1, Cause: errors.New("signal: killed")}
	if isRetryable(signaled) {
		t.Fatalf("process killed by signal must not be retryable")
	}
	notFound := &CommandError{Name: "x", ExitCode: -1, Cause: &exec.Error{Name: "x", Err: exec.ErrNotFound}, spawnFailed: true}
	if isRetryable(notFound) {
		t.Fatalf("exec.ErrNotFound must not be retryable")
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
	case "count-sleep":
		f, err := os.OpenFile(os.Getenv("HELPER_COUNT_FILE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, "attempt")
			_ = f.Close()
		}
		time.Sleep(10 * time.Second)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode: %s\n", mode)
		os.Exit(2)
	}
}
