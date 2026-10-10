package cfclear

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// TestMain lets the test binary act as a fake Firefox: it serves a minimal
// WebDriver BiDi endpoint whose page shows the check first and sets the
// clearance cookie after a few polls, the way a real check passes.
func TestMain(m *testing.M) {
	if os.Getenv("PPGMODS_FAKE_FIREFOX") == "1" {
		fakeFirefox()
		return
	}
	os.Exit(m.Run())
}

func fakeFirefox() {
	// The profile must carry the preference that hides navigator.webdriver.
	for i, a := range os.Args {
		if a == "--profile" && i+1 < len(os.Args) {
			b, _ := os.ReadFile(filepath.Join(os.Args[i+1], "user.js"))
			if !strings.Contains(string(b), `"dom.webdriver.enabled", false`) {
				fmt.Fprintln(os.Stderr, "missing dom.webdriver.enabled")
				os.Exit(2)
			}
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "WebDriver BiDi listening on ws://%s\n", ln.Addr())
	polls := 0
	navigated := ""
	http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			b, err := wsutil.ReadClientText(conn)
			if err != nil {
				return
			}
			var req struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			json.Unmarshal(b, &req)
			var result any = map[string]any{}
			switch req.Method {
			case "browsingContext.getTree":
				result = map[string]any{"contexts": []any{map[string]any{"context": "tab1"}}}
			case "browsingContext.navigate":
				var p struct{ URL string }
				json.Unmarshal(req.Params, &p)
				navigated = p.URL
			case "script.evaluate":
				if strings.Contains(string(req.Params), "outerHTML") {
					result = map[string]any{"type": "success", "result": map[string]any{"type": "string", "value": "<html>Skymods</html>"}}
					break
				}
				polls++
				title := "Just a moment..."
				if polls > 2 {
					title = "Skymods"
				}
				v, _ := json.Marshal(map[string]any{"ua": "Mozilla/5.0 (X11; Linux x86_64; rv:157.0) Gecko/20100101 Firefox/157.0", "title": title, "ready": "complete", "check": polls <= 2})
				result = map[string]any{"type": "success", "result": map[string]any{"type": "string", "value": string(v)}}
			case "storage.getCookies":
				var cs []any
				if polls > 2 && navigated != "" {
					cs = append(cs, map[string]any{"name": "cf_clearance", "domain": ".smods.ru", "value": map[string]any{"type": "string", "value": "ok123"}})
				}
				result = map[string]any{"cookies": cs}
			}
			out, _ := json.Marshal(map[string]any{"type": "success", "id": req.ID, "result": result})
			wsutil.WriteServerText(conn, out)
			// An event in between replies must be ignored.
			wsutil.WriteServerText(conn, []byte(`{"type":"event","method":"log.entryAdded","params":{}}`))
		}
	}))
}

// A Firefox-based browser passes the check through WebDriver BiDi: the
// clearance cookie and the browser's User-Agent come back.
func TestRunFirefox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake browser is the test binary under a Firefox-like name")
	}
	exe, _ := os.Executable()
	ff := filepath.Join(t.TempDir(), "firefox")
	if err := os.Symlink(exe, ff); err != nil {
		t.Skip(err)
	}
	t.Setenv("PPGMODS_FAKE_FIREFOX", "1")
	res, err := Run("https://catalogue.smods.ru/", Options{Browser: ff, Timeout: 20 * time.Second, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cookie("cf_clearance") != "ok123" || !strings.Contains(res.UserAgent, "Firefox/157") || res.HTML != "<html>Skymods</html>" {
		t.Fatalf("result: %+v", res)
	}
}
