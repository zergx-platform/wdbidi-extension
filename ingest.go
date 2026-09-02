package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ingestResult is the agent's /files/ingest response (file record).
type ingestResult struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// ingestByteArray uploads raw bytes to the agent's file store and returns the
// resulting `file:<code>` reference (with a name/mime/size label), so a tool
// that produces a binary artifact (screenshot, PDF) hands the model a stable
// reference instead of embedding the bytes.
func ingestByteArray(ctx context.Context, filename, mime string, data []byte) (string, error) {
	agentURL := envOr("AGENT_BASE_URL", envOr("ZERGX_AGENT_URL", "http://agent.zergx.svc.cluster.local:80"))
	u := strings.TrimRight(agentURL, "/") + "/api/v1/files/ingest"
	q := url.Values{}
	q.Set("name", filename)
	q.Set("content_type", mime)
	u += "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if tok := envOr("AGENT_API_KEY", ""); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("agent ingest HTTP %d: %.200s", resp.StatusCode, body)
	}
	var r ingestResult
	if err := json.Unmarshal(body, &r); err != nil {
		return "", err
	}
	name := r.Name
	if name == "" {
		name = filename
	}
	return fmt.Sprintf("[附件 %s | file:%s | %s | %d B]", name, r.Code, r.Mime, r.Size), nil
}
