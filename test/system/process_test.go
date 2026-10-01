//go:build system

package system

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
)

// kill sends SIGKILL (no graceful shutdown at all).
func (c *cluster) kill(p *process) {
	c.t.Helper()
	_ = p.cmd.Process.Signal(syscall.SIGKILL)
	<-p.done
	c.remove(p)
}

// waitExit waits for a process to die by itself (fault point).
func (c *cluster) waitExit(p *process, timeout time.Duration) {
	c.t.Helper()
	select {
	case <-p.done:
		c.remove(p)
	case <-time.After(timeout):
		c.t.Fatalf("%s did not crash at its fault point", p.name)
	}
}

func (c *cluster) remove(p *process) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, q := range c.procs {
		if q == p {
			c.procs = append(c.procs[:i], c.procs[i+1:]...)
			return
		}
	}
}

// shutdown stops every process gracefully and fails on any reported data race.
func (c *cluster) shutdown() {
	c.mu.Lock()
	procs := append([]*process(nil), c.procs...)
	c.mu.Unlock()
	for _, p := range procs {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(30 * time.Second):
			c.t.Errorf("%s did not stop on SIGTERM", p.name)
			_ = p.cmd.Process.Kill()
		}
		if strings.Contains(p.stderr.String(), "DATA RACE") {
			c.t.Errorf("%s reported a data race:\n%s", p.name, p.stderr.String())
		}
		if p.cmd.ProcessState != nil && p.cmd.ProcessState.ExitCode() != 0 {
			c.t.Errorf("%s exited with %d on SIGTERM:\n%s", p.name, p.cmd.ProcessState.ExitCode(), p.stderr.String())
		}
	}
}

// pick returns live processes round-robin.
func (c *cluster) pick() *process {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.procs[c.next%len(c.procs)]
	c.next++
	return p
}

type reply struct {
	body   map[string]any
	status int
}

func (p *process) call(t *testing.T, method, path, token string, body any, headers ...string) reply {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, "http://"+p.addr+path, reader)
	req.Header.Set("Authorization", "Bearer "+token)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil { // Errorf (not Fatalf): call runs inside goroutines too
		t.Errorf("%s %s on %s: %v", method, path, p.name, err)
		return reply{}
	}
	defer res.Body.Close()
	out := reply{status: res.StatusCode, body: map[string]any{}}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out.body)
	return out
}
