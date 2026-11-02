package service

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestIsDaemonCmdline(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{[]string{"/usr/bin/hypr-ws-brightness", "daemon"}, true},
		{[]string{"hypr-ws-brightness", "--config", "/x.toml", "daemon", "-v"}, true},
		{[]string{"/home/u/.local/bin/hypr-ws-brightness", "daemon", "--verbose"}, true},
		{[]string{"hypr-ws-brightness", "tui"}, false},
		{[]string{"hypr-ws-brightness"}, false},
		{[]string{"fish", "-c", "hypr-ws-brightness daemon"}, false},
		{[]string{"/usr/bin/pkill", "-f", "hypr-ws-brightness", "daemon"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := IsDaemonCmdline(c.argv); got != c.want {
			t.Errorf("IsDaemonCmdline(%q) = %v, want %v", c.argv, got, c.want)
		}
	}
}

func TestFindDaemonsFakeProc(t *testing.T) {
	root := t.TempDir()
	write := func(pid int, cmdline string) {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(4242, "/usr/bin/hypr-ws-brightness\x00daemon\x00")
	write(17, "hypr-ws-brightness\x00tui\x00")
	write(99, "/home/u/.local/bin/hypr-ws-brightness\x00daemon\x00-v\x00")
	write(os.Getpid(), "hypr-ws-brightness\x00daemon\x00")
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}

	old := procRoot
	procRoot = root
	defer func() { procRoot = old }()

	got, err := FindDaemons()
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{99, 4242}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FindDaemons() = %v, want %v", got, want)
	}
}
