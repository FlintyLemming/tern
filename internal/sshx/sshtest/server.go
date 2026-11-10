// Package sshtest is an in-process SSH server for tests. It speaks just
// enough of the protocol for handshakes, sftp subsystem requests and exec.
package sshtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

type options struct {
	authorized ssh.PublicKey
	noSFTP     bool
	hang       bool
	listen     string
	exec       func(cmd string) (stdout string, exit int)
}

type Option func(*options)

// WithAuthorized lets pub log in. Without it every key is refused.
func WithAuthorized(pub ssh.PublicKey) Option { return func(o *options) { o.authorized = pub } }

// WithoutSFTP makes the server refuse sftp subsystem requests.
func WithoutSFTP() Option { return func(o *options) { o.noSFTP = true } }

// WithHang makes the server accept TCP connections and never send a byte.
func WithHang() Option { return func(o *options) { o.hang = true } }

// WithListen sets the listen address; the default is "127.0.0.1:0".
func WithListen(addr string) Option { return func(o *options) { o.listen = addr } }

// WithExec answers exec requests with fn's output and exit status.
func WithExec(fn func(cmd string) (stdout string, exit int)) Option {
	return func(o *options) { o.exec = fn }
}

type Server struct {
	o      options
	ln     net.Listener
	signer ssh.Signer
	cfg    *ssh.ServerConfig

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// Start runs a server with a fresh ed25519 host key until the test ends or
// Close is called.
func Start(t testing.TB, opts ...Option) *Server {
	t.Helper()
	s := &Server{o: options{listen: "127.0.0.1:0"}, conns: map[net.Conn]struct{}{}}
	for _, opt := range opts {
		opt(&s.o)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if s.signer, err = ssh.NewSignerFromKey(priv); err != nil {
		t.Fatal(err)
	}
	s.cfg = &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if s.o.authorized != nil && bytes.Equal(key.Marshal(), s.o.authorized.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("key not authorized")
		},
	}
	s.cfg.AddHostKey(s.signer)
	if s.ln, err = net.Listen("tcp", s.o.listen); err != nil {
		t.Fatalf("sshtest: listen %s: %v", s.o.listen, err)
	}
	go s.serve()
	t.Cleanup(s.Close)
	return s
}

// Host is the listen address without port and without brackets.
func (s *Server) Host() string { return s.ln.Addr().(*net.TCPAddr).IP.String() }

func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *Server) HostPublicKey() ssh.PublicKey { return s.signer.PublicKey() }

// Close stops the server and drops every open connection.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.ln.Close()
	for c := range s.conns {
		c.Close()
	}
}

func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		c.Close()
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	c.Close()
}

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		if s.track(c) {
			go s.handle(c)
		}
	}
}

func (s *Server) handle(c net.Conn) {
	defer s.untrack(c)
	if s.o.hang {
		io.Copy(io.Discard, c) // until the peer or Close hangs up
		return
	}
	conn, chans, reqs, err := ssh.NewServerConn(c, s.cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go s.session(ch, creqs)
	}
}

func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		var arg struct{ Value string }
		switch req.Type {
		case "subsystem":
			ssh.Unmarshal(req.Payload, &arg)
			if arg.Value != "sftp" || s.o.noSFTP {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			io.Copy(io.Discard, ch) // no sftp protocol: wait for the client to leave
			return
		case "exec":
			ssh.Unmarshal(req.Payload, &arg)
			if s.o.exec == nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			out, code := s.o.exec(arg.Value)
			io.WriteString(ch, out)
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
			return
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// Addr is "host:port" (IPv6 in brackets), usable with WithListen to bring a
// replacement server up on the same port after Close.
func (s *Server) Addr() string { return net.JoinHostPort(s.Host(), strconv.Itoa(s.Port())) }
