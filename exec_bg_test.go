package slicer

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestExecKillUsesReplayableBody(t *testing.T) {
	transport := execBgRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.GetBody == nil {
			t.Fatal("kill request body is not replayable")
		}
		original, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := request.GetBody()
		if err != nil {
			t.Fatal(err)
		}
		defer replayed.Close()
		copyBody, err := io.ReadAll(replayed)
		if err != nil {
			t.Fatal(err)
		}
		if string(original) == "" || string(original) != string(copyBody) {
			t.Fatalf("original/replayed body = %q/%q", original, copyBody)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"exec_id":"ex_abc123","pid":42,"running":false,"signal_sent":"TERM"}`)),
			Request:    request,
		}, nil
	})
	client := NewSlicerClient("http://slicer.invalid", "", "test", &http.Client{Transport: transport})
	if _, err := client.ExecKill(t.Context(), "vm-1", "ex_abc123", KillOptions{Signal: "TERM"}); err != nil {
		t.Fatal(err)
	}
}

type execBgRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn execBgRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
