// Package gui is ppgmods' graphical interface. Builds with cgo show a native
// window (Fyne, native.go); the window calls the API below in-process. Builds
// without cgo, or with --web, serve the same API as a small web app shown in
// a chromeless Edge/Chrome window. The API also listens on 127.0.0.1 so a
// second launch or a Nexus link can reach the running window; every request
// must carry a random per-session token, and the Host header is checked, so
// other websites cannot drive it.
package gui

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/DogeKingC/SWG/internal/app"
	"github.com/DogeKingC/SWG/internal/desktop"
	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/selfupdate"
	"github.com/DogeKingC/SWG/internal/sources"
	"github.com/DogeKingC/SWG/internal/workshop"
)

//go:embed web
var webFS embed.FS

type Options struct {
	Port     int
	NoWindow bool
	Web      bool // the browser window even when the native one is built in
	Version  string
}

type server struct {
	opt     app.Options
	version string
	token   string
	host    string
	quit    chan struct{}

	logMu sync.Mutex
	logs  []string
	base  int // index of logs[0] in the overall log

	jobMu sync.Mutex
	job   *job

	relMu   sync.Mutex
	rel     *selfupdate.Release
	relErr  string
	relTime time.Time

	pingMu   sync.Mutex
	lastPing time.Time

	logFile  *os.File
	relaunch string // executable to start after shutting down

	handler  http.Handler  // the API, called in-process by the native window
	show     chan struct{} // native window: bring it to the front
	offerMu  sync.Mutex
	nxmOffer *nxmOffer // a Nexus "Mod Manager Download" link waiting for the person to confirm
	quitOnce sync.Once
}

type job struct {
	ID         int               `json:"id"`
	Name       string            `json:"name"`
	Running    bool              `json:"running"`
	OK         bool              `json:"ok"`
	Error      string            `json:"error,omitempty"`
	Reasons    []string          `json:"reasons,omitempty"`
	Summary    *app.Summary      `json:"summary,omitempty"`
	Problems   []manager.Problem `json:"problems,omitempty"`
	Data       map[string]string `json:"data,omitempty"`
	Started    time.Time         `json:"started"`
	Finished   time.Time         `json:"finished,omitempty"`
	Retry      map[string]any    `json:"retry,omitempty"` // request to repeat with an override
	Findings   []string          `json:"findings,omitempty"`
	Browser    *app.NeedsBrowser `json:"browser,omitempty"`
	Risk       bool              `json:"risk,omitempty"`       // the retry accepts CRITICAL findings: needs the typed phrase
	Background bool              `json:"background,omitempty"` // started by ppgmods itself (folder watching), not shown as "last task"
	opts       map[string]bool
	logStart   int
}

const maxLogLines = 5000

func (s *server) logf(format string, a ...any) {
	line := time.Now().Format("15:04:05 ") + fmt.Sprintf(format, a...)
	s.logMu.Lock()
	s.logs = append(s.logs, line)
	if len(s.logs) > maxLogLines {
		drop := len(s.logs) - maxLogLines
		s.logs = s.logs[drop:]
		s.base += drop
	}
	if s.logFile != nil {
		fmt.Fprintln(s.logFile, time.Now().Format("2006-01-02 ")+line)
	}
	s.logMu.Unlock()
	fmt.Println(line)
}

func (s *server) stop(relaunch string) {
	s.quitOnce.Do(func() {
		s.relaunch = relaunch
		close(s.quit)
	})
}

