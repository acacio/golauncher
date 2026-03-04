package main

import (
	"bytes"
	"io"
	"log"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// writeConfig creates a temp file with the given content and registers cleanup.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "golauncher-test-*.textproto")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

// ---------------------------------------------------------------------------
// unquote
// ---------------------------------------------------------------------------

func TestUnquote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"hello", "hello"},
		{`"simple"`, "simple"},
		{`"with \"quote\""`, `with "quote"`},
		{`"with \\ backslash"`, `with \ backslash`},
		{`"both \" and \\"`, `both " and \`},
		{`"x"`, "x"},
		{`"unclosed`, `"unclosed`},
	}
	for _, tc := range cases {
		got := unquote(tc.in)
		if got != tc.want {
			t.Errorf("unquote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// parseConfig
// ---------------------------------------------------------------------------

func TestParseConfig(t *testing.T) {
	t.Run("FileNotFound", func(t *testing.T) {
		_, err := parseConfig("/nonexistent/path/config.textproto")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("EmptyFile", func(t *testing.T) {
		path := writeConfig(t, "")
		cfg, err := parseConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Processes) != 0 {
			t.Errorf("expected 0 processes, got %d", len(cfg.Processes))
		}
	})

	t.Run("CommentsAndBlanks", func(t *testing.T) {
		path := writeConfig(t, "# comment\n\n# another\n")
		cfg, err := parseConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Processes) != 0 {
			t.Errorf("expected 0 processes, got %d", len(cfg.Processes))
		}
	})

	t.Run("SingleProcessNameCommand", func(t *testing.T) {
		path := writeConfig(t, "process {\n  name: \"web\"\n  command: \"/bin/sh\"\n}\n")
		cfg, err := parseConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Processes) != 1 {
			t.Fatalf("expected 1 process, got %d", len(cfg.Processes))
		}
		p := cfg.Processes[0]
		if p.Name != "web" {
			t.Errorf("name = %q, want %q", p.Name, "web")
		}
		if p.Command != "/bin/sh" {
			t.Errorf("command = %q, want %q", p.Command, "/bin/sh")
		}
	})

	t.Run("AllFields", func(t *testing.T) {
		path := writeConfig(t, "process {\n  name: \"app\"\n  command: \"/bin/sh\"\n  args: \"-c\"\n  args: \"echo hi\"\n  env: \"FOO=bar\"\n  env: \"BAZ=qux\"\n}\n")
		cfg, err := parseConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Processes) != 1 {
			t.Fatalf("expected 1 process, got %d", len(cfg.Processes))
		}
		p := cfg.Processes[0]
		if len(p.Args) != 2 || p.Args[0] != "-c" || p.Args[1] != "echo hi" {
			t.Errorf("args = %v, want [-c, echo hi]", p.Args)
		}
		if len(p.Env) != 2 || p.Env[0] != "FOO=bar" || p.Env[1] != "BAZ=qux" {
			t.Errorf("env = %v, want [FOO=bar BAZ=qux]", p.Env)
		}
	})

	t.Run("MultipleProcesses", func(t *testing.T) {
		path := writeConfig(t, "process {\n  name: \"a\"\n  command: \"/bin/sh\"\n}\nprocess {\n  name: \"b\"\n  command: \"/bin/sh\"\n}\nprocess {\n  name: \"c\"\n  command: \"/bin/sh\"\n}\n")
		cfg, err := parseConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Processes) != 3 {
			t.Fatalf("expected 3 processes, got %d", len(cfg.Processes))
		}
		for i, name := range []string{"a", "b", "c"} {
			if cfg.Processes[i].Name != name {
				t.Errorf("process[%d].Name = %q, want %q", i, cfg.Processes[i].Name, name)
			}
		}
	})

	t.Run("UnknownFieldIgnored", func(t *testing.T) {
		path := writeConfig(t, "process {\n  name: \"app\"\n  command: \"/bin/sh\"\n  unknown: \"value\"\n}\n")
		cfg, err := parseConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Processes) != 1 {
			t.Fatalf("expected 1 process, got %d", len(cfg.Processes))
		}
	})

	t.Run("FieldOutsideBlock", func(t *testing.T) {
		path := writeConfig(t, "name: \"toplevel\"\nprocess {\n  name: \"app\"\n  command: \"/bin/sh\"\n}\n")
		cfg, err := parseConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Processes) != 1 {
			t.Fatalf("expected 1 process, got %d", len(cfg.Processes))
		}
	})

	t.Run("UnquotedValue", func(t *testing.T) {
		path := writeConfig(t, "process {\n  name: unquoted\n  command: /bin/sh\n}\n")
		cfg, err := parseConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Processes) != 1 {
			t.Fatalf("expected 1 process, got %d", len(cfg.Processes))
		}
		p := cfg.Processes[0]
		if p.Name != "unquoted" {
			t.Errorf("name = %q, want %q", p.Name, "unquoted")
		}
		if p.Command != "/bin/sh" {
			t.Errorf("command = %q, want %q", p.Command, "/bin/sh")
		}
	})
}

// ---------------------------------------------------------------------------
// lineWriter
// ---------------------------------------------------------------------------

func TestLineWriter(t *testing.T) {
	t.Run("SingleLine", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &lineWriter{dst: &buf, prefix: "pfx"}
		n, err := lw.Write([]byte("hello\n"))
		if err != nil {
			t.Fatal(err)
		}
		if n != 6 {
			t.Errorf("n = %d, want 6", n)
		}
		want := "[pfx] hello\n"
		if got := buf.String(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("MultipleLines", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &lineWriter{dst: &buf, prefix: "p"}
		lw.Write([]byte("line1\nline2\nline3\n")) //nolint:errcheck
		want := "[p] line1\n[p] line2\n[p] line3\n"
		if got := buf.String(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("SplitAcrossWrites", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &lineWriter{dst: &buf, prefix: "p"}
		lw.Write([]byte("hel"))  //nolint:errcheck
		lw.Write([]byte("lo\n")) //nolint:errcheck
		want := "[p] hello\n"
		if got := buf.String(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("NoTrailingNewline", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &lineWriter{dst: &buf, prefix: "p"}
		lw.Write([]byte("partial")) //nolint:errcheck
		if got := buf.String(); got != "" {
			t.Errorf("expected no output, got %q", got)
		}
	})

	t.Run("EmptyWrite", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &lineWriter{dst: &buf, prefix: "p"}
		n, err := lw.Write([]byte{})
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("n = %d, want 0", n)
		}
		if buf.Len() != 0 {
			t.Error("expected empty output")
		}
	})

	t.Run("OnlyNewline", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &lineWriter{dst: &buf, prefix: "pfx"}
		lw.Write([]byte("\n")) //nolint:errcheck
		want := "[pfx] \n"
		if got := buf.String(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("EmptyPrefix", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &lineWriter{dst: &buf, prefix: ""}
		lw.Write([]byte("msg\n")) //nolint:errcheck
		want := "[] msg\n"
		if got := buf.String(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("MultiplePartialWrites", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &lineWriter{dst: &buf, prefix: "x"}
		lw.Write([]byte("ab"))    //nolint:errcheck
		lw.Write([]byte("c\nd"))  //nolint:errcheck
		lw.Write([]byte("ef\n"))  //nolint:errcheck
		want := "[x] abc\n[x] def\n"
		if got := buf.String(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// launch — integration tests
// ---------------------------------------------------------------------------

func runLaunch(t *testing.T, cfg *config, stop chan os.Signal) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- launch(cfg, stop) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for launch to return")
		return nil
	}
}

func TestLaunch(t *testing.T) {
	t.Run("SingleProcess", func(t *testing.T) {
		cfg := &config{
			Processes: []process{
				{Name: "echo", Command: "echo", Args: []string{"hello"}},
			},
		}
		if err := runLaunch(t, cfg, make(chan os.Signal, 1)); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("MultipleProcessesAllExit", func(t *testing.T) {
		cfg := &config{
			Processes: []process{
				{Name: "a", Command: "echo", Args: []string{"a"}},
				{Name: "b", Command: "echo", Args: []string{"b"}},
				{Name: "c", Command: "echo", Args: []string{"c"}},
			},
		}
		if err := runLaunch(t, cfg, make(chan os.Signal, 1)); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("OneExits_TerminatesOthers", func(t *testing.T) {
		cfg := &config{
			Processes: []process{
				{Name: "fast", Command: "echo", Args: []string{"done"}},
				{Name: "slow", Command: "sh", Args: []string{"-c", "sleep 100"}},
			},
		}
		if err := runLaunch(t, cfg, make(chan os.Signal, 1)); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("StopChannel_SIGTERMForwarded", func(t *testing.T) {
		cfg := &config{
			Processes: []process{
				{Name: "a", Command: "sh", Args: []string{"-c", "sleep 100"}},
				{Name: "b", Command: "sh", Args: []string{"-c", "sleep 100"}},
			},
		}
		stop := make(chan os.Signal, 1)
		done := make(chan error, 1)
		go func() { done <- launch(cfg, stop) }()
		time.Sleep(200 * time.Millisecond)
		stop <- syscall.SIGTERM
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for launch to return after SIGTERM")
		}
	})

	t.Run("StartFailure_NonExistent", func(t *testing.T) {
		cfg := &config{
			Processes: []process{
				{Name: "bad", Command: "/nonexistent/binary"},
			},
		}
		err := runLaunch(t, cfg, make(chan os.Signal, 1))
		if err == nil {
			t.Fatal("expected error for non-existent binary")
		}
		if !strings.Contains(err.Error(), "start") {
			t.Errorf("error %q does not contain 'start'", err.Error())
		}
	})

	t.Run("PartialStartFailure", func(t *testing.T) {
		cfg := &config{
			Processes: []process{
				{Name: "good", Command: "sh", Args: []string{"-c", "sleep 100"}},
				{Name: "bad", Command: "/nonexistent/binary"},
			},
		}
		err := runLaunch(t, cfg, make(chan os.Signal, 1))
		if err == nil {
			t.Fatal("expected error when second process fails to start")
		}
	})

	t.Run("WithEnv", func(t *testing.T) {
		cfg := &config{
			Processes: []process{
				{Name: "env-test", Command: "sh", Args: []string{"-c", "echo $VAR"}, Env: []string{"VAR=hi"}},
			},
		}
		if err := runLaunch(t, cfg, make(chan os.Signal, 1)); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
