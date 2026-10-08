package scan

import "bytes"

// LooseJSON turns a mod.json as the game accepts it into strict JSON: any
// byte order mark is decoded, // and /* */ comments are dropped and commas
// before } or ] removed. Text inside strings is left alone.
func LooseJSON(raw []byte) []byte {
	b := []byte(decodeText(raw))
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(b) && b[j] != '"' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(b) {
				j = len(b) - 1
			}
			out = append(out, b[i:j+1]...)
			i = j
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			// Newtonsoft ends a line comment at \r or \n; stopping only at
			// \n would hide what the game reads after a bare \r.
			for i < len(b) && b[i] != '\n' && b[i] != '\r' {
				i++
			}
			if i < len(b) {
				out = append(out, b[i])
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			end := bytes.Index(b[i+2:], []byte("*/"))
			if end < 0 {
				// Unterminated: keep it, so the result stays invalid as
				// it is for the game.
				return append(out, b[i:]...)
			}
			i += end + 3
			out = append(out, ' ')
		case c == '}' || c == ']':
			k := len(out) - 1
			for k >= 0 && isJSONSpace(out[k]) {
				k--
			}
			if k >= 0 && out[k] == ',' {
				out = append(out[:k], out[k+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

func isJSONSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
