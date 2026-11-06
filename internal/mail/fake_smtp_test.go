package mail

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeSMTPServer is a minimal, single-connection SMTP server for testing
// Send's handling of SMTP-level failures (missing STARTTLS, a failed TLS
// upgrade) that a mailpit-backed integration test can't provoke on demand.
// handle is called with each line the client sends (without its trailing
// CRLF) and returns the raw bytes to write back (already CRLF-terminated,
// or "" for no reply) plus whether to close the connection afterwards.
type fakeSMTPServer struct {
	ln net.Listener

	mu    sync.Mutex
	lines []string
}

func startFakeSMTPServer(t *testing.T, handle func(line string) (reply string, closeConn bool)) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	s := &fakeSMTPServer{ln: ln}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("220 fake.local ESMTP ready\r\n")); err != nil {
			return
		}
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			s.mu.Lock()
			s.lines = append(s.lines, line)
			s.mu.Unlock()

			reply, closeConn := handle(line)
			if reply != "" {
				if _, err := conn.Write([]byte(reply)); err != nil {
					return
				}
			}
			if closeConn {
				return
			}
		}
	}()
	return s
}

// hostPort returns cfg's Host/Port for connecting to the fake server.
func (s *fakeSMTPServer) hostPort(t *testing.T) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.ln.Addr().String())
	if err != nil {
		t.Fatalf("splitting listener address: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parsing port: %v", err)
	}
	return host, port
}

// Lines returns the SMTP command lines the server has received so far.
func (s *fakeSMTPServer) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// linesWithPrefix reports whether any received line starts with prefix.
func (s *fakeSMTPServer) linesWithPrefix(prefix string) []string {
	var matched []string
	for _, l := range s.Lines() {
		if strings.HasPrefix(l, prefix) {
			matched = append(matched, l)
		}
	}
	return matched
}
