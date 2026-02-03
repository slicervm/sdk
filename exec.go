package slicer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// RemoteProcessState stores information about an exited remote process.
// It mirrors os.ProcessState but for remote executions.
type RemoteProcessState struct {
	exitCode int
	exited   bool
	pid      int // Remote PID if available, -1 otherwise
}

// ExitCode returns the exit code of the exited process.
// Returns -1 if the process hasn't exited or was terminated abnormally.
func (p *RemoteProcessState) ExitCode() int {
	if !p.exited {
		return -1
	}
	return p.exitCode
}

// Exited reports whether the program has exited.
func (p *RemoteProcessState) Exited() bool {
	return p.exited
}

// Success reports whether the program exited successfully (exit status 0).
func (p *RemoteProcessState) Success() bool {
	return p.exited && p.exitCode == 0
}

// Pid returns the process id of the exited process.
// Returns -1 if the PID is not available (remote execution).
func (p *RemoteProcessState) Pid() int {
	return p.pid
}

// String returns a human-readable description of the process state.
func (p *RemoteProcessState) String() string {
	if !p.exited {
		return "process not exited"
	}
	if p.exitCode == 0 {
		return "exit status 0"
	}
	return fmt.Sprintf("exit status %d", p.exitCode)
}

// ExitError is returned by RemoteCmd methods when a command exits with a non-zero status.
// It mirrors exec.ExitError but for remote executions.
type ExitError struct {
	*RemoteProcessState

	// Stderr holds stderr output if it was collected.
	Stderr []byte
}

// Error returns a string representation of the exit error.
func (e *ExitError) Error() string {
	return e.RemoteProcessState.String()
}

// Unwrap returns the underlying process state for errors.Is/As compatibility.
func (e *ExitError) Unwrap() error {
	return nil
}

// RemoteCmd represents a remote command to be executed on a VM.
// It mirrors the os/exec.Cmd API but executes commands on remote VMs.
//
// A RemoteCmd cannot be reused after calling Run, Output, or CombinedOutput.
type RemoteCmd struct {
	// Path is the command to run. This is the only field that must be set.
	// If Path contains no path separators, it will be resolved using the
	// remote system's PATH.
	Path string

	// Args holds command line arguments, including the command as Args[0].
	// If Args is empty or nil, Run uses {Path}.
	Args []string

	// Env specifies the environment of the process.
	// Each entry is of the form "key=value".
	// If Env is nil, the remote process inherits the default environment.
	// Note: Environment variable support depends on the remote agent's capabilities.
	Env []string

	// Dir specifies the working directory of the command.
	// If Dir is empty, the command runs in the remote agent's default directory.
	Dir string

	// Stdin specifies the process's standard input.
	// If Stdin is nil, the process reads from an empty reader.
	// If Stdin is an *os.File, it is connected directly.
	// Otherwise, a goroutine reads from Stdin and delivers to the remote.
	Stdin io.Reader

	// Stdout specifies the process's standard output.
	// If Stdout is nil, output is discarded.
	// If Stdout is an *os.File, output is written directly.
	// Otherwise, a goroutine copies output to Stdout.
	Stdout io.Writer

	// Stderr specifies the process's standard error.
	// If Stderr is nil, error output is discarded.
	// If Stderr is the same as Stdout, both are combined.
	// Otherwise, a goroutine copies error output to Stderr.
	Stderr io.Writer

	// ProcessState contains information about an exited process.
	// It is available after a call to Wait or Run.
	ProcessState *RemoteProcessState

	// UID specifies the user ID to run the command as (Linux only).
	// If zero, uses the remote system's default (usually root).
	UID uint32

	// GID specifies the group ID to run the command as (Linux only).
	// If zero, uses the remote system's default (usually root).
	GID uint32

	// Shell specifies the shell interpreter to use.
	// If empty, defaults to "/bin/bash" on the remote.
	// Set to an empty string explicitly to disable shell interpretation.
	Shell string

	// ctx is the context for the command execution
	ctx context.Context

	// client is the Slicer client used to execute the command
	client *SlicerClient

	// vmName is the target VM hostname
	vmName string

	// started tracks whether Start() has been called
	started bool

	// finished tracks whether the command has completed
	finished bool

	// mu protects concurrent access to state fields
	mu sync.Mutex

	// waitCh is closed when the command completes
	waitCh chan struct{}

	// waitErr stores the error from Wait
	waitErr error

	// stdinPipe is the pipe writer for stdin if created
	stdinPipe io.WriteCloser

	// stdoutPipe is the pipe reader for stdout if created
	stdoutPipe io.ReadCloser

	// stderrPipe is the pipe reader for stderr if created
	stderrPipe io.ReadCloser

	// collectedStdout stores stdout when using Output()
	collectedStdout bytes.Buffer

	// collectedStderr stores stderr when using Output()
	collectedStderr bytes.Buffer

	// resultChan receives results from the streaming exec
	resultChan chan SlicerExecWriteResult
}

