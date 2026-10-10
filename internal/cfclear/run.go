package cfclear

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// Cookie is a cookie the check set.
type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
}

// Result is what a finished check leaves behind: the real page loaded. An
// empty cookie value means no check was served (there was nothing to pass).
type Result struct {
	Cookies   []Cookie  `json:"cookies"`
	UserAgent string    `json:"user_agent"`
	At        time.Time `json:"at"`
	// HTML is the page as the browser shows it once the check is passed.
	HTML string `json:"-"`
}

// Cookie returns the value of the named cookie, or "".
func (r *Result) Cookie(name string) string {
	for _, c := range r.Cookies {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// ErrNeedsPerson means the check did not pass by itself in the background:
// Cloudflare wants a person to tick its "Verify you are human" box. Left
// alone, the box times out after about two minutes and starts over, so
// waiting longer doesn't help; a visible window (Options.Headed) does.
var ErrNeedsPerson = errors.New(`smods.ru wants a person to tick its "Verify you are human" box`)

// Options controls Run.
type Options struct {
	// ProfileDir is the browser profile to use. Empty means a throwaway
	// profile that is deleted afterwards. The person's own browser profile
	// is never used.
	ProfileDir string
	// Headed shows a visible window instead of running the browser in the
	// background, so the person can tick the check's box if it asks.
	Headed bool
	// Timeout is how long the check may take. Default 3 minutes.
	Timeout time.Duration
	// PersonAfter: in the background, how long the check may stay up before
	// Run gives up with ErrNeedsPerson. A check that passes by itself does
	// so within seconds. Default 25 seconds; it doesn't apply to a window.
	PersonAfter time.Duration
	// Browser is a browser executable to use. Empty: found automatically.
	Browser string
	// Logf receives progress notes.
	Logf func(string, ...any)
}

// testChromeFlags are extra Chrome flags for tests.
var testChromeFlags []chromedp.ExecAllocatorOption

// Run opens url in a browser and waits until the real page shows, past any
// Cloudflare check. In the default background mode nothing appears on
// screen. It returns the page, the cookies the check set and the browser's
// User-Agent, which a clearance is bound to.
func Run(u string, opt Options) (*Result, error) {
	if opt.Timeout == 0 {
		opt.Timeout = 3 * time.Minute
	}
	if opt.PersonAfter == 0 {
		opt.PersonAfter = 25 * time.Second
	}
	logf := opt.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	exe := opt.Browser
	if exe == "" {
		var err error
		if exe, err = FindBrowser(); err != nil {
			return nil, err
		}
	}
	profile := opt.ProfileDir
	if profile == "" {
		base := sandboxProfileBase(exe)
		if base != "" {
			os.MkdirAll(base, 0o700)
		}
		d, err := os.MkdirTemp(base, "ppgmods-browser-")
		if err != nil {
			return nil, err
		}
		profile = d
		defer os.RemoveAll(profile)
	} else if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, err
	}

	if opt.Headed {
		logf(`opened %s in a browser window: if it shows "Verify you are human", tick the box`, hostOf(u))
	} else {
		logf("opening %s in a background browser", hostOf(u))
	}
	if isFirefox(exe) {
		return runFirefox(u, exe, profile, opt, logf)
	}
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(exe),
		chromedp.UserDataDir(profile),
		chromedp.Flag("disable-session-crashed-bubble", true),
		// The check compares the browser against known automation tells;
		// these keep a driven browser indistinguishable from the person's
		// own, which is the point: the page is viewed as they would view it.
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("enable-automation", false),
	)
	allocOpts = append(allocOpts, testChromeFlags...)
	if opt.Headed {
		allocOpts = append(allocOpts, chromedp.Flag("headless", false), chromedp.WindowSize(880, 720))
	} else {
		// The new headless mode runs the full browser without a window. Its
		// User-Agent would still say HeadlessChrome, which the check flags:
		// override it with the same browser's normal one before navigating.
		allocOpts = append(allocOpts, chromedp.Flag("headless", "new"))
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer cancelAlloc()
	ctx, cancelBrowser := chromedp.NewContext(allocCtx, chromedp.WithErrorf(quietErrorf))
	defer cancelBrowser()
	ctx, cancelTimeout := context.WithTimeout(ctx, opt.Timeout)
	defer cancelTimeout()

	open := chromedp.ActionFunc(func(ctx context.Context) error {
		// This browser only passes a check. A page or an ad must not be able
		// to drop a file into Downloads, where ppgmods' browser fallback
		// picks up new archives to install.
		if err := browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorDeny).Do(ctx); err != nil {
			return err
		}
		var ua string
		if err := chromedp.Evaluate(`navigator.userAgent`, &ua).Do(ctx); err != nil {
			return err
		}
		if clean := strings.ReplaceAll(ua, "HeadlessChrome", "Chrome"); clean != ua {
			return emulation.SetUserAgentOverride(clean).Do(ctx)
		}
		return nil
	})
	if err := chromedp.Run(ctx, open, chromedp.Navigate(u)); err != nil {
		if ctx.Err() != nil {
			return nil, timeoutErr(opt)
		}
		return nil, fmt.Errorf("the browser could not open the page: %w", err)
	}
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()
	prog := newProgress(logf)
	for {
		var (
			st      pageState
			html    string
			cookies []*network.Cookie
		)
		err := chromedp.Run(ctx, chromedp.Evaluate(pageStateJS, &st))
		if err == nil && st.loaded() {
			err = chromedp.Run(ctx,
				chromedp.Evaluate(`document.documentElement.outerHTML`, &html),
				chromedp.ActionFunc(func(ctx context.Context) error {
					cs, err := network.GetCookies().Do(ctx)
					cookies = cs
					return err
				}),
			)
			if err == nil {
				res := &Result{UserAgent: st.UA, At: time.Now().UTC(), HTML: html}
				for _, c := range cookies {
					res.Cookies = append(res.Cookies, Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain})
				}
				prog.done(res)
				return res, nil
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, timeoutErr(opt)
			}
			if opt.Headed {
				return nil, errors.New("the browser window was closed before the check passed")
			}
			return nil, fmt.Errorf("the background browser stopped: %w", err)
		}
		if !opt.Headed && prog.elapsed() >= opt.PersonAfter {
			return nil, ErrNeedsPerson
		}
		prog.note(st.Title)
		select {
		case <-ctx.Done():
			return nil, timeoutErr(opt)
		case <-tick.C:
		}
	}
}

