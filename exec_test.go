package slicer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type capturedRequest struct {
	Method      string
	Path        string
	QueryParams url.Values
}

func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}

	wrapper := func(w http.ResponseWriter, r *http.Request) {
		captured.Method = r.Method
		captured.Path = r.URL.Path
		captured.QueryParams = r.URL.Query()
		handler(w, r)
	}

	server := httptest.NewServer(http.HandlerFunc(wrapper))
	t.Cleanup(server.Close)
	return server, captured
}

func writeExecResult(w http.ResponseWriter, result SlicerExecWriteResult) {
	data, _ := json.Marshal(result)
	data = append(data, '\n')
	w.Write(data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func TestRemoteCmd_Output_SimpleExit0(t *testing.T) {
	server, captured := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Stdout:    "hello world\n",
		})
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			ExitCode:  0,
		})
	})

	client := NewSlicerClient(server.URL, "test-token", "test-agent", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := client.Command(ctx, "test-vm", "echo", "hello", "world")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Output() failed: %v", err)
	}

	got := strings.TrimSpace(string(output))
	if got != "hello world" {
		t.Errorf("Output() = %q, want %q", got, "hello world")
	}

	if captured.Method != "POST" {
		t.Errorf("Method = %q, want POST", captured.Method)
	}
	if captured.Path != "/vm/test-vm/exec" {
		t.Errorf("Path = %q, want /vm/test-vm/exec", captured.Path)
	}
	if captured.QueryParams.Get("cmd") != "echo" {
		t.Errorf("cmd = %q, want echo", captured.QueryParams.Get("cmd"))
	}
	args := captured.QueryParams["args"]
	if len(args) != 2 || args[0] != "hello" || args[1] != "world" {
		t.Errorf("args = %v, want [hello world]", args)
	}

	if cmd.ProcessState == nil {
		t.Fatal("ProcessState is nil")
	}
	if !cmd.ProcessState.Success() {
		t.Error("ProcessState.Success() = false, want true")
	}
	if cmd.ProcessState.ExitCode() != 0 {
		t.Errorf("ExitCode() = %d, want 0", cmd.ProcessState.ExitCode())
	}
}

func TestRemoteCmd_Output_MultipleOutputThenNonZeroExit(t *testing.T) {
	server, captured := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Stdout:    "line 1\n",
		})
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Stdout:    "line 2\n",
		})
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Stderr:    "error occurred\n",
		})
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Error:     "exit status 42",
			ExitCode:  42,
		})
	})

	client := NewSlicerClient(server.URL, "test-token", "test-agent", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := client.Command(ctx, "test-vm", "failing-command")
	output, err := cmd.Output()

	if err == nil {
		t.Fatal("Output() should have returned an error")
	}

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error should be *ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 42 {
		t.Errorf("ExitCode() = %d, want 42", exitErr.ExitCode())
	}

	gotOutput := string(output)
	if !strings.Contains(gotOutput, "line 1") || !strings.Contains(gotOutput, "line 2") {
		t.Errorf("Output should contain stdout lines, got: %q", gotOutput)
	}

	if captured.QueryParams.Get("cmd") != "failing-command" {
		t.Errorf("cmd = %q, want failing-command", captured.QueryParams.Get("cmd"))
	}
}

func TestRemoteCmd_Output_ImmediateFailure(t *testing.T) {
	server, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Error:     "exec: \"nonexistent\": executable file not found in $PATH",
			ExitCode:  127,
		})
	})

	client := NewSlicerClient(server.URL, "test-token", "test-agent", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := client.Command(ctx, "test-vm", "nonexistent")
	_, err := cmd.Output()

	if err == nil {
		t.Fatal("Output() should have returned an error")
	}

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error should be *ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 127 {
		t.Errorf("ExitCode() = %d, want 127", exitErr.ExitCode())
	}
}

func TestRemoteCmd_EnvPassedCorrectly(t *testing.T) {
	server, captured := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Stdout:    "value1\n",
		})
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			ExitCode:  0,
		})
	})

	client := NewSlicerClient(server.URL, "test-token", "test-agent", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := client.Command(ctx, "test-vm", "printenv", "MY_VAR")
	cmd.Env = []string{"MY_VAR=value1", "OTHER_VAR=value2"}

	_, err := cmd.Output()
	if err != nil {
		t.Fatalf("Output() failed: %v", err)
	}

	envs := captured.QueryParams["env"]
	if len(envs) != 2 {
		t.Fatalf("expected 2 env vars, got %d: %v", len(envs), envs)
	}
	if envs[0] != "MY_VAR=value1" || envs[1] != "OTHER_VAR=value2" {
		t.Errorf("env = %v, want [MY_VAR=value1 OTHER_VAR=value2]", envs)
	}
}

func TestRemoteCmd_DirAndUIDPassedCorrectly(t *testing.T) {
	server, captured := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Stdout:    "/tmp\n",
		})
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			ExitCode:  0,
		})
	})

	client := NewSlicerClient(server.URL, "test-token", "test-agent", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := client.Command(ctx, "test-vm", "pwd")
	cmd.Dir = "/tmp"
	cmd.UID = 1000
	cmd.GID = 1000

	_, err := cmd.Output()
	if err != nil {
		t.Fatalf("Output() failed: %v", err)
	}

	if captured.QueryParams.Get("cwd") != "/tmp" {
		t.Errorf("cwd = %q, want /tmp", captured.QueryParams.Get("cwd"))
	}
	if captured.QueryParams.Get("uid") != "1000" {
		t.Errorf("uid = %q, want 1000", captured.QueryParams.Get("uid"))
	}
	if captured.QueryParams.Get("gid") != "1000" {
		t.Errorf("gid = %q, want 1000", captured.QueryParams.Get("gid"))
	}
}

