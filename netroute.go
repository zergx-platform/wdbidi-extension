package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Network interception (BiDi network.addIntercept + beforeRequestSent). Routes
// are process-wide, matched by URL pattern, and dispatch to the configured
// mode (abort / provide / continue).

type routeKind int

const (
	routeAbort routeKind = iota
	routeProvide
	routeContinue
)

type netRoute struct {
	ID      string
	Pattern string
	Kind    routeKind
	Status  int
	Body    string
	CT      string
}

type routeState struct {
	mu     sync.Mutex
	nextID int
	routes map[string]netRoute
}

func newRouteState() *routeState {
	return &routeState{routes: map[string]netRoute{}}
}

// addRoute registers an intercept and returns its id.
func (s *server) addRoute(pattern string, kind routeKind, status int, body, ct string) (string, error) {
	s.routes.mu.Lock()
	defer s.routes.mu.Unlock()

	s.routes.nextID++
	rid := fmt.Sprintf("route-%d", s.routes.nextID)

	d, err := s.ensureDriver()
	if err != nil {
		return "", err
	}
	urlPattern := map[string]any{"type": "string", "pattern": pattern}
	res, err := d.conn.Call("network.addIntercept", map[string]any{
		"phases":      []string{"beforeRequestSent"},
		"urlPatterns": []map[string]any{urlPattern},
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Intercept string `json:"intercept"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	s.routes.routes[rid] = netRoute{ID: out.Intercept, Pattern: pattern, Kind: kind, Status: status, Body: body, CT: ct}
	return rid, nil
}

// removeRoute removes a route by our route id (not the BiDi intercept id).
func (s *server) removeRoute(rid string) error {
	s.routes.mu.Lock()
	r, ok := s.routes.routes[rid]
	if ok {
		delete(s.routes.routes, rid)
	}
	s.routes.mu.Unlock()
	if !ok {
		return fmt.Errorf("route not found: %s", rid)
	}
	_, err := s.bidiCall("network.removeIntercept", map[string]any{"intercept": r.ID})
	return err
}

// findRoute returns the matching route for a URL and its BiDi intercept id.
func (s *server) findRoute(url string) (netRoute, string, bool) {
	s.routes.mu.Lock()
	defer s.routes.mu.Unlock()
	// The event carries the intercept id that matched; match by BiDi id.
	for _, r := range s.routes.routes {
		if matchPattern(r.Pattern, url) {
			return r, r.ID, true
		}
	}
	return netRoute{}, "", false
}

// matchPattern does a simple substring/glob match on the URL.
func matchPattern(pattern, url string) bool {
	if pattern == "" {
		return false
	}
	if strings.ContainsAny(pattern, "*?") {
		// Simple glob: * matches any sequence.
		return globMatch(pattern, url)
	}
	return strings.Contains(url, pattern)
}

func globMatch(pattern, s string) bool {
	// Convert * to a regex-free recursive matcher.
	p, t := 0, 0
	star, mark := -1, 0
	for t < len(s) {
		if p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[t]) {
			p++
			t++
		} else if p < len(pattern) && pattern[p] == '*' {
			star = p
			mark = t
			p++
		} else if star != -1 {
			p = star + 1
			mark++
			t = mark
		} else {
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
