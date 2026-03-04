package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
)

type process struct {
	Name    string
	Command string
	Args    []string
	Env     []string
}

type config struct {
	Processes []process
}

// lineWriter prefixes each output line with [name] before writing to dst.
type lineWriter struct {
	dst    io.Writer
	prefix string
	buf    []byte
}

func (lw *lineWriter) Write(p []byte) (int, error) {
	lw.buf = append(lw.buf, p...)
	for {
		i := bytes.IndexByte(lw.buf, '\n')
		if i < 0 {
			break
		}
		fmt.Fprintf(lw.dst, "[%s] %s\n", lw.prefix, lw.buf[:i])
		lw.buf = lw.buf[i+1:]
	}
	return len(p), nil
}

// parseConfig reads a minimal subset of textproto format.
//
// Supported syntax:
//
//	process {
//	  name: "label"
//	  command: "/path/to/binary"
//	  args: "arg1"
//	  args: "arg2"
//	  env: "KEY=VALUE"
//	}
func parseConfig(path string) (*config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := &config{}
	var cur *process
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "process {" {
			cur = &process{}
			continue
		}
		if line == "}" {
			if cur != nil {
				cfg.Processes = append(cfg.Processes, *cur)
				cur = nil
			}
			continue
		}
		if cur == nil {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = unquote(strings.TrimSpace(val))
		switch key {
		case "name":
			cur.Name = val
		case "command":
			cur.Command = val
		case "args":
			cur.Args = append(cur.Args, val)
		case "env":
			cur.Env = append(cur.Env, val)
		}
	}
	return cfg, scanner.Err()
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
		s = strings.ReplaceAll(s, `\"`, `"`)
		s = strings.ReplaceAll(s, `\\`, `\`)
	}
	return s
}

func launch(cfg *config, stop <-chan os.Signal) error {
	cmds := make([]*exec.Cmd, len(cfg.Processes))
	for i, p := range cfg.Processes {
		cmd := exec.Command(p.Command, p.Args...)
		cmd.Env = append(os.Environ(), p.Env...)
		cmd.Stdout = &lineWriter{dst: os.Stdout, prefix: p.Name}
		cmd.Stderr = &lineWriter{dst: os.Stderr, prefix: p.Name}
		cmds[i] = cmd
	}

	// Start all processes; on failure, clean up already-started ones.
	for i, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			for j := 0; j < i; j++ {
				cmds[j].Process.Signal(syscall.SIGTERM) //nolint:errcheck
				cmds[j].Wait()                          //nolint:errcheck
			}
			return fmt.Errorf("[%s] start: %w", cfg.Processes[i].Name, err)
		}
		log.Printf("[%s] started pid=%d", cfg.Processes[i].Name, cmd.Process.Pid)
	}

	// firstExit is signalled when any child exits.
	firstExit := make(chan struct{}, 1)
	var wg sync.WaitGroup

	for i, cmd := range cmds {
		wg.Add(1)
		go func(cmd *exec.Cmd, name string) {
			defer wg.Done()
			if err := cmd.Wait(); err != nil {
				log.Printf("[%s] exited: %v", name, err)
			} else {
				log.Printf("[%s] exited ok", name)
			}
			select {
			case firstExit <- struct{}{}:
			default:
			}
		}(cmd, cfg.Processes[i].Name)
	}

	// Block until a signal arrives or the first child exits.
	select {
	case sig := <-stop:
		log.Printf("received %v, shutting down", sig)
	case <-firstExit:
		log.Println("process exited, shutting down all")
	}

	// Forward SIGTERM to every still-running child.
	for i, cmd := range cmds {
		if cmd.Process == nil {
			continue
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			log.Printf("[%s] sigterm: %v", cfg.Processes[i].Name, err)
		}
	}

	wg.Wait()
	log.Println("shutdown complete")
	return nil
}

func main() {
	log.SetFlags(log.Ltime)

	configPath := "golauncher.cfg"
	if len(os.Args) >= 2 {
		configPath = os.Args[1]
	}

	cfg, err := parseConfig(configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if len(cfg.Processes) == 0 {
		log.Fatal("no processes defined in config")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	if err := launch(cfg, sigCh); err != nil {
		log.Fatalf("launch: %v", err)
	}
}
