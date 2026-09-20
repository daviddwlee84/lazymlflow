package connection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

// startSSH owns a fresh noninteractive OpenSSH process, never a user's shared
// control master. Only the forwarding endpoint is local; this is a remote store.
func (m *Manager) startSSH(ctx context.Context, t core.Target, secrets []string) (string, func() error, error) {
	executable, err := exec.LookPath("ssh")
	if err != nil {
		return "", nil, errors.New("SSH targets require OpenSSH (ssh) on PATH")
	}
	upstream, err := url.Parse(t.TrackingURI)
	if err != nil {
		return "", nil, err
	}
	port := upstream.Port()
	if port == "" {
		port = "80"
		if upstream.Scheme == "https" {
			port = "443"
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	localAddress := listener.Addr().String()
	listener.Close()
	forward := localAddress + ":" + net.JoinHostPort(upstream.Hostname(), port)
	argv := []string{executable, "-N", "-T", "-n", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no", "-o", "ExitOnForwardFailure=yes", "-o", "ForkAfterAuthentication=no", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-L", forward, "--", t.SSHHost}
	p, err := startProcess(argv, "", setEnv(os.Environ(), "SSH_ASKPASS_REQUIRE", "never"), secrets, 64<<10)
	if err != nil {
		return "", nil, fmt.Errorf("start SSH to %s: %w", t.SSHHost, err)
	}
	deadline, cancel := context.WithTimeout(ctx, m.startupTimeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.Done():
			_ = p.stop()
			return "", nil, sshError(t.SSHHost, deadline.Err(), p.logs.String())
		case <-p.done:
			_ = p.stop()
			return "", nil, sshError(t.SSHHost, p.err, p.logs.String())
		case <-ticker.C:
			conn, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(deadline, "tcp4", localAddress)
			if err == nil {
				conn.Close()
				return localAddress, p.stop, nil
			}
		}
	}
}

func sshError(host string, err error, logs string) error {
	if err == nil {
		err = errors.New("SSH exited before forwarding became ready")
	}
	return fmt.Errorf("SSH target %s: %w; establish a working `ssh %s` login first (verify its host key and unlock/add your SSH key); lazymlflow cannot prompt for passwords or trust new host keys\n%s", host, err, host, strings.TrimSpace(logs))
}

func withinBase(p, base string) bool {
	// Decoding and cleaning catches encoded traversal while preserving ordinary
	// Unicode/artifact paths. Never allow this proxy to become a general relay.
	if strings.ContainsAny(p, "\\\x00") {
		return false
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(p, "/"))
	return base == "" || base == "/" || cleaned == base || strings.HasPrefix(cleaned, base+"/")
}

// startSSHProxy gives REST, the official artifact CLI, and the browser one
// loopback endpoint. TLS uses the configured origin name and CA; only dialing
// is redirected through SSH. No proxy environment variable can bypass SSH.
func startSSHProxy(t core.Target, forwarded string, upstreamClient *http.Client, credentials credentials, closeForward func() error) (string, func() error, error) {
	upstream, err := url.Parse(t.TrackingURI)
	if err != nil {
		return "", nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	localURL := &url.URL{Scheme: "http", Host: listener.Addr().String(), Path: upstream.Path, RawPath: upstream.RawPath}
	transport := upstreamClient.Transport.(*http.Transport)
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		expectedPort := upstream.Port()
		if expectedPort == "" {
			expectedPort = "80"
			if upstream.Scheme == "https" {
				expectedPort = "443"
			}
		}
		if address != net.JoinHostPort(upstream.Hostname(), expectedPort) {
			return nil, errors.New("SSH proxy refused an unrelated upstream")
		}
		return dialer.DialContext(ctx, "tcp4", forwarded)
	}
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		ErrorLog:  log.New(io.Discard, "", 0),
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = upstream.Scheme
			r.Out.URL.Host = upstream.Host
			r.Out.Host = upstream.Host
			// Browser-supplied routing headers must not change the configured origin.
			r.Out.Header.Del("Forwarded")
			for _, name := range []string{"X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-For", "X-Forwarded-Port", "Proxy-Authorization"} {
				r.Out.Header.Del(name)
			}
			if credentials.username != "" {
				r.Out.SetBasicAuth(credentials.username, credentials.password)
			} else if credentials.token != "" {
				r.Out.Header.Set("Authorization", "Bearer "+credentials.token)
			}
			if origin := r.Out.Header.Get("Origin"); origin == localURL.Scheme+"://"+localURL.Host {
				r.Out.Header.Set("Origin", upstream.Scheme+"://"+upstream.Host)
			}
		},
		ModifyResponse: func(r *http.Response) error {
			// Keep target-local redirects in the tunnel. External signed downloads
			// remain external; the proxy never follows them or sends target auth there.
			location, e := r.Location()
			if e == nil && strings.EqualFold(location.Scheme, upstream.Scheme) && strings.EqualFold(location.Host, upstream.Host) && withinBase(location.Path, strings.TrimRight(upstream.Path, "/")) {
				location.Scheme = localURL.Scheme
				location.Host = localURL.Host
				r.Header.Set("Location", location.String())
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "SSH target is unavailable; check SSH authentication, the tracking URI, and the target TLS certificate/CA", http.StatusBadGateway)
		},
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != localURL.Host {
			http.Error(w, "Invalid local proxy Host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != localURL.Scheme+"://"+localURL.Host && origin != upstream.Scheme+"://"+upstream.Host {
			http.Error(w, "Unrelated browser origin cannot use this SSH proxy", http.StatusForbidden)
			return
		}
		if !withinBase(r.URL.Path, strings.TrimRight(upstream.Path, "/")) {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	go func() { _ = server.Serve(listener) }()
	var once sync.Once
	var closeErr error
	closeProxy := func() error {
		once.Do(func() {
			closeErr = server.Close()
			if errors.Is(closeErr, http.ErrServerClosed) {
				closeErr = nil
			}
			transport.CloseIdleConnections()
			closeErr = errors.Join(closeErr, closeForward())
		})
		return closeErr
	}
	return strings.TrimRight(localURL.String(), "/"), closeProxy, nil
}
