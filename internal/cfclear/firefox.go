package cfclear

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// Firefox-based browsers (Firefox, Zen, LibreWolf, Floorp, Waterfox) don't
// speak Chrome's DevTools protocol; they are driven through WebDriver BiDi,
// the W3C protocol Firefox ships. The browser runs with a throwaway profile
// whose preferences keep it quiet (no first-run pages, no telemetry) and
// send any download into the profile, never into Downloads.

// firefoxPrefs is the throwaway profile's user.js.
var firefoxPrefs = map[string]any{
	// A driven browser reports navigator.webdriver = true, which the check
	// flags; this preference turns that report off.
	"dom.webdriver.enabled":                                 false,
	"browser.shell.checkDefaultBrowser":                     false,
	"browser.aboutwelcome.enabled":                          false,
	"browser.startup.homepage_override.mstone":              "ignore",
	"startup.homepage_welcome_url":                          "about:blank",
	"browser.startup.page":                                  0,
	"datareporting.policy.dataSubmissionEnabled":            false,
	"datareporting.healthreport.uploadEnabled":              false,
	"toolkit.telemetry.enabled":                             false,
	"app.update.auto":                                       false,
	"app.update.enabled":                                    false,
	"browser.download.folderList":                           2,
	"browser.download.useDownloadDir":                       true,
	"browser.download.always_ask_before_handling_new_types": false,
	"browser.sessionstore.resume_from_crash":                false,
	"remote.active-protocols":                               1, // WebDriver BiDi only
}

var reBiDiListening = regexp.MustCompile(`WebDriver BiDi listening on (ws://[0-9.:\[\]a-z]+)`)

// runFirefox is Run for a Firefox-based browser.
func runFirefox(u, exe, profile string, opt Options, logf func(string, ...any)) (*Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opt.Timeout)
	defer cancel()

	downloads := filepath.Join(profile, "downloads")
	os.MkdirAll(downloads, 0o700)
	var js strings.Builder
	for k, v := range firefoxPrefs {
		b, _ := json.Marshal(v)
		fmt.Fprintf(&js, "user_pref(%q, %s);\n", k, b)
	}
	b, _ := json.Marshal(downloads)
	fmt.Fprintf(&js, "user_pref(%q, %s);\n", "browser.download.dir", b)
	if err := os.WriteFile(filepath.Join(profile, "user.js"), []byte(js.String()), 0o600); err != nil {
		return nil, err
	}

	args := []string{"--remote-debugging-port", "0", "--profile", profile, "--no-remote", "--new-instance"}
	if !opt.Headed {
		args = append(args, "--headless")
	}
	args = append(args, "about:blank")
	cmd := exec.CommandContext(ctx, exe, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", filepath.Base(exe), err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	addr := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if m := reBiDiListening.FindStringSubmatch(sc.Text()); m != nil {
				addr <- m[1]
			}
		}
	}()
	var wsURL string
	select {
	case wsURL = <-addr:
	case <-time.After(30 * time.Second):
		return nil, errors.New("the browser didn't open its WebDriver BiDi port (is it Firefox-based?)")
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	c, err := dialBiDi(ctx, wsURL+"/session")
	if err != nil {
		return nil, err
	}
	defer c.close()
	if _, err := c.call(ctx, "session.new", map[string]any{"capabilities": map[string]any{}}); err != nil {
		return nil, fmt.Errorf("WebDriver BiDi session: %w", err)
	}
	tree, err := c.call(ctx, "browsingContext.getTree", map[string]any{})
	if err != nil {
		return nil, err
	}
	var t struct {
		Contexts []struct {
			Context string `json:"context"`
		} `json:"contexts"`
	}
	if json.Unmarshal(tree, &t) != nil || len(t.Contexts) == 0 {
		return nil, errors.New("WebDriver BiDi: no browser tab")
	}
	tab := t.Contexts[0].Context
	if _, err := c.call(ctx, "browsingContext.navigate", map[string]any{"context": tab, "url": u, "wait": "none"}); err != nil {
		return nil, fmt.Errorf("the background browser could not open the page: %w", err)
	}

	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()
	for {
		var ua, title string
		raw, err := c.call(ctx, "script.evaluate", map[string]any{
			"expression":   `JSON.stringify([navigator.userAgent, document.title])`,
			"target":       map[string]any{"context": tab},
			"awaitPromise": false,
		})
		if err == nil {
			var ev struct {
				Result struct {
					Value string `json:"value"`
				} `json:"result"`
			}
			var pair []string
			if json.Unmarshal(raw, &ev) == nil && json.Unmarshal([]byte(ev.Result.Value), &pair) == nil && len(pair) == 2 {
				ua, title = pair[0], pair[1]
			}
		}
		res := &Result{UserAgent: ua, At: time.Now().UTC()}
		if raw, err := c.call(ctx, "storage.getCookies", map[string]any{"partition": map[string]any{"type": "context", "context": tab}}); err == nil {
			var cs struct {
				Cookies []struct {
					Name   string `json:"name"`
					Domain string `json:"domain"`
					Value  struct {
						Value string `json:"value"`
					} `json:"value"`
				} `json:"cookies"`
			}
			json.Unmarshal(raw, &cs)
			for _, k := range cs.Cookies {
				res.Cookies = append(res.Cookies, Cookie{Name: k.Name, Value: k.Value.Value, Domain: k.Domain})
			}
		}
		if ua != "" && res.Cookie("cf_clearance") != "" {
			logf("check passed; captured the clearance cookie")
			return res, nil
		}
		if ua != "" && title != "" && !strings.Contains(strings.ToLower(title), "just a moment") {
			logf("no check was served; the page loaded directly")
			return res, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("the check did not finish within %s (try again)", opt.Timeout)
		case <-tick.C:
		}
	}
}

