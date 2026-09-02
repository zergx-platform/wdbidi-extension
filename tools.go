package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/abcp-sdk/abc-protocol-go/extension"
)

type Args = map[string]any

func argString(a Args, k string) string {
	if v, ok := a[k].(string); ok {
		return v
	}
	return ""
}

func argInt(a Args, k string, def int) int {
	if v, ok := a[k].(float64); ok {
		return int(v)
	}
	return def
}

func argBool(a Args, k string, def bool) bool {
	if v, ok := a[k].(bool); ok {
		return v
	}
	return def
}

func argStringSlice(a Args, k string) []string {
	if v, ok := a[k].([]any); ok {
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (s *server) handlers() map[string]extension.ToolSpec {
	return map[string]extension.ToolSpec{
		"navigate": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				url := argString(args, "url")
				if url == "" {
					return extension.ToolResultData{}, fmt.Errorf("missing %q argument", "url")
				}
				u, err := s.navigate(sessionName, url)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"url": u}), Data: map[string]any{"url": u}}, nil
			},
		},
		"navigate_back": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				u, err := s.traverseHistory(sessionName, -1)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"url": u}), Data: map[string]any{"url": u}}, nil
			},
		},
		"navigate_forward": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				u, err := s.traverseHistory(sessionName, 1)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"url": u}), Data: map[string]any{"url": u}}, nil
			},
		},
		"close": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				if ctxid, err := s.currentContext(sessionName); err == nil {
					_, _ = s.bidiCall("browsingContext.close", map[string]any{"context": ctxid})
				}
				s.resetContext(sessionName)
				return extension.ToolResultData{Content: jsonString(map[string]any{"closed": true}), Data: map[string]any{"closed": true}}, nil
			},
		},
		"resize": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				w := argInt(args, "width", 1280)
				h := argInt(args, "height", 720)
				ctxid, err := s.ensureContext(sessionName)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				if _, err := s.bidiCall("browsingContext.setViewport", map[string]any{
					"context": ctxid, "viewport": map[string]any{"width": w, "height": h},
				}); err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"width": w, "height": h}), Data: map[string]any{"width": w, "height": h}}, nil
			},
		},
		"snapshot": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				boxes := argBool(args, "boxes", true)
				depth := argInt(args, "depth", 0)
				yamlText, jsonTree, boxMap, err := s.ariaSnapshot(sessionName, boxes, depth)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: yamlText, Data: map[string]any{"json": jsonTree, "boxes": boxMap}}, nil
			},
		},
		"screenshot": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				element := argString(args, "element")
				fullPage := argBool(args, "fullPage", false)
				data, err := s.screenshotElement(sessionName, element, fullPage)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				content := jsonString(map[string]any{"screenshot": "data:image/png;base64," + data})
				return extension.ToolResultData{Content: content, Data: map[string]any{"type": "png", "bytes": len(data) * 3 / 4}}, nil
			},
		},
		"click": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				element := argString(args, "element")
				if element == "" {
					return extension.ToolResultData{}, fmt.Errorf("missing %q argument", "element")
				}
				button := 0
				switch argString(args, "button") {
				case "right":
					button = 2
				case "middle":
					button = 1
				}
				dbl := argBool(args, "doubleClick", false)
				if err := s.clickAt(sessionName, element, button); err != nil {
					return extension.ToolResultData{}, err
				}
				if dbl {
					if err := s.clickAt(sessionName, element, button); err != nil {
						return extension.ToolResultData{}, err
					}
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
		"hover": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				element := argString(args, "element")
				if element == "" {
					return extension.ToolResultData{}, fmt.Errorf("missing %q argument", "element")
				}
				ctxid, err := s.ensureContext(sessionName)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				pt, err := s.waitActionable(sessionName, element, false)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				if err := s.pointerActions(ctxid, []map[string]any{
					{"type": "pointerMove", "x": round(pt.X), "y": round(pt.Y)},
				}); err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
		"type": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				element := argString(args, "element")
				text := argString(args, "text")
				if element == "" || text == "" {
					return extension.ToolResultData{}, fmt.Errorf("missing %q or %q argument", "element", "text")
				}
				submit := argBool(args, "submit", false)
				if err := s.typeInto(sessionName, element, text, submit); err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
		"select_option": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				element := argString(args, "element")
				values := argStringSlice(args, "values")
				if element == "" {
					return extension.ToolResultData{}, fmt.Errorf("missing %q argument", "element")
				}
				sel := toSelector(element)
				if _, err := s.waitActionable(sessionName, element, true); err != nil {
					return extension.ToolResultData{}, err
				}
				fn := `(sel, values) => { const el = document.querySelector(sel); if (!el || el.tagName !== 'SELECT') return false;
            for (const o of el.options) o.selected = values.includes(o.value);
            el.dispatchEvent(new Event('input', { bubbles: true }));
            el.dispatchEvent(new Event('change', { bubbles: true }));
            return true; }`
				vals := make([]any, len(values))
				for i, v := range values {
					vals[i] = map[string]any{"type": "string", "value": v}
				}
				if _, err := s.callFunction(sessionName, fn, []map[string]any{
					{"type": "string", "value": sel},
					{"type": "array", "value": vals},
				}); err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
		"fill_form": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				fields := []struct{ Target, Value string }{}
				if arr, ok := args["fields"].([]any); ok {
					for _, e := range arr {
						if m, ok := e.(map[string]any); ok {
							t, _ := m["target"].(string)
							v, _ := m["value"].(string)
							if t != "" {
								fields = append(fields, struct{ Target, Value string }{t, v})
							}
						}
					}
				}
				for _, f := range fields {
					if err := s.typeInto(sessionName, f.Target, f.Value, false); err != nil {
						return extension.ToolResultData{}, err
					}
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"filled": len(fields)}), Data: map[string]any{"filled": len(fields)}}, nil
			},
		},
		"press_key": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				key := argString(args, "key")
				ctxid, err := s.ensureContext(sessionName)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				if err := s.keyActions(ctxid, []map[string]any{
					{"type": "keyDown", "value": key},
					{"type": "keyUp", "value": key},
				}); err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
		"drag": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				start := argString(args, "startElement")
				end := argString(args, "endElement")
				ctxid, err := s.ensureContext(sessionName)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				sp, err := s.waitActionable(sessionName, start, false)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				ep, err := s.waitActionable(sessionName, end, false)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				if err := s.pointerActions(ctxid, []map[string]any{
					{"type": "pointerMove", "x": round(sp.X), "y": round(sp.Y)},
					{"type": "pointerDown", "button": 0},
					{"type": "pointerMove", "x": round(ep.X), "y": round(ep.Y)},
					{"type": "pointerUp", "button": 0},
				}); err != nil {
					// Best-effort: release any stuck input state after a failed drag.
					_ = s.releaseActions(sessionName)
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
		"drop": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				element := argString(args, "element")
				if element == "" {
					return extension.ToolResultData{}, fmt.Errorf("drop requires element")
				}
				files := argStringSlice(args, "paths")
				data := map[string]string{}
				if dm, ok := args["data"].(map[string]any); ok {
					for k, v := range dm {
						if s, ok := v.(string); ok {
							data[k] = s
						}
					}
				}
				if len(files) == 0 && len(data) == 0 {
					return extension.ToolResultData{}, fmt.Errorf("drop requires paths or data")
				}
				if err := s.dropData(sessionName, element, files, data); err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
		"file_upload": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				element := argString(args, "element")
				files := argStringSlice(args, "paths")
				if err := s.setFiles(sessionName, element, files); err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
		"find": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				text := strings.ToLower(argString(args, "text"))
				regex := argString(args, "regex")
				yamlText, _, _, err := s.ariaSnapshot(sessionName, false, 0)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				lines := strings.Split(yamlText, "\n")
				matches := []string{}
				var re *regexp.Regexp
				if regex != "" && len(regex) <= 200 {
					if r, e := regexp.Compile(regex); e == nil {
						re = r
					}
				}
				for i, line := range lines {
					var hit bool
					if re != nil {
						hit = re.MatchString(line)
					} else {
						hit = strings.Contains(strings.ToLower(line), text)
					}
					if hit {
						lo := i - 3
						if lo < 0 {
							lo = 0
						}
						hi := i + 4
						if hi > len(lines) {
							hi = len(lines)
						}
						matches = append(matches, strings.Join(lines[lo:hi], "\n"))
					}
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"matches": matches, "count": len(matches)}), Data: map[string]any{"count": len(matches)}}, nil
			},
		},
		"wait_for": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				if t := argInt(args, "time", 0); t > 0 {
					time.Sleep(time.Duration(t) * time.Second)
					return extension.ToolResultData{Content: jsonString(map[string]any{"waited": t}), Data: map[string]any{"waited": t}}, nil
				}
				text := argString(args, "text")
				regex := argString(args, "regex")
				if text == "" && regex == "" {
					return extension.ToolResultData{}, fmt.Errorf("wait_for requires time | text | regex")
				}
				var re *regexp.Regexp
				if regex != "" && len(regex) <= 200 {
					re, _ = regexp.Compile(regex)
				}
				needle := text
				if needle == "" {
					needle = regex
				}
				deadline := time.Now().Add(30 * time.Second)
				for time.Now().Before(deadline) {
					body, _ := s.pageBody(sessionName)
					var hit bool
					if re != nil {
						hit = re.MatchString(body)
					} else {
						hit = strings.Contains(strings.ToLower(body), strings.ToLower(needle))
					}
					if hit {
						return extension.ToolResultData{Content: jsonString(map[string]any{"matched": needle}), Data: map[string]any{"matched": needle}}, nil
					}
					time.Sleep(200 * time.Millisecond)
				}
				return extension.ToolResultData{}, fmt.Errorf("wait_for timed out")
			},
		},
		"expect_text": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				text := argString(args, "text")
				if text == "" {
					return extension.ToolResultData{}, fmt.Errorf("missing %q argument", "text")
				}
				// Playwright fly: verify_text_visible is an immediate assertion —
				// no polling, no auto-wait. Report error when the text is not
				// currently visible.
				body, err := s.pageBody(sessionName)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				ok := strings.Contains(body, text)
				if !ok {
					return extension.ToolResultData{}, fmt.Errorf("text not found: %q", text)
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true, "text": text}), Data: map[string]any{"ok": true, "text": text}}, nil
			},
		},
		"handle_dialog": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				accept := argBool(args, "accept", true)
				promptText := argString(args, "promptText")
				// Wait for the dialog to open (userPromptOpened event) and then
				// handle it — rather than firing handleUserPrompt blind.
				handled, err := s.waitForPrompt(sessionName, accept, promptText, 30*time.Second)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"registered": true, "handled": handled}), Data: map[string]any{}}, nil
			},
		},
		"evaluate": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				code := argString(args, "code")
				if code == "" {
					code = argString(args, "expression")
				}
				v, err := s.evaluate(sessionName, code)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				result := unwrapRemote(v)
				return extension.ToolResultData{Content: jsonString(map[string]any{"result": result}), Data: map[string]any{"result": result}}, nil
			},
		},
		"tabs": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				action := argString(args, "action")
				if action == "" {
					action = argString(args, "tabAction")
				}
				if action == "" {
					action = "list"
				}
				tree, err := s.bidiCall("browsingContext.getTree", map[string]any{})
				if err != nil {
					return extension.ToolResultData{}, err
				}
				var out struct {
					Contexts []struct {
						Context string `json:"context"`
						URL     string `json:"url"`
					} `json:"contexts"`
				}
				_ = json.Unmarshal(tree, &out)
				switch action {
				case "list":
					tabs := make([]map[string]any, len(out.Contexts))
					for i, c := range out.Contexts {
						tabs[i] = map[string]any{"index": i, "url": c.URL}
					}
					return extension.ToolResultData{Content: jsonString(map[string]any{"tabs": tabs}), Data: map[string]any{}}, nil
				case "new":
					url := argString(args, "url")
					res, err := s.bidiCall("browsingContext.create", map[string]any{"type": "tab"})
					if err != nil {
						return extension.ToolResultData{}, err
					}
					var cr struct {
						Context string `json:"context"`
					}
					_ = json.Unmarshal(res, &cr)
					if url != "" && cr.Context != "" {
						_, _ = s.bidiCall("browsingContext.navigate", map[string]any{"context": cr.Context, "url": url, "wait": "complete"})
						return extension.ToolResultData{Content: jsonString(map[string]any{"url": url}), Data: map[string]any{"url": url}}, nil
					}
					return extension.ToolResultData{Content: jsonString(map[string]any{}), Data: map[string]any{}}, nil
				case "close":
					if ctxid, err := s.currentContext(sessionName); err == nil {
						_, _ = s.bidiCall("browsingContext.close", map[string]any{"context": ctxid})
					}
					s.resetContext(sessionName)
					return extension.ToolResultData{Content: jsonString(map[string]any{"closed": true}), Data: map[string]any{"closed": true}}, nil
				default:
					idx := argInt(args, "index", 0)
					if idx >= 0 && idx < len(out.Contexts) {
						// Activate the chosen context (real tab switch, not just an
						// index echo) via browsingContext.activate.
						target := out.Contexts[idx].Context
						if target != "" {
							_, _ = s.bidiCall("browsingContext.activate", map[string]any{"context": target})
						}
						return extension.ToolResultData{Content: jsonString(map[string]any{"selected": idx}), Data: map[string]any{"selected": idx}}, nil
					}
					return extension.ToolResultData{Content: jsonString(map[string]any{"selected": -1}), Data: map[string]any{"selected": -1}}, nil
				}
			},
		},
		"network_requests": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				reqs := s.networkLog(sessionName)
				return extension.ToolResultData{Content: jsonString(map[string]any{"requests": reqs}), Data: map[string]any{"requests": reqs}}, nil
			},
		},
		"webfetch": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				url := argString(args, "url")
				format := argString(args, "format")
				timeout := argInt(args, "timeout", 30)
				r, err := webfetch(url, format, timeout)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(r), Data: r}, nil
			},
		},
		"reload": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				if err := s.reload(sessionName); err != nil {
					return extension.ToolResultData{}, err
				}
				u, _ := s.contextURLOf(sessionName)
				return extension.ToolResultData{Content: jsonString(map[string]any{"reloaded": true, "url": u}), Data: map[string]any{"url": u}}, nil
			},
		},
		"print_pdf": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				data, err := s.printPDF(sessionName)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"pdf": "data:application/pdf;base64," + data}), Data: map[string]any{"bytes": len(data) * 3 / 4}}, nil
			},
		},
		"cookies_get": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				cookies, err := s.getCookies(sessionName)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"cookies": cookies}), Data: map[string]any{"cookies": cookies}}, nil
			},
		},
		"cookies_set": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				name := argString(args, "name")
				value := argString(args, "value")
				domain := argString(args, "domain")
				path := argString(args, "path")
				if name == "" || domain == "" {
					return extension.ToolResultData{}, fmt.Errorf("cookies_set requires name, value, domain")
				}
				if path == "" {
					path = "/"
				}
				if err := s.setCookie(sessionName, name, value, domain, path); err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
		"cookies_delete": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				name := argString(args, "name")
				if err := s.deleteCookies(sessionName, name); err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
		"route": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				url := argString(args, "url")
				mode := argString(args, "mode")
				if url == "" || mode == "" {
					return extension.ToolResultData{}, fmt.Errorf("route requires url and mode")
				}
				var kind routeKind
				switch mode {
				case "abort":
					kind = routeAbort
				case "provide":
					kind = routeProvide
				default:
					kind = routeContinue
				}
				status := argInt(args, "status", 200)
				body := argString(args, "body")
				ct := argString(args, "contentType")
				if ct == "" {
					ct = "text/plain"
				}
				rid, err := s.addRoute(url, kind, status, body, ct)
				if err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"routeId": rid}), Data: map[string]any{"routeId": rid}}, nil
			},
		},
		"unroute": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				rid := argString(args, "routeId")
				if rid == "" {
					return extension.ToolResultData{}, fmt.Errorf("unroute requires routeId")
				}
				if err := s.removeRoute(rid); err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
		"console_logs": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				entries := s.consoleLogs(sessionName)
				return extension.ToolResultData{Content: jsonString(map[string]any{"logs": entries}), Data: map[string]any{"logs": entries}}, nil
			},
		},
		"emulate": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				ua := argString(args, "userAgent")
				tz := argString(args, "timezone")
				var lat, lon *float64
				if v, ok := args["latitude"].(float64); ok {
					lat = &v
				}
				if v, ok := args["longitude"].(float64); ok {
					lon = &v
				}
				if err := s.emulate(sessionName, ua, tz, lat, lon); err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
		"add_init_script": {
			Execute: func(ctx context.Context, args map[string]any, callID, sessionName string) (extension.ToolResultData, error) {
				script := argString(args, "script")
				if script == "" {
					return extension.ToolResultData{}, fmt.Errorf("add_init_script requires script")
				}
				if err := s.addInitScript(sessionName, script); err != nil {
					return extension.ToolResultData{}, err
				}
				return extension.ToolResultData{Content: jsonString(map[string]any{"ok": true}), Data: map[string]any{"ok": true}}, nil
			},
		},
	}
}

