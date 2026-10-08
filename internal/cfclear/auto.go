package cfclear

import (
	"sync"
	"time"

	"github.com/Trlydev/SWG/internal/sources"
)

const skymodsURL = "https://catalogue.smods.ru/"

// attemptCooldown is how long after a failed automatic attempt the next one
// waits: a blocked site is not helped by launching browsers at it, and the
// check usually clears on its own.
const attemptCooldown = 10 * time.Minute

type attempt struct {
	At    time.Time `json:"at"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`

	Running bool `json:"running"`
}

var (
	autoMu      sync.Mutex
	autoRunning bool
	autoDone    chan struct{}
	lastAttempt = map[string]attempt{}
)

// Status reports how the last automatic check for a site went.
func Status(site string) attempt {
	autoMu.Lock()
	defer autoMu.Unlock()
	a := lastAttempt[site]
	a.Running = autoRunning
	return a
}

// CheckRunning reports whether a check is in flight right now.
func CheckRunning() bool {
	autoMu.Lock()
	defer autoMu.Unlock()
	return autoRunning
}

// Ensure makes sure a clearance for site exists: if none is stored, it
// passes the check with a background browser and stores what it earns.
// Concurrent callers wait for the same attempt instead of starting their
// own; a failed attempt is not repeated for attemptCooldown. It reports
// whether the site is usable now (a clearance is stored, or the page turned
// out not to be checked at all).
func Ensure(site, url string, opt Options, force bool) bool {
	return ensure(site, url, opt, force, Run)
}

func ensure(site, url string, opt Options, force bool, run func(string, Options) (*Result, error)) bool {
	logf := opt.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	autoMu.Lock()
	if !force {
		if c, _ := Load(site); c != nil {
			autoMu.Unlock()
			return true
		}
		if a, ok := lastAttempt[site]; ok && time.Since(a.At) < attemptCooldown {
			autoMu.Unlock()
			// An attempt was made recently; only a stored clearance counts
			// now (the loop in skyGet must not re-trigger it for a while).
			c, _ := Load(site)
			return c != nil
		}
	}
	if autoRunning {
		done := autoDone
		autoMu.Unlock()
		<-done // another attempt is in flight; wait for it and use its result
		c, _ := Load(site)
		return c != nil
	}
	autoRunning = true
	autoDone = make(chan struct{})
	autoMu.Unlock()
	defer func() {
		autoMu.Lock()
		autoRunning = false
		close(autoDone)
		autoMu.Unlock()
	}()

	res, err := run(url, opt)
	ok, errStr := false, ""
	switch {
	case err != nil:
		errStr = err.Error()
		logf("the background browser check failed: %v", err)
	case res.Cookie("cf_clearance") == "":
		ok = true // the page loaded without any check being served
	default:
		if err = Save(&Clearance{Site: site, Cookie: res.Cookie("cf_clearance"), UserAgent: res.UserAgent, At: time.Now().UTC()}); err != nil {
			errStr = err.Error()
			logf("could not store the clearance: %v", err)
		} else {
			ok = true
			logf("stored the browser-check clearance for %s", site)
		}
	}
	autoMu.Lock()
	lastAttempt[site] = attempt{At: time.Now(), OK: ok, Error: errStr}
	autoMu.Unlock()
	return ok
}

// Expire drops a clearance and the record of the attempt that earned it:
// Cloudflare stopped accepting the cookie, so a fresh check may run right
// away (hours will have passed since the attempt - well past the cooldown).
func Expire(site string) {
	Clear(site)
	autoMu.Lock()
	delete(lastAttempt, site)
	autoMu.Unlock()
}

// RunSkymodsCheck passes smods.ru's check (automatically, in the background)
// and refreshes the Skymods caches when it succeeds. force repeats an
// attempt that failed recently, for the Settings button.
func RunSkymodsCheck(force bool, logf func(string, ...any)) bool {
	ok := Ensure(SkymodsSite, skymodsURL, Options{Logf: logf}, force)
	if ok {
		sources.InvalidateSkyCache()
	}
	return ok
}

// WireSkymods connects the clearance store to the Skymods client: stored
// clearances are attached to its requests, and when Cloudflare intercepts,
// a background browser passes the check on its own - no interaction.
func WireSkymods(logf func(string, ...any)) {
	sources.SkyClearance = func() (string, string) {
		c, err := Load(SkymodsSite)
		if err != nil || c == nil {
			return "", ""
		}
		return c.Cookie, c.UserAgent
	}
	sources.SkyClearanceExpired = func() {
		Expire(SkymodsSite)
	}
	sources.SkyAutoCheck = func() bool {
		return RunSkymodsCheck(false, logf)
	}
}
