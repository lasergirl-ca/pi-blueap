package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"palm-gw/internal/release"
	"palm-gw/internal/setup"
)

// cmdUpgrade is `palm-gw upgrade` (PLAN.md section 8.11): fetch a release's
// .deb, check it against the release's SHA256SUMS and install it with apt,
// whose postinst re-applies the existing config (D-043). `--check` only
// reports whether a newer release exists; it needs no root and is never run
// automatically.
func cmdUpgrade(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("palm-gw upgrade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "only report whether a newer release exists")
	want := fs.String("version", "", "install this release tag (e.g. v1.2.0) instead of the latest")
	from := fs.String("from", "", "install from this release directory (an https:// or file:// URL holding SHA256SUMS and the .deb) instead of GitHub")
	force := fs.Bool("force", false, "install even if the release is not newer than the installed package")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: palm-gw upgrade [--check] [--version vX.Y.Z | --from URL] [--force]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || (*want != "" && *from != "") {
		fs.Usage()
		return 2
	}
	if *want != "" && !release.ValidTag(*want) {
		fmt.Fprintf(stderr, "palm-gw upgrade: %q is not a release tag (e.g. v1.2.0)\n", *want)
		return 2
	}

	installed, err := dpkgVersion()
	if err != nil {
		if currentLayout() == setup.SourceLayout {
			fmt.Fprintln(stderr, "palm-gw upgrade: this palm-gw was installed from source (make install).")
			fmt.Fprintln(stderr, "Upgrade it the same way: git pull && sudo make install")
		} else {
			fmt.Fprintf(stderr, "palm-gw upgrade: the palm-gw package is not installed: %v\n", err)
		}
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var c release.Client

	// Work out the target, unless it comes from a directory whose version we
	// only learn by downloading.
	tag, base := *want, *from
	if base == "" {
		if tag == "" {
			if tag, err = c.Latest(ctx); err != nil {
				fmt.Fprintf(stderr, "palm-gw upgrade: %v\n", err)
				return 1
			}
		}
		base = c.BaseURL(tag)
		newer, err := versionNewer(release.DebVersion(tag), installed)
		if err != nil {
			fmt.Fprintf(stderr, "palm-gw upgrade: %v\n", err)
			return 1
		}
		if *check {
			if newer {
				fmt.Fprintf(stdout, "palm-gw %s is available (installed: %s). Install it with: sudo palm-gw upgrade\n", tag, installed)
			} else {
				fmt.Fprintf(stdout, "palm-gw is up to date (installed: %s, latest release: %s)\n", installed, tag)
			}
			return 0
		}
		if !newer && !*force {
			fmt.Fprintf(stdout, "palm-gw is up to date (installed: %s, release: %s). Use --force to reinstall it.\n", installed, tag)
			return 0
		}
	} else if *check {
		fmt.Fprintln(stderr, "palm-gw upgrade: --check asks GitHub; it does not take --from")
		return 2
	}

	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "palm-gw upgrade: run it as root: sudo palm-gw upgrade")
		return 1
	}

	// apt reads a local .deb as the _apt user where it can, so the directory
	// and the file must be readable by others.
	dir, err := os.MkdirTemp("", "palm-gw-upgrade-")
	if err != nil {
		fmt.Fprintf(stderr, "palm-gw upgrade: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "palm-gw upgrade: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Downloading %s/\n", strings.TrimSuffix(base, "/"))
	deb, asset, err := c.Fetch(ctx, base, dir)
	if err != nil {
		fmt.Fprintf(stderr, "palm-gw upgrade: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s: checksum OK (%s)\n", asset.Name, asset.SHA256)

	target, err := debFileVersion(deb)
	if err != nil {
		fmt.Fprintf(stderr, "palm-gw upgrade: %v\n", err)
		return 1
	}
	newer, err := versionNewer(target, installed)
	if err != nil {
		fmt.Fprintf(stderr, "palm-gw upgrade: %v\n", err)
		return 1
	}
	if !newer && !*force {
		fmt.Fprintf(stdout, "palm-gw is up to date (installed: %s, offered: %s). Use --force to reinstall it.\n", installed, target)
		return 0
	}

	fmt.Fprintf(stdout, "Installing palm-gw %s over %s\n", target, installed)
	apt := exec.CommandContext(ctx, "apt-get", "install", "-y", "--allow-downgrades", deb)
	apt.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	apt.Stdout, apt.Stderr = stdout, stderr
	if err := apt.Run(); err != nil {
		fmt.Fprintf(stderr, "palm-gw upgrade: apt-get install failed: %v\n", err)
		return 1
	}
	now, err := dpkgVersion()
	if err != nil || now != target {
		fmt.Fprintf(stderr, "palm-gw upgrade: the package reports version %q after the install, expected %s\n", now, target)
		return 1
	}
	fmt.Fprintf(stdout, "palm-gw upgraded: %s -> %s. Config and paired devices are unchanged.\n", installed, now)
	return 0
}

// dpkgVersion is the installed palm-gw package's version.
func dpkgVersion() (string, error) {
	out, err := exec.Command("dpkg-query", "-W", "-f=${Status}\t${Version}", "palm-gw").Output()
	if err != nil {
		return "", errors.New("dpkg-query does not know palm-gw")
	}
	status, ver, ok := strings.Cut(strings.TrimSpace(string(out)), "\t")
	if !ok || !strings.HasSuffix(status, " installed") || ver == "" {
		return "", fmt.Errorf("package state %q", status)
	}
	return ver, nil
}

// debFileVersion reads the Version field of a .deb file.
func debFileVersion(path string) (string, error) {
	out, err := exec.Command("dpkg-deb", "-f", path, "Version").Output()
	if err != nil {
		return "", fmt.Errorf("%s is not a readable package: %w", path, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// versionNewer reports whether Debian version a sorts after b.
func versionNewer(a, b string) (bool, error) {
	err := exec.Command("dpkg", "--compare-versions", a, "gt", b).Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return false, nil
	default:
		return false, fmt.Errorf("comparing versions %q and %q: %w", a, b, err)
	}
}
