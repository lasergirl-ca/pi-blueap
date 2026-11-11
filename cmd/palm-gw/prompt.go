package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	"palm-gw/internal/setup"
)

// errAborted is returned when the user cancels the wizard.
var errAborted = errors.New("setup cancelled")

// huhPrompter asks on a terminal with charmbracelet/huh (PLAN.md section 2).
type huhPrompter struct{ out io.Writer }

func runField(f huh.Field) error {
	err := huh.NewForm(huh.NewGroup(f)).Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return errAborted
	}
	return err
}

func (p huhPrompter) Note(text string) { fmt.Fprintln(p.out, "  "+text) }

func (p huhPrompter) Input(title, help, def string, validate func(string) error) (string, error) {
	v := def
	in := huh.NewInput().Title(title).Value(&v)
	if help != "" {
		in = in.Description(help)
	}
	if validate != nil {
		in = in.Validate(validate)
	}
	if err := runField(in); err != nil {
		return "", err
	}
	fmt.Fprintf(p.out, "%s: %s\n", title, v)
	return strings.TrimSpace(v), nil
}

func (p huhPrompter) Password(title string, validate func(string) error) (string, error) {
	var v string
	in := huh.NewInput().Title(title).EchoMode(huh.EchoModePassword).Value(&v)
	if validate != nil {
		in = in.Validate(validate)
	}
	if err := runField(in); err != nil {
		return "", err
	}
	fmt.Fprintf(p.out, "%s: ********\n", title)
	return v, nil
}

func (p huhPrompter) Select(title, help string, opts []setup.Option, def string) (string, error) {
	v := def
	var hopts []huh.Option[string]
	label := map[string]string{}
	for _, o := range opts {
		hopts = append(hopts, huh.NewOption(o.Label, o.Value))
		label[o.Value] = o.Label
	}
	sel := huh.NewSelect[string]().Title(title).Options(hopts...).Value(&v)
	if help != "" {
		sel = sel.Description(help)
	}
	if err := runField(sel); err != nil {
		return "", err
	}
	fmt.Fprintf(p.out, "%s %s\n", title, label[v])
	return v, nil
}

func (p huhPrompter) Confirm(title, help string, def bool) (bool, error) {
	v := def
	c := huh.NewConfirm().Title(title).Value(&v)
	if help != "" {
		c = c.Description(help)
	}
	if err := runField(c); err != nil {
		return false, err
	}
	fmt.Fprintf(p.out, "%s %s\n", title, map[bool]string{true: "Yes", false: "No"}[v])
	return v, nil
}

// plainPrompter asks line by line, for when stdin is not a terminal (PLAN.md
// section 2: "falls back to plain prompts when not a TTY").
type plainPrompter struct {
	in  *bufio.Reader
	out io.Writer
	// fd, if a terminal, lets Password turn echo off.
	fd int
}

func newPlainPrompter(in io.Reader, out io.Writer) *plainPrompter {
	fd := -1
	if f, ok := in.(*os.File); ok {
		fd = int(f.Fd())
	}
	return &plainPrompter{in: bufio.NewReader(in), out: out, fd: fd}
}

func (p *plainPrompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%w: input ended", errAborted)
		}
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func (p *plainPrompter) Note(text string) { fmt.Fprintln(p.out, "  "+text) }

func (p *plainPrompter) Input(title, help, def string, validate func(string) error) (string, error) {
	if help != "" {
		fmt.Fprintln(p.out, help)
	}
	for {
		fmt.Fprintf(p.out, "%s [%s]: ", title, def)
		s, err := p.line()
		if err != nil {
			return "", err
		}
		if s = strings.TrimSpace(s); s == "" {
			s = def
		}
		if validate != nil {
			if err := validate(s); err != nil {
				fmt.Fprintf(p.out, "  %v\n", err)
				continue
			}
		}
		return s, nil
	}
}

func (p *plainPrompter) Password(title string, validate func(string) error) (string, error) {
	for {
		fmt.Fprintf(p.out, "%s: ", title)
		var s string
		if p.fd >= 0 && term.IsTerminal(p.fd) {
			b, err := term.ReadPassword(p.fd)
			fmt.Fprintln(p.out)
			if err != nil {
				return "", err
			}
			s = string(b)
		} else {
			var err error
			if s, err = p.line(); err != nil {
				return "", err
			}
		}
		if validate != nil {
			if err := validate(s); err != nil {
				fmt.Fprintf(p.out, "  %v\n", err)
				continue
			}
		}
		return s, nil
	}
}

func (p *plainPrompter) Select(title, help string, opts []setup.Option, def string) (string, error) {
	fmt.Fprintln(p.out, title)
	if help != "" {
		fmt.Fprintln(p.out, help)
	}
	defN := 1
	for i, o := range opts {
		fmt.Fprintf(p.out, "  %d) %s\n", i+1, o.Label)
		if o.Value == def {
			defN = i + 1
		}
	}
	for {
		fmt.Fprintf(p.out, "Choice [%d]: ", defN)
		s, err := p.line()
		if err != nil {
			return "", err
		}
		if s = strings.TrimSpace(s); s == "" {
			return opts[defN-1].Value, nil
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(opts) {
			return opts[n-1].Value, nil
		}
		for _, o := range opts {
			if s == o.Value {
				return o.Value, nil
			}
		}
		fmt.Fprintf(p.out, "  enter 1 to %d\n", len(opts))
	}
}

func (p *plainPrompter) Confirm(title, help string, def bool) (bool, error) {
	if help != "" {
		fmt.Fprintln(p.out, help)
	}
	hint := map[bool]string{true: "Y/n", false: "y/N"}[def]
	for {
		fmt.Fprintf(p.out, "%s [%s]: ", title, hint)
		s, err := p.line()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}
