package main

import (
	"encoding/json"
	"fmt"
	"forgejo.develop.10.199.64.20.nip.io/zergx/go-shared/env"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	abep "abep.dev/sdk"
	"github.com/gorilla/websocket"
)

// WebDriver BiDi client over a single WebSocket connection. The heavy lifting
// (request/response matching, event dispatch) is minimal: BiDi messages are
// JSON with an "id" for command responses and a "method" for events.

const bidiTimeout = 30 * time.Second

type bidiResponse struct {
	ID      int             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Type    string          `json:"type,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
	Message string          `json:"message,omitempty"`
}

type pendingCall struct {
	resolve func(json.RawMessage)
	reject  func(error)
	timer   *time.Timer
}

// BiDiConn is a single WebDriver BiDi connection (one per Selenium session).
type BiDiConn struct {
	ws      *websocket.Conn
	mu      sync.Mutex
	nextID  int
	pending map[int]*pendingCall
	events  map[string]func(json.RawMessage)
	closed  bool
}

func newBiDiConn(wsURL string) (*BiDiConn, error) {
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bidi ws dial: %w", err)
	}
	c := &BiDiConn{
		ws:      ws,
		nextID:  1,
		pending: map[int]*pendingCall{},
		events:  map[string]func(json.RawMessage){},
	}
	go c.readLoop()
	return c, nil
}

func (c *BiDiConn) readLoop() {
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.failAll(fmt.Errorf("bidi read: %w", err))
			return
		}
		var msg bidiResponse
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.ID > 0 {
			c.mu.Lock()
			p := c.pending[msg.ID]
			if p != nil {
				delete(c.pending, msg.ID)
				p.timer.Stop()
			}
			c.mu.Unlock()
			if p == nil {
				continue
			}
			if msg.Type == "error" || msg.Error != "" {
				p.reject(fmt.Errorf("%s", orEmpty(msg.Message, msg.Error)))
			} else {
				p.resolve(msg.Result)
			}
			continue
		}
		if msg.Method != "" {
			c.mu.Lock()
			h := c.events[msg.Method]
			c.mu.Unlock()
			if h != nil {
				h(msg.Params)
			}
		}
	}
}

func (c *BiDiConn) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	for id, p := range c.pending {
		p.timer.Stop()
		p.reject(err)
		delete(c.pending, id)
	}
}

// Call sends a BiDi command and awaits its result.
func (c *BiDiConn) Call(method string, params map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("bidi connection closed")
	}
	id := c.nextID
	c.nextID++
	req := map[string]any{"id": id, "method": method, "params": params}
	body, err := json.Marshal(req)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	pc := &pendingCall{}
	done := make(chan json.RawMessage, 1)
	errCh := make(chan error, 1)
	pc.resolve = func(r json.RawMessage) { done <- r }
	pc.reject = func(e error) { errCh <- e }
	pc.timer = time.AfterFunc(bidiTimeout, func() {
		c.mu.Lock()
		if _, ok := c.pending[id]; ok {
			delete(c.pending, id)
			errCh <- fmt.Errorf("bidi call %s timed out", method)
		}
		c.mu.Unlock()
	})
	c.pending[id] = pc
	c.mu.Unlock()

	if err := c.ws.WriteMessage(websocket.TextMessage, body); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case r := <-done:
		return r, nil
	case e := <-errCh:
		return nil, e
	}
}

// On registers an event handler by method name (params is the full event).
func (c *BiDiConn) On(method string, h func(json.RawMessage)) {
	c.mu.Lock()
	c.events[method] = h
	c.mu.Unlock()
}

func (c *BiDiConn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.ws.Close()
}

func orEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- selenium session -------------------------------------------------------

// driverSession holds the process-wide WebDriver session: one Selenium session
// with one BiDi WebSocket; each agent session gets its own browsing context.
type driverSession struct {
	sessionID string
	conn      *BiDiConn
}

type netEntry struct {
	Method string `json:"method"`
	URL    string `json:"url"`
}

type consoleEntry struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Level string `json:"level"`
}

type promptEvent struct {
	ctx string
}

type server struct {
	ext         *abep.Extension
	seleniumURL string

	mu       sync.Mutex
	driver   *driverSession
	contexts map[string]string // agent sessionName -> browsing context id
	ctx2ses  map[string]string // browsing context id -> agent sessionName

	netMu         sync.Mutex
	netLog        map[string][]netEntry // agent sessionName -> captured requests
	subscribed    bool
	promptMu      sync.Mutex
	promptWaiters map[string][]chan promptEvent // context id -> waiters
	consoleMu     sync.Mutex
	consoles      map[string][]consoleEntry // agent sessionName -> console logs
	routes        *routeState
}

func newServer() *server {
	return &server{
		seleniumURL:   env.Or("ZERGX_SELENIUM_URL", "http://selenium.temp.svc.cluster.local:4444"),
		contexts:      map[string]string{},
		ctx2ses:       map[string]string{},
		netLog:        map[string][]netEntry{},
		promptWaiters: map[string][]chan promptEvent{},
		consoles:      map[string][]consoleEntry{},
		routes:        newRouteState(),
	}
}

func (s *server) browserName() string {
	return env.Or("ZERGX_BROWSER_NAME", "chrome")
}

func (s *server) actionTimeout() time.Duration {
	if v := os.Getenv("ZERGX_ACTION_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 30 * time.Second
}

// newWebDriverSession creates a Selenium session with the BiDi websocket url.
func (s *server) newWebDriverSession() (*driverSession, error) {
	payload := map[string]any{
		"capabilities": map[string]any{
			"alwaysMatch": map[string]any{
				"browserName":  s.browserName(),
				"webSocketUrl": true,
				// Keep dialogs open so the tool's handle_dialog can wait for
				// the event and call browsingContext.handleUserPrompt; the
				// default "dismiss and notify" auto-closes them first.
				"unhandledPromptBehavior": "ignore",
				"goog:chromeOptions": map[string]any{
					"args": []string{"--no-sandbox", "--disable-dev-shm-usage", "--headless=new"},
				},
			},
		},
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(s.seleniumURL+"/session", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("selenium session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var eb struct {
			Value struct {
				Message string `json:"message"`
			} `json:"value"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&eb)
		return nil, fmt.Errorf("selenium session create: %s %s", resp.Status, eb.Value.Message)
	}
	var out struct {
		Value struct {
			SessionID    string `json:"sessionId"`
			Capabilities struct {
				WebSocketURL string `json:"webSocketUrl"`
			} `json:"capabilities"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Value.SessionID == "" || out.Value.Capabilities.WebSocketURL == "" {
		return nil, fmt.Errorf("selenium session missing sessionId/webSocketUrl")
	}
	// Selenium may report ws://0.0.0.0 — rebase onto the service hostname.
	wsURL := out.Value.Capabilities.WebSocketURL
	if u, err := url.Parse(wsURL); err == nil && (u.Hostname() == "0.0.0.0" || u.Hostname() == "") {
		if base, err2 := url.Parse(s.seleniumURL); err2 == nil {
			u.Host = base.Host
			wsURL = u.String()
		}
	}
	conn, err := newBiDiConn(wsURL)
	if err != nil {
		return nil, err
	}
	return &driverSession{sessionID: out.Value.SessionID, conn: conn}, nil
}

// ensureDriver lazily creates the process-wide Selenium session + BiDi conn.
func (s *server) ensureDriver() (*driverSession, error) {
	s.mu.Lock()
	if s.driver != nil {
		d := s.driver
		s.mu.Unlock()
		return d, nil
	}
	s.mu.Unlock()

	d, err := s.newWebDriverSession()
	if err != nil {
		return nil, err
	}
	// Wire event subscriptions before exposing the driver, so no event is
	// missed between session creation and the first tool call.
	if err := s.subscribeEvents(d); err != nil {
		d.conn.Close()
		return nil, err
	}
	s.mu.Lock()
	s.driver = d
	s.mu.Unlock()
	return d, nil
}

// subscribeEvents registers the BiDi event handlers once per process lifetime
// (network.beforeRequestSent, browsingContext.userPromptOpened, contextCreated).
func (s *server) subscribeEvents(d *driverSession) error {
	s.netMu.Lock()
	already := s.subscribed
	s.subscribed = true
	s.netMu.Unlock()
	if already {
		return nil
	}

	d.conn.On("network.beforeRequestSent", func(raw json.RawMessage) {
		var ev struct {
			Context string `json:"context"`
			Request struct {
				Method string `json:"method"`
				URL    string `json:"url"`
			} `json:"request"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return
		}
		s.netMu.Lock()
		sid := s.ctx2ses[ev.Context]
		if sid != "" {
			s.netLog[sid] = append(s.netLog[sid], netEntry{Method: ev.Request.Method, URL: ev.Request.URL})
		}
		s.netMu.Unlock()
	})

	d.conn.On("browsingContext.userPromptOpened", func(raw json.RawMessage) {
		var ev struct {
			Context string `json:"context"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return
		}
		ctx := ev.Context
		s.promptMu.Lock()
		waiters := s.promptWaiters[ctx]
		delete(s.promptWaiters, ctx)
		s.promptMu.Unlock()
		for _, ch := range waiters {
			select {
			case ch <- promptEvent{ctx: ctx}:
			default:
			}
		}
	})

	d.conn.On("browsingContext.contextCreated", func(raw json.RawMessage) {
		var ev struct {
			Context string `json:"context"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return
		}
		// Track reverse mapping so network events can attribute to a session.
		s.mu.Lock()
		for sid, cid := range s.contexts {
			if cid == ev.Context {
				s.ctx2ses[cid] = sid
				break
			}
		}
		s.mu.Unlock()
	})

	_, err := d.conn.Call("session.subscribe", map[string]any{
		"events": []string{"network.beforeRequestSent", "browsingContext.userPromptOpened"},
	})
	return err
}

