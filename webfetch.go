package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"golang.org/x/net/html"
)

// webfetch mirrors opencode's webfetch: fetch an HTTP(S) URL and return
// text / markdown / html. No browser involved.

const (
	webfetchMaxBytes       = 5 * 1024 * 1024
	webfetchMaxTimeoutSecs = 120
	webfetchUA             = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
)

func webfetch(rawURL, format string, timeoutSecs int) (map[string]any, error) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return nil, fmt.Errorf("URL must start with http:// or https://")
	}
	if format == "" {
		format = "markdown"
	}
	timeout := timeoutSecs
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > webfetchMaxTimeoutSecs {
		timeout = webfetchMaxTimeoutSecs
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	client := &http.Client{}

	doFetch := func(ua string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", acceptHeader(format))
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		return client.Do(req)
	}

	resp, err := doFetch(webfetchUA)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 403 && resp.Header.Get("cf-mitigated") == "challenge" {
		resp.Body.Close()
		resp, err = doFetch("wdbidi-extension")
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > webfetchMaxBytes {
		return nil, fmt.Errorf("Response too large (exceeds 5MB limit)")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, webfetchMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > webfetchMaxBytes {
		return nil, fmt.Errorf("Response too large (exceeds 5MB limit)")
	}

	contentType := resp.Header.Get("Content-Type")
	mime := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	base := map[string]any{"url": resp.Request.URL.String(), "contentType": contentType}

	if strings.HasPrefix(mime, "image/") {
		base["format"] = "image"
		base["image"] = "data:" + mime + ";base64," // placeholder; sized out
		return base, nil
	}

	content := string(body)
	isHTML := strings.Contains(mime, "text/html")
	switch format {
	case "text":
		base["format"] = "text"
		if isHTML {
			base["output"] = extractText(content)
		} else {
			base["output"] = content
		}
	case "html":
		base["format"] = "html"
		base["output"] = content
	default:
		base["format"] = "markdown"
		if isHTML {
			m, _ := htmlToMarkdown(content)
			base["output"] = m
		} else {
			base["output"] = content
		}
	}
	return base, nil
}

func acceptHeader(format string) string {
	switch format {
	case "markdown":
		return "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"
	case "text":
		return "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"
	case "html":
		return "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"
	default:
		return "*/*"
	}
}

func extractText(htmlStr string) string {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return ""
	}
	skip := map[string]bool{"script": true, "style": true, "noscript": true, "iframe": true, "object": true, "embed": true}
	inSkip := func(n *html.Node) bool {
		for p := n; p != nil; p = p.Parent {
			if p.Type == html.ElementNode && skip[p.Data] {
				return true
			}
		}
		return false
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode && !inSkip(n) {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return strings.TrimSpace(sb.String())
}

func htmlToMarkdown(htmlStr string) (string, error) {
	converter := md.NewConverter("", true, nil)
	return converter.ConvertString(htmlStr)
}