// bidiConn is a minimal WebDriver BiDi client: commands with ids, replies
// matched by id, events ignored.
type bidiConn struct {
	conn    net.Conn
	mu      sync.Mutex
	next    int
	pending map[int]chan bidiReply
	done    chan struct{}
}

type bidiReply struct {
	Result  json.RawMessage `json:"result"`
	Error   string          `json:"error"`
	Message string          `json:"message"`
}

func dialBiDi(ctx context.Context, u string) (*bidiConn, error) {
	conn, _, _, err := ws.Dial(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("WebDriver BiDi: %w", err)
	}
	c := &bidiConn{conn: conn, pending: map[int]chan bidiReply{}, done: make(chan struct{})}
	go c.read()
	return c, nil
}

func (c *bidiConn) read() {
	defer close(c.done)
	for {
		b, err := wsutil.ReadServerText(c.conn)
		if err != nil {
			return
		}
		var m struct {
			ID   *int   `json:"id"`
			Type string `json:"type"`
			bidiReply
		}
		if json.Unmarshal(b, &m) != nil || m.ID == nil {
			continue // an event
		}
		c.mu.Lock()
		ch := c.pending[*m.ID]
		delete(c.pending, *m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- m.bidiReply
		}
	}
}

func (c *bidiConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.next++
	id := c.next
	ch := make(chan bidiReply, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := wsutil.WriteClientText(c.conn, b); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Error != "" {
			return nil, fmt.Errorf("%s: %s %s", method, r.Error, r.Message)
		}
		return r.Result, nil
	case <-c.done:
		return nil, errors.New("WebDriver BiDi: the browser closed the connection")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *bidiConn) close() { c.conn.Close() }

// isFirefox reports whether exe is a Firefox-based browser.
func isFirefox(exe string) bool {
	n := strings.ToLower(filepath.Base(exe))
	for _, f := range []string{"firefox", "zen", "librewolf", "floorp", "waterfox", "mercury", "icecat"} {
		if strings.Contains(n, f) {
			return true
		}
	}
	return false
}
