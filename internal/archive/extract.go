// Package archive extracts .zip, .rar and .7z mod archives with path
// traversal, symlink and size protections.
package archive

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"
)

const (
	MaxTotal = 2 << 30 // 2 GiB unpacked
	MaxFiles = 20000
)

var ErrUnsupported = errors.New("unsupported archive format")

// Extract unpacks src into dest (which must exist and be empty).
func Extract(src, dest string) error {
	head := make([]byte, 8)
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	n, _ := io.ReadFull(f, head)
	f.Close()
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")) || bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return extractZip(src, dest)
	case bytes.HasPrefix(head, []byte("Rar!\x1a\x07")):
		return extractRar(src, dest)
	case bytes.HasPrefix(head, []byte("7z\xbc\xaf\x27\x1c")):
		return extract7z(src, dest)
	}
	return fmt.Errorf("%w: %s", ErrUnsupported, filepath.Base(src))
}

type limiter struct {
	total int64
	files int
}

func (l *limiter) target(dest, name string) (string, error) {
	name = strings.ReplaceAll(name, `\`, "/")
	// ':' would write an NTFS alternate data stream (hidden from the
	// scanner) on Windows; control characters have no place in a mod.
	if strings.ContainsAny(name, ":\x00") || strings.IndexFunc(name, func(r rune) bool { return r < 0x20 }) >= 0 {
		return "", fmt.Errorf("archive entry %q has an invalid name", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.VolumeName(clean) != "" {
		return "", fmt.Errorf("archive entry %q escapes the extraction folder", name)
	}
	l.files++
	if l.files > MaxFiles {
		return "", fmt.Errorf("archive has more than %d entries", MaxFiles)
	}
	return filepath.Join(dest, clean), nil
}

func (l *limiter) write(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(r, MaxTotal-l.total+1))
	l.total += n
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && l.total > MaxTotal {
		err = fmt.Errorf("archive unpacks to more than %d bytes", int64(MaxTotal))
	}
	return err
}

func extractZip(src, dest string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	l := &limiter{}
	for _, f := range zr.File {
		p, err := l.target(dest, f.Name)
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %q is a symlink", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = l.write(p, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractRar(src, dest string) error {
	rr, err := rardecode.OpenReader(src)
	if err != nil {
		return err
	}
	defer rr.Close()
	l := &limiter{}
	for {
		h, err := rr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := l.target(dest, h.Name)
		if err != nil {
			return err
		}
		if h.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %q is a symlink", h.Name)
		}
		if h.IsDir {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := l.write(p, rr); err != nil {
			return err
		}
	}
}

func extract7z(src, dest string) error {
	zr, err := sevenzip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	l := &limiter{}
	for _, f := range zr.File {
		p, err := l.target(dest, f.Name)
		if err != nil {
			return err
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %q is a symlink", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = l.write(p, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