// Run starts the GUI and blocks until the window is closed (the page stops
// sending heartbeats), Quit is pressed, or the process is interrupted. If
// another ppgmods window is already running, it is brought up instead.
func Run(opt app.Options, g Options) error {
	selfupdate.Cleanup()
	app.PruneCache(14 * 24 * time.Hour)
	if st, err := loadSettings(); err == nil {
		st.apply(&opt)
	}
	// Restarted by an update (or "switch to the installed copy"): take over
	// the old process's address and session token, so the window that is
	// already open reloads into this version instead of a second window
	// opening next to it.
	resumePort, resumeToken := takeResume()
	startNXM := os.Getenv(nxmEnv) // launched by a Nexus link with no window open
	os.Unsetenv(nxmEnv)
	native := nativeUI != nil && !g.NoWindow && !g.Web
	if !g.NoWindow && g.Port == 0 && resumeToken == "" {
		if url := runningInstance(); url != "" {
			fmt.Println("ppgmods is already running; opening its window")
			if showRunning() {
				return nil
			}
			return openWindow(url)
		}
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	token := hex.EncodeToString(b)
	var ln net.Listener
	var err error
	resumed := false
	if resumeToken != "" {
		// The old process may still be closing its listener.
		for i := 0; i < 40 && ln == nil; i++ {
			if ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", resumePort)); err != nil {
				time.Sleep(250 * time.Millisecond)
			}
		}
		if ln != nil {
			token, resumed = resumeToken, true
		}
	}
	if ln == nil {
		if ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", g.Port)); err != nil {
			return err
		}
	}
	s := &server{opt: opt, version: g.Version, token: token, host: ln.Addr().String(), quit: make(chan struct{})}
	if native {
		s.show = make(chan struct{}, 1)
	}
	if d, err := manager.ConfigDir(); err == nil {
		lp := filepath.Join(d, "ppgmods.log")
		if st, err := os.Stat(lp); err == nil && st.Size() > 2<<20 {
			os.Rename(lp, lp+".1")
		}
		s.logFile, _ = os.OpenFile(lp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	}
	if startNXM != "" {
		s.offerNXM(startNXM)
	}
	s.handler = s.routes()
	srv := &http.Server{Handler: s.handler, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)

	url := fmt.Sprintf("http://%s/#%s", s.host, s.token)
	writeInstance(s.host, s.token)
	defer removeInstance(s.token)
	s.logf("ppgmods %s started (window address http://%s)", g.Version, s.host)
	if native {
		// The window runs on the main goroutine below.
	} else if resumed {
		s.logf("restarted; the open window reloads into this version")
		go s.watchdog()
	} else if !g.NoWindow {
		if err := openWindow(url); err != nil {
			s.logf("could not open a window (%v); open this address in your browser: %s", err, url)
		}
		go s.watchdog()
	} else {
		fmt.Println(url)
	}
	// Track mods already in the game folders (installed by hand or from the
	// sites) so they show as installed.
	// It runs as a queued task like any other, so it can never interleave
	// with an install or a removal: each loads state.json, changes it and
	// saves it, and two at once would undo each other (a removed mod came
	// back). The slow part, True Workshop's catalogue (its free server can
	// take a minute to wake up), is fetched first, outside the queue.
	go func() {
		sources.TWAll()
		for i := 0; i < 120; i++ {
			if _, err := s.start(actionReq{Action: "find-installed", background: true}); err == nil {
				return
			}
			select { // a task is running; try again shortly
			case <-time.After(5 * time.Second):
			case <-s.quit:
				return
			}
		}
	}()
	go s.watchFolders()
	go func() {
		for {
			s.checkRelease(true)
			select {
			case <-time.After(6 * time.Hour):
			case <-s.quit:
				return
			}
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-s.quit:
		case <-sig:
			s.stop("")
		}
	}()
	if native {
		if err := nativeUI(s); err != nil {
			s.logf("the window failed: %v", err)
		}
		s.stop("") // the window was closed (no-op after Quit or an update)
	}
	<-s.quit
	srv.Close()
	if s.logFile != nil {
		s.logFile.Close()
	}
	removeInstance(s.token)
	if s.relaunch != "" {
		// Hand the address and token to the new process through its
		// environment (not its command line, which other users can read).
		_, port, _ := net.SplitHostPort(s.host)
		os.Setenv(resumeEnv, port+":"+s.token)
		return desktop.Launch(s.relaunch, "gui")
	}
	return nil
}

const resumeEnv = "PPGMODS_RESUME"

// nxmEnv carries a Nexus link to a window started for it.
const nxmEnv = "PPGMODS_NXM"

// OpenNXM handles `ppgmods nxm <link>` (what the browser runs for a Nexus
// "Mod Manager Download" link): hand it to the open window, or start one
// that shows it.
func OpenNXM(opt app.Options, g Options, raw string) error {
	for i := 0; i < 3; i++ {
		if ForwardNXM(raw) {
			return nil
		}
		if runningInstance() == "" {
			break
		}
		time.Sleep(time.Second)
	}
	os.Setenv(nxmEnv, raw)
	return Run(opt, g)
}

// takeResume reads and clears the address handed over by a restart.
func takeResume() (int, string) {
	v := os.Getenv(resumeEnv)
	os.Unsetenv(resumeEnv)
	port, token, ok := strings.Cut(v, ":")
	var p int
	if !ok || len(token) != 48 || strings.Trim(token, "0123456789abcdef") != "" {
		return 0, ""
	}
	if _, err := fmt.Sscan(port, &p); err != nil || p <= 0 || p > 65535 {
		return 0, ""
	}
	return p, token
}

// watchFolders follows the Mods and Contraptions folders while ppgmods runs:
// a folder added by hand (or by another tool) is identified and tracked
// within seconds, and a deleted one shows as missing on the next refresh of
// the window. Listing two folders every few seconds costs nothing.
func (s *server) watchFolders() {
	last := ""
	pending := false
	for {
		select {
		case <-s.quit:
			return
		case <-time.After(4 * time.Second):
		}
		p := s.newApp(nil).Paths()
		if p.Mods == "" {
			continue
		}
		var names []string
		untracked := false
		st, err := manager.LoadState()
		for _, d := range []struct{ kind, dir string }{{manager.KindMod, p.Mods}, {manager.KindContraption, p.Contraptions}} {
			ents, _ := os.ReadDir(d.dir)
			for _, e := range ents {
				if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				name := d.kind + "/" + e.Name()
				if err == nil && st.OwnerOf(d.kind, e.Name()) == nil {
					untracked = true
					// A folder still being copied in has no mod.json/.jaap
					// yet: look again once it does.
					if looksComplete(filepath.Join(d.dir, e.Name())) {
						name += "*"
					}
				}
				names = append(names, name)
			}
		}
		sig := strings.Join(names, "\x00")
		if sig != last {
			pending = untracked
			last = sig
		}
		if pending && untracked {
			sources.TWAll() // the slow part, outside the task queue (cached)
			// Queued like any task; if one is running, try again next tick.
			if _, err := s.start(actionReq{Action: "find-installed", background: true}); err == nil {
				pending = false
			}
		}
	}
}

