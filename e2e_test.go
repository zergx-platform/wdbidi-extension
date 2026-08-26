package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestE2E(t *testing.T) {
	if os.Getenv("RUCODER_E2E") != "1" {
		t.Skip("set RUCODER_E2E=1 to run against the cluster selenium")
	}
	os.Setenv("RUCODER_SELENIUM_URL", "http://rucoder-selenium.temp.svc.cluster.local:4444")
	os.Setenv("RUCODER_BROWSER_NAME", "chrome")
	s := newServer()
	sid := fmt.Sprintf("go-e2e-%d", time.Now().UnixNano())
	h := s.handlers()
	run := func(tool string, args map[string]any) (string, error) {
		c, _, e := h[tool].Execute(nil, args, "cid", sid, func(string) {})
		return c, e
	}
	fail := 0
	step := func(name string, fn func() (string, error)) {
		content, err := fn()
		if err != nil {
			fail++
			t.Errorf("FAIL %s: %v", name, err)
			return
		}
		t.Logf("PASS %s | %s", name, trunc(content, 80))
	}
	enc := func(s string) string { return strings.ReplaceAll(urlQueryEscape(s), "+", "%20") }

	dyn := `<!DOCTYPE html><html><body><h1>D</h1><div id="a"></div><script>setTimeout(()=>{const b=document.createElement("button");b.id="late";b.textContent="Late";b.onclick=()=>{document.getElementById("out").textContent="ok"};document.getElementById("a").appendChild(b);const p=document.createElement("p");p.id="out";document.getElementById("a").appendChild(p)},1200)</script></body></html>`
	step("navigate(dyn)", func() (string, error) {
		return run("navigate", map[string]any{"url": "data:text/html;charset=utf-8," + enc(dyn)})
	})
	step("click(#late 延迟)", func() (string, error) { return run("click", map[string]any{"element": "#late"}) })
	step("expect_text(ok)", func() (string, error) { return run("expect_text", map[string]any{"text": "ok"}) })

	form := `<!DOCTYPE html><html><body><input id="n"><button id="b" onclick='document.getElementById("o").textContent="h:"+document.getElementById("n").value'>G</button><p id="o"></p></body></html>`
	step("navigate(form)", func() (string, error) {
		return run("navigate", map[string]any{"url": "data:text/html;charset=utf-8," + enc(form)})
	})
	step("type", func() (string, error) { return run("type", map[string]any{"element": "#n", "text": "w"}) })
	step("click(#b)", func() (string, error) { return run("click", map[string]any{"element": "#b"}) })
	step("expect_text(h:w)", func() (string, error) { return run("expect_text", map[string]any{"text": "h:w"}) })

	step("navigate(example.com)", func() (string, error) { return run("navigate", map[string]any{"url": "https://example.com"}) })
	step("snapshot", func() (string, error) { return run("snapshot", map[string]any{}) })
	step("find", func() (string, error) { return run("find", map[string]any{"text": "example domain"}) })
	step("close", func() (string, error) { return run("close", map[string]any{}) })

	if fail > 0 {
		t.Fatalf("%d steps failed", fail)
	}
}

func urlQueryEscape(s string) string {
	// encodeURIComponent-equivalent for data: URLs (spaces as %20).
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	return b.String()
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
