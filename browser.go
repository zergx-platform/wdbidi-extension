package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// Browser-side helpers: script evaluation, RemoteValue unwrapping, navigation,
// screenshots, and the aria snapshot / actionability injection.

// evaluate runs a raw JS expression in the session's browsing context.
func (s *server) evaluate(sessionName, expression string) (any, error) {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return nil, err
	}
	return s.evalInContext(ctx, map[string]any{
		"expression": expression,
	})
}

// callFunction runs a functionDeclaration with optional RemoteValue args.
func (s *server) callFunction(sessionName, functionDeclaration string, args []map[string]any) (any, error) {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return nil, err
	}
	params := map[string]any{
		"functionDeclaration": functionDeclaration,
	}
	if args != nil {
		params["arguments"] = args
	}
	return s.evalInContext(ctx, params)
}

func (s *server) evalInContext(ctx string, params map[string]any) (any, error) {
	params["target"] = map[string]any{"context": ctx}
	params["awaitPromise"] = true
	d, err := s.ensureDriver()
	if err != nil {
		return nil, err
	}
	method := "script.evaluate"
	if _, isCall := params["functionDeclaration"]; isCall {
		method = "script.callFunction"
	}
	res, err := d.conn.Call(method, params)
	if err != nil {
		return nil, err
	}
	var out struct {
		Type             string          `json:"type"`
		Result           json.RawMessage `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("%s decode: %w", method, err)
	}
	if out.Type == "exception" || len(out.ExceptionDetails) > 0 {
		return nil, fmt.Errorf("%s failed: %s", method, string(out.ExceptionDetails))
	}
	var rv any
	if err := json.Unmarshal(out.Result, &rv); err != nil {
		return nil, err
	}
	return rv, nil
}

// remoteString extracts a string RemoteValue ("type":"string","value":...).
func remoteString(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	if t, _ := m["type"].(string); t != "string" {
		return "", false
	}
	s, ok := m["value"].(string)
	return s, ok
}

// unwrapRemote converts a BiDi RemoteValue into a plain Go value.
func unwrapRemote(v any) any {
	switch x := v.(type) {
	case map[string]any:
		t, _ := x["type"].(string)
		if t == "undefined" || t == "null" {
			return nil
		}
		if val, ok := x["value"]; ok {
			return unwrapRemote(val)
		}
		out := map[string]any{}
		for k, vv := range x {
			out[k] = unwrapRemote(vv)
		}
		return out
	case []any:
		// A Remote "object" may serialize as a list of [key, value] pairs.
		if len(x) > 0 {
			if pair, ok := x[0].([]any); ok && len(pair) == 2 {
				if _, ok2 := pair[0].(string); ok2 {
					obj := map[string]any{}
					for _, e := range x {
						if p, ok3 := e.([]any); ok3 && len(p) == 2 {
							if k, ok4 := p[0].(string); ok4 {
								obj[k] = unwrapRemote(p[1])
							}
						}
					}
					return obj
				}
			}
		}
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = unwrapRemote(vv)
		}
		return out
	default:
		return v
	}
}

// navigate loads a URL and waits for load completion.
func (s *server) navigate(sessionName, url string) (string, error) {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return "", err
	}
	if _, err := s.bidiCall("browsingContext.navigate", map[string]any{
		"context": ctx, "url": url, "wait": "complete",
	}); err != nil {
		return "", err
	}
	return s.contextURL(ctx)
}

func (s *server) traverseHistory(sessionName string, delta int) (string, error) {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return "", err
	}
	if _, err := s.bidiCall("browsingContext.traverseHistory", map[string]any{
		"context": ctx, "delta": delta,
	}); err != nil {
		return "", err
	}
	return s.contextURL(ctx)
}

func (s *server) contextURL(ctx string) (string, error) {
	res, err := s.bidiCall("browsingContext.getTree", map[string]any{})
	if err != nil {
		return "", err
	}
	var out struct {
		Contexts []struct {
			Context string `json:"context"`
			URL     string `json:"url"`
		} `json:"contexts"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	for _, c := range out.Contexts {
		if c.Context == ctx {
			return c.URL, nil
		}
	}
	return "", nil
}

func (s *server) screenshot(sessionName string) (string, error) {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return "", err
	}
	res, err := s.bidiCall("browsingContext.captureScreenshot", map[string]any{"context": ctx})
	if err != nil {
		return "", err
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	if out.Data == "" {
		return "", fmt.Errorf("captureScreenshot returned no data")
	}
	return out.Data, nil
}

// toSelector converts an element reference (aria ref "e3" or CSS selector)
// into a selector for injected scripts. Refs only resolve after a snapshot has
// stamped data-pw-aria-ref attributes.
func toSelector(element string) string {
	if isAriaRef(element) {
		return fmt.Sprintf(`[data-pw-aria-ref="%s"]`, element)
	}
	return element
}

func isAriaRef(s string) bool {
	if len(s) < 2 {
		return false
	}
	// (f<digits>)?e<digits>
	i := 0
	if s[0] == 'f' {
		i = 1
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	}
	if i >= len(s) || s[i] != 'e' {
		return false
	}
	i++
	if i >= len(s) {
		return false
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ariaSnapshot returns the injected snapshot (yaml text, json, boxes).
func (s *server) ariaSnapshot(sessionName string, boxes bool) (yamlText string, jsonTree any, boxMap map[string]box, err error) {
	fn := fmt.Sprintf(`() => { %s ; return globalThis.__ariaSnapshot.snapshotWithBoxes(document.body, { mode: 'ai', boxes: %t }); }`, ariaBundle, boxes)
	v, err := s.callFunction(sessionName, fn, nil)
	if err != nil {
		return "", nil, nil, err
	}
	str, ok := remoteString(v)
	if !ok {
		return "", nil, nil, fmt.Errorf("ariaSnapshot did not return a string")
	}
	var parsed struct {
		Yaml  string          `json:"yaml"`
		Json  json.RawMessage `json:"json"`
		Boxes map[string]box  `json:"boxes"`
	}
	if err := json.Unmarshal([]byte(str), &parsed); err != nil {
		return "", nil, nil, err
	}
	if err := json.Unmarshal(parsed.Json, &jsonTree); err != nil {
		return "", nil, nil, err
	}
	return parsed.Yaml, jsonTree, parsed.Boxes, nil
}

type box struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type actionableResult struct {
	Point point  `json:"point"`
	Box   box    `json:"box"`
	Error string `json:"error"`
}

// waitActionable waits (with client-side retry + backoff) until element is
// attached, visible, enabled, stable and hit-testable; returns a clickable
// point. Mirrors Playwright actionability + the TS webdriver-extension.
func (s *server) waitActionable(sessionName, element string, enabled bool) (point, error) {
	sel := toSelector(element)
	deadline := time.Now().Add(s.actionTimeout())
	wait := []time.Duration{0, 20 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond, 500 * time.Millisecond}
	retry := 0
	var lastErr string

	for time.Now().Before(deadline) {
		if retry > 0 {
			idx := retry - 1
			if idx >= len(wait) {
				idx = len(wait) - 1
			}
			if wait[idx] > 0 {
				time.Sleep(wait[idx])
			}
		}
		fn := fmt.Sprintf(`(sel, timeoutMs, enabled) => {
      if (!globalThis.__ariaSnapshot) { %s }
      return globalThis.__ariaSnapshot.waitActionable(sel, timeoutMs, enabled);
    }`, ariaBundle)
		v, err := s.callFunction(sessionName, fn, []map[string]any{
			{"type": "string", "value": sel},
			{"type": "number", "value": 30000},
			{"type": "boolean", "value": enabled},
		})
		if err != nil {
			// callFunction throws on a JS exception; treat as not-yet-actionable.
			lastErr = err.Error()
			retry++
			continue
		}
		str, ok := remoteString(v)
		if !ok {
			lastErr = fmt.Sprintf("bad waitActionable result: %v", v)
			retry++
			continue
		}
		var r actionableResult
		if err := json.Unmarshal([]byte(str), &r); err != nil {
			lastErr = err.Error()
			retry++
			continue
		}
		if r.Point.X != 0 || r.Point.Y != 0 || (r.Box.Width > 0 && r.Box.Height > 0) {
			return r.Point, nil
		}
		lastErr = r.Error
		retry++
	}
	return point{}, fmt.Errorf("element not actionable within %s: %s", s.actionTimeout(), lastErr)
}

// evaluateSharedID resolves a CSS/ref selector to the element's BiDi sharedId
// so it can be passed to input.setFiles. Returns "" when the element is
// missing. Uses a DOM-query + a round-trip: we evaluate with resultOwnership
// "root" and a script that returns the element; BiDi serializes it as a node
// RemoteValue carrying a sharedId.
func (s *server) evaluateSharedID(sessionName, sel string) (string, error) {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return "", err
	}
	// callFunction returns document.querySelector(sel); with resultOwnership
	// root the returned value is a node reference. We request serialization so
	// the RemoteValue is the node handle itself.
	res, err := s.evalRaw(ctx, map[string]any{
		"functionDeclaration": `(sel) => document.querySelector(sel)`,
		"arguments":           []map[string]any{{"type": "string", "value": sel}},
		"resultOwnership":     "root",
	})
	if err != nil {
		return "", err
	}
	var rv struct {
		Type     string `json:"type"`
		SharedID string `json:"sharedId"`
	}
	if err := json.Unmarshal(res, &rv); err != nil {
		return "", err
	}
	if rv.SharedID != "" {
		return rv.SharedID, nil
	}
	return "", nil
}

// evalRaw issues script.callFunction with explicit params and returns the raw
// result JSON (used for node-handle serialization).
func (s *server) evalRaw(ctx string, params map[string]any) (json.RawMessage, error) {
	params["target"] = map[string]any{"context": ctx}
	params["awaitPromise"] = true
	d, err := s.ensureDriver()
	if err != nil {
		return nil, err
	}
	method := "script.evaluate"
	if _, isCall := params["functionDeclaration"]; isCall {
		method = "script.callFunction"
	}
	res, err := d.conn.Call(method, params)
	if err != nil {
		return nil, err
	}
	var out struct {
		Type   string          `json:"type"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	if out.Type == "exception" {
		return nil, fmt.Errorf("evalRaw exception")
	}
	return out.Result, nil
}

// screenshotElement captures the browsing context screenshot, optionally
// cropping to an element's bounding box (via injected getBoundingClientRect).
// The base64 is always PNG (BiDi captureScreenshot only supports PNG).
func (s *server) screenshotElement(sessionName, element string) (string, error) {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return "", err
	}
	if element != "" {
		// Get the element's box, then capture the context and crop client-side
		// (BiDi has no element-scoped captureScreenshot).
		sel := toSelector(element)
		fn := `(sel) => { const el = document.querySelector(sel); if (!el) return null;
      const r = el.getBoundingClientRect();
      return { x: Math.round(r.x), y: Math.round(r.y), width: Math.round(r.width), height: Math.round(r.height) }; }`
		v, err := s.callFunction(sessionName, fn, []map[string]any{{"type": "string", "value": sel}})
		if err != nil {
			return "", err
		}
		boxVal := unwrapRemote(v)
		boxMap, ok := boxVal.(map[string]any)
		if !ok || boxMap["width"] == nil {
			return "", fmt.Errorf("element not found: %s", element)
		}
		cx := int(numF(boxMap, "x"))
		cy := int(numF(boxMap, "y"))
		cw := int(numF(boxMap, "width"))
		ch := int(numF(boxMap, "height"))
		if cw <= 0 || ch <= 0 {
			return "", fmt.Errorf("element has zero size")
		}
		// Capture full context then crop via Go image decoding.
		raw, err := s.bidiCall("browsingContext.captureScreenshot", map[string]any{"context": ctx})
		if err != nil {
			return "", err
		}
		var out struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", err
		}
		return cropPNG(out.Data, cx, cy, cw, ch)
	}
	raw, err := s.bidiCall("browsingContext.captureScreenshot", map[string]any{"context": ctx})
	if err != nil {
		return "", err
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.Data, nil
}

func numF(m map[string]any, k string) float64 {
	if v, ok := m[k].(float64); ok {
		return v
	}
	return 0
}

// evalRawForSel probes the raw result of a script.callFunction returning an
// element, for debugging node-handle serialization.
func (s *server) evalRawForSel(sessionName, sel string) (json.RawMessage, error) {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return nil, err
	}
	return s.evalRaw(ctx, map[string]any{
		"functionDeclaration": `(sel) => document.querySelector(sel)`,
		"arguments":           []map[string]any{{"type": "string", "value": sel}},
		"resultOwnership":     "root",
	})
}

// ---- newly covered BiDi capabilities (BiDi-native) ----

func (s *server) reload(sessionName string) error {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return err
	}
	_, err = s.bidiCall("browsingContext.reload", map[string]any{"context": ctx, "wait": "complete"})
	return err
}

func (s *server) contextURLOf(sessionName string) (string, error) {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return "", err
	}
	return s.contextURL(ctx)
}

func (s *server) printPDF(sessionName string) (string, error) {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return "", err
	}
	res, err := s.bidiCall("browsingContext.print", map[string]any{"context": ctx})
	if err != nil {
		return "", err
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	return out.Data, nil
}

func (s *server) activate(sessionName string) error {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return err
	}
	_, err = s.bidiCall("browsingContext.activate", map[string]any{"context": ctx})
	return err
}

func (s *server) releaseActions(sessionName string) error {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return err
	}
	_, err = s.bidiCall("input.releaseActions", map[string]any{"context": ctx})
	return err
}

func (s *server) bypassCSP(sessionName string, enabled bool) error {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return err
	}
	// browsingContext.setBypassCSP: `bypass` bool + contexts list (may be nil).
	params := map[string]any{"contexts": []string{ctx}}
	if enabled {
		params["bypass"] = true
	} else {
		params["bypass"] = nil
	}
	_, err = s.bidiCall("browsingContext.setBypassCSP", params)
	return err
}

func (s *server) addInitScript(sessionName, functionDeclaration string) error {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return err
	}
	_, err = s.bidiCall("script.addPreloadScript", map[string]any{
		"functionDeclaration": functionDeclaration,
		"contexts":            []string{ctx},
	})
	return err
}

func (s *server) emulate(sessionName, userAgent, timezone string, lat, lon *float64) error {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return err
	}
	if userAgent != "" {
		if _, err := s.bidiCall("emulation.setUserAgentOverride", map[string]any{
			"userAgent": userAgent, "contexts": []string{ctx},
		}); err != nil {
			return err
		}
	}
	if timezone != "" {
		if _, err := s.bidiCall("emulation.setTimezoneOverride", map[string]any{
			"timezone": timezone, "contexts": []string{ctx},
		}); err != nil {
			return err
		}
	}
	if lat != nil && lon != nil {
		if _, err := s.bidiCall("emulation.setGeolocationOverride", map[string]any{
			"coordinates": map[string]any{
				"latitude": *lat, "longitude": *lon, "accuracy": float64(1.0),
			},
			"contexts": []string{ctx},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) getCookies(sessionName string) ([]map[string]any, error) {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return nil, err
	}
	res, err := s.bidiCall("storage.getCookies", map[string]any{
		"partition": map[string]any{"type": "context", "context": ctx},
	})
	if err != nil {
		return nil, err
	}
	var out struct {
		Cookies []struct {
			Name  string `json:"name"`
			Value struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"value"`
			Domain   string `json:"domain"`
			Path     string `json:"path"`
			HttpOnly bool   `json:"httpOnly"`
			Secure   bool   `json:"secure"`
			SameSite string `json:"sameSite"`
			Expiry   int64  `json:"expiry"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	cookies := make([]map[string]any, 0, len(out.Cookies))
	for _, c := range out.Cookies {
		cookies = append(cookies, map[string]any{
			"name": c.Name, "value": c.Value.Value, "domain": c.Domain,
			"path": c.Path, "httpOnly": c.HttpOnly, "secure": c.Secure, "sameSite": c.SameSite,
		})
	}
	return cookies, nil
}

func (s *server) setCookie(sessionName, name, value, domain, path string) error {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return err
	}
	_, err = s.bidiCall("storage.setCookie", map[string]any{
		"cookie": map[string]any{
			"name":   name,
			"value":  map[string]any{"type": "string", "value": value},
			"domain": domain,
			"path":   path,
		},
		"partition": map[string]any{"type": "context", "context": ctx},
	})
	return err
}

func (s *server) deleteCookies(sessionName, name string) error {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return err
	}
	filter := map[string]any{}
	if name != "" {
		filter["name"] = name
	}
	params := map[string]any{
		"partition": map[string]any{"type": "context", "context": ctx},
	}
	if len(filter) > 0 {
		params["filter"] = filter
	}
	_, err = s.bidiCall("storage.deleteCookies", params)
	return err
}
