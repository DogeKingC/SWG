package cfclear

import (
	"context"
	"fmt"
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

// Result is what a finished check leaves behind. An empty cookie value means
// the page loaded without any check being served (there was nothing to pass).
type Result struct {
	Cookies   []Cookie  `json:"cookies"`
	UserAgent string    `json:"user_agent"`
	At        time.Time `json:"at"`
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

// Options controls Run.
type Options struct {
	// ProfileDir is the browser profile to use. Empty means a throwaway
	// profile that is deleted afterwards. The person's own browser profile
	// is never used.
	ProfileDir string
	// Headed shows a visible window instead of running the browser in the
	// background. The background mode is the default: a headless Chrome
	// running the check exactly as the person's browser would.
	Headed bool
	// Timeout is how long the check may take. Default 3 minutes.
	Timeout time.Duration
	// Browser is a browser executable to use. Empty: found automatically.
	Browser string
	// Logf receives progress notes.
	Logf func(string, ...any)
}

// Run opens url in a browser and waits for its Cloudflare check to pass.
// In the default background mode nothing appears on screen: a headless
// browser runs the check the way the person's own browser would. It returns
// the cookies the check set and the browser's User-Agent, which the
// clearance is bound to.
func Run(u string, opt Options) (*Result, error) {
	if opt.Timeout == 0 {
		opt.Timeout = 3 * time.Minute
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
	}

	logf("passing %s's browser check in a background browser", hostOf(u))
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
	ctx, cancelBrowser := chromedp.NewContext(allocCtx)
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
		return nil, fmt.Errorf("the background browser could not open the page: %w", err)
	}
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()
	for {
		var (
			ua      string
			title   string
			cookies []*network.Cookie
		)
		err := chromedp.Run(ctx,
			chromedp.Evaluate(`navigator.userAgent`, &ua),
			chromedp.Title(&title),
			chromedp.ActionFunc(func(ctx context.Context) error {
				cs, err := network.GetCookies().Do(ctx)
				cookies = cs
				return err
			}),
		)
		if err != nil {
			return nil, fmt.Errorf("the background browser could not finish the check: %w", err)
		}
		res := &Result{UserAgent: ua, At: time.Now().UTC()}
		for _, c := range cookies {
			res.Cookies = append(res.Cookies, Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain})
		}
		if res.Cookie("cf_clearance") != "" {
			logf("check passed; captured the clearance cookie")
			return res, nil
		}
		// The check page calls itself "Just a moment..."; any other title
		// means the real page loaded and no check was served at all.
		if title != "" && !strings.Contains(strings.ToLower(title), "just a moment") {
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

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}
