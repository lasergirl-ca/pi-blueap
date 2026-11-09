package ctl

import (
	"flag"
	"io"
	"reflect"
	"testing"
)

func TestParseInterleaved(t *testing.T) {
	cases := []struct {
		args []string
		pos  []string
		one  bool
		sock string
	}{
		{[]string{"pair", "5", "--one"}, []string{"pair", "5"}, true, "def"},
		{[]string{"pair", "--one", "5"}, []string{"pair", "5"}, true, "def"},
		{[]string{"--one", "pair", "5"}, []string{"pair", "5"}, true, "def"},
		{[]string{"status", "-socket", "/tmp/x.sock"}, []string{"status"}, false, "/tmp/x.sock"},
		{[]string{"forget", "00:07:E0:3C:5E:FA", "-socket=/s"}, []string{"forget", "00:07:E0:3C:5E:FA"}, false, "/s"},
		{[]string{"pair", "--", "--one"}, []string{"pair", "--one"}, false, "def"},
		{[]string{"status"}, []string{"status"}, false, "def"},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		one := fs.Bool("one", false, "")
		sock := fs.String("socket", "def", "")
		pos, err := parseInterleaved(fs, c.args)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if !reflect.DeepEqual(pos, c.pos) || *one != c.one || *sock != c.sock {
			t.Errorf("%v: got pos=%v one=%v socket=%q, want pos=%v one=%v socket=%q",
				c.args, pos, *one, *sock, c.pos, c.one, c.sock)
		}
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if _, err := parseInterleaved(fs, []string{"pair", "--bogus"}); err == nil {
		t.Error("unknown flag after the command was accepted")
	}
}
