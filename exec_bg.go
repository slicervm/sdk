package slicer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"
)

// ExecBackgroundRequest carries parameters to launch a long-running process
// inside a VM via POST /vm/{hostname}/exec?background=true.
type ExecBackgroundRequest struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`
	UID     uint32   `json:"uid,omitempty"`
	GID     uint32   `json:"gid,omitempty"`
	Shell   string   `json:"shell,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`

	// RingBytes sets the per-process ring buffer cap. Zero means server
	// default (1 MiB). Negative values are rejected.
	RingBytes int64 `json:"ring_bytes,omitempty"`
}

// ExecBackgroundResponse is returned by ExecBackground on success.
type ExecBackgroundResponse struct {
	ExecID    string    `json:"exec_id"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	RingBytes int64     `json:"ring_bytes"`
}

// ExecBackgroundInfo mirrors the server's info shape for a background exec.
type ExecBackgroundInfo struct {
	ExecID       string     `json:"exec_id"`
	PID          int        `json:"pid"`
	Command      string     `json:"command"`
	Args         []string   `json:"args,omitempty"`
	Cwd          string     `json:"cwd,omitempty"`
	UID          uint32     `json:"uid,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	Running      bool       `json:"running"`
	ExitCode     *int       `json:"exit_code,omitempty"`
	Signal       string     `json:"signal,omitempty"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	BytesWritten int64      `json:"bytes_written"`
	BytesDropped int64      `json:"bytes_dropped"`
	NextID       uint64     `json:"next_id"`
	RingBytes    int64      `json:"ring_bytes"`
}

// LogOptions tunes GET /vm/{hostname}/exec/{exec_id}/logs.
type LogOptions struct {
	// Follow=true keeps the connection open and streams live frames after
	// replaying the ring contents. Follow=false replays and closes.
	Follow bool
	// FromID starts the replay cursor at frame N if present in the ring.
	// If FromID is beyond the live cursor, the server waits for that frame
	// when Follow=true. If the frame has been evicted a gap frame is emitted.
	FromID uint64
}

// KillOptions tunes POST /vm/{hostname}/exec/{exec_id}/kill.
type KillOptions struct {
	Signal  string `json:"signal,omitempty"`
	GraceMs int    `json:"grace_ms,omitempty"`
}

// ExecBackgroundKillResponse is returned by ExecKill.
type ExecBackgroundKillResponse struct {
	ExecID     string `json:"exec_id"`
	PID        int    `json:"pid"`
	Running    bool   `json:"running"`
	SignalSent string `json:"signal_sent"`
}

// ExecBackgroundWaitExitResponse is returned by ExecWaitExit.
type ExecBackgroundWaitExitResponse struct {
	ExecID   string     `json:"exec_id"`
	Running  bool       `json:"running"`
	ExitCode *int       `json:"exit_code,omitempty"`
	Signal   string     `json:"signal,omitempty"`
	EndedAt  *time.Time `json:"ended_at,omitempty"`
	TimedOut bool       `json:"timed_out"`
}

// ExecBackgroundDeleteResponse is returned by ExecDelete.
type ExecBackgroundDeleteResponse struct {
	ExecID string `json:"exec_id"`
	Reaped bool   `json:"reaped"`
}

// ExecBackground launches a long-running process inside the named VM and
// returns the server-chosen exec_id plus process metadata. The child survives
// client disconnect — use ExecLogs, ExecInfo, ExecWaitExit, ExecKill and
// ExecDelete on the returned ExecID to interact with it.
func (c *SlicerClient) ExecBackground(ctx context.Context, vmName string, req ExecBackgroundRequest) (*ExecBackgroundResponse, error) {
	if req.Command == "" {
		return nil, fmt.Errorf("slicer: ExecBackground: command is required")
	}

	u, err := c.vmURL(vmName, "exec", "")
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("background", "true")
	q.Set("cmd", req.Command)
	for _, a := range req.Args {
		q.Add("args", a)
	}
	for _, e := range req.Env {
		q.Add("env", e)
	}
	// NonRootUser is the "auto-detect" sentinel — leave it off the wire so
	// the agent's resolveUIDGID picks a default non-root user (ubuntu). Any
	// other value, including 0, is sent as an explicit choice. Conflating
	// UID==0 with "unset" would mean there's no way to request root.
	if req.UID != NonRootUser {
		q.Set("uid", strconv.FormatUint(uint64(req.UID), 10))
	}
	if req.GID != NonRootUser {
		q.Set("gid", strconv.FormatUint(uint64(req.GID), 10))
	}
	if req.Shell != "" {
		q.Set("shell", req.Shell)
	}
	if req.Cwd != "" {
		q.Set("cwd", req.Cwd)
	}
	if req.RingBytes > 0 {
		q.Set("ring_bytes", strconv.FormatInt(req.RingBytes, 10))
	}
	q.Set("stdio", ExecStdioBase64)
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecBackground: %w", err)
	}
	c.setCommonHeaders(httpReq)

	res, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecBackground: %w", err)
	}
	defer drainClose(res.Body)

	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		return nil, readAPIError(res, "ExecBackground")
	}

	var out ExecBackgroundResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("slicer: ExecBackground: decode: %w", err)
	}
	return &out, nil
}

// ExecList returns all background execs tracked by the VM's agent.
func (c *SlicerClient) ExecList(ctx context.Context, vmName string) ([]ExecBackgroundInfo, error) {
	u, err := c.vmURL(vmName, "exec", "")
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecList: %w", err)
	}
	c.setCommonHeaders(httpReq)
	res, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecList: %w", err)
	}
	defer drainClose(res.Body)
	if res.StatusCode != http.StatusOK {
		return nil, readAPIError(res, "ExecList")
	}
	var out []ExecBackgroundInfo
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("slicer: ExecList: decode: %w", err)
	}
	return out, nil
}

// ExecInfo fetches the latest status for a single background exec.
func (c *SlicerClient) ExecInfo(ctx context.Context, vmName, execID string) (*ExecBackgroundInfo, error) {
	u, err := c.vmURL(vmName, "exec", execID)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecInfo: %w", err)
	}
	c.setCommonHeaders(httpReq)
	res, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecInfo: %w", err)
	}
	defer drainClose(res.Body)
	if res.StatusCode != http.StatusOK {
		return nil, readAPIError(res, "ExecInfo")
	}
	var out ExecBackgroundInfo
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("slicer: ExecInfo: decode: %w", err)
	}
	return &out, nil
}

// ExecLogs opens the NDJSON log stream for a background exec.
//
// The returned channel is closed when the stream ends (follow=false: after all
// ring contents are drained; follow=true: when the child exits or the
// context is cancelled).
func (c *SlicerClient) ExecLogs(ctx context.Context, vmName, execID string, opts LogOptions) (<-chan SlicerExecWriteResult, error) {
	u, err := c.vmURL(vmName, "exec", path.Join(execID, "logs"))
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if opts.Follow {
		q.Set("follow", "true")
	}
	if opts.FromID > 0 {
		q.Set("from_id", strconv.FormatUint(opts.FromID, 10))
	}
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecLogs: %w", err)
	}
	c.setCommonHeaders(httpReq)

	res, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecLogs: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		defer drainClose(res.Body)
		return nil, readAPIError(res, "ExecLogs")
	}

	out := make(chan SlicerExecWriteResult, 32)
	go func() {
		defer res.Body.Close()
		defer close(out)

		r := bufio.NewReaderSize(res.Body, 64*1024)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				var frame SlicerExecWriteResult
				if jerr := json.Unmarshal(line, &frame); jerr == nil {
					_ = decodeExecWriteResult(&frame)
					select {
					case out <- frame:
					case <-ctx.Done():
						return
					}
				}
			}
			if err != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()
	return out, nil
}

// ExecKill signals a running background exec. Default: SIGTERM with a 5 s
// grace period before the server escalates to SIGKILL. Calling ExecKill on
// an already-exited exec is a no-op (running=false is returned).
func (c *SlicerClient) ExecKill(ctx context.Context, vmName, execID string, opts KillOptions) (*ExecBackgroundKillResponse, error) {
	u, err := c.vmURL(vmName, "exec", path.Join(execID, "kill"))
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecKill: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), newJSONReader(body))
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecKill: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.setCommonHeaders(httpReq)
	res, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecKill: %w", err)
	}
	defer drainClose(res.Body)
	if res.StatusCode != http.StatusOK {
		return nil, readAPIError(res, "ExecKill")
	}
	var out ExecBackgroundKillResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("slicer: ExecKill: decode: %w", err)
	}
	return &out, nil
}

// ExecWaitExit long-polls until the child exits or the timeout elapses. If
// timeout is zero the server default (30s) is used.
func (c *SlicerClient) ExecWaitExit(ctx context.Context, vmName, execID string, timeout time.Duration) (*ExecBackgroundWaitExitResponse, error) {
	u, err := c.vmURL(vmName, "exec", path.Join(execID, "wait-exit"))
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if timeout > 0 {
		q.Set("timeout", strconv.FormatInt(int64(timeout/time.Second), 10))
	}
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecWaitExit: %w", err)
	}
	c.setCommonHeaders(httpReq)
	res, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecWaitExit: %w", err)
	}
	defer drainClose(res.Body)
	if res.StatusCode != http.StatusOK {
		return nil, readAPIError(res, "ExecWaitExit")
	}
	var out ExecBackgroundWaitExitResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("slicer: ExecWaitExit: decode: %w", err)
	}
	return &out, nil
}

// ExecDelete reaps an exec's ring buffer and registry entry. Does not kill a
// running process — pair with ExecKill for "stop and clean up".
func (c *SlicerClient) ExecDelete(ctx context.Context, vmName, execID string) (*ExecBackgroundDeleteResponse, error) {
	u, err := c.vmURL(vmName, "exec", execID)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecDelete: %w", err)
	}
	c.setCommonHeaders(httpReq)
	res, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("slicer: ExecDelete: %w", err)
	}
	defer drainClose(res.Body)
	if res.StatusCode != http.StatusOK {
		return nil, readAPIError(res, "ExecDelete")
	}
	var out ExecBackgroundDeleteResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("slicer: ExecDelete: decode: %w", err)
	}
	return &out, nil
}

// ----- helpers ------------------------------------------------------------

func (c *SlicerClient) vmURL(vmName, root, sub string) (*url.URL, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("slicer: invalid base URL: %w", err)
	}
	p := path.Join("/vm", vmName, root)
	if sub != "" {
		p = path.Join(p, sub)
	}
	u.Path = p
	return u, nil
}

func (c *SlicerClient) setCommonHeaders(req *http.Request) {
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func drainClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}

func readAPIError(res *http.Response, op string) error {
	body, _ := io.ReadAll(res.Body)
	return fmt.Errorf("slicer: %s: %s - %s", op, res.Status, string(body))
}

func newJSONReader(b []byte) io.Reader {
	if len(b) == 0 || string(b) == "{}" {
		return nil
	}
	return &jsonReader{data: b}
}

type jsonReader struct {
	data []byte
	off  int
}

func (r *jsonReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
