package cmdrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCommandTimeout = 120 * time.Second
	minCommandTimeout     = 1 * time.Second
	maxOutputPreviewLen   = 1024
)

var ErrCommandTimeout = errors.New("command timeout")

type RunOptions struct {
	Timeout time.Duration
	Dir     string
	Env     []string
	Stdin   io.Reader
}

type Result struct {
	Name      string
	Args      []string
	Stdout    string
	Stderr    string
	Combined  string
	ExitCode  int
	Duration  time.Duration
	TimedOut  bool
	Cancelled bool
}

type CommandError struct {
	Name      string
	Args      []string
	ExitCode  int
	TimedOut  bool
	Cancelled bool
	Output    string
	Cause     error
}

func (e *CommandError) Error() string {
	if e == nil {
		return ""
	}
	reason := "failed"
	if e.TimedOut {
		reason = "timed out"
	} else if e.Cancelled {
		reason = "cancelled"
	}
	msg := fmt.Sprintf("command %s %s", quoteCommand(e.Name, e.Args), reason)
	if e.ExitCode >= 0 {
		msg += fmt.Sprintf(" (exit=%d)", e.ExitCode)
	}
	if e.Output != "" {
		msg += fmt.Sprintf(": %s", truncateOutput(e.Output))
	}
	return msg
}

func (e *CommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.TimedOut {
		return ErrCommandTimeout
	}
	return e.Cause
}

func IsTimeout(err error) bool {
	if errors.Is(err, ErrCommandTimeout) {
		return true
	}
	var ce *CommandError
	return errors.As(err, &ce) && ce.TimedOut
}

type Runner struct {
	defaultTimeout time.Duration
}

var Default = New(DefaultTimeout())

func New(timeout time.Duration) *Runner {
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	return &Runner{defaultTimeout: timeout}
}

func DefaultTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("AMDL_CMD_TIMEOUT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d >= minCommandTimeout {
			return d
		}
	}
	if raw := strings.TrimSpace(os.Getenv("AMDL_CMD_TIMEOUT_SEC")); raw != "" {
		if sec, err := strconv.Atoi(raw); err == nil {
			d := time.Duration(sec) * time.Second
			if d >= minCommandTimeout {
				return d
			}
		}
	}
	return defaultCommandTimeout
}

func Run(name string, args ...string) (Result, error) {
	return Default.Run(context.Background(), name, args...)
}

func RunWithOptions(ctx context.Context, name string, args []string, opts RunOptions) (Result, error) {
	return Default.RunWithOptions(ctx, name, args, opts)
}

func (r *Runner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	return r.RunWithOptions(ctx, name, args, RunOptions{})
}

func (r *Runner) RunWithOptions(ctx context.Context, name string, args []string, opts RunOptions) (Result, error) {
	if strings.TrimSpace(name) == "" {
		return Result{}, &CommandError{Name: name, Args: append([]string{}, args...), ExitCode: -1, Cause: errors.New("empty command name")}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = r.defaultTimeout
	}
	if timeout < minCommandTimeout {
		timeout = minCommandTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, name, args...)
	applyProcessGroup(cmd)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), opts.Env...)
	}
	cmd.Stdin = opts.Stdin

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	started := time.Now()
	result := Result{
		Name: name,
		Args: append([]string{}, args...),
	}

	if err := cmd.Start(); err != nil {
		result.Duration = time.Since(started)
		return result, &CommandError{
			Name:     name,
			Args:     append([]string{}, args...),
			ExitCode: -1,
			Cause:    err,
		}
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	waitErr := error(nil)
	select {
	case waitErr = <-waitCh:
	case <-runCtx.Done():
		result.TimedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
		result.Cancelled = errors.Is(runCtx.Err(), context.Canceled)
		_ = terminateProcess(cmd)
		waitErr = <-waitCh
	}

	result.Duration = time.Since(started)
	result.Stdout = strings.TrimSpace(stdoutBuf.String())
	result.Stderr = strings.TrimSpace(stderrBuf.String())
	result.Combined = strings.TrimSpace(strings.Join([]string{result.Stdout, result.Stderr}, "\n"))
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	} else {
		result.ExitCode = -1
	}

	if waitErr != nil || result.TimedOut || result.Cancelled {
		cause := waitErr
		if cause == nil {
			cause = runCtx.Err()
		}
		return result, &CommandError{
			Name:      name,
			Args:      append([]string{}, args...),
			ExitCode:  result.ExitCode,
			TimedOut:  result.TimedOut,
			Cancelled: result.Cancelled,
			Output:    result.Combined,
			Cause:     cause,
		}
	}
	return result, nil
}

func truncateOutput(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	if len(output) <= maxOutputPreviewLen {
		return output
	}
	return output[:maxOutputPreviewLen] + "...(truncated)"
}

func quoteCommand(name string, args []string) string {
	parts := []string{name}
	for _, arg := range args {
		if strings.ContainsAny(arg, " \t\n\"'") {
			parts = append(parts, strconv.Quote(arg))
		} else {
			parts = append(parts, arg)
		}
	}
	return strings.Join(parts, " ")
}