func TestRemoteCmd_Run_WithStdoutStderr(t *testing.T) {
	server, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Stdout:    "stdout line\n",
		})
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Stderr:    "stderr line\n",
		})
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			ExitCode:  0,
		})
	})

	client := NewSlicerClient(server.URL, "test-token", "test-agent", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := client.Command(ctx, "test-vm", "test-cmd")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		t.Fatalf("Run() failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "stdout line") {
		t.Errorf("stdout = %q, want to contain 'stdout line'", stdout.String())
	}
	if !strings.Contains(stderr.String(), "stderr line") {
		t.Errorf("stderr = %q, want to contain 'stderr line'", stderr.String())
	}
}

func TestRemoteCmd_CombinedOutput(t *testing.T) {
	server, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Stdout:    "out\n",
		})
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Stderr:    "err\n",
		})
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			ExitCode:  0,
		})
	})

	client := NewSlicerClient(server.URL, "test-token", "test-agent", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := client.Command(ctx, "test-vm", "test-cmd")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CombinedOutput() failed: %v", err)
	}

	got := string(output)
	if !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Errorf("CombinedOutput() = %q, want to contain both 'out' and 'err'", got)
	}
}

func TestRemoteCmd_StartWait(t *testing.T) {
	server, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			Stdout:    "async output\n",
		})
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			ExitCode:  0,
		})
	})

	client := NewSlicerClient(server.URL, "test-token", "test-agent", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var stdout bytes.Buffer
	cmd := client.Command(ctx, "test-vm", "test-cmd")
	cmd.Stdout = &stdout

	err := cmd.Start()
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	err = cmd.Wait()
	if err != nil {
		t.Fatalf("Wait() failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "async output") {
		t.Errorf("stdout = %q, want to contain 'async output'", stdout.String())
	}
}

func TestRemoteCmd_DoubleStart(t *testing.T) {
	server, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			ExitCode:  0,
		})
	})

	client := NewSlicerClient(server.URL, "test-token", "test-agent", nil)
	ctx := context.Background()

	cmd := client.Command(ctx, "test-vm", "echo", "test")

	err := cmd.Start()
	if err != nil {
		t.Fatalf("First Start() failed: %v", err)
	}

	err = cmd.Start()
	if err == nil {
		t.Error("Second Start() should have returned an error")
	}

	cmd.Wait()
}

func TestRemoteCmd_WaitWithoutStart(t *testing.T) {
	client := NewSlicerClient("http://unused", "test-token", "test-agent", nil)
	ctx := context.Background()

	cmd := client.Command(ctx, "test-vm", "echo", "test")

	err := cmd.Wait()
	if err == nil {
		t.Error("Wait() without Start() should return an error")
	}
}

func TestRemoteCmd_String(t *testing.T) {
	client := NewSlicerClient("http://unused", "test-token", "test-agent", nil)
	ctx := context.Background()

	cmd := client.Command(ctx, "test-vm", "ls", "-la", "/tmp")
	got := cmd.String()
	want := "[ls -la /tmp]"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestRemoteProcessState_String(t *testing.T) {
	tests := []struct {
		name     string
		state    RemoteProcessState
		expected string
	}{
		{
			name:     "not exited",
			state:    RemoteProcessState{exited: false},
			expected: "process not exited",
		},
		{
			name:     "exit status 0",
			state:    RemoteProcessState{exited: true, exitCode: 0},
			expected: "exit status 0",
		},
		{
			name:     "exit status 1",
			state:    RemoteProcessState{exited: true, exitCode: 1},
			expected: "exit status 1",
		},
		{
			name:     "exit status 42",
			state:    RemoteProcessState{exited: true, exitCode: 42},
			expected: "exit status 42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.state.String()
			if got != tt.expected {
				t.Errorf("String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestRemoteProcessState_ExitCode(t *testing.T) {
	tests := []struct {
		name     string
		state    RemoteProcessState
		expected int
	}{
		{
			name:     "not exited returns -1",
			state:    RemoteProcessState{exited: false, exitCode: 5},
			expected: -1,
		},
		{
			name:     "exited with 0",
			state:    RemoteProcessState{exited: true, exitCode: 0},
			expected: 0,
		},
		{
			name:     "exited with 1",
			state:    RemoteProcessState{exited: true, exitCode: 1},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.state.ExitCode()
			if got != tt.expected {
				t.Errorf("ExitCode() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestExitError_Error(t *testing.T) {
	err := &ExitError{
		RemoteProcessState: &RemoteProcessState{
			exited:   true,
			exitCode: 1,
		},
	}

	got := err.Error()
	want := "exit status 1"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestRemoteCmd_ShellPassedCorrectly(t *testing.T) {
	server, captured := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeExecResult(w, SlicerExecWriteResult{
			Timestamp: time.Now(),
			ExitCode:  0,
		})
	})

	client := NewSlicerClient(server.URL, "test-token", "test-agent", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := client.Command(ctx, "test-vm", "echo", "test")
	cmd.Shell = "/bin/bash"

	_, err := cmd.Output()
	if err != nil {
		t.Fatalf("Output() failed: %v", err)
	}

	if captured.QueryParams.Get("shell") != "/bin/bash" {
		t.Errorf("shell = %q, want /bin/bash", captured.QueryParams.Get("shell"))
	}
}