// ensureContext lazily creates the agent session's browsing context (tab).
func (s *server) ensureContext(sessionName string) (string, error) {
	s.mu.Lock()
	if id, ok := s.contexts[sessionName]; ok {
		s.mu.Unlock()
		return id, nil
	}
	s.mu.Unlock()

	d, err := s.ensureDriver()
	if err != nil {
		return "", err
	}
	res, err := d.conn.Call("browsingContext.create", map[string]any{"type": "tab"})
	if err != nil {
		return "", err
	}
	var out struct {
		Context string `json:"context"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", fmt.Errorf("browsingContext.create: %w", err)
	}
	if out.Context == "" {
		return "", fmt.Errorf("browsingContext.create returned no context")
	}
	s.mu.Lock()
	s.contexts[sessionName] = out.Context
	s.ctx2ses[out.Context] = sessionName
	s.mu.Unlock()
	return out.Context, nil
}

func (s *server) resetContext(sessionName string) {
	s.mu.Lock()
	if cid, ok := s.contexts[sessionName]; ok {
		delete(s.contexts, sessionName)
		delete(s.ctx2ses, cid)
	}
	s.mu.Unlock()
}

func (s *server) currentContext(sessionName string) (string, error) {
	return s.ensureContext(sessionName)
}

// bidiCall issues a command against the shared connection.
func (s *server) bidiCall(method string, params map[string]any) (json.RawMessage, error) {
	d, err := s.ensureDriver()
	if err != nil {
		return nil, err
	}
	return d.conn.Call(method, params)
}

// networkLog returns the captured requests for a session (subscribed at the
// process level via network.beforeRequestSent).
func (s *server) networkLog(sessionName string) []map[string]any {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	entries := s.netLog[sessionName]
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{"method": e.Method, "url": e.URL})
	}
	return out
}

// waitForPrompt waits for a user prompt (alert/confirm/prompt) on the context,
// then handles it. Mirrors Playwright's handle-dialog-on-open semantics.
func (s *server) waitForPrompt(sessionName string, accept bool, promptText string, timeout time.Duration) (bool, error) {
	ctx, err := s.ensureContext(sessionName)
	if err != nil {
		return false, err
	}
	ch := make(chan promptEvent, 1)
	s.promptMu.Lock()
	s.promptWaiters[ctx] = append(s.promptWaiters[ctx], ch)
	s.promptMu.Unlock()

	wait := func() {
		// Remove our waiter on exit.
		s.promptMu.Lock()
		ws := s.promptWaiters[ctx]
		for i, w := range ws {
			if w == ch {
				s.promptWaiters[ctx] = append(ws[:i], ws[i+1:]...)
				break
			}
		}
		s.promptMu.Unlock()
	}
	defer wait()

	select {
	case <-ch:
	case <-time.After(timeout):
		return false, nil
	}

	params := map[string]any{"context": ctx, "accept": accept}
	if promptText != "" {
		params["userText"] = promptText
	}
	_, err = s.bidiCall("browsingContext.handleUserPrompt", params)
	return err == nil, err
}

// consoleLogs returns captured console entries for a session.
func (s *server) consoleLogs(sessionName string) []map[string]any {
	s.consoleMu.Lock()
	defer s.consoleMu.Unlock()
	entries := s.consoles[sessionName]
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{"type": e.Type, "level": e.Level, "text": e.Text})
	}
	return out
}