// nxmOffer is a Nexus link handed over by the browser, shown in the window
// for the person to confirm: any website can open an nxm:// link, so a link
// alone never installs anything.
type nxmOffer struct {
	URL   string    `json:"url"`
	ModID int       `json:"mod_id"`
	Name  string    `json:"name"`
	Page  string    `json:"page"`
	At    time.Time `json:"at"`
}

// offerNXM validates a link and shows it in the window.
func (s *server) offerNXM(raw string) error {
	link, err := sources.ParseNXM(raw)
	if errors.Is(err, sources.ErrNXMOtherGame) {
		s.logf("a Nexus Mods link for another game arrived; ppgmods only handles People Playground. Turn off \"Handle Mod Manager Download links\" in Settings to give them back to your other mod manager.")
		return err
	}
	if err != nil {
		return err
	}
	o := &nxmOffer{URL: raw, ModID: link.ModID, Name: fmt.Sprintf("Nexus Mods mod %d", link.ModID),
		Page: fmt.Sprintf("https://www.nexusmods.com/peopleplayground/mods/%d", link.ModID), At: time.Now()}
	if it, err := sources.NXGet(link.ModID); err == nil {
		o.Name = it.Name
	}
	s.offerMu.Lock()
	s.nxmOffer = o
	s.offerMu.Unlock()
	s.logf("Nexus Mods sent %q; confirm it in the window", o.Name)
	return nil
}

type nexusView struct {
	Linked  bool   `json:"linked"`
	User    string `json:"user,omitempty"`
	Premium bool   `json:"premium,omitempty"`
	Handler bool   `json:"handler,omitempty"`
}

func nexusStatus() nexusView {
	a := app.LoadNexus()
	if a == nil {
		return nexusView{}
	}
	return nexusView{Linked: true, User: a.User, Premium: a.Premium, Handler: a.Handler}
}

// handleNexus links (POST {key, handler}), changes the link handler (POST
// {handler}), or unlinks (DELETE) the Nexus Mods account. The key is never
// sent back.
func (s *server) handleNexus(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, nexusStatus())
		return
	case http.MethodDelete:
		if a := app.LoadNexus(); a != nil && a.Handler {
			desktop.UnregisterNXM(a.PrevNXM)
		}
		if err := app.UnlinkNexus(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.logf("Nexus Mods account unlinked")
		writeJSON(w, nexusStatus())
		return
	case http.MethodPost:
	default:
		http.Error(w, "GET, POST or DELETE", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Key     string `json:"key"`
		Handler *bool  `json:"handler"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	acct := app.LoadNexus()
	if req.Key != "" {
		a, err := app.LinkNexus(req.Key)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		acct = a
		s.logf("linked Nexus Mods account %s (%s)", a.User, map[bool]string{true: "premium", false: "free"}[a.Premium])
	}
	if acct == nil {
		http.Error(w, "link an account first", http.StatusBadRequest)
		return
	}
	if req.Handler != nil && *req.Handler != acct.Handler {
		if *req.Handler {
			exe := desktop.InstallPath()
			if !desktop.InstalledCopyExists() {
				exe = desktop.Executable() // not installed: links go to this copy
			}
			prev, err := desktop.RegisterNXM(exe)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				writeJSON(w, map[string]string{"error": err.Error()})
				return
			}
			acct.Handler, acct.PrevNXM = true, prev
			s.logf("ppgmods now opens Nexus Mods \"Mod Manager Download\" links")
		} else {
			desktop.UnregisterNXM(acct.PrevNXM)
			acct.Handler, acct.PrevNXM = false, ""
			s.logf("Nexus Mods links handed back to the previous handler")
		}
		if err := acct.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, nexusStatus())
}

// ForwardNXM hands a Nexus link to a running ppgmods window. It reports
// whether one took it.
func ForwardNXM(raw string) bool {
	var in instance
	b, err := os.ReadFile(instancePath())
	if err != nil || json.Unmarshal(b, &in) != nil || in.Host == "" {
		return false
	}
	body, _ := json.Marshal(map[string]string{"url": raw})
	req, _ := http.NewRequest("POST", "http://"+in.Host+"/api/nxm-offer", bytes.NewReader(body))
	req.Header.Set("X-Token", in.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// looksComplete reports whether a folder holds a mod.json or a .jaap file
// (at the top or one level down).
func looksComplete(dir string) bool {
	for _, pat := range []string{"mod.json", "*.jaap", "*/mod.json", "*/*.jaap"} {
		if m, _ := filepath.Glob(filepath.Join(dir, pat)); len(m) > 0 {
			return true
		}
	}
	return false
}

// watchdog quits once the window has been closed: the page sends a
// heartbeat every 20 s (browsers slow background timers to once a minute,
// hence the generous limit). Nothing runs in the background after the
// window is gone, except a task still in progress.
func (s *server) watchdog() {
	started := time.Now()
	for {
		select {
		case <-s.quit:
			return
		case <-time.After(15 * time.Second):
		}
		s.pingMu.Lock()
		last := s.lastPing
		s.pingMu.Unlock()
		s.jobMu.Lock()
		busy := s.job != nil && s.job.Running
		s.jobMu.Unlock()
		if busy {
			continue
		}
		if last.IsZero() && time.Since(started) > 10*time.Minute || !last.IsZero() && time.Since(last) > 3*time.Minute {
			s.logf("window closed; quitting")
			s.stop("")
			return
		}
	}
}

type instance struct {
	Host  string `json:"host"`
	Token string `json:"token"`
}

func instancePath() string {
	d, err := manager.ConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "instance.json")
}

func writeInstance(host, token string) {
	if p := instancePath(); p != "" {
		b, _ := json.Marshal(instance{host, token})
		os.WriteFile(p, b, 0o600)
	}
}

func removeInstance(token string) {
	p := instancePath()
	var in instance
	if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &in) == nil && in.Token == token {
		os.Remove(p)
	}
}

// showRunning asks a running native window to come to the front.
func showRunning() bool {
	var in instance
	b, err := os.ReadFile(instancePath())
	if err != nil || json.Unmarshal(b, &in) != nil || in.Host == "" {
		return false
	}
	req, _ := http.NewRequest("POST", "http://"+in.Host+"/api/show", strings.NewReader("{}"))
	req.Header.Set("X-Token", in.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// runningInstance returns the window address of a live ppgmods, or "".
func runningInstance() string {
	var in instance
	b, err := os.ReadFile(instancePath())
	if err != nil || json.Unmarshal(b, &in) != nil || in.Host == "" {
		return ""
	}
	req, _ := http.NewRequest("GET", "http://"+in.Host+"/api/ping", nil)
	req.Header.Set("X-Token", in.Token)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	return fmt.Sprintf("http://%s/#%s", in.Host, in.Token)
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	if s.show != nil {
		// The native window is in use. A browser window left over from an
		// update of an older version reloads into this note.
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>PPG Mod Manager</title>`+
				`<body style="font:16px system-ui;background:#161616;color:#eee;display:grid;place-items:center;height:90vh">`+
				`<p>PPG Mod Manager now has its own window. You can close this one.</p>`)
		})
	} else {
		mux.Handle("/", http.FileServer(http.FS(static)))
	}
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/log", s.handleLog)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/job", s.handleJob)
	mux.HandleFunc("/api/action", s.handleAction)
	mux.HandleFunc("/api/upload", s.handleUpload)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/release", s.handleRelease)
	mux.HandleFunc("/api/open", s.handleOpen)
	mux.HandleFunc("/api/details", s.handleDetails)
	mux.HandleFunc("/api/preview", s.handlePreview)
	mux.HandleFunc("/api/thumb", s.handleThumb)
	mux.HandleFunc("/api/nexus", s.handleNexus)
	mux.HandleFunc("/api/nxm-offer", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req) != nil {
			http.Error(w, "POST {url}", http.StatusBadRequest)
			return
		}
		if err := s.offerNXM(req.URL); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/nxm-dismiss", func(w http.ResponseWriter, r *http.Request) {
		s.offerMu.Lock()
		s.nxmOffer = nil
		s.offerMu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		if s.show == nil || r.Method != http.MethodPost {
			http.NotFound(w, r) // a browser window: the caller opens it
			return
		}
		select {
		case s.show <- struct{}{}:
		default:
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true})
		go func() { time.Sleep(200 * time.Millisecond); s.stop("") }()
	})
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		s.pingMu.Lock()
		s.lastPing = time.Now()
		s.pingMu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
	})
	return s.guard(mux)
}