func (s *server) clickAt(sessionName string, element string, button int) error {
	ctxid, err := s.ensureContext(sessionName)
	if err != nil {
		return err
	}
	pt, err := s.waitActionable(sessionName, element, true)
	if err != nil {
		return err
	}
	return s.pointerActions(ctxid, []map[string]any{
		{"type": "pointerMove", "x": round(pt.X), "y": round(pt.Y)},
		{"type": "pointerDown", "button": button},
		{"type": "pointerUp", "button": button},
	})
}

func (s *server) typeInto(sessionName, element, text string, submit bool) error {
	ctxid, err := s.ensureContext(sessionName)
	if err != nil {
		return err
	}
	sel := toSelector(element)
	if err := s.clickAt(sessionName, element, 0); err != nil {
		return err
	}
	fn := `(sel, text) => { const el = document.querySelector(sel); if (!el) return false;
      if (el.tagName === 'SELECT') return false;
      el.focus();
      const proto = Object.getPrototypeOf(el);
      const desc = Object.getOwnPropertyDescriptor(proto, 'value');
      if (desc && desc.set) desc.set.call(el, text);
      else el.value = text;
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
      return true; }`
	if _, err := s.callFunction(sessionName, fn, []map[string]any{
		{"type": "string", "value": sel},
		{"type": "string", "value": text},
	}); err != nil {
		return err
	}
	if submit {
		return s.keyActions(ctxid, []map[string]any{
			{"type": "keyDown", "value": "\uE007"},
			{"type": "keyUp", "value": "\uE007"},
		})
	}
	return nil
}

