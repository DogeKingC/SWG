package scan

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A small C# lexer. It does not need to understand the language, only to
// split source into identifiers, strings and punctuation the way the
// compiler would, so that comments, string contents, \uXXXX identifier
// escapes, verbatim/interpolated/raw strings and line breaks cannot hide a
// call from the rules.

type tokKind int

const (
	tIdent tokKind = iota
	tString
	tChar
	tNumber
	tPunct
)

type token struct {
	kind tokKind
	text string // identifier name (escapes decoded), string value, or punctuation
	line int
}

type lexResult struct {
	toks           []token
	unicodeEscapes int // \uXXXX escapes used inside identifiers
}

func lexCSharp(src string) lexResult {
	l := &lexer{src: src, line: 1}
	l.run(0)
	return lexResult{toks: l.toks, unicodeEscapes: l.esc}
}

type lexer struct {
	src  string
	pos  int
	line int
	toks []token
	esc  int
}

func (l *lexer) peek(off int) byte {
	if l.pos+off < len(l.src) {
		return l.src[l.pos+off]
	}
	return 0
}

func (l *lexer) emit(k tokKind, text string, line int) {
	l.toks = append(l.toks, token{k, text, line})
}

// run lexes until the end of input, or until an unmatched '}' when depth>0
// (the end of an interpolation hole).
func (l *lexer) run(depth int) {
	braces := 0
	atLineStart := true
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\n':
			l.line++
			l.pos++
			atLineStart = true
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			l.pos++
			continue
		case c == '#' && atLineStart && depth == 0:
			// Preprocessor directive: skip the line. Both branches of
			// #if/#else are lexed, so code cannot hide behind a symbol.
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
			continue
		}
		atLineStart = false
		switch {
		case c == '/' && l.peek(1) == '/':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		case c == '/' && l.peek(1) == '*':
			l.pos += 2
			for l.pos < len(l.src) && !(l.src[l.pos] == '*' && l.peek(1) == '/') {
				if l.src[l.pos] == '\n' {
					l.line++
				}
				l.pos++
			}
			l.pos += 2
		case c == '"' || c == '$' && (l.peek(1) == '"' || l.peek(1) == '@' || l.peek(1) == '$') || c == '@' && (l.peek(1) == '"' || l.peek(1) == '$'):
			l.stringLit()
		case c == '\'':
			l.charLit()
		case c >= '0' && c <= '9':
			start := l.pos
			for l.pos < len(l.src) && (isIdentByte(l.src[l.pos]) || l.src[l.pos] == '.' && l.pos+1 < len(l.src) && l.src[l.pos+1] >= '0' && l.src[l.pos+1] <= '9') {
				l.pos++
			}
			l.emit(tNumber, l.src[start:l.pos], l.line)
		case c == '@' || c == '_' || c == '\\' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80:
			if !l.ident() {
				l.pos++
			}
		case c == '{':
			braces++
			l.emit(tPunct, "{", l.line)
			l.pos++
		case c == '}':
			if depth > 0 && braces == 0 {
				return // end of interpolation hole; caller consumes '}'
			}
			braces--
			l.emit(tPunct, "}", l.line)
			l.pos++
		case c == ':' && l.peek(1) == ':':
			l.emit(tPunct, "::", l.line)
			l.pos += 2
		case c == '=' && l.peek(1) == '>':
			l.emit(tPunct, "=>", l.line)
			l.pos += 2
		default:
			l.emit(tPunct, string(c), l.line)
			l.pos++
		}
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// ident lexes an identifier, decoding \uXXXX / \UXXXXXXXX escapes.
func (l *lexer) ident() bool {
	line := l.line
	if l.src[l.pos] == '@' {
		l.pos++
	}
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\\' && (l.peek(1) == 'u' || l.peek(1) == 'U'):
			n := 4
			if l.peek(1) == 'U' {
				n = 8
			}
			if l.pos+2+n > len(l.src) {
				return b.Len() > 0
			}
			v, err := strconv.ParseUint(l.src[l.pos+2:l.pos+2+n], 16, 32)
			if err != nil {
				return b.Len() > 0
			}
			b.WriteRune(rune(v))
			l.esc++
			l.pos += 2 + n
		case isIdentByte(c):
			b.WriteByte(c)
			l.pos++
		case c >= 0x80:
			r, size := utf8.DecodeRuneInString(l.src[l.pos:])
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.Is(unicode.Mn, r) && !unicode.Is(unicode.Pc, r) {
				goto done
			}
			b.WriteRune(r)
			l.pos += size
		default:
			goto done
		}
	}