// guard rejects requests whose Host is not our loopback address (DNS
// rebinding) and API requests without the session token (CSRF from other
// sites, other local users).
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.host && r.Host != strings.Replace(s.host, "127.0.0.1", "localhost", 1) {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			tok := r.Header.Get("X-Token")
			if r.URL.Path == "/api/thumb" && tok == "" {
				tok = r.URL.Query().Get("t") // <img> tags cannot send headers
			}
			if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
				http.Error(w, "missing or bad token", http.StatusForbidden)
				return
			}
			if r.Method == http.MethodPost && r.URL.Path != "/api/upload" && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "json only", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func (s *server) newApp(over map[string]bool) *app.App {
	o := s.opt
	if over["allow_high"] {
		o.Policy.AllowHigh = true
	}
	if over["accept_risk"] {
		o.Policy.AcceptRisk = true
	}
	if over["skip_cooldown"] {
		o.Policy.Cooldown = 0
	}
	if over["allow_new_findings"] {
		o.Policy.AllowNewFindings = true
	}
	o.BrowserFallback = over["browser"]
	return &app.App{Opt: o, Logf: s.logf}
}

type installedView struct {
	*manager.Installed
	Missing bool `json:"missing,omitempty"` // a folder is gone from the game folder
	// Withdrawn: the Open Workshop took this mod down (reason), e.g. a
	// problem found after it was published.
	Withdrawn string `json:"withdrawn,omitempty"`
	Kind      string `json:"kind"`      // source label
	ItemKind  string `json:"item_kind"` // mod or contraption
	Link      string `json:"link"`
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	a := s.newApp(nil)
	paths := a.Paths()
	var mods []installedView
	if st, err := manager.LoadState(); err == nil {
		mg := &manager.Manager{State: st, ModsDir: paths.Mods, ContraptionsDir: paths.Contraptions}
		for _, m := range st.Sorted() {
			v := installedView{Installed: m, Link: m.Source, ItemKind: m.Kind}
			v.Missing = paths.Mods != "" && mg.MissingFolders(m)
			if ix := workshop.CachedIndex(); ix != nil && strings.HasPrefix(m.Mirror, "openworkshop:") {
				if e := ix.Find(strings.TrimPrefix(m.Mirror, "openworkshop:")); e != nil && e.Withdrawn {
					v.Withdrawn = e.WithdrawnReason
				}
			}
			switch {
			case strings.HasPrefix(m.Key, "gb:"):
				v.Kind = "GameBanana"
			case strings.HasPrefix(m.Key, "sky:"):
				v.Kind = "Steam Workshop"
			case strings.HasPrefix(m.Key, "tw:"):
				v.Kind = "True Workshop"
			case strings.HasPrefix(m.Key, "nx:"):
				v.Kind = "Nexus Mods"
			case strings.HasPrefix(m.Key, "ow:"):
				v.Kind = "Open Workshop"
			default:
				v.Kind = "Local file"
				v.Link = ""
			}
			mods = append(mods, v)
		}
	}
	s.jobMu.Lock()
	j := s.job
	s.jobMu.Unlock()
	s.offerMu.Lock()
	offer := s.nxmOffer
	s.offerMu.Unlock()
	writeJSON(w, map[string]any{
		"nxm_offer":  offer,
		"nexus":      nexusStatus(),
		"version":    s.version,
		"paths":      paths,
		"installed":  mods,
		"job":        j,
		"settings":   settingsFrom(s.opt),
		"cutoff":     manager.WormCutoff.Format("2006-01-02"),
		"lastBackup": lastBackup(paths.Data),
		"desktop": map[string]any{
			"installed":    desktop.IsInstalled(),
			"copy_exists":  desktop.InstalledCopyExists(),
			"install_path": desktop.InstallPath(),
			"running_from": desktop.Executable(),
			"platform":     runtime.GOOS,
		},
	})
}

