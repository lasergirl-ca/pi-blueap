package diag

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// File is a capture or bundle in the state directory.
type File struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// Kinds of file and where they live under the state directory.
const (
	Captures = "captures"
	Bundles  = "bundles"
	keep     = 5 // newest files kept of each kind
)

var nameRe = map[string]*regexp.Regexp{
	Captures: regexp.MustCompile(`^capture-\d{8}-\d{6}\.btsnoop$`),
	Bundles:  regexp.MustCompile(`^palm-gw-support-\d{8}-\d{6}\.tar\.gz$`),
}

// ValidName reports whether name is a file of that kind, which also keeps a
// download request inside its directory.
func ValidName(kind, name string) bool {
	re, ok := nameRe[kind]
	return ok && re.MatchString(name)
}

// NewName is a file name for kind at t.
func NewName(kind string, t time.Time) string {
	ts := t.Format("20060102-150405")
	if kind == Captures {
		return "capture-" + ts + ".btsnoop"
	}
	return "palm-gw-support-" + ts + ".tar.gz"
}

// List returns the files of a kind in dir, newest first.
func List(dir, kind string) ([]File, error) {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []File{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []File{}
	for _, e := range ents {
		if !ValidName(kind, e.Name()) {
			continue
		}
		if fi, err := e.Info(); err == nil {
			out = append(out, File{Name: e.Name(), Size: fi.Size(), ModTime: fi.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// Prune deletes all but the newest few files of a kind.
func Prune(dir, kind string) {
	files, err := List(dir, kind)
	if err != nil {
		return
	}
	for i := keep; i < len(files); i++ {
		_ = os.Remove(filepath.Join(dir, files[i].Name))
	}
}

var (
	hashLineRe = regexp.MustCompile(`(?m)^(\s*(?:viewer_)?password_hash\s*=\s*).*$`)
	argonRe    = regexp.MustCompile(`\$argon2(?:id|i|d)\$[^\s"',}\\]*`)
	pskRe      = regexp.MustCompile(`(?mi)^(\s*psk\s*=\s*).*$`)
)

// Redact removes password hashes and Wi-Fi keys: the config's hash lines,
// and any argon2 hash or psk= line anywhere (a log line, a JSON value).
func Redact(text string) string {
	text = hashLineRe.ReplaceAllString(text, `${1}"REDACTED"`)
	text = pskRe.ReplaceAllString(text, `${1}REDACTED`)
	return argonRe.ReplaceAllString(text, "REDACTED")
}

// Item is one file in a support bundle: Text (redacted), or the contents of
// File (copied as is; only a btmon capture).
type Item struct {
	Name string
	Text string
	File string
}

// WriteBundle writes items as dir/<name>, a gzipped tar with everything
// under one directory named like the file, and prunes old bundles.
func WriteBundle(ctx context.Context, dir string, now time.Time, items []Item) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	name := NewName(Bundles, now)
	top := strings.TrimSuffix(name, ".tar.gz")
	tmp, err := os.CreateTemp(dir, ".bundle-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	add := func(n string, data []byte, mode fs.FileMode) error {
		if err := tw.WriteHeader(&tar.Header{Name: top + "/" + n, Mode: int64(mode), Size: int64(len(data)), ModTime: now, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var data []byte
		if it.File != "" {
			b, err := os.ReadFile(it.File)
			if err != nil {
				data = []byte(fmt.Sprintf("(could not read %s: %v)\n", it.File, err))
				it.Name += ".error.txt"
			} else {
				data = b
			}
		} else {
			data = []byte(Redact(it.Text))
		}
		if err := add(it.Name, data, 0o644); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return "", err
	}
	Prune(dir, Bundles)
	return name, nil
}