// Command returns a RemoteCmd to execute the named program on the specified VM.
// The returned Cmd's Args field is set to the command name followed by any arguments.
//
// The provided context is used for the lifetime of the command execution.
// If the context is canceled, the command will be terminated.
func (c *SlicerClient) Command(ctx context.Context, vmName string, name string, arg ...string) *RemoteCmd {
	cmd := &RemoteCmd{
		Path:   name,
		Args:   append([]string{name}, arg...),
		ctx:    ctx,
		client: c,
		vmName: vmName,
		waitCh: make(chan struct{}),
	}
	return cmd
}

// CommandContext is an alias for Command that makes the API more familiar
// to users of os/exec.CommandContext.
func (c *SlicerClient) CommandContext(ctx context.Context, vmName string, name string, arg ...string) *RemoteCmd {
	return c.Command(ctx, vmName, name, arg...)
}

// String returns a human-readable description of the command.
// It is intended only for debugging, not for execution.
func (c *RemoteCmd) String() string {
	if len(c.Args) == 0 {
		return c.Path
	}
	return fmt.Sprintf("%v", c.Args)
}

// Run starts the specified command and waits for it to complete.
//
// If the command runs successfully and copies all output, the error is nil.
// If the command fails to start or doesn't complete successfully, the error
// is of type *ExitError. Other error types may be returned for other situations.
func (c *RemoteCmd) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

// Start starts the specified command but does not wait for it to complete.
//
// If Start returns successfully, the c.ProcessState field will be set after
// a successful call to Wait.
//
// After a successful call to Start, the Wait method must be called to release
// associated system resources.
func (c *RemoteCmd) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.started {
		return errors.New("slicer: command already started")
	}
	c.started = true

	// Build the exec request
	args := c.Args
	if len(args) > 0 {
		args = args[1:] // Remove the command name from args
	}

	execReq := SlicerExecRequest{
		Command: c.Path,
		Args:    args,
		Env:     c.Env,
		UID:     c.UID,
		GID:     c.GID,
		Shell:   c.Shell,
		Cwd:     c.Dir,
		Stdin:   c.Stdin != nil,
	}

	// Start the async execution
	resChan, err := c.execAsync(c.ctx, execReq)
	if err != nil {
		return err
	}
	c.resultChan = resChan

	// Start a goroutine to process results
	go c.processResults()

	return nil
}

// Wait waits for the command to exit and waits for any copying to complete.
//
// The command must have been started by Start.
//
// If the command runs successfully, the error is nil.
// If the command fails, the error is of type *ExitError.
//
// Wait releases any resources associated with the Cmd.
func (c *RemoteCmd) Wait() error {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return errors.New("slicer: command not started")
	}
	if c.finished {
		c.mu.Unlock()
		return errors.New("slicer: Wait was already called")
	}
	c.mu.Unlock()

	// Wait for completion
	<-c.waitCh

	c.mu.Lock()
	c.finished = true
	c.mu.Unlock()

	return c.waitErr
}

// Output runs the command and returns its standard output.
//
// If the command fails, the error is of type *ExitError.
// If stderr was not otherwise collected, the ExitError.Stderr field
// will contain any captured stderr output.
func (c *RemoteCmd) Output() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errors.New("slicer: Stdout already set")
	}
	c.Stdout = &c.collectedStdout

	// Capture stderr for ExitError if not already set
	captureStderr := c.Stderr == nil
	if captureStderr {
		c.Stderr = &c.collectedStderr
	}

	err := c.Run()
	if err != nil {
		var exitErr *ExitError
		if errors.As(err, &exitErr) && captureStderr {
			exitErr.Stderr = c.collectedStderr.Bytes()
		}
	}

	return c.collectedStdout.Bytes(), err
}