func lastBackup(dataDir string) string {
	// Newest by time, not name: older backups were named workshop-backup-20260921.
	ms, _ := filepath.Glob(filepath.Join(dataDir, "workshop-backup-*"))
	best, bestT := "", time.Time{}
	for _, p := range ms {
		if st, err := os.Stat(p); err == nil && st.IsDir() && st.ModTime().After(bestT) {
			best, bestT = p, st.ModTime()
		}
	}
	return best
}

func (s *server) handleLog(w http.ResponseWriter, r *http.Request) {
	var since int
	fmt.Sscan(r.URL.Query().Get("since"), &since)
	s.logMu.Lock()
	start := since - s.base
	if start < 0 {
		start = 0
	}
	if start > len(s.logs) {
		start = len(s.logs)
	}
	lines := append([]string{}, s.logs[start:]...)
	next := s.base + len(s.logs)
	s.logMu.Unlock()
	writeJSON(w, map[string]any{"lines": lines, "next": next})
}

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	page := 1
	fmt.Sscan(r.URL.Query().Get("page"), &page)
	if page < 1 {
		page = 1
	}
	parts := map[string]bool{"gb": true, "tw": true, "ws": true}
	if p := r.URL.Query().Get("part"); p != "" {
		parts = map[string]bool{p: true}
	}
	qs := r.URL.Query()
	writeJSON(w, app.SearchParts(qs.Get("q"), page, parts, app.SearchOpts{Kind: qs.Get("kind"), Sort: qs.Get("sort"), Period: qs.Get("period")}))
}

func (s *server) handleJob(w http.ResponseWriter, r *http.Request) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	writeJSON(w, s.job)
}

type actionReq struct {
	Action     string          `json:"action"`
	Refs       []string        `json:"refs,omitempty"`
	Key        string          `json:"key,omitempty"`
	Path       string          `json:"path,omitempty"`
	WorkshopID string          `json:"workshop_id,omitempty"`
	Apply      bool            `json:"apply,omitempty"`
	Pinned     bool            `json:"pinned,omitempty"`
	Override   map[string]bool `json:"override,omitempty"`
	Mirror     string          `json:"mirror,omitempty"`
	Confirm    string          `json:"confirm,omitempty"` // RiskPhrase, typed by the person, with override accept_risk
	background bool            // started by ppgmods itself; never set from a request
}

// RiskPhrase must be typed to install a mod with CRITICAL findings.
const RiskPhrase = "I accept the risk"

var errBusy = errors.New("another task is still running")

func (s *server) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req actionReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Override["accept_risk"] {
		// One mod at a time, and only with the phrase typed in the window.
		if req.Confirm != RiskPhrase || len(req.Refs) > 1 || (req.Action != "install" && req.Action != "import") {
			http.Error(w, "accepting the risk needs the confirmation phrase and a single mod", http.StatusBadRequest)
			return
		}
	}
	j, err := s.start(req)
	if err != nil {
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, j)
}

var jobSeq int

// start runs an action in the background; only one runs at a time.
func (s *server) start(req actionReq) (*job, error) {
	s.jobMu.Lock()
	if s.job != nil && s.job.Running {
		s.jobMu.Unlock()
		return nil, errBusy
	}
	jobSeq++
	s.logMu.Lock()
	logStart := s.base + len(s.logs)
	s.logMu.Unlock()
	j := &job{ID: jobSeq, Name: req.Action, Running: true, Started: time.Now(), opts: req.Override, logStart: logStart, Background: req.background}
	s.job = j
	s.jobMu.Unlock()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				s.finish(j, fmt.Errorf("internal error: %v", p))
			}
		}()
		s.finish(j, s.do(j, req))
	}()
	return j, nil
}

