package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// TxAdmin drives one FXServer through txAdmin's web API, the same calls its UI makes.
type TxAdmin struct {
	t      serverTarget
	mu     sync.Mutex
	hc     *http.Client
	csrf   string
	cookie string // raw "name=value" session cookie, see sessionCookies
}

func NewTxAdmin(t serverTarget) *TxAdmin {
	return &TxAdmin{t: t, hc: &http.Client{Timeout: 20 * time.Second}}
}

// sessionCookies keeps every cookie from Set-Cookie verbatim. txAdmin names its session cookie
// "txa:sess:<id>"; a colon is not a legal cookie-name character, so net/http and cookiejar drop it
// silently and every call after login would go out unauthenticated.
func sessionCookies(h http.Header) string {
	var out []string
	for _, sc := range h.Values("Set-Cookie") {
		if nv, _, _ := strings.Cut(sc, ";"); strings.Contains(nv, "=") {
			out = append(out, strings.TrimSpace(nv))
		}
	}
	return strings.Join(out, "; ")
}

func (x *TxAdmin) login() error {
	if x.t.TxPassword == "" {
		return fmt.Errorf("%s: txAdmin password not configured", x.t.Name)
	}
	body, _ := json.Marshal(map[string]string{"username": x.t.TxUser, "password": x.t.TxPassword})
	resp, err := x.hc.Post(x.t.TxURL+"/auth/password", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		CsrfToken string `json:"csrfToken"`
		Error     string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("%s: txAdmin login: %w", x.t.Name, err)
	}
	if r.CsrfToken == "" {
		return fmt.Errorf("%s: txAdmin login refused: %s", x.t.Name, r.Error)
	}
	x.csrf, x.cookie = r.CsrfToken, sessionCookies(resp.Header)
	return nil
}

func (x *TxAdmin) post(route string, payload any) (string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if x.csrf == "" {
			if err := x.login(); err != nil {
				return "", err
			}
		}
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", x.t.TxURL+route, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-TxAdmin-CsrfToken", x.csrf)
		req.Header.Set("Cookie", x.cookie)
		resp, err := x.hc.Do(req)
		if err != nil {
			return "", err
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s := string(b)
		if resp.StatusCode == 401 || strings.Contains(s, "logout") || strings.Contains(s, "CSRF") {
			x.csrf = ""
			continue
		}
		var r struct {
			Type string `json:"type"`
			Msg  string `json:"msg"`
			Md   bool   `json:"md"`
		}
		if json.Unmarshal(b, &r) == nil && r.Type == "error" {
			return s, fmt.Errorf("%s: txAdmin %s: %s", x.t.Name, route, r.Msg)
		}
		return s, nil
	}
	return "", fmt.Errorf("%s: txAdmin session expired twice", x.t.Name)
}

func (x *TxAdmin) Command(action, param string) error {
	_, err := x.post("/fxserver/commands", map[string]string{"action": action, "parameter": param})
	return err
}

func (x *TxAdmin) Restart() error { return x.Control("restart") }

// Control starts, stops or restarts the game server the way txAdmin's buttons do.
func (x *TxAdmin) Control(action string) error {
	_, err := x.post("/fxserver/controls", map[string]string{"action": action})
	return err
}

// Login checks the configured account against txAdmin.
func (x *TxAdmin) Login() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.login()
}

func (x *TxAdmin) Broadcast(msg string) error { return x.Command("admin_broadcast", msg) }

// Online reports whether the game server itself (not txAdmin) is answering.
func (x *TxAdmin) Online() bool {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(x.t.InfoURL)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func (x *TxAdmin) WaitOnline(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if x.Online() {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

func (x *TxAdmin) LogSize() int64 {
	st, err := os.Stat(x.t.LogPath)
	if err != nil {
		return 0
	}
	return st.Size()
}

func (x *TxAdmin) LogSince(off int64) string {
	f, err := os.Open(x.t.LogPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, _ := f.Stat()
	if st.Size() < off { // rotated
		off = 0
	}
	f.Seek(off, io.SeekStart)
	b, _ := io.ReadAll(io.LimitReader(f, 4<<20))
	return string(b)
}

var (
	reAnsi     = regexp.MustCompile(`\x1b\[[0-9;]*m|\^[0-9]`)
	reErrorish = regexp.MustCompile(`(?i)SCRIPT ERROR|Couldn't start resource|Couldn't find resource|Failed to load script|Could not find dependency|error loading|Error parsing script|failed to start|attempt to (call|index|perform)|stack traceback`)
)

// ConsoleErrors pulls error lines out of a console excerpt, most relevant first.
func ConsoleErrors(log string, resources []string) []string {
	var hits, other []string
	seen := map[string]bool{}
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(reAnsi.ReplaceAllString(line, ""))
		if line == "" || !reErrorish.MatchString(line) || seen[line] {
			continue
		}
		seen[line] = true
		mine := false
		for _, r := range resources {
			if strings.Contains(line, r) {
				mine = true
				break
			}
		}
		if mine {
			hits = append(hits, line)
		} else {
			other = append(other, line)
		}
	}
	return append(hits, other...)
}

// splitKnown separates errors seen on the previous test from new ones and counts the known ones.
func splitKnown(errs, known []string) (fresh []string, nKnown int) {
	was := make(map[string]bool, len(known))
	for _, k := range known {
		was[k] = true
	}
	for _, e := range errs {
		if was[e] {
			nKnown++
		} else {
			fresh = append(fresh, e)
		}
	}
	return fresh, nKnown
}
