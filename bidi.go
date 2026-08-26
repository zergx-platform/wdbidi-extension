package main

import (
	"encoding/json"
	"fmt"
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
				params, _ := json.Marshal(msg)
				h(params)
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

type server struct {
	ext         *abep.Extension
	seleniumURL string

	mu       sync.Mutex
	driver   *driverSession
	contexts map[string]string // agent sessionName -> browsing context id
}

func newServer() *server {
	return &server{
		seleniumURL: envOr("RUCODER_SELENIUM_URL", "http://selenium.temp.svc.cluster.local:4444"),
		contexts:    map[string]string{},
	}
}

func (s *server) browserName() string {
	return envOr("RUCODER_BROWSER_NAME", "chrome")
}

func (s *server) actionTimeout() time.Duration {
	if v := os.Getenv("RUCODER_ACTION_TIMEOUT_MS"); v != "" {
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
	defer s.mu.Unlock()
	if s.driver != nil {
		return s.driver, nil
	}
	d, err := s.newWebDriverSession()
	if err != nil {
		return nil, err
	}
	s.driver = d
	return d, nil
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
	s.mu.Unlock()
	return out.Context, nil
}

func (s *server) resetContext(sessionName string) {
	s.mu.Lock()
	delete(s.contexts, sessionName)
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
