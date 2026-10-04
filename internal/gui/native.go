package gui

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// nativeUI runs the native window until it is closed; nil when the binary
// was built without one (no cgo), and then the browser window is used.
var nativeUI func(s *server) error

// call runs an API request in-process, the way the web page sends it over
// HTTP: same handlers, same token check, no network.
func (s *server) call(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Host = s.host
	req.Header.Set("X-Token", s.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return errors.New(strings.TrimSpace(rec.Body.String()))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(rec.Body.Bytes(), out)
}
