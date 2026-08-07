package slicer

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// APIError describes a non-successful response from the Slicer API.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Status     string
	Body       string
}

func (e *APIError) Error() string {
	detail := strings.TrimSpace(e.Body)
	if detail == "" {
		return fmt.Sprintf("slicer %s %s failed: %s", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("slicer %s %s failed: %s: %s", e.Method, e.Path, e.Status, detail)
}

// IsAPIStatus reports whether err contains an APIError with the given HTTP
// status code.
func IsAPIStatus(err error, statusCode int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == statusCode
}

func newAPIError(res *http.Response, body []byte) *APIError {
	apiErr := &APIError{
		StatusCode: res.StatusCode,
		Status:     res.Status,
		Body:       strings.TrimSpace(string(body)),
	}
	if res.Request != nil {
		apiErr.Method = res.Request.Method
		if res.Request.URL != nil {
			apiErr.Path = res.Request.URL.EscapedPath()
		}
	}
	return apiErr
}