// findingLines returns the scan findings this job logged.
func (s *server) findingLines(j *job) []string {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	start := j.logStart - s.base
	if start < 0 {
		start = 0
	}
	var out []string
	for _, l := range s.logs[min(start, len(s.logs)):] {
		if strings.Contains(l, "[CRITICAL ") || strings.Contains(l, "[HIGH ") || strings.Contains(l, "[MEDIUM ") {
			out = append(out, strings.TrimSpace(l[min(9, len(l)):]))
		}
		if len(out) == 30 {
			break
		}
	}
	return out
}

func (s *server) finish(j *job, err error) {
	findings := s.findingLines(j)
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	j.Running, j.Finished = false, time.Now()
	j.Findings = findings
	var rej *manager.Rejection
	switch {
	case err == nil:
		j.OK = true
	case errors.As(err, &rej):
		j.Error, j.Reasons = "refused", rej.Reasons
	default:
		j.Error = err.Error()
	}
	if err != nil {
		s.logf("%s: %v", j.Name, err)
	} else {
		s.logf("%s: done", j.Name)
	}
}

func (s *server) do(j *job, req actionReq) error {
	a := s.newApp(req.Override)
	switch req.Action {
	case "self-update":
		rel, err := selfupdate.Latest()
		if err != nil {
			return err
		}
		if !rel.Newer {
			s.logf("ppgmods %s is the latest version", s.version)
			return nil
		}
		if err := selfupdate.Apply(rel, s.logf); err != nil {
			return err
		}
		j.Data = map[string]string{"restart": "1", "version": rel.Version}
		return nil
	case "backup":
		dest, err := a.Backup()
		j.Data = map[string]string{"dest": dest}
		return err
	case "install-app":
		exe, err := desktop.Install(desktop.Options{DesktopShortcut: req.Apply, Version: s.version}, s.logf)
		j.Data = map[string]string{"exe": exe}
		return err
	case "uninstall-app":
		if err := desktop.Uninstall(s.logf); err != nil {
			return err
		}
		go func() { time.Sleep(time.Second); s.stop("") }()
		return nil
	case "restart":
		exe := desktop.StartedAs()
		if req.Key == "installed" {
			exe = desktop.InstallPath()
		}
		go func() { time.Sleep(500 * time.Millisecond); s.stop(exe) }()
		return nil
	}

	m, err := a.Manager(true)
	if err != nil {
		return err
	}
	a.Opt.Mirror = req.Mirror
	switch req.Action {
	case "install":
		if len(req.Refs) == 0 {
			return errors.New("nothing selected")
		}
		j.Data = map[string]string{"mods_dir": m.ModsDir, "contraptions_dir": m.ContraptionsDir}
		if len(req.Refs) == 1 {
			err := a.Install(m, req.Refs[0])
			s.offerRetry(j, err, req)
			if inst := m.State.Find(app.NormalizeRef(req.Refs[0])); err == nil && inst != nil {
				j.Data["kind"] = inst.Kind
			}
			return err
		}
		sum := a.InstallMany(m, req.Refs)
		j.Summary = &sum
		if sum.Refused+sum.Failed > 0 {
			return fmt.Errorf("%d of %d not installed; see the log for reasons", sum.Refused+sum.Failed, len(req.Refs))
		}
		return nil
	case "import":
		a.Opt.WorkshopID = strings.TrimSpace(req.WorkshopID)
		err := a.Import(m, req.Path)
		s.offerRetry(j, err, req)
		if strings.HasPrefix(req.Path, uploadDir()) {
			os.Remove(req.Path)
		}
		return err
	case "update":
		a.Opt.Yes = req.Apply
		sum := a.Update(m)
		j.Summary = &sum
		return nil
	case "nxm":
		// Only from the window, after the person confirmed the offer.
		s.offerMu.Lock()
		offer := s.nxmOffer
		s.nxmOffer = nil
		s.offerMu.Unlock()
		if offer == nil || offer.URL != req.Path {
			return fmt.Errorf("that Nexus link is no longer waiting; click Mod Manager Download again")
		}
		return a.InstallNXM(m, offer.URL)
	case "ow-share":
		dl := a.Opt.Downloads
		if dl == "" {
			dl = app.DownloadFolder()
		}
		if dl == "" {
			d, _ := manager.ConfigDir()
			dl = d
		}
		sh, err := a.PrepareShare(m, req.Key, filepath.Join(dl, "ppgmods-share"))
		if err != nil {
			return err
		}
		j.Data = map[string]string{"zip": sh.Zip, "sha256": sh.SHA256, "slug": sh.Slug, "issue_url": sh.IssueURL}
		s.logf("packed %s for the Open Workshop: %s", req.Key, sh.Zip)
		return nil
	case "repair":
		sum := a.Repair(m, req.Refs)
		j.Summary = &sum
		return nil
	case "forget":
		keys := req.Refs
		if req.Key != "" {
			keys = append(keys, req.Key)
		}
		for _, k := range keys {
			if err := m.Forget(k); err != nil {
				return err
			}
		}
		return nil
	case "restore":
		sum, err := a.Restore(m, req.Path)
		j.Summary = &sum
		return err
	case "find-installed":
		found, err := a.FindExisting(m)
		j.Data = map[string]string{"found": strconv.Itoa(len(found))}
		if len(found) > 0 {
			s.logf("now tracking %d mod(s)/contraption(s) that were already installed", len(found))
		}
		return err
	case "verify":
		probs, err := m.Verify()
		j.Problems = probs
		for _, p := range probs {
			if p.Bad {
				s.logf("verify: %s: %s", p.Folder, p.Issue)
			}
		}
		return err
	case "rollback":
		return m.Rollback(req.Key)
	case "remove":
		others, err := m.RemoveReport(req.Key)
		if len(others) > 0 {
			j.Data = map[string]string{"others": strings.Join(others, ", ")}
		}
		return err
	case "pin":
		return m.SetPinned(req.Key, req.Pinned)
	}
	return fmt.Errorf("unknown action %q", req.Action)
}

