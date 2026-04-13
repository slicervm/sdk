package slicer

import (
	"encoding/base64"
	"fmt"
	"net/url"
)

const (
	ExecStdioText   = "text"
	ExecStdioBase64 = "base64"
)

func setExecStdioQuery(q url.Values, execReq SlicerExecRequest) error {
	stdio := execReq.Stdio
	if stdio == "" {
		stdio = ExecStdioBase64
	}

	switch stdio {
	case ExecStdioText, ExecStdioBase64:
		q.Set("stdio", stdio)
		return nil
	default:
		return fmt.Errorf("unsupported exec stdio encoding %q", stdio)
	}
}

func decodeExecWriteResult(result *SlicerExecWriteResult) error {
	if result.Encoding != ExecStdioBase64 {
		return nil
	}

	var err error
	if result.Data != "" {
		result.Data, err = decodeBase64String(result.Data)
		if err != nil {
			return fmt.Errorf("failed to decode exec data: %w", err)
		}
	}
	if result.Stdout != "" {
		result.Stdout, err = decodeBase64String(result.Stdout)
		if err != nil {
			return fmt.Errorf("failed to decode exec stdout: %w", err)
		}
	}
	if result.Stderr != "" {
		result.Stderr, err = decodeBase64String(result.Stderr)
		if err != nil {
			return fmt.Errorf("failed to decode exec stderr: %w", err)
		}
	}

	result.Encoding = ""
	return nil
}

func decodeExecResult(result *ExecResult) error {
	if result.Encoding != ExecStdioBase64 {
		return nil
	}

	var err error
	if result.Stdout != "" {
		result.Stdout, err = decodeBase64String(result.Stdout)
		if err != nil {
			return fmt.Errorf("failed to decode exec stdout: %w", err)
		}
	}
	if result.Stderr != "" {
		result.Stderr, err = decodeBase64String(result.Stderr)
		if err != nil {
			return fmt.Errorf("failed to decode exec stderr: %w", err)
		}
	}

	result.Encoding = ""
	return nil
}

func decodeBase64String(value string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func (r SlicerExecWriteResult) DataBytes() ([]byte, error) {
	if r.Encoding == ExecStdioBase64 {
		return base64.StdEncoding.DecodeString(r.Data)
	}
	return []byte(r.Data), nil
}

func (r SlicerExecWriteResult) StdoutBytes() ([]byte, error) {
	if r.Encoding == ExecStdioBase64 {
		return base64.StdEncoding.DecodeString(r.Stdout)
	}
	return []byte(r.Stdout), nil
}

func (r SlicerExecWriteResult) StderrBytes() ([]byte, error) {
	if r.Encoding == ExecStdioBase64 {
		return base64.StdEncoding.DecodeString(r.Stderr)
	}
	return []byte(r.Stderr), nil
}

func (r ExecResult) StdoutBytes() ([]byte, error) {
	if r.Encoding == ExecStdioBase64 {
		return base64.StdEncoding.DecodeString(r.Stdout)
	}
	return []byte(r.Stdout), nil
}

func (r ExecResult) StderrBytes() ([]byte, error) {
	if r.Encoding == ExecStdioBase64 {
		return base64.StdEncoding.DecodeString(r.Stderr)
	}
	return []byte(r.Stderr), nil
}
