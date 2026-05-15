package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type notifyFn func(event string, payload any)

type singboxSupervisor struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	port    int
	cfgPath string
	bin     string
	notify  notifyFn
}

func newSupervisor(notify notifyFn) *singboxSupervisor {
	return &singboxSupervisor{notify: notify}
}

func (s *singboxSupervisor) ensureBin() error {
	if s.bin != "" {
		if _, err := os.Stat(s.bin); err == nil {
			return nil
		}
		s.bin = ""
	}
	bin, err := locateSingBox()
	if err != nil {
		return err
	}
	s.bin = bin
	return nil
}

func locateSingBox() (string, error) {
	if env := os.Getenv("SINGBOX_BIN"); env != "" {
		if info, err := os.Stat(env); err == nil && !info.IsDir() {
			return env, nil
		}
	}
	if exePath, err := os.Executable(); err == nil {
		dir := filepath.Dir(exePath)
		for _, candidate := range []string{
			filepath.Join(dir, "sing-box"),
			filepath.Join(dir, "embed", "sing-box"),
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	if path, err := exec.LookPath("sing-box"); err == nil {
		return path, nil
	}
	return "", errors.New("sing-box binary not found (set SINGBOX_BIN or place beside helper)")
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("unexpected listener address")
	}
	return addr.Port, nil
}

func waitPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(80 * time.Millisecond)
	}
	return fmt.Errorf("port %d not ready", port)
}

func injectSocksPort(raw json.RawMessage, port int) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("config not json: %w", err)
	}
	inbounds, ok := doc["inbounds"].([]any)
	if !ok || len(inbounds) == 0 {
		return nil, errors.New("config missing inbounds")
	}
	patched := false
	for _, ib := range inbounds {
		m, ok := ib.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "socks" {
			m["listen_port"] = port
			patched = true
		}
	}
	if !patched {
		return nil, errors.New("config has no socks inbound")
	}
	return json.MarshalIndent(doc, "", "  ")
}

// defaultPhysicalInterface returns the name of an up, non-loopback, non-tunnel
// interface that has at least one IPv4 address. Used to bypass TUN-mode VPNs
// that would otherwise mangle the proxy's outbound TLS/REALITY handshake.
func defaultPhysicalInterface() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	skipPrefix := []string{"utun", "tun", "tap", "ppp", "awdl", "llw", "bridge", "ap", "anpi", "gif", "stf"}
	var fallback string
	for _, iface := range ifs {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		skip := false
		for _, p := range skipPrefix {
			if strings.HasPrefix(iface.Name, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		hasV4 := false
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip.To4() != nil {
				hasV4 = true
				break
			}
		}
		if !hasV4 {
			continue
		}
		if iface.Name == "en0" {
			return "en0"
		}
		if fallback == "" {
			fallback = iface.Name
		}
	}
	return fallback
}

var proxyOutboundTypes = map[string]bool{
	"vless": true, "vmess": true, "trojan": true, "shadowsocks": true,
	"hysteria": true, "hysteria2": true, "tuic": true, "wireguard": true,
	"anytls": true, "shadowtls": true, "ssh": true, "socks": true, "http": true,
}

func isProxyOutboundType(t string) bool {
	return proxyOutboundTypes[t]
}

func injectBindInterface(raw []byte, iface string) ([]byte, error) {
	if iface == "" {
		return raw, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	obs, ok := doc["outbounds"].([]any)
	if !ok {
		return raw, nil
	}
	for _, ob := range obs {
		m, ok := ob.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); isProxyOutboundType(t) {
			m["bind_interface"] = iface
		}
	}
	return json.MarshalIndent(doc, "", "  ")
}

func writeTempConfig(payload []byte) (string, error) {
	dir := filepath.Join(os.TempDir(), "noctis")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (s *singboxSupervisor) start(raw json.RawMessage) (int, error) {
	if err := s.ensureBin(); err != nil {
		return 0, err
	}

	s.mu.Lock()
	if s.cmd != nil {
		s.mu.Unlock()
		return 0, errors.New("already running")
	}
	s.mu.Unlock()

	port, err := freePort()
	if err != nil {
		return 0, fmt.Errorf("pick port: %w", err)
	}
	patched, err := injectSocksPort(raw, port)
	if err != nil {
		return 0, err
	}
	if iface := defaultPhysicalInterface(); iface != "" {
		if p2, err := injectBindInterface(patched, iface); err == nil {
			patched = p2
			fmt.Fprintf(os.Stderr, "noctis-host: bind_interface=%s\n", iface)
		}
	}
	cfgPath, err := writeTempConfig(patched)
	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, s.bin, "run", "-c", cfgPath)
	cmd.Stdout = newLogPipe(s.notify, "stdout")
	cmd.Stderr = newLogPipe(s.notify, "stderr")

	if err := cmd.Start(); err != nil {
		cancel()
		return 0, fmt.Errorf("spawn sing-box: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.cancel = cancel
	s.port = port
	s.cfgPath = cfgPath
	s.mu.Unlock()

	go s.supervise(cmd, port)

	if err := waitPort(port, 5*time.Second); err != nil {
		s.stop()
		return 0, fmt.Errorf("sing-box did not bind socks: %w", err)
	}
	return port, nil
}

func (s *singboxSupervisor) supervise(cmd *exec.Cmd, port int) {
	err := cmd.Wait()
	s.mu.Lock()
	owned := s.cmd == cmd
	if owned {
		s.cmd = nil
		if s.cancel != nil {
			s.cancel = nil
		}
		s.port = 0
	}
	s.mu.Unlock()
	if owned && s.notify != nil {
		s.notify("child_exit", map[string]any{
			"port":   port,
			"error":  errString(err),
			"exited": cmd.ProcessState != nil && cmd.ProcessState.Exited(),
		})
	}
}

func (s *singboxSupervisor) stop() {
	s.mu.Lock()
	cmd := s.cmd
	cancel := s.cancel
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	go func() {
		time.Sleep(2 * time.Second)
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
		}
		if cancel != nil {
			cancel()
		}
	}()
}

func (s *singboxSupervisor) reload(raw json.RawMessage) (int, error) {
	s.stop()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		running := s.cmd != nil
		s.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return s.start(raw)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type logPipe struct {
	notify notifyFn
	stream string
	mu     sync.Mutex
	buf    bytes.Buffer
}

func newLogPipe(notify notifyFn, stream string) *logPipe {
	return &logPipe{notify: notify, stream: stream}
}

func (p *logPipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf.Write(b)
	for {
		idx := bytes.IndexByte(p.buf.Bytes(), '\n')
		if idx < 0 {
			break
		}
		line := string(p.buf.Bytes()[:idx])
		p.buf.Next(idx + 1)
		if p.notify != nil {
			p.notify("log", map[string]any{
				"stream": p.stream,
				"line":   line,
			})
		}
	}
	return len(b), nil
}