func (s *server) pointerActions(ctxid string, actions []map[string]any) error {
	_, err := s.bidiCall("input.performActions", map[string]any{
		"context": ctxid,
		"actions": []map[string]any{
			{"type": "pointer", "id": "mouse", "parameters": map[string]any{"pointerType": "mouse"}, "actions": actions},
		},
	})
	return err
}

func (s *server) keyActions(ctxid string, actions []map[string]any) error {
	_, err := s.bidiCall("input.performActions", map[string]any{
		"context": ctxid,
		"actions": []map[string]any{
			{"type": "key", "id": "keyboard", "actions": actions},
		},
	})
	return err
}

func (s *server) setFiles(sessionName, element string, files []string) error {
	ctxid, err := s.ensureContext(sessionName)
	if err != nil {
		return err
	}
	// BiDi input.setFiles needs the element's sharedId and absolute paths on
	// the server; Selenium standalone forwards them to the browser process.
	rid, err := s.evaluateSharedID(sessionName, toSelector(element))
	if err != nil {
		return err
	}
	if rid == "" {
		return fmt.Errorf("element not found: %s", element)
	}
	_, err = s.bidiCall("input.setFiles", map[string]any{
		"context": ctxid,
		"element": map[string]any{"sharedId": rid},
		"files":   files,
	})
	if err != nil {
		return fmt.Errorf("input.setFiles: %w", err)
	}
	return nil
}

func (s *server) pageBody(sessionName string) (string, error) {
	v, err := s.evaluate(sessionName, "document.body.innerText")
	if err != nil {
		return "", err
	}
	if m, ok := v.(map[string]any); ok {
		if str, ok := m["value"].(string); ok {
			return str, nil
		}
	}
	return "", nil
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func round(f float64) int {
	return int(math.Round(f))
}