// offerRetry lets the UI re-run a refused install with an override, but only
// for reasons a person may override (manager.Overridable), or with the
// browser fallback when modsbase refused to hand out a link.
func (s *server) offerRetry(j *job, err error, req actionReq) {
	over := map[string]bool{}
	for k, v := range req.Override {
		over[k] = v
	}
	retry := func() {
		j.Retry = map[string]any{"action": req.Action, "refs": req.Refs, "path": req.Path, "workshop_id": req.WorkshopID, "override": over, "mirror": req.Mirror}
	}
	var nb *app.NeedsBrowser
	if errors.As(err, &nb) {
		j.Browser = nb
		over["browser"] = true
		retry()
		return
	}
	var rej *manager.Rejection
	if !errors.As(err, &rej) {
		return
	}
	for _, r := range rej.Reasons {
		if !manager.Overridable(r) {
			return
		}
		switch {
		case manager.RiskReason(r):
			if len(req.Refs) > 1 {
				return // one mod at a time
			}
			over["accept_risk"] = true
			j.Risk = true
		case strings.Contains(r, "--allow-high"):
			over["allow_high"] = true
		case strings.Contains(r, "--cooldown"):
			over["skip_cooldown"] = true
		case strings.Contains(r, "--allow-new-findings"):
			over["allow_new_findings"] = true
		}
	}
	retry()
}

func uploadDir() string {
	d, _ := manager.ConfigDir()
	return filepath.Join(d, "uploads")
}

// handleUpload stores a file chosen in the window so it can be imported.
func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	name := filepath.Base(r.URL.Query().Get("name"))
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".zip" && ext != ".rar" && ext != ".7z" {
		http.Error(w, "only .zip, .rar or .7z archives", http.StatusBadRequest)
		return
	}
	dir := uploadDir()
	os.MkdirAll(dir, 0o755)
	f, err := os.CreateTemp(dir, "up-*-"+name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(f, io.LimitReader(r.Body, 1<<30))
	f.Close()
	if err != nil {
		os.Remove(f.Name())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"path": f.Name()})
}

func (s *server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var st settings
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&st); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if st.Game != "" {
			if fi, err := os.Stat(st.Game); err != nil || !fi.IsDir() {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]string{"error": "that game folder does not exist"})
				return
			}
		}
		st.apply(&s.opt)
		if err := st.save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.logf("settings saved")
	}
	writeJSON(w, settingsFrom(s.opt))
}

func (s *server) checkRelease(force bool) {
	s.relMu.Lock()
	if !force && time.Since(s.relTime) < 30*time.Minute && (s.rel != nil || s.relErr != "") {
		s.relMu.Unlock()
		return
	}
	s.relMu.Unlock()
	rel, err := selfupdate.Latest()
	s.relMu.Lock()
	defer s.relMu.Unlock()
	s.rel, s.relErr, s.relTime = rel, "", time.Now()
	if err != nil {
		s.relErr = err.Error()
	} else if rel.Newer {
		s.logf("ppgmods %s is available (you have %s)", rel.Version, s.version)
	}
}

func (s *server) handleRelease(w http.ResponseWriter, r *http.Request) {
	s.checkRelease(r.URL.Query().Get("force") == "1")
	s.relMu.Lock()
	defer s.relMu.Unlock()
	writeJSON(w, map[string]any{"current": s.version, "latest": s.rel, "error": s.relErr})
}

// handleOpen opens a known folder (Mods, data, backup) or a mod's web page.
func (s *server) handleOpen(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("what")
	p := s.newApp(nil).Paths()
	var path string
	switch target {
	case "mods":
		path = p.Mods
	case "contraptions":
		path = p.Contraptions
	case "data":
		path = p.Data
	case "backup":
		path = lastBackup(p.Data)
	case "share":
		dl := app.DownloadFolder()
		if dl == "" {
			dl = p.Data
		}
		path = filepath.Join(dl, "ppgmods-share")
	case "url":
		u := r.URL.Query().Get("url")
		if app.AllowedURL(u) && !strings.HasPrefix(u, "http://") {
			app.OpenBrowser(u)
			writeJSON(w, map[string]bool{"ok": true})
			return
		}
		http.Error(w, "url not allowed", http.StatusBadRequest)
		return
	}
	if path == "" {
		http.Error(w, "folder not found", http.StatusNotFound)
		return
	}
	os.MkdirAll(path, 0o755)
	app.OpenFolder(path)
	writeJSON(w, map[string]bool{"ok": true})
}

type detailsView struct {
	app.SearchResult
	sources.Details
	Installed bool         `json:"installed"`
	Page      string       `json:"page"` // the mod's page on GameBanana / Steam
	Mirrors   []app.Mirror `json:"mirrors,omitempty"`
	Revision  string       `json:"revision,omitempty"`
}

