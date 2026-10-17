package socks5

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func echo(t *testing.T) (string, int) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
}

func TestServeAuth(t *testing.T) {
	host, port := echo(t)
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	go func() {
		for {
			c, err := proxy.Accept()
			if err != nil {
				return
			}
			go Serve(c, ServerConfig{User: "u", Pass: "p", Dial: func(h string, p int) (net.Conn, error) {
				return net.Dial("tcp", net.JoinHostPort(h, itoa(p)))
			}})
		}
	}()
	addr := proxy.Addr().String()
	for _, c := range [][2]string{{"", ""}, {"u", "wrong"}, {"x", "p"}} {
		if conn, err := Dial(addr, c[0], c[1], host, port, 3*time.Second); err == nil {
			conn.Close()
			t.Fatalf("login %q/%q was accepted", c[0], c[1])
		}
	}
	conn, err := Dial(addr, "u", "p", host, port, 3*time.Second)
	if err != nil {
		t.Fatalf("valid login rejected: %v", err)
	}
	defer conn.Close()
	conn.Write([]byte("hi"))
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hi" {
		t.Fatalf("echo: %q %v", buf, err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
