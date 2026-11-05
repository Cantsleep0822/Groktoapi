// This standalone supervisor is shared with the grok2api deployment adapter.
// It keeps cloudflared and the application in one container resource budget.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type tunnelConfig struct {
	args     []string
	env      []string
	listen   string
	readyURL string
	timeout  time.Duration
}

var hostnamePattern = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

func configure(role string, getenv func(string) string) (tunnelConfig, error) {
	c := tunnelConfig{timeout: 30 * time.Second}
	// Do not hand application/database credentials or proxy variables to cloudflared.
	for _, key := range []string{"PATH", "HOME", "USER", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR", "SystemRoot", "WINDIR"} {
		if value := getenv(key); value != "" {
			c.env = append(c.env, key+"="+value)
		}
	}
	c.env = append(c.env, "GOMAXPROCS=1", "GOMEMLIMIT=96MiB", "GOGC=75")
	switch role {
	case "server":
		token := strings.TrimSpace(getenv("RESIN_CF_TUNNEL_TOKEN"))
		if token == "" {
			return c, errors.New("RESIN_CF_TUNNEL_TOKEN is required")
		}
		c.env = append(c.env, "TUNNEL_TOKEN="+token)
		c.args = []string{"--no-autoupdate", "tunnel", "--protocol", "http2", "--edge-ip-version", "4", "--ha-connections", "1", "--metrics", "127.0.0.1:20241", "run"}
		c.readyURL = "http://127.0.0.1:20241/ready"
		c.timeout = 90 * time.Second
	case "client":
		host := strings.TrimSpace(getenv("GROK2API_CF_ACCESS_HOSTNAME"))
		if !hostnamePattern.MatchString(host) || !strings.Contains(host, ".") || strings.Contains(host, "..") {
			return c, errors.New("GROK2API_CF_ACCESS_HOSTNAME must be a hostname without scheme, port or path")
		}
		id, secret := getenv("TUNNEL_SERVICE_TOKEN_ID"), getenv("TUNNEL_SERVICE_TOKEN_SECRET")
		if strings.TrimSpace(id) == "" || strings.TrimSpace(secret) == "" {
			return c, errors.New("TUNNEL_SERVICE_TOKEN_ID and TUNNEL_SERVICE_TOKEN_SECRET are required for unattended Access authentication")
		}
		portText := getenv("GROK2API_CF_ACCESS_PORT")
		if portText == "" {
			portText = "2261"
		}
		port, err := strconv.Atoi(portText)
		appPort, _ := strconv.Atoi(getenv("PORT"))
		if err != nil || port < 1024 || port > 65535 || port == 8000 || port == appPort {
			return c, errors.New("GROK2API_CF_ACCESS_PORT must be an unprivileged port distinct from the application port")
		}
		c.listen = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		c.args = []string{"access", "tcp", "--hostname", host, "--url", c.listen}
		c.env = append(c.env, "TUNNEL_SERVICE_TOKEN_ID="+id, "TUNNEL_SERVICE_TOKEN_SECRET="+secret)
	default:
		return c, errors.New("expected server or client mode")
	}
	return c, nil
}

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func start(cmd *exec.Cmd) (*child, error) {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &child{cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *child) stop() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func waitReady(ctx context.Context, c tunnelConfig, p *child) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	for {
		select {
		case <-ctx.Done():
			return errors.New("cloudflared readiness timed out or startup was cancelled")
		case <-p.done:
			return errors.New("cloudflared exited before readiness")
		case <-ticker.C:
			if c.listen != "" {
				conn, err := net.DialTimeout("tcp4", c.listen, 100*time.Millisecond)
				if err == nil {
					_ = conn.Close()
					return nil
				}
			} else {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.readyURL, nil)
				response, err := client.Do(req)
				if err == nil {
					_ = response.Body.Close()
					if response.StatusCode == http.StatusOK {
						return nil
					}
				}
			}
		}
	}
}

func applicationEnv() []string {
	var result []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "RESIN_CF_TUNNEL_TOKEN", "TUNNEL_TOKEN", "TUNNEL_SERVICE_TOKEN_ID", "TUNNEL_SERVICE_TOKEN_SECRET":
			continue
		}
		result = append(result, item)
	}
	return result
}

func supervise(ctx context.Context, c tunnelConfig, appArgs []string) error {
	if len(appArgs) == 0 {
		return errors.New("application command is required")
	}
	if c.listen != "" {
		listener, err := net.Listen("tcp4", c.listen)
		if err != nil {
			return errors.New("local Cloudflare Access port is already occupied")
		}
		_ = listener.Close()
	}
	command := exec.Command("cloudflared", c.args...)
	command.Env = c.env
	tunnel, err := start(command)
	if err != nil {
		return fmt.Errorf("start cloudflared: %w", err)
	}
	defer tunnel.stop()
	if err := waitReady(ctx, c, tunnel); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	// Client readiness confirms the local listener, not the remote Access policy.
	fmt.Fprintln(os.Stderr, "cloudflared supervisor: local tunnel ready; starting application")
	command = exec.Command(appArgs[0], appArgs[1:]...)
	command.Env = applicationEnv()
	app, err := start(command)
	if err != nil {
		return fmt.Errorf("start application: %w", err)
	}
	defer app.stop()
	select {
	case <-ctx.Done():
		return nil
	case <-tunnel.done:
		return errors.New("cloudflared exited; stopping application so the platform can restart the container")
	case <-app.done:
		return app.err
	}
}

func run() error {
	if len(os.Args) < 4 || os.Args[2] != "--" {
		return errors.New("usage: cloudflared-supervisor server|client -- application [arguments]")
	}
	c, err := configure(os.Args[1], os.Getenv)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return supervise(ctx, c, os.Args[3:])
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cloudflared supervisor:", err)
		os.Exit(1)
	}
}