// CombinedOutput runs the command and returns its combined stdout and stderr.
func (c *RemoteCmd) CombinedOutput() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errors.New("slicer: Stdout already set")
	}
	if c.Stderr != nil {
		return nil, errors.New("slicer: Stderr already set")
	}

	var combined bytes.Buffer
	c.Stdout = &combined
	c.Stderr = &combined

	err := c.Run()
	return combined.Bytes(), err
}

// StdinPipe returns a pipe that will be connected to the command's standard input
// when the command starts.
//
// The pipe will be closed automatically after Wait sees the command exit.
// The caller only needs to call Close to force the pipe to close sooner.
// For example, if the command being run won't exit until standard input is closed,
// the caller must close the pipe.
func (c *RemoteCmd) StdinPipe() (io.WriteCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.started {
		return nil, errors.New("slicer: StdinPipe after process started")
	}
	if c.Stdin != nil {
		return nil, errors.New("slicer: Stdin already set")
	}

	pr, pw := io.Pipe()
	c.Stdin = pr
	c.stdinPipe = pw
	return pw, nil
}

// StdoutPipe returns a pipe that will be connected to the command's standard output
// when the command starts.
//
// Wait will close the pipe after seeing the command exit, so most callers
// need not close the pipe themselves. It is thus incorrect to call Wait
// before all reads from the pipe have completed.
//
// For the same reason, it is incorrect to call Run when using StdoutPipe.
func (c *RemoteCmd) StdoutPipe() (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.started {
		return nil, errors.New("slicer: StdoutPipe after process started")
	}
	if c.Stdout != nil {
		return nil, errors.New("slicer: Stdout already set")
	}

	pr, pw := io.Pipe()
	c.Stdout = pw
	c.stdoutPipe = pr
	return pr, nil
}

// StderrPipe returns a pipe that will be connected to the command's standard error
// when the command starts.
//
// Wait will close the pipe after seeing the command exit, so most callers
// need not close the pipe themselves. It is thus incorrect to call Wait
// before all reads from the pipe have completed.
//
// For the same reason, it is incorrect to call Run when using StderrPipe.
func (c *RemoteCmd) StderrPipe() (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.started {
		return nil, errors.New("slicer: StderrPipe after process started")
	}
	if c.Stderr != nil {
		return nil, errors.New("slicer: Stderr already set")
	}

	pr, pw := io.Pipe()
	c.Stderr = pw
	c.stderrPipe = pr
	return pr, nil
}

// Environ returns a copy of the environment in which the command would be run.
func (c *RemoteCmd) Environ() []string {
	if c.Env == nil {
		return nil
	}
	return append([]string(nil), c.Env...)
}

// execAsync executes the command and returns a channel for streaming results.
// This is an internal method that wraps the lower-level Exec functionality
// but allows for custom stdin handling.
func (c *RemoteCmd) execAsync(ctx context.Context, execReq SlicerExecRequest) (chan SlicerExecWriteResult, error) {
	// Use the existing Exec method but we need to handle stdin differently
	// For now, we create a modified exec request

	// If stdin pipe is set, we need to handle it
	if c.Stdin != nil {
		execReq.Stdin = true
	}

	return c.client.ExecWithReader(ctx, c.vmName, execReq, c.Stdin)
}