// pageStateJS reads what the poll needs to tell the check from the page.
const pageStateJS = `({ua: navigator.userAgent, title: document.title, ready: document.readyState,
	check: !!window._cf_chl_opt || !!document.querySelector('script[src*="/cdn-cgi/challenge-platform/"]')})`

type pageState struct {
	UA    string `json:"ua"`
	Title string `json:"title"`
	Ready string `json:"ready"`
	Check bool   `json:"check"`
}

// loaded reports whether the real page shows, not the check. Only the real
// page counts: the check sets a cf_clearance cookie even when it times out
// unsolved, so the cookie alone proves nothing.
func (s pageState) loaded() bool {
	t := strings.ToLower(s.Title)
	return s.UA != "" && s.Ready != "" && s.Ready != "loading" && !s.Check &&
		!strings.Contains(t, "just a moment") && !strings.Contains(t, "attention required")
}

func timeoutErr(opt Options) error {
	if opt.Headed {
		return fmt.Errorf("the check was not passed within %s", opt.Timeout)
	}
	return ErrNeedsPerson
}

// quietErrorf drops chromedp's complaints about protocol events it doesn't
// know (newer browsers add them); they mean nothing for the check.
func quietErrorf(format string, a ...any) {
	if msg := fmt.Sprintf(format, a...); !strings.Contains(msg, "could not unmarshal event") {
		log.Print(msg)
	}
}

// progress logs, every 10 seconds, that a check is still being waited on and
// what the page shows, so a slow check can be told from a stuck one.
type progress struct {
	logf  func(string, ...any)
	start time.Time
	last  time.Time
}

func newProgress(logf func(string, ...any)) *progress {
	now := time.Now()
	return &progress{logf: logf, start: now, last: now}
}

func (p *progress) elapsed() time.Duration { return time.Since(p.start) }

func (p *progress) note(title string) {
	if time.Since(p.last) < 10*time.Second {
		return
	}
	p.last = time.Now()
	if title == "" {
		title = "(still loading)"
	}
	p.logf("still on the browser check after %ds (the page says %q)", int(p.elapsed().Seconds()), title)
}

func (p *progress) done(res *Result) {
	s := int(p.elapsed().Seconds())
	if res.Cookie("cf_clearance") != "" {
		p.logf("the page loaded past the browser check (%ds)", s)
	} else {
		p.logf("the page loaded; no browser check was served (%ds)", s)
	}
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}
