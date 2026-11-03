package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/api"
)

// Client talks to a daemon's HTTP API.
type Client struct {
	// Base is the daemon's address without a trailing slash.
	Base string
	http *http.Client
}

func NewClient(url string) *Client {
	return &Client{Base: trimBase(url), http: &http.Client{Timeout: 30 * time.Second}}
}

// Healthy reports whether a daemon answers at the address.
func (c *Client) Healthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/health", nil)
	if err != nil {
		return false
	}
	response, err := c.http.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

// WaitHealthy waits until the started daemon answers, or reports why not.
func (c *Client) WaitHealthy(ctx context.Context, daemon *Daemon) error {
	deadline := time.NewTimer(startupTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for !c.Healthy(ctx) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-daemon.done:
			return fmt.Errorf("the daemon exited during startup (%v); see its log", daemon.err)
		case <-deadline.C:
			return fmt.Errorf("the daemon did not answer within %s", startupTimeout)
		case <-tick.C:
		}
	}
	return nil
}

// Version is the version the daemon reports.
func (c *Client) Version(ctx context.Context) (string, error) {
	var health struct {
		Version string `json:"version"`
	}
	if err := c.call(ctx, http.MethodGet, "/health", nil, &health); err != nil {
		return "", err
	}
	return health.Version, nil
}

// OpenProject makes folder the daemon's project unless it already is:
// restore saved data for a known path, otherwise import it.
func (c *Client) OpenProject(ctx context.Context, folder string) error {
	var current struct {
		Path string `json:"path"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/projects/current", nil, &current); err == nil && sameFolder(current.Path, folder) {
		return nil
	}
	body := map[string]string{"project_path": folder}
	if err := c.call(ctx, http.MethodPost, "/api/projects/restore", body, nil); err == nil {
		return nil
	}
	if err := c.call(ctx, http.MethodPost, "/api/projects/import", body, nil); err != nil {
		return fmt.Errorf("open %s in the daemon: %w", folder, err)
	}
	return nil
}

// ConfigNote says which configuration a running daemon uses; it keeps the one
// it started with until it is restarted.
func (c *Client) ConfigNote(ctx context.Context) string {
	var catalog struct {
		ConfigFile *string `json:"config_file"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/models/current", nil, &catalog); err != nil || catalog.ConfigFile == nil {
		return "Restart the daemon to load another configuration."
	}
	if *catalog.ConfigFile == "" {
		return "The running daemon found no config.yaml and uses the built-in local model defaults; stop it and run kogin again after adding one."
	}
	return "The running daemon uses " + *catalog.ConfigFile + "; restart it to load another configuration."
}

func (c *Client) call(ctx context.Context, method, path string, body, result any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.Base+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		token, err := readWriteToken()
		if err != nil {
			return err
		}
		request.Header.Set(api.WriteTokenHeader, token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", method, path, daemonMessage(data, response.Status))
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(data, result)
}

func daemonMessage(data []byte, status string) string {
	var failure struct {
		UserMessage string `json:"user_message"`
		Message     string `json:"message"`
	}
	if json.Unmarshal(data, &failure) == nil {
		if failure.UserMessage != "" {
			return failure.UserMessage
		}
		if failure.Message != "" {
			return failure.Message
		}
	}
	return status
}

func readWriteToken() (string, error) {
	path, err := api.WriteTokenFile()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("the daemon's write token %s is missing; restart the daemon", path)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