// processResults processes the streaming results from the exec channel
func (c *RemoteCmd) processResults() {
	defer close(c.waitCh)

	// Close pipes when done
	defer func() {
		if pw, ok := c.Stdout.(io.WriteCloser); ok && c.stdoutPipe != nil {
			pw.Close()
		}
		if pw, ok := c.Stderr.(io.WriteCloser); ok && c.stderrPipe != nil {
			pw.Close()
		}
	}()

	var exitCode int
	var hasError bool
	var errorMsg string

	for result := range c.resultChan {
		// Write stdout if present
		if result.Stdout != "" && c.Stdout != nil {
			c.Stdout.Write([]byte(result.Stdout))
		}

		// Write stderr if present
		if result.Stderr != "" && c.Stderr != nil {
			c.Stderr.Write([]byte(result.Stderr))
		}

		// Check for exit code
		if result.ExitCode != 0 {
			exitCode = result.ExitCode
			hasError = true
		}

		// Check for error message
		if result.Error != "" {
			errorMsg = result.Error
			hasError = true
			// Try to extract exit code from error message if not already set
			if exitCode == 0 {
				exitCode = 1 // Default to 1 for errors
			}
		}
	}

	// Create process state
	c.ProcessState = &RemoteProcessState{
		exitCode: exitCode,
		exited:   true,
		pid:      -1,
	}

	// Set wait error if there was a failure
	if hasError {
		c.waitErr = &ExitError{
			RemoteProcessState: c.ProcessState,
			Stderr:             c.collectedStderr.Bytes(),
		}
		// If there's an additional error message beyond just exit code, wrap it
		if errorMsg != "" && exitCode == 0 {
			c.waitErr = fmt.Errorf("command failed: %s", errorMsg)
		}
	}
}

// ExecWithReader is like Exec but accepts a custom io.Reader for stdin
// instead of using os.Stdin.
func (c *SlicerClient) ExecWithReader(ctx context.Context, nodeName string, execReq SlicerExecRequest, stdin io.Reader) (chan SlicerExecWriteResult, error) {
	resChan := make(chan SlicerExecWriteResult)

	command := execReq.Command
	args := execReq.Args
	uid := execReq.UID
	gid := execReq.GID
	shell := execReq.Shell
	hasStdin := execReq.Stdin

	cwd := execReq.Cwd

	q := url.Values{}
	q.Set("cmd", command)

	for _, arg := range args {
		q.Add("args", arg)
	}

	for _, env := range execReq.Env {
		q.Add("env", env)
	}

	q.Set("uid", strconv.FormatUint(uint64(uid), 10))
	q.Set("gid", strconv.FormatUint(uint64(gid), 10))

	if len(cwd) > 0 {
		q.Set("cwd", cwd)
	}

	if len(execReq.Permissions) > 0 {
		q.Set("permissions", execReq.Permissions)
	}

	var bodyReader io.Reader

	if hasStdin && stdin != nil {
		q.Set("stdin", "true")
		bodyReader = stdin
	}
	if len(shell) > 0 {
		q.Set("shell", shell)
	}

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return resChan, fmt.Errorf("failed to parse API URL: %w", err)
	}
	u.Path = fmt.Sprintf("/vm/%s/exec", nodeName)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bodyReader)
	if err != nil {
		return resChan, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)

	req.URL.RawQuery = q.Encode()

	res, err := c.httpClient.Do(req)
	if err != nil {
		return resChan, fmt.Errorf("failed to execute request: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		var body []byte
		if res.Body != nil {
			defer res.Body.Close()
			body, _ = io.ReadAll(res.Body)
		}
		return resChan, fmt.Errorf("failed to execute command: %s %s", res.Status, string(body))
	}

	if res.Body == nil {
		return resChan, fmt.Errorf("no body received from VM")
	}

	go func() {
		r := bufio.NewReader(res.Body)

		defer res.Body.Close()
		defer close(resChan)

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line, err := r.ReadBytes('\n')
			if err == io.EOF {
				// Handle any remaining data before EOF
				if len(line) > 0 {
					var result SlicerExecWriteResult
					if jsonErr := json.Unmarshal(line, &result); jsonErr == nil {
						resChan <- result
					}
				}
				break
			}

			if err != nil {
				resChan <- SlicerExecWriteResult{
					Timestamp: time.Now(),
					Error:     fmt.Sprintf("failed to read response: %v", err),
				}
				return
			}

			var result SlicerExecWriteResult
			if err := json.Unmarshal(line, &result); err != nil {
				resChan <- SlicerExecWriteResult{
					Timestamp: result.Timestamp,
					Error:     fmt.Sprintf("failed to decode response: %v", err),
				}
				return
			}

			// Send all results through the channel - let the caller handle exit codes
			resChan <- result

			// If there's an error or non-zero exit code, this is the last message
			if result.Error != "" || result.ExitCode != 0 {
				return
			}
		}
	}()

	return resChan, nil
}
