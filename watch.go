package slicer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// SlicerFSWatchRequest configures a filesystem watch stream.
//
// Paths, Patterns and Events are repeated query parameters. Empty Events
// delivers all types (create, write, remove, rename, chmod).
//
// LastEventID is sent as the SSE `Last-Event-ID` HTTP header. The server
// validates it as a uint64; cross-connection replay is not yet implemented
// — the field is accepted for forward compatibility.
type SlicerFSWatchRequest struct {
	Paths       []string `json:"paths,omitempty"`
	Patterns    []string `json:"patterns,omitempty"`
	Events      []string `json:"events,omitempty"`
	UID         uint32   `json:"uid,omitempty"`
	Recursive   bool     `json:"recursive,omitempty"`
	OneShot     bool     `json:"one_shot,omitempty"`
	Debounce    string   `json:"debounce,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	MaxEvents   int      `json:"max_events,omitempty"`
	LastEventID string   `json:"last_event_id,omitempty"`
}

// SlicerFSWatchEvent is one filesystem event delivered over the watch stream.
// ID is the monotonic per-stream id from the SSE `id:` line. Timestamp is
// left as a string to isolate the SDK from any future wire-format drift;
// use ParseFSWatchTimestamp to decode it.
type SlicerFSWatchEvent struct {
	ID        uint64 `json:"id"`
	Type      string `json:"type"`
	Path      string `json:"path"`
	Timestamp string `json:"timestamp"`
	Size      int64  `json:"size"`
	IsDir     bool   `json:"isDir"`
	Message   string `json:"message,omitempty"`
}

// watchEventPayload mirrors the JSON payload emitted by the agent.
type watchEventPayload struct {
	ID        uint64 `json:"id"`
	Type      string `json:"type"`
	Path      string `json:"path"`
	Timestamp string `json:"timestamp"`
	Size      int64  `json:"size"`
	IsDir     bool   `json:"isDir"`
	Message   string `json:"message,omitempty"`
}

func (r SlicerFSWatchRequest) toQuery() (url.Values, error) {
	if len(r.Paths) == 0 {
		return nil, fmt.Errorf("paths is required")
	}

	qs := url.Values{}
	for _, p := range r.Paths {
		if p = strings.TrimSpace(p); p != "" {
			qs.Add("paths", p)
		}
	}
	if len(qs["paths"]) == 0 {
		return nil, fmt.Errorf("paths is required")
	}
	for _, p := range r.Patterns {
		if p = strings.TrimSpace(p); p != "" {
			qs.Add("patterns", p)
		}
	}
	for _, e := range r.Events {
		if e = strings.TrimSpace(e); e != "" {
			qs.Add("events", e)
		}
	}
	if r.UID != 0 && r.UID != NonRootUser {
		qs.Set("uid", strconv.FormatUint(uint64(r.UID), 10))
	}
	if r.Recursive {
		qs.Set("recursive", "true")
	}
	if r.OneShot {
		qs.Set("one_shot", "true")
	}
	if d := strings.TrimSpace(r.Debounce); d != "" {
		qs.Set("debounce", d)
	}
	if t := strings.TrimSpace(r.Timeout); t != "" {
		qs.Set("timeout", t)
	}
	if r.MaxEvents > 0 {
		qs.Set("max_events", strconv.Itoa(r.MaxEvents))
	}
	return qs, nil
}

// WatchFS opens a filesystem watch stream at /vm/{vmName}/fs/watch and
// returns a channel of decoded events and a channel carrying any terminal
// error. Both channels are closed when the stream ends.
//
// Heartbeat SSE comments and `event:` lines are silently discarded; each
// delivered event includes its `id:` in SlicerFSWatchEvent.ID.
func (c *SlicerClient) WatchFS(ctx context.Context, vmName string, req SlicerFSWatchRequest) (<-chan SlicerFSWatchEvent, <-chan error) {
	events := make(chan SlicerFSWatchEvent)
	errs := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errs)

		qs, err := req.toQuery()
		if err != nil {
			errs <- err
			return
		}

		u, err := url.Parse(c.baseURL)
		if err != nil {
			errs <- fmt.Errorf("invalid base URL: %w", err)
			return
		}
		u.Path = path.Join(u.Path, fmt.Sprintf("/vm/%s/fs/watch", vmName))
		u.RawQuery = qs.Encode()

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			errs <- fmt.Errorf("failed to create watch request: %w", err)
			return
		}
		if c.userAgent != "" {
			httpReq.Header.Set("User-Agent", c.userAgent)
		}
		if c.token != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.token)
		}
		httpReq.Header.Set("Accept", "text/event-stream")
		if id := strings.TrimSpace(req.LastEventID); id != "" {
			httpReq.Header.Set("Last-Event-ID", id)
		}

		res, err := c.httpClient.Do(httpReq)
		if err != nil {
			errs <- fmt.Errorf("failed to open watch stream: %w", err)
			return
		}
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			errs <- fmt.Errorf("watch request failed: %s - %s", res.Status, strings.TrimSpace(string(body)))
			return
		}

		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var (
			dataLines []string
			eventID   uint64
		)

		emit := func() bool {
			if len(dataLines) == 0 {
				return true
			}
			payload := strings.Join(dataLines, "\n")
			dataLines = nil

			var p watchEventPayload
			if err := json.Unmarshal([]byte(payload), &p); err != nil {
				errs <- fmt.Errorf("failed to parse watch event: %w", err)
				return false
			}

			evt := SlicerFSWatchEvent{
				ID:        p.ID,
				Type:      p.Type,
				Path:      p.Path,
				Timestamp: p.Timestamp,
				Size:      p.Size,
				IsDir:     p.IsDir,
				Message:   p.Message,
			}
			if evt.ID == 0 {
				evt.ID = eventID
			}

			select {
			case <-ctx.Done():
				return false
			case events <- evt:
				return true
			}
		}

		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if !emit() {
					return
				}
			case strings.HasPrefix(line, ":"):
				// heartbeat / comment, ignore
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			case strings.HasPrefix(line, "id:"):
				if parsed, perr := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "id:")), 10, 64); perr == nil {
					eventID = parsed
				}
			case strings.HasPrefix(line, "event:"):
				// named event type, ignored (we only use `watch`)
			}
		}

		if err := scanner.Err(); err != nil {
			if ctx.Err() != nil {
				return
			}
			errs <- fmt.Errorf("watch stream read error: %w", err)
			return
		}

		// Flush any trailing SSE block without a closing blank line.
		_ = emit()
	}()

	return events, errs
}

// WatchFSIter is a range-over-func adapter over WatchFS.
//
// Usage:
//
//	for evt, err := range client.WatchFSIter(ctx, vm, req) {
//		if err != nil {
//			// stream error or cancellation
//			break
//		}
//		// consume evt
//	}
func (c *SlicerClient) WatchFSIter(ctx context.Context, vmName string, req SlicerFSWatchRequest) iter.Seq2[SlicerFSWatchEvent, error] {
	return func(yield func(SlicerFSWatchEvent, error) bool) {
		iterCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		events, errs := c.WatchFS(iterCtx, vmName, req)
		for {
			select {
			case err, ok := <-errs:
				if !ok {
					return
				}
				if err != nil {
					_ = yield(SlicerFSWatchEvent{}, err)
				}
				return
			case evt, ok := <-events:
				if !ok {
					return
				}
				if !yield(evt, nil) {
					return
				}
			case <-iterCtx.Done():
				return
			}
		}
	}
}

// ParseFSWatchTimestamp parses the wire timestamp from a watch event.
// The timestamp is RFC3339Nano when present; an empty value returns the
// zero time without error.
func ParseFSWatchTimestamp(v string) (time.Time, error) {
	if strings.TrimSpace(v) == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, v)
}
