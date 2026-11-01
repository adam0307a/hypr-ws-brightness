package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIVersionAndHelp(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"version"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("version exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "hypr-ws-brightness") {
		t.Errorf("unexpected version output: %q", out.String())
	}

	out.Reset()
	code = run([]string{"--help"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("--help exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Errorf("unexpected help output: %q", out.String())
	}
}

func TestCLIInitCreatesConfigAndPrintsExecOnce(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "hypr-ws-brightness", "config.toml")

	var out, errBuf bytes.Buffer
	code := run([]string{"init", "--config", cfgPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("init exit code = %d, stderr=%s", code, errBuf.String())
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("expected config file at %q: %v", cfgPath, err)
	}
	if !strings.Contains(out.String(), "exec-once = hypr-ws-brightness daemon") {
		t.Errorf("expected exec-once snippet in output, got:\n%s", out.String())
	}

	// Running init again without --force preserves existing file.
	if err := os.WriteFile(cfgPath, []byte("default_brightness = 88\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code = run([]string{"init", "--config", cfgPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("second init exit code = %d", code)
	}
	content, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(content), "88") {
		t.Errorf("init without --force should not overwrite existing file")
	}
}
