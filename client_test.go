package slicer

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMakeRequest_AuthHeaderWithToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		want := "Bearer test-token"
		if auth != want {
			t.Errorf("Want '%s', got '%s'", want, auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "test-token", "test-agent", nil)
	resp, err := client.makeRequest(http.MethodGet, "/test", nil)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestMakeRequest_NoAuthHeaderWhenTokenEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "" {
			t.Errorf("Want no Authorization header, got '%s'", auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "test-agent", nil)
	resp, err := client.makeRequest(http.MethodGet, "/test", nil)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestMakeRequest_WithBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Content-Type header
		ct := r.Header.Get("Content-Type")
		want := "application/json"
		if ct != want {
			t.Errorf("Want '%s', got '%s'", want, ct)
		}

		// Verify body content
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("Failed to read request body: %v", err)
		}
		wantBody := `{"name":"test","value":"data"}`
		if string(body) != wantBody {
			t.Errorf("Want body '%s', got '%s'", wantBody, string(body))
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "token", "agent", nil)
	requestBody := map[string]string{"name": "test", "value": "data"}
	resp, err := client.makeRequest(http.MethodPost, "/test", requestBody)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestMakeRequest_WithoutBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify no Content-Type header for requests without body
		ct := r.Header.Get("Content-Type")
		if ct != "" {
			t.Errorf("Want no Content-Type header, got '%s'", ct)
		}

		// Verify method and path
		if r.Method != http.MethodGet {
			t.Errorf("Want %s method, got %s", http.MethodGet, r.Method)
		}
		want := "/test"
		if r.URL.Path != want {
			t.Errorf("Want %s path, got %s", want, r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "token", "agent", nil)
	resp, err := client.makeRequest(http.MethodGet, "/test", nil)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestMakeRequest_InvalidJSON(t *testing.T) {
	client := NewSlicerClient("http://localhost", "token", "agent", nil)

	// Use a channel which can't be marshaled to JSON
	invalidBody := make(chan int)
	_, err := client.makeRequest(http.MethodPost, "/test", invalidBody)

	if err == nil {
		t.Error("Want error, got nil")
	}
}

func TestMakeRequest_CustomUserAgent(t *testing.T) {
	customAgent := "custom-user-agent/1.0"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := r.Header.Get("User-Agent")
		if ua != customAgent {
			t.Errorf("Want '%s', got '%s'", customAgent, ua)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "token", customAgent, nil)
	resp, err := client.makeRequest(http.MethodGet, "/test", nil)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestMakeRequest_InvalidBaseURL(t *testing.T) {
	client := NewSlicerClient("://invalid-url", "token", "agent", nil)
	_, err := client.makeRequest(http.MethodGet, "/test", nil)

	if err == nil {
		t.Error("Want error, got nil")
	}
}