done:
	if b.Len() == 0 {
		return false
	}
	l.emit(tIdent, b.String(), line)
	return true
}

func (l *lexer) charLit() {
	line := l.line
	l.pos++
	var b strings.Builder
	for l.pos < len(l.src) && l.src[l.pos] != '\'' && l.src[l.pos] != '\n' {
		if l.src[l.pos] == '\\' && l.pos+1 < len(l.src) {
			b.WriteByte(l.src[l.pos])
			l.pos++
		}
		b.WriteByte(l.src[l.pos])
		l.pos++
	}
	l.pos++
	l.emit(tChar, b.String(), line)
}

// stringLit lexes regular, verbatim (@""), interpolated ($"", $@"") and raw
// ("""...""", $"""...""") string literals. Interpolation holes are lexed as
// code, so a call inside {...} is seen like any other.
func (l *lexer) stringLit() {
	line := l.line
	interp, verbatim, dollars := false, false, 0
	for l.pos < len(l.src) && (l.src[l.pos] == '$' || l.src[l.pos] == '@') {
		if l.src[l.pos] == '$' {
			interp = true
			dollars++
		} else {
			verbatim = true
		}
		l.pos++
	}
	if l.pos >= len(l.src) || l.src[l.pos] != '"' {
		return
	}
	// Raw string literal: three or more quotes.
	q := 0
	for l.pos+q < len(l.src) && l.src[l.pos+q] == '"' {
		q++
	}
	if q >= 3 {
		l.pos += q
		var b strings.Builder
		for l.pos < len(l.src) {
			if l.src[l.pos] == '"' && strings.HasPrefix(l.src[l.pos:], strings.Repeat(`"`, q)) {
				l.pos += q
				break
			}
			if interp && l.src[l.pos] == '{' && strings.HasPrefix(l.src[l.pos:], strings.Repeat("{", max(dollars, 1))) {
				l.pos += max(dollars, 1)
				l.hole()
				for i := 0; i < max(dollars, 1) && l.pos < len(l.src) && l.src[l.pos] == '}'; i++ {
					l.pos++
				}
				continue
			}
			if l.src[l.pos] == '\n' {
				l.line++
			}
			b.WriteByte(l.src[l.pos])
			l.pos++
		}
		l.emit(tString, b.String(), line)
		return
	}
	l.pos++ // opening quote
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '"' && verbatim && l.peek(1) == '"':
			b.WriteByte('"')
			l.pos += 2
		case c == '"':
			l.pos++
			l.emit(tString, b.String(), line)
			return
		case c == '\\' && !verbatim && l.pos+1 < len(l.src):
			b.WriteString(unescape(l.src[l.pos : l.pos+2+escLen(l.src[l.pos+1:])]))
			l.pos += 2 + escLen(l.src[l.pos+1:])
		case interp && c == '{' && l.peek(1) == '{':
			b.WriteByte('{')
			l.pos += 2
		case interp && c == '}' && l.peek(1) == '}':
			b.WriteByte('}')
			l.pos += 2
		case interp && c == '{':
			l.pos++
			l.hole()
			if l.pos < len(l.src) && l.src[l.pos] == '}' {
				l.pos++
			}
		case c == '\n' && !verbatim:
			l.emit(tString, b.String(), line) // unterminated
			return
		default:
			if c == '\n' {
				l.line++
			}
			b.WriteByte(c)
			l.pos++
		}
	}
	l.emit(tString, b.String(), line)
}

// hole lexes an interpolation expression as code.
func (l *lexer) hole() {
	sub := &lexer{src: l.src, pos: l.pos, line: l.line}
	sub.run(1)
	l.toks = append(l.toks, sub.toks...)
	l.esc += sub.esc
	l.pos, l.line = sub.pos, sub.line
}

func escLen(s string) int {
	if s == "" {
		return 0
	}
	switch s[0] {
	case 'u':
		return 4
	case 'U':
		return 8
	case 'x':
		n := 0
		for n < 4 && 1+n < len(s) && strings.ContainsRune("0123456789abcdefABCDEF", rune(s[1+n])) {
			n++
		}
		return n
	}
	return 0
}

func unescape(e string) string {
	if len(e) < 2 {
		return e
	}
	switch e[1] {
	case 'n':
		return "\n"
	case 't':
		return "\t"
	case 'r':
		return "\r"
	case '0':
		return "\x00"
	case '\\', '"', '\'':
		return e[1:2]
	case 'u', 'U', 'x':
		if v, err := strconv.ParseUint(e[2:], 16, 32); err == nil {
			return string(rune(v))
		}
	}
	return e[1:]
}
