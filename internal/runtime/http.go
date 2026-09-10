package runtime

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"
)

var runtimeHTTPClient = &http.Client{Timeout: 30 * time.Second}

func doHTTPRequest(method, url, body string) (string, error) {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = bytes.NewBufferString(body)
	}

	req, err := http.NewRequest(method, url, bodyReader) //nolint:noctx
	if err != nil {
		return "", err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := runtimeHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("http %s %s returned %d: %s", method, url, resp.StatusCode, string(respBody))
	}
	return string(respBody), nil
}
