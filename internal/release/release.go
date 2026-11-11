// Package release finds and fetches palm-gw releases (PLAN.md section 8.11):
// the latest tag from GitHub, then a release's SHA256SUMS and the arm64 .deb it
// lists, checked against that sum. `palm-gw upgrade` and its `--check` use it;
// packaging/bootstrap-install.sh does the same steps in shell.
package release

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Repo is the GitHub repository releases come from.
const Repo = "lasergirl-ca/pi-blueap"

// SumsFile is the checksum list published with each release.
const SumsFile = "SHA256SUMS"

// debRe matches the package asset in SHA256SUMS.
var debRe = regexp.MustCompile(`^palm-gw_[A-Za-z0-9.+~-]+_arm64\.deb$`)

// tagRe is a release tag: v, then a version.
var tagRe = regexp.MustCompile(`^v[0-9][A-Za-z0-9.+~-]*$`)

// Client fetches release metadata and files. The zero value talks to GitHub
// with a default HTTP client that can also read file:// URLs (for a release
// directory on disk, as in `palm-gw upgrade --from file:///tmp/rel`).
type Client struct {
	HTTP *http.Client
	// API is the GitHub API root; empty means https://api.github.com.
	API string
	// Download is the release download root; empty means
	// https://github.com/<Repo>/releases/download.
	Download string
}

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.RegisterProtocol("file", http.NewFileTransport(http.Dir("/")))
	return &http.Client{Transport: t, Timeout: 5 * time.Minute}
}

// ValidTag reports whether tag looks like a release tag (v1.2.3, v0.6.0-rc1).
func ValidTag(tag string) bool { return tagRe.MatchString(tag) }

// DebVersion turns a tag into the package version `make deb` gives it:
// v1.2.3 -> 1.2.3, v0.6.0-rc1 -> 0.6.0~rc1 (a pre-release sorts before the
// release, as Debian's ~ does).
func DebVersion(tag string) string {
	return strings.ReplaceAll(strings.TrimPrefix(tag, "v"), "-", "~")
}

// Latest returns the tag of the latest published (non-draft, non-prerelease)
// release.
func (c Client) Latest(ctx context.Context) (string, error) {
	api := c.API
	if api == "" {
		api = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "palm-gw-upgrade")
	resp, err := c.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("asking GitHub for the latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", errors.New("no release has been published yet")
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("asking GitHub for the latest release: %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("reading GitHub's answer: %w", err)
	}
	if !ValidTag(body.TagName) {
		return "", fmt.Errorf("GitHub returned an unexpected tag %q", body.TagName)
	}
	return body.TagName, nil
}

// BaseURL is the download directory of the release tagged tag.
func (c Client) BaseURL(tag string) string {
	root := c.Download
	if root == "" {
		root = "https://github.com/" + Repo + "/releases/download"
	}
	return strings.TrimSuffix(root, "/") + "/" + tag
}

// Asset is the package a release offers.
type Asset struct {
	Name   string // file name, as listed in SHA256SUMS
	SHA256 string // lower-case hex
}

// ParseSums finds the arm64 .deb in a SHA256SUMS file (sha256sum's format,
// "<hex>  <name>" or "<hex> *<name>"). Exactly one is expected.
func ParseSums(r io.Reader) (Asset, error) {
	var found []Asset
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		name := strings.TrimPrefix(f[1], "*")
		sum := strings.ToLower(f[0])
		if !debRe.MatchString(name) {
			continue
		}
		if b, err := hex.DecodeString(sum); err != nil || len(b) != sha256.Size {
			return Asset{}, fmt.Errorf("%s: bad checksum for %s", SumsFile, name)
		}
		found = append(found, Asset{Name: name, SHA256: sum})
	}
	if err := sc.Err(); err != nil {
		return Asset{}, err
	}
	switch len(found) {
	case 0:
		return Asset{}, fmt.Errorf("%s lists no palm-gw arm64 package", SumsFile)
	case 1:
		return found[0], nil
	default:
		return Asset{}, fmt.Errorf("%s lists %d packages; expected one", SumsFile, len(found))
	}
}

func (c Client) get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "palm-gw-upgrade")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

// Fetch downloads the release at base (a directory URL) into dir: its
// SHA256SUMS, then the package that file lists, which must match its sum.
// It returns the package's path.
func (c Client) Fetch(ctx context.Context, base, dir string) (string, Asset, error) {
	base = strings.TrimSuffix(base, "/")
	body, err := c.get(ctx, base+"/"+SumsFile)
	if err != nil {
		return "", Asset{}, err
	}
	asset, err := ParseSums(io.LimitReader(body, 1<<20))
	_ = body.Close()
	if err != nil {
		return "", Asset{}, err
	}
	body, err = c.get(ctx, base+"/"+asset.Name)
	if err != nil {
		return "", asset, err
	}
	defer func() { _ = body.Close() }()
	path := filepath.Join(dir, asset.Name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", asset, err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(body, 256<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", asset, fmt.Errorf("downloading %s: %w", asset.Name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != asset.SHA256 {
		_ = os.Remove(path)
		return "", asset, fmt.Errorf("%s: checksum mismatch (got %s, %s says %s)", asset.Name, got, SumsFile, asset.SHA256)
	}
	return path, asset, nil
}
