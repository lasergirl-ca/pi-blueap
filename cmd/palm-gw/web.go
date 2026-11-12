package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/setup"
)

// cmdWeb is `palm-gw web set-password` (PLAN.md section 8.12): it sets the
// admin's (or, with --viewer, the read-only account's) password in
// config.toml and reloads palm-gwd, which applies accounts live (D-051).
func cmdWeb(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "set-password" {
		fmt.Fprintln(stderr, "Usage: palm-gw web set-password [--viewer] [--user NAME] [--config PATH]")
		return 2
	}
	fs := flag.NewFlagSet("palm-gw web set-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	viewer := fs.Bool("viewer", false, "set the read-only viewer account's password instead of the admin's")
	user := fs.String("user", "", "also set the account's username")
	cfgPath := fs.String("config", config.DefaultPath, "configuration file")
	socket := fs.String("socket", ctl.DefaultSocket, "control socket, for the reload")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "palm-gw web set-password: run it as root: sudo palm-gw web set-password")
		return 1
	}
	data, err := os.ReadFile(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "palm-gw web:", err)
		return 1
	}
	cur, err := config.Parse(string(data))
	if err != nil {
		fmt.Fprintln(stderr, "palm-gw web:", err)
		return 1
	}
	name := cur.Web.Username
	userKey, hashKey := "username", "password_hash"
	if *viewer {
		name = cur.Web.ViewerUsername
		userKey, hashKey = "viewer_username", "viewer_password_hash"
	}
	if *user != "" {
		name = *user
	}
	if name == "" {
		fmt.Fprintln(stderr, "palm-gw web set-password: the viewer account has no username yet; give one with --user")
		return 2
	}

	p := newPlainPrompter(stdin, stdout)
	fmt.Fprintf(stdout, "New web UI password for %q", name)
	if *viewer {
		fmt.Fprint(stdout, " (read-only)")
	}
	fmt.Fprintln(stdout)
	var pw string
	for {
		a, err := p.Password("Password", setup.CheckNewPassword)
		if err != nil {
			fmt.Fprintln(stderr, "palm-gw web:", err)
			return 1
		}
		b, err := p.Password("Again", nil)
		if err != nil {
			fmt.Fprintln(stderr, "palm-gw web:", err)
			return 1
		}
		if a == b {
			pw = a
			break
		}
		fmt.Fprintln(stdout, "  The two do not match; try again.")
	}
	hash, err := setup.HashPassword(pw)
	if err != nil {
		fmt.Fprintln(stderr, "palm-gw web:", err)
		return 1
	}
	text := setup.SetKeys(string(data), []setup.KV{
		{Section: "web", Key: userKey, Value: setup.TOMLString(name)},
		{Section: "web", Key: hashKey, Value: setup.TOMLString(hash)},
	})
	next, err := config.Parse(text)
	if err == nil {
		err = next.Validate()
	}
	if err != nil {
		fmt.Fprintln(stderr, "palm-gw web: not saved:", err)
		return 1
	}
	if err := writeConfig(*cfgPath, text); err != nil {
		fmt.Fprintln(stderr, "palm-gw web:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Password set for %q in %s.\n", name, *cfgPath)

	var r ctl.ReloadReply
	if err := ctl.Call(*socket, ctl.Request{Cmd: "reload"}, &r); err != nil {
		if errors.Is(err, ctl.ErrNotRunning) {
			fmt.Fprintln(stdout, "palm-gwd is not running; it will use the new password when it starts.")
			return 0
		}
		fmt.Fprintln(stderr, "palm-gw web: saved, but reloading palm-gwd failed:", err)
		return 1
	}
	fmt.Fprintln(stdout, "palm-gwd reloaded: the new password works now. Sessions already logged in stay logged in.")
	return 0
}