// handleDetails returns the overview of one mod (description, images,
// required items) without downloading it.
func (s *server) handleDetails(w http.ResponseWriter, r *http.Request) {
	ref := app.NormalizeRef(r.URL.Query().Get("ref"))
	var v detailsView
	switch {
	case strings.HasPrefix(ref, "gb:"):
		id, err := strconv.Atoi(strings.TrimPrefix(ref, "gb:"))
		if err != nil {
			http.Error(w, "bad ref", http.StatusBadRequest)
			return
		}
		mod, err := sources.GBGetMod(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		d, err := sources.GBDetails(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		v.SearchResult = app.SearchResult{Ref: ref, Source: "GameBanana", Name: mod.Name, Author: mod.Submitter.Name,
			Category: mod.Category.Name, Date: time.Unix(mod.Modified, 0).Format("2006-01-02"), URL: mod.URL}
		v.Details, v.Page = *d, mod.URL
		if files, err := sources.GBFiles(id); err == nil && len(files) > 0 {
			v.Size = app.HumanSize(files[0].Size)
		}
	case strings.HasPrefix(ref, "sky:"):
		ws := strings.TrimPrefix(ref, "sky:")
		mirrors, err := app.WorkshopMirrors(ws, r.URL.Query().Get("name"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		newest := mirrors[0]
		v.SearchResult = app.SearchResult{Ref: ref, Source: "Steam Workshop", Name: newest.Title, Author: newest.Author, Date: newest.Version,
			Size: newest.Size, URL: "https://steamcommunity.com/sharedfiles/filedetails/?id=" + ws, AfterCutoff: true}
		v.Page = v.SearchResult.URL
		for _, mr := range mirrors {
			if !mr.AfterCutoff {
				v.AfterCutoff = false
			}
			if v.Image == "" || mr.Source == "top-mods" && mr.Image != "" {
				v.Image = mr.Image
			}
			if v.Author == "" {
				v.Author = mr.Author
			}
		}
		v.Mirrors = mirrors
		// Description and required items: Skymods has the full Steam text;
		// top-mods has its own copy of the description.
		for _, mr := range mirrors {
			if mr.Source == "Skymods" {
				if d, err := sources.SkyDetails(mr.Page); err == nil {
					v.Details = *d
					break
				}
			}
		}
		if v.Description == "" {
			for _, mr := range mirrors {
				if mr.Source == "top-mods" {
					if it, err := sources.TMDetails(mr.Page); err == nil && it.Description != "" {
						v.Description = it.Description
						break
					}
				}
			}
		}
		if v.Image != "" {
			v.Images = append([]string{v.Image}, v.Images...)
		}
	case strings.HasPrefix(ref, "tw:"):
		id, err := strconv.Atoi(strings.TrimPrefix(ref, "tw:"))
		if err != nil {
			http.Error(w, "bad ref", http.StatusBadRequest)
			return
		}
		it, err := sources.TWGet(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		v.SearchResult = app.TWResult(*it)
		v.Page = it.Page()
		// True Workshop has no description field; the GUI shows the one
		// from the mod's own mod.json once the safety check has run.
		v.Details = sources.Details{Downloads: it.Downloads, Likes: it.Likes, Images: []string{it.Thumb()}}
	case strings.HasPrefix(ref, "ow:"):
		ix, err := workshop.FetchIndex()
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		e := ix.Find(strings.TrimPrefix(ref, "ow:"))
		if e == nil {
			http.Error(w, "not on the Open Workshop", http.StatusNotFound)
			return
		}
		v.SearchResult = app.OWResult(*e)
		v.Page = workshop.Page(e.Slug)
		desc := e.Description
		if e.License != "" {
			desc += "\n\nLicense: " + e.License
		}
		if e.Withdrawn {
			desc = "WITHDRAWN: " + e.WithdrawnReason + "\n\n" + desc
		}
		v.Details = sources.Details{Description: desc, Downloads: e.Downloads}
		if e.Image != "" {
			v.Details.Images = []string{e.Image}
		}
	case strings.HasPrefix(ref, "nx:"):
		id, err := strconv.Atoi(strings.TrimPrefix(ref, "nx:"))
		if err != nil {
			http.Error(w, "bad ref", http.StatusBadRequest)
			return
		}
		it, err := sources.NXGet(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		v.SearchResult = app.NXResult(*it, app.NXKind(*it, nil))
		v.Page = it.Page()
		v.Details = sources.Details{Description: it.Summary, Downloads: it.Downloads, Likes: it.Endorsements, Images: []string{it.Picture}}
	default:
		http.Error(w, "unknown ref", http.StatusBadRequest)
		return
	}
	if st, err := manager.LoadState(); err == nil {
		v.Installed = st.Find(ref) != nil
	}
	writeJSON(w, v)
}

// handlePreview downloads and scans a mod without installing it.
func (s *server) handlePreview(w http.ResponseWriter, r *http.Request) {
	a := s.newApp(nil)
	m, err := a.Manager(true)
	if err != nil {
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	ref := app.NormalizeRef(r.URL.Query().Get("ref"))
	a.Opt.Mirror = r.URL.Query().Get("mirror")
	s.logf("previewing %s", ref)
	p, err := a.Preview(m, ref)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, p)
}

// handleThumb serves a thumbnail extracted from a previewed mod archive.
func (s *server) handleThumb(w http.ResponseWriter, r *http.Request) {
	p := app.ThumbPath(r.URL.Query().Get("ref"))
	if p == "" {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", app.ImageType(b))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(b)
}
