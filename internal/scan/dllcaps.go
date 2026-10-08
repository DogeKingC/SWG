package scan

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Capability analysis of compiled mods. A mod's DLL can't be read like its
// C# source, but its metadata names every outside type and member it calls,
// every native function it imports, and its string literals (see ReadCLR).
// Those are checked against the same API rules as source code, so the
// report says what the DLL can do: start programs, use the network, touch
// files, load other code, call native code, or reach code by name through
// reflection. Code that isn't .NET, or whose metadata can't be read, stays
// unreadable (CRITICAL).

// reflectionCalls are the calls that reach code chosen by name at run time:
// with them, the metadata no longer lists everything a DLL can reach.
var reflectionCalls = []string{
	"System.Reflection.MethodBase.Invoke", "System.Reflection.MethodInfo.Invoke", "System.Reflection.ConstructorInfo.Invoke",
	"System.Reflection.PropertyInfo.SetValue", "System.Reflection.PropertyInfo.GetValue",
	"System.Type.InvokeMember", "System.Type.GetType", "System.Activator.CreateInstance", "System.Delegate.CreateDelegate",
	"System.Reflection.Assembly.GetType", "System.Reflection.Assembly.CreateInstance",
}

// clrName turns a metadata type name into the dotted form the rules use:
// nested types joined with ".", generic arity ("List`1") dropped.
func clrName(t string) string {
	t = strings.ReplaceAll(t, "/", ".")
	for {
		i := strings.IndexByte(t, '`')
		if i < 0 {
			return t
		}
		j := i + 1
		for j < len(t) && t[j] >= '0' && t[j] <= '9' {
			j++
		}
		t = t[:i] + t[j:]
	}
}

// analyzeDLL reports what a mod's DLL can do, read from its metadata.
func analyzeDLL(r *Report, rel, path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if len(b) > 64<<20 {
		r.add(Critical, "executable-file", rel, 0, "compiled library too large to read: its code cannot be checked")
		return
	}
	info, err := ReadCLR(b)
	switch {
	case errors.Is(err, errNotCLR):
		r.add(Critical, "executable-file", rel, 0, "native (not .NET) library: its code cannot be checked")
		return
	case err != nil:
		r.add(Critical, "executable-file", rel, 0, "compiled library whose metadata can't be read ("+err.Error()+"): its code cannot be checked")
		return
	case !info.ILOnly:
		r.add(Critical, "native-code", rel, 0, "mixed .NET and native library: its native code cannot be checked")
	}

	type capHit struct {
		sev      Severity
		desc     string
		examples []string
	}
	caps := map[string]*capHit{}
	note := func(id string, sev Severity, desc, example string) {
		h := caps[id]
		if h == nil {
			h = &capHit{sev: sev, desc: desc}
			caps[id] = h
		}
		if sev > h.sev {
			h.sev = sev
		}
		if len(h.examples) < 3 && !containsString(h.examples, example) {
			h.examples = append(h.examples, example)
		}
	}
	// Attributes are markers the compiler adds (GeneratedCode,
	// SecurityPermission...): naming one runs nothing.
	attr := func(t string) bool { return strings.HasSuffix(t, "Attribute") }
	refs := map[string]bool{}
	namespaces := map[string]bool{}
	for _, t := range info.TypeRefs {
		if attr(t) {
			continue
		}
		refs[clrName(t)] = true
		outer, _, _ := strings.Cut(t, "/")
		if i := strings.LastIndexByte(outer, '.'); i > 0 {
			namespaces[outer[:i]] = true
		}
	}
	for _, mr := range info.MemberRefs {
		if mr.Type != "" && !attr(mr.Type) {
			refs[clrName(mr.Type)+"."+mr.Name] = true
		}
	}
	shown := func(name string) string {
		if t, ok := strings.CutSuffix(name, "..ctor"); ok {
			return "new " + t
		}
		return name
	}
	dynamic := false
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		matchRules(name, func(p string, ru *apiRule) {
			if ru.sev > Info {
				note(ru.id, ru.sev, ru.desc, shown(name))
			}
		})
		for _, rc := range reflectionCalls {
			if name == rc {
				dynamic = true
				note("reflection-invoke", High, "calls code chosen by name at run time (reflection), so its full reach can't be read from the file", name)
			}
		}
	}
	var nsList []string
	for ns := range namespaces {
		nsList = append(nsList, ns)
	}
	sort.Strings(nsList)
	for _, ns := range nsList {
		if ns == "System.Security.Permissions" {
			continue // only the enums of security attributes the compiler adds
		}
		if sev, _, ok := namespaceVerdict(ns); ok && sev >= High {
			note("namespace", High, "uses parts of .NET outside the allowlist for mods", ns)
		}
	}
	for _, pi := range info.PInvokes {
		note("native-interop", Critical, "calls native code", pi)
	}
	// String literals: names used with reflection, shells, worm markers.
	hit := func(id string, sev Severity, _ int, detail string) { note(id, sev, detail, "") }
	for _, s := range info.Strings {
		low := strings.ToLower(strings.Join(strings.Fields(s), ""))
		sev, ok := sensitiveExact[low]
		if !ok {
			sev, ok = sensitiveExact[lastSegment(low)]
		}
		for _, w := range sensitiveSubstr {
			if strings.Contains(low, w.s) && (!ok || w.sev > sev) {
				sev, ok = w.sev, true
			}
		}
		switch {
		case ok && dynamic:
			note("reflection-sensitive-type", sev, "names APIs in strings and calls code by name, which reaches them without listing them", clip(s))
		case ok:
			note("sensitive-string", Medium, "mentions sensitive APIs in strings", clip(s))
		}
		if reShellStr.MatchString(s) {
			note("shell-string", High, "names a shell or download tool in a string", clip(s))
		}
		checkMarkers(hit, 0, low, "a string")
	}

	ids := make([]string, 0, len(caps))
	for id := range caps {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if caps[ids[i]].sev != caps[ids[j]].sev {
			return caps[ids[i]].sev > caps[ids[j]].sev
		}
		return ids[i] < ids[j]
	})
	var can []string
	for _, id := range ids {
		h := caps[id]
		detail := h.desc
		var ex []string
		for _, e := range h.examples {
			if e != "" {
				ex = append(ex, e)
			}
		}
		if len(ex) > 0 {
			detail += ": " + strings.Join(ex, ", ")
		}
		r.add(h.sev, id, rel, 0, "compiled code "+detail)
		if h.sev >= Medium {
			can = append(can, id)
		}
	}
	summary := "only the game, Unity and basic .NET (read from its metadata)"
	if len(can) > 0 {
		summary = "see the findings: " + strings.Join(can, ", ")
	}
	r.add(High, "compiled-code", rel, 0, fmt.Sprintf("compiled library: it can't be read like source, but its metadata lists what it uses (%d outside types, %d calls): %s", len(info.TypeRefs), len(info.MemberRefs), summary))
}

func containsString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
