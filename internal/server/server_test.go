package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnech/DyroKol/internal/agent"
	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/socks5"
)

type testStatus struct {
	Fingerprint string    `json:"fingerprint"`
	Version     string    `json:"version"`
	ConfigVer   int64     `json:"config_version"`
	Clients     []tClient `json:"clients"`
	InPorts     []struct {
		ID       int64  `json:"id"`
		Port     int    `json:"port"`
		Machine  string `json:"machine"`
		BytesIn  int64  `json:"bytes_in"`
		BytesOut int64  `json:"bytes_out"`
		Conns    int64  `json:"conns_total"`
		Target   string `json:"target"`
	} `json:"inports"`
	Endpoints []struct {
		ID      int64  `json:"id"`
		Machine string `json:"machine"`
		Type    string `json:"type"`
		Host    string `json:"host"`
		Display string `json:"display"`
	} `json:"endpoints"`
	Routes []struct {
		ID int64 `json:"id"`
	} `json:"routes"`
}

type tClient struct {
	KeyHash      string `json:"key_hash"`
	LastIP       string `json:"last_ip"`
	Online       bool   `json:"online"`
	AgentVersion string `json:"agent_version"`
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func waitFor(t *testing.T, d time.Duration, what string, fn func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(d)
	var lastErr error
	for time.Now().Before(deadline) {
		ok, err := fn()
		if ok {
			return
		}
		lastErr = err
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s (last error: %v)", what, lastErr)
}

// testAPIToken guards the admin api in every test relay.
const testAPIToken = "test-admin-token"

// newAPIRequest builds a request carrying the admin api token.
func newAPIRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Dyrokol-Api", testAPIToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// noKeepAlive opens a fresh connection per request so that assertions are
// never satisfied by a pooled connection left over from an earlier state.
var noKeepAlive = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

func fetchStatus(base string) (*testStatus, error) {
	req, _ := http.NewRequest(http.MethodGet, base+"/api/status", nil)
	req.Header.Set("X-Dyrokol-Api", testAPIToken)
	resp, err := noKeepAlive.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}
	var st testStatus
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func postJSON(t *testing.T, url string, payload any) *http.Response {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := newAPIRequest(t, http.MethodPost, url, bytes.NewReader(data))
	resp, err := noKeepAlive.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// mustPost posts a JSON payload and decodes the response body into out,
// failing the test on a non-2xx status.
func mustPost(t *testing.T, url string, payload any, out any) {
	t.Helper()
	resp := postJSON(t, url, payload)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("POST %s: %d %s", url, resp.StatusCode, body)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("POST %s: decode: %v", url, err)
		}
	}
}

func doDelete(t *testing.T, url string, want ...int) {
	t.Helper()
	req := newAPIRequest(t, http.MethodDelete, url, nil)
	resp, err := noKeepAlive.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", url, err)
	}
	resp.Body.Close()
	for _, code := range want {
		if resp.StatusCode == code {
			return
		}
	}
	t.Fatalf("DELETE %s: got %d, want one of %v", url, resp.StatusCode, want)
}

// patchJSON sends a JSON PATCH and returns the response.
func patchJSON(t *testing.T, url string, payload any) *http.Response {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := newAPIRequest(t, http.MethodPatch, url, bytes.NewReader(data))
	resp, err := noKeepAlive.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// get authenticates: these tests read the api, not the static ui.
func get(url string) (string, error) {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("X-Dyrokol-Api", testAPIToken)
	r, err := noKeepAlive.Do(req)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", r.StatusCode, b)
	}
	return string(b), nil
}

func echoServer(t *testing.T) (*httptest.Server, string, int) {
	t.Helper()
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "pong")
	}))
	t.Cleanup(echo.Close)
	addr := strings.TrimPrefix(echo.URL, "http://")
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return echo, addr, port
}

type relayFixture struct {
	srv       *Server
	cancel    context.CancelFunc
	done      chan struct{}
	base      string
	dbPath    string
	tmp       string
	relayAddr string
	pinsPath  string
	restart   func()
}

func startRelay(t *testing.T, tmp string, opts ...func(*Config)) *relayFixture {
	t.Helper()
	discard := log.New(io.Discard, "", 0)
	relayAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	adminAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	f := &relayFixture{
		tmp:       tmp,
		dbPath:    filepath.Join(tmp, "relay.db.enc"),
		relayAddr: relayAddr,
		pinsPath:  filepath.Join(tmp, "pins.json"),
		base:      "http://" + adminAddr,
		done:      make(chan struct{}),
	}
	newSrv := func() *Server {
		cfg := Config{
			Listen:     relayAddr,
			AdminAddr:  adminAddr,
			DBPath:     f.dbPath,
			CertFile:   filepath.Join(tmp, "relay.crt"),
			KeyFile:    filepath.Join(tmp, "relay.key"),
			Logger:     discard,
			AdminToken: testAPIToken,
		}
		for _, o := range opts {
			o(&cfg)
		}
		return New(cfg)
	}
	f.srv = newSrv()
	f.restart = func() {
		if f.cancel != nil {
			f.cancel()
			<-f.done
		}
		f.srv = newSrv()
		f.done = make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		f.cancel = cancel
		go func() {
			f.srv.Run(ctx)
			close(f.done)
		}()
		<-f.srv.Ready()
	}
	f.restart()
	t.Cleanup(func() {
		if f.cancel != nil {
			f.cancel()
			select {
			case <-f.done:
			case <-time.After(5 * time.Second):
			}
		}
	})
	return f
}

// startAgent runs an agent against the fixture until the test ends.
func startAgent(t *testing.T, fx *relayFixture, entry agent.ServerEntry) *agent.Agent {
	t.Helper()
	a := agent.New(
		agent.Config{Servers: []agent.ServerEntry{entry}},
		agent.Options{
			PinsPath:       fx.pinsPath,
			BackoffInitial: 100 * time.Millisecond,
			BackoffMax:     500 * time.Millisecond,
			Logger:         log.New(io.Discard, "", 0),
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return a
}

// newMachine registers a machine through the admin api and returns its token.
func newMachine(t *testing.T, base, label string) (token, keyHash string) {
	t.Helper()
	var out struct {
		Token  string `json:"token"`
		Client struct {
			KeyHash string `json:"key_hash"`
		} `json:"client"`
	}
	mustPost(t, base+"/api/clients", map[string]string{"label": label}, &out)
	return out.Token, out.Client.KeyHash
}

func clientByHash(st *testStatus, keyHash string) (tClient, bool) {
	for _, c := range st.Clients {
		if c.KeyHash == keyHash {
			return c, true
		}
	}
	return tClient{}, false
}

// --- end-to-end: agent-side ingress, self egress, stats, restart ---

func TestEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	_, echoAddr, echoPort := echoServer(t)
	echoHost, _, err := net.SplitHostPort(echoAddr)
	if err != nil {
		t.Fatal(err)
	}
	publicPort := freePort(t)

	fx := startRelay(t, tmp)

	// 1. Bootstrap the relay database over the network (dyrokol client --server).
	token, password, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	keyHash := protocol.KeyHash(token)

	// 2. Start the agent with admin rights (it also owns the ingress port).
	adminListen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	startAgent(t, fx, agent.ServerEntry{
		Addr: fx.relayAddr, Token: token, DBPassword: password, AdminListen: adminListen,
	})

	// 3. The machine comes online (it also auto-unlocks the db).
	waitFor(t, 15*time.Second, "machine online", func() (bool, error) {
		st, err := fetchStatus(fx.base)
		if err != nil {
			return false, err
		}
		c, ok := clientByHash(st, keyHash)
		return ok && c.Online, fmt.Errorf("not online yet")
	})

	// 4. Build the path: ingress port → route → egress on the agent machine.
	var inport struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/inports", map[string]any{
		"port":    publicPort,
		"machine": keyHash,
		"label":   "echo ingress",
	}, &inport)

	// A second ingress on the same machine and port must be rejected.
	conflict := postJSON(t, fx.base+"/api/inports", map[string]any{"port": publicPort, "machine": keyHash})
	conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate ingress: got %d, want 409", conflict.StatusCode)
	}
	// Host must not contain a port.
	bad := postJSON(t, fx.base+"/api/endpoints", map[string]any{
		"machine": keyHash, "host": "127.0.0.1:8080", "port": 80,
	})
	badBody, _ := io.ReadAll(bad.Body)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad host: got %d %s, want 400", bad.StatusCode, badBody)
	}

	var endpoint struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/endpoints", map[string]any{
		"machine": keyHash,
		"host":    echoHost,
		"port":    echoPort,
		"label":   "echo service",
	}, &endpoint)

	var route struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/routes", map[string]any{
		"inport_id": inport.ID, "endpoint_id": endpoint.ID,
	}, &route)
	dup := postJSON(t, fx.base+"/api/routes", map[string]any{
		"inport_id": inport.ID, "endpoint_id": endpoint.ID,
	})
	dup.Body.Close()
	if dup.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate route: got %d, want 409", dup.StatusCode)
	}

	// 5. The agent bound the ingress port and serves it locally.
	waitFor(t, 15*time.Second, "agent ingress forwarding", func() (bool, error) {
		body, err := get(fmt.Sprintf("http://127.0.0.1:%d/", publicPort))
		if err != nil {
			return false, err
		}
		return body == "pong", nil
	})

	// 6. Admin UI is reachable through the agent's reverse tunnel.
	waitFor(t, 15*time.Second, "admin reverse tunnel", func() (bool, error) {
		body, err := get("http://" + adminListen + "/")
		if err != nil {
			return false, err
		}
		return strings.Contains(body, "DyroKol"), nil
	})

	// 7. Counters are reported by the agent and flushed to the database.
	waitFor(t, 30*time.Second, "stats flushed", func() (bool, error) {
		st, err := fetchStatus(fx.base)
		if err != nil {
			return false, err
		}
		for _, p := range st.InPorts {
			if p.ID == inport.ID && p.BytesIn > 0 && p.BytesOut > 0 && p.Target != "" {
				return true, nil
			}
		}
		return false, fmt.Errorf("counters still zero")
	})

	// 8. The remote address of the visitor is recorded as a peer.
	waitFor(t, 30*time.Second, "peer recorded", func() (bool, error) {
		body, err := get(fx.base + "/api/peers")
		if err != nil {
			return false, err
		}
		var out struct {
			Peers []struct {
				IP       string `json:"ip"`
				InPortID int64  `json:"inport_id"`
			} `json:"peers"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			return false, err
		}
		for _, p := range out.Peers {
			if p.InPortID == inport.ID && p.IP == "127.0.0.1" {
				return true, nil
			}
		}
		return false, fmt.Errorf("no peer for ingress yet: %s", body)
	})

	// 9. Restart the relay: db locks, the agent must auto-unlock it again.
	fx.restart()
	waitFor(t, 20*time.Second, "auto-unlock after restart", func() (bool, error) {
		st, err := fetchStatus(fx.base)
		if err != nil {
			return false, err
		}
		c, ok := clientByHash(st, keyHash)
		return ok && c.Online, fmt.Errorf("not online yet")
	})
	body, err := get(fmt.Sprintf("http://127.0.0.1:%d/", publicPort))
	if err != nil || body != "pong" {
		t.Fatalf("after restart forwarding failed: %q, %v", body, err)
	}

	// 10. Deleting the route disconnects the ingress from the egress.
	doDelete(t, fmt.Sprintf("%s/api/routes/%d", fx.base, route.ID), http.StatusNoContent)
	waitFor(t, 15*time.Second, "unrouted ingress drops connections", func() (bool, error) {
		if _, err := get(fmt.Sprintf("http://127.0.0.1:%d/", publicPort)); err != nil {
			return true, nil
		}
		return false, fmt.Errorf("still forwarding")
	})

	// 11. Deleting the ingress port releases the socket on the agent.
	doDelete(t, fmt.Sprintf("%s/api/inports/%d", fx.base, inport.ID), http.StatusNoContent)
	waitFor(t, 15*time.Second, "port released", func() (bool, error) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", publicPort), time.Second)
		if err != nil {
			return true, nil
		}
		conn.Close()
		return false, fmt.Errorf("port still open")
	})
}

// TestRelayIngressAgentEgress: the relay owns the ingress port and the
// tunnel exits on the agent machine (server → dial stream → agent).
func TestRelayIngressAgentEgress(t *testing.T) {
	tmp := t.TempDir()
	_, echoAddr, echoPort := echoServer(t)
	echoHost, _, err := net.SplitHostPort(echoAddr)
	if err != nil {
		t.Fatal(err)
	}
	publicPort := freePort(t)

	fx := startRelay(t, tmp)
	_, password, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	token, clientHash := newMachine(t, fx.base, "nas")
	startAgent(t, fx, agent.ServerEntry{
		Addr: fx.relayAddr, Token: token, DBPassword: password,
	})

	waitFor(t, 15*time.Second, "machine online", func() (bool, error) {
		st, err := fetchStatus(fx.base)
		if err != nil {
			return false, err
		}
		c, ok := clientByHash(st, clientHash)
		return ok && c.Online, fmt.Errorf("not online yet")
	})

	var inport struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/inports", map[string]any{"port": publicPort, "label": "relay ingress"}, &inport)
	var endpoint struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/endpoints", map[string]any{
		"machine": clientHash, "host": echoHost, "port": echoPort, "label": "nas echo",
	}, &endpoint)
	mustPost(t, fx.base+"/api/routes", map[string]any{
		"inport_id": inport.ID, "endpoint_id": endpoint.ID,
	}, nil)

	waitFor(t, 15*time.Second, "relay ingress → agent egress", func() (bool, error) {
		body, err := get(fmt.Sprintf("http://127.0.0.1:%d/", publicPort))
		if err != nil {
			return false, err
		}
		return body == "pong", nil
	})

	// The agent reports its address and version as service information.
	waitFor(t, 20*time.Second, "client ip recorded", func() (bool, error) {
		st, err := fetchStatus(fx.base)
		if err != nil {
			return false, err
		}
		c, ok := clientByHash(st, clientHash)
		if !ok || c.LastIP != "127.0.0.1" {
			return false, fmt.Errorf("last_ip=%q", c.LastIP)
		}
		return c.AgentVersion == protocol.AppVersion, fmt.Errorf("agent_version=%q", c.AgentVersion)
	})

	// The relay-side peer (the visitor) shows up in the peer list too.
	waitFor(t, 30*time.Second, "relay peer recorded", func() (bool, error) {
		body, err := get(fx.base + "/api/peers")
		if err != nil {
			return false, err
		}
		return strings.Contains(body, "127.0.0.1") && strings.Contains(body, fmt.Sprintf("%d", inport.ID)), nil
	})
}

// TestEgressOnSecondMachine: relay ingress → agent A (owning the ingress and
// forwarding through the tunnel) resolves the route to an endpoint owned by
// agent B, which dials the final target. Replaces the removed forward type.
func TestEgressOnSecondMachine(t *testing.T) {
	tmp := t.TempDir()
	_, echoAddr, echoPort := echoServer(t)
	echoHost, _, err := net.SplitHostPort(echoAddr)
	if err != nil {
		t.Fatal(err)
	}
	publicPort := freePort(t)

	fx := startRelay(t, tmp)
	_, password, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	tokenA, hashA := newMachine(t, fx.base, "edge-a")
	tokenB, hashB := newMachine(t, fx.base, "edge-b")
	startAgent(t, fx, agent.ServerEntry{Addr: fx.relayAddr, Token: tokenA, DBPassword: password})
	startAgent(t, fx, agent.ServerEntry{Addr: fx.relayAddr, Token: tokenB, DBPassword: password})

	for _, h := range []string{hashA, hashB} {
		h := h
		waitFor(t, 15*time.Second, "machine online", func() (bool, error) {
			st, err := fetchStatus(fx.base)
			if err != nil {
				return false, err
			}
			c, ok := clientByHash(st, h)
			return ok && c.Online, fmt.Errorf("not online yet")
		})
	}

	// The legacy forward type is gone: the api rejects it.
	fwdReq := map[string]any{"type": "forward", "machine": hashA, "host": echoHost, "port": echoPort}
	badResp := postJSON(t, fx.base+"/api/endpoints", fwdReq)
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("forward type accepted: got %d, want 400", badResp.StatusCode)
	}

	// Ingress lives on agent A, the egress endpoint on agent B.
	var inport struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/inports", map[string]any{"port": publicPort, "machine": hashA, "label": "edge-a ingress"}, &inport)
	var endpoint struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/endpoints", map[string]any{
		"machine": hashB, "host": echoHost, "port": echoPort,
		"label": "echo via edge-b",
	}, &endpoint)
	mustPost(t, fx.base+"/api/routes", map[string]any{
		"inport_id": inport.ID, "endpoint_id": endpoint.ID,
	}, nil)

	waitFor(t, 20*time.Second, "cross-machine egress", func() (bool, error) {
		body, err := get(fmt.Sprintf("http://127.0.0.1:%d/", publicPort))
		if err != nil {
			return false, err
		}
		return body == "pong", nil
	})

	st, err := fetchStatus(fx.base)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Endpoints) != 1 || st.Endpoints[0].Type != "direct" {
		t.Fatalf("endpoint: %+v", st.Endpoints)
	}
}

func TestAPITokenGuardsEverything(t *testing.T) {
	tmp := t.TempDir()
	fx := startRelay(t, tmp)
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// No token: rejected.
	resp, err := http.Get(fx.base + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", resp.StatusCode)
	}
	// Wrong token: rejected.
	req, _ := http.NewRequest(http.MethodGet, fx.base+"/api/status", nil)
	req.Header.Set("X-Dyrokol-Api", "nope")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", resp.StatusCode)
	}
	// Cross-origin browser request: rejected even with a valid token.
	req, _ = http.NewRequest(http.MethodGet, fx.base+"/api/status", nil)
	req.Header.Set("X-Dyrokol-Api", testAPIToken)
	req.Header.Set("Origin", "http://evil.example")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross origin: got %d, want 403", resp.StatusCode)
	}
	// Static assets stay open so the login page can load.
	resp, err = http.Get(fx.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("static: got %d, want 200", resp.StatusCode)
	}
	// Correct token works.
	st, err := fetchStatus(fx.base)
	if err != nil || st.Version == "" {
		t.Fatalf("authed status: %v, %+v", err, st)
	}
}

func TestSourceFilterBlocksVisitor(t *testing.T) {
	tmp := t.TempDir()
	_, echoAddr, echoPort := echoServer(t)
	echoHost, _, _ := net.SplitHostPort(echoAddr)
	publicPort := freePort(t)

	fx := startRelay(t, tmp)
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	var inport struct {
		ID int64 `json:"id"`
	}
	// Deny everything from loopback: our test visitor comes from 127.0.0.1.
	mustPost(t, fx.base+"/api/inports", map[string]any{
		"port": publicPort, "label": "blocked", "deny": []string{"127.0.0.0/8"},
	}, &inport)
	var endpoint struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/endpoints", map[string]any{
		"machine": "relay", "host": echoHost, "port": echoPort,
	}, &endpoint)
	mustPost(t, fx.base+"/api/routes", map[string]any{
		"inport_id": inport.ID, "endpoint_id": endpoint.ID,
	}, nil)

	// Wait until the relay bound the port, then confirm traffic is blocked.
	waitFor(t, 10*time.Second, "ingress bound", func() (bool, error) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", publicPort), time.Second)
		if err != nil {
			return false, err
		}
		conn.Close()
		return true, nil
	})
	time.Sleep(300 * time.Millisecond)
	body, err := get(fmt.Sprintf("http://127.0.0.1:%d/", publicPort))
	if err == nil && body == "pong" {
		t.Fatal("deny filter did not block the loopback visitor")
	}

	// Clearing the deny list lets traffic through.
	resp := patchJSON(t, fmt.Sprintf("%s/api/inports/%d", fx.base, inport.ID), map[string]any{"deny": []string{}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("clear filter: got %d, want 204", resp.StatusCode)
	}
	waitFor(t, 10*time.Second, "filter cleared", func() (bool, error) {
		body, err := get(fmt.Sprintf("http://127.0.0.1:%d/", publicPort))
		if err != nil {
			return false, err
		}
		return body == "pong", nil
	})
}

func TestBackupRestoreEndpoint(t *testing.T) {
	tmp := t.TempDir()
	fx := startRelay(t, tmp)
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	token, hash := newMachine(t, fx.base, "nas")
	_ = token
	var inport struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/inports", map[string]any{"port": freePort(t), "machine": hash, "label": "web"}, &inport)

	body, err := get(fx.base + "/api/backup?secrets=1")
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if !strings.Contains(body, "dyrokol-config") || !strings.Contains(body, hash) {
		t.Fatalf("unexpected backup body: %s", body)
	}

	// A secretless snapshot must not leak the machine's presence-free data
	// but keeps the same structure.
	bodyNoSecret, err := get(fx.base + "/api/backup?secrets=0")
	if err != nil {
		t.Fatalf("backup without secrets: %v", err)
	}
	if !strings.Contains(bodyNoSecret, "dyrokol-config") {
		t.Fatalf("redacted snapshot malformed: %s", bodyNoSecret)
	}

	// Restore the full snapshot back into the same relay.
	req := newAPIRequest(t, http.MethodPost, fx.base+"/api/restore", strings.NewReader(body))
	resp, err := noKeepAlive.Do(req)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restore: %d %s", resp.StatusCode, rb)
	}

	st, err := fetchStatus(fx.base)
	if err != nil {
		t.Fatalf("status after restore: %v", err)
	}
	found := false
	for _, c := range st.Clients {
		if c.KeyHash == hash {
			found = true
		}
	}
	if !found {
		t.Fatalf("machine lost after restore: %+v", st.Clients)
	}

	// A malformed snapshot is rejected.
	req = newAPIRequest(t, http.MethodPost, fx.base+"/api/restore", strings.NewReader(`{"format":"nope"}`))
	resp, _ = noKeepAlive.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad snapshot: got %d, want 400", resp.StatusCode)
	}
}

func TestLiveAndLogs(t *testing.T) {
	tmp := t.TempDir()
	fx := startRelay(t, tmp)
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// /api/live and /api/logs must be servable even when empty.
	body, err := get(fx.base + "/api/live")
	if err != nil || !strings.Contains(body, "live") {
		t.Fatalf("live: %v %s", err, body)
	}
	body, err = get(fx.base + "/api/logs")
	if err != nil || !strings.Contains(body, "logs") {
		t.Fatalf("logs: %v %s", err, body)
	}
	// /api/config describes the shell contract.
	body, err = get(fx.base + "/api/config")
	if err != nil || !strings.Contains(body, "api_header") {
		t.Fatalf("config: %v %s", err, body)
	}
}

// TestSocks5Egress: a socks5 egress point is a SOCKS5 server built into the
// exit machine. A SOCKS5 client connects to the ingress port, picks the
// destination, and the exit machine opens the outbound connection itself.
func TestSocks5Egress(t *testing.T) {
	tmp := t.TempDir()
	_, echoAddr, echoPort := echoServer(t)
	echoHost, _, err := net.SplitHostPort(echoAddr)
	if err != nil {
		t.Fatal(err)
	}
	publicPort := freePort(t)

	fx := startRelay(t, tmp)
	_, password, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	token, clientHash := newMachine(t, fx.base, "socksbox")
	startAgent(t, fx, agent.ServerEntry{Addr: fx.relayAddr, Token: token, DBPassword: password})
	waitFor(t, 15*time.Second, "machine online", func() (bool, error) {
		st, err := fetchStatus(fx.base)
		if err != nil {
			return false, err
		}
		c, ok := clientByHash(st, clientHash)
		return ok && c.Online, fmt.Errorf("not online yet")
	})

	var inport struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/inports", map[string]any{"port": publicPort}, &inport)

	// Login without a password is rejected by the api.
	bad := postJSON(t, fx.base+"/api/endpoints", map[string]any{
		"machine": clientHash, "type": "socks5", "socks5_user": "u",
	})
	badBody, _ := io.ReadAll(bad.Body)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("socks5 login without password: got %d %s, want 400", bad.StatusCode, badBody)
	}

	// No host, port or proxy address: the exit is the machine itself.
	var endpoint struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/endpoints", map[string]any{
		"machine": clientHash, "type": "socks5",
		"deny": []string{"127.0.0.2"}, "label": "socks5 exit",
	}, &endpoint)
	mustPost(t, fx.base+"/api/routes", map[string]any{
		"inport_id": inport.ID, "endpoint_id": endpoint.ID,
	}, nil)

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", publicPort)
	waitFor(t, 15*time.Second, "socks5 egress", func() (bool, error) {
		conn, err := socks5.Dial(proxyAddr, "", "", echoHost, echoPort, 5*time.Second)
		if err != nil {
			return false, err
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(conn, "GET / HTTP/1.0\r\n\r\n")
		body, _ := io.ReadAll(conn)
		return strings.Contains(string(body), "pong"), nil
	})

	// The destination filter applies to what the client asks for.
	if conn, err := socks5.Dial(proxyAddr, "", "", "127.0.0.2", echoPort, 5*time.Second); err == nil {
		conn.Close()
		t.Fatal("denied destination was reached through the socks5 exit")
	}
}

// TestRelayEgress checks that an endpoint on the relay machine itself is
// served locally, without any tunnel.
func TestRelayEgress(t *testing.T) {
	tmp := t.TempDir()
	_, _, echoPort := echoServer(t)
	publicPort := freePort(t)

	fx := startRelay(t, tmp)
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	var inport struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/inports", map[string]any{"port": publicPort}, &inport)
	var endpoint struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/endpoints", map[string]any{
		"machine": "relay", "port": echoPort, "label": "local echo",
	}, &endpoint)
	mustPost(t, fx.base+"/api/routes", map[string]any{
		"inport_id": inport.ID, "endpoint_id": endpoint.ID,
	}, nil)

	waitFor(t, 10*time.Second, "relay egress forwarding", func() (bool, error) {
		body, err := get(fmt.Sprintf("http://127.0.0.1:%d/", publicPort))
		if err != nil {
			return false, err
		}
		return body == "pong", nil
	})

	st, err := fetchStatus(fx.base)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.InPorts) != 1 || len(st.Endpoints) != 1 || len(st.Routes) != 1 {
		t.Fatalf("status: %+v", st)
	}
	if st.Endpoints[0].Host != "127.0.0.1" || st.Endpoints[0].Machine != "relay" {
		t.Fatalf("endpoint: %+v", st.Endpoints[0])
	}
	// Config changes bump the version the agents apply.
	before := st.ConfigVer
	mustPost(t, fx.base+"/api/inports", map[string]any{"port": freePort(t)}, nil)
	st2, err := fetchStatus(fx.base)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st2.ConfigVer <= before {
		t.Fatalf("config version did not grow: %d → %d", before, st2.ConfigVer)
	}
}

// TestRouteSwitch: a route that is switched off carries no traffic until it
// is switched on again.
func TestRouteSwitch(t *testing.T) {
	tmp := t.TempDir()
	_, _, echoPort := echoServer(t)
	publicPort := freePort(t)

	fx := startRelay(t, tmp)
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	var inport, endpoint, route struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/inports", map[string]any{"port": publicPort}, &inport)
	mustPost(t, fx.base+"/api/endpoints", map[string]any{"machine": "relay", "port": echoPort}, &endpoint)
	mustPost(t, fx.base+"/api/routes", map[string]any{"inport_id": inport.ID, "endpoint_id": endpoint.ID}, &route)

	url := fmt.Sprintf("http://127.0.0.1:%d/", publicPort)
	works := func() bool {
		body, err := get(url)
		return err == nil && body == "pong"
	}
	waitFor(t, 10*time.Second, "route on", func() (bool, error) { return works(), fmt.Errorf("not yet") })

	patch := func(on bool) {
		t.Helper()
		resp := patchJSON(t, fmt.Sprintf("%s/api/routes/%d", fx.base, route.ID), map[string]any{"enabled": on})
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("patch enabled=%v: %d", on, resp.StatusCode)
		}
	}
	patch(false)
	waitFor(t, 10*time.Second, "route off", func() (bool, error) { return !works(), fmt.Errorf("still forwarding") })
	st, err := fetchStatus(fx.base)
	if err != nil || len(st.Routes) != 1 {
		t.Fatalf("status: %v %+v", err, st)
	}
	patch(true)
	waitFor(t, 10*time.Second, "route back on", func() (bool, error) { return works(), fmt.Errorf("not forwarding") })
}

// TestSettingsName: the relay name and public address are set through the
// settings api, appear in the status, and survive a partial update.
func TestSettingsName(t *testing.T) {
	fx := startRelay(t, t.TempDir())
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	put := func(payload map[string]any) int {
		t.Helper()
		data, _ := json.Marshal(payload)
		req := newAPIRequest(t, http.MethodPut, fx.base+"/api/settings", bytes.NewReader(data))
		resp, err := noKeepAlive.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := put(map[string]any{"name": "Frankfurt", "public_addr": "relay.example.com"}); code != http.StatusOK {
		t.Fatalf("save: %d", code)
	}
	if code := put(map[string]any{"autostart": true}); code != http.StatusOK {
		t.Fatalf("partial save: %d", code)
	}
	body, err := get(fx.base + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"name":"Frankfurt"`, `"public_addr":"relay.example.com"`, `"autostart":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("status lacks %s", want)
		}
	}
	if code := put(map[string]any{"public_addr": "bad host"}); code != http.StatusBadRequest {
		t.Fatalf("bad address: got %d, want 400", code)
	}
}

// TestAdminOnlyThroughTunnel: with no AdminAddr the relay opens no admin port.
// The admin ui is reached through the admin machine's tunnel, using a token the
// relay generated once and keeps inside its encrypted database.
func TestAdminOnlyThroughTunnel(t *testing.T) {
	fx := startRelay(t, t.TempDir(), func(c *Config) { c.AdminAddr, c.AdminToken = "", "" })
	token, password, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	entrance := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	startAgent(t, fx, agent.ServerEntry{Addr: fx.relayAddr, Token: token, DBPassword: password, AdminListen: entrance})

	call := func(tok string) int {
		req, _ := http.NewRequest(http.MethodGet, "http://"+entrance+"/api/status", nil)
		req.Header.Set("X-Dyrokol-Api", tok)
		resp, err := noKeepAlive.Do(req)
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	var adminTok string
	waitFor(t, 15*time.Second, "admin ui through the tunnel", func() (bool, error) {
		adminTok = fx.srv.AdminToken()
		return adminTok != "" && call(adminTok) == http.StatusOK, fmt.Errorf("not yet")
	})
	if !strings.HasPrefix(adminTok, "api_") {
		t.Fatalf("unexpected admin token %q", adminTok)
	}
	if call("api_wrong") != http.StatusUnauthorized {
		t.Fatal("a wrong token must be rejected")
	}

	// the token survives a restart (it lives in the database) and is not
	// sitting in the clear next to it
	fx.restart()
	waitFor(t, 15*time.Second, "admin ui after restart", func() (bool, error) {
		return fx.srv.AdminToken() == adminTok && call(adminTok) == http.StatusOK, fmt.Errorf("not yet")
	})
	raw, err := os.ReadFile(fx.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), adminTok) {
		t.Fatal("admin token is readable in the database file")
	}
}

// TestTokenPrefixFollowsRole: adm_ for admin machines, cl_ for the others, also
// after a role change and a token regeneration.
func TestTokenPrefixFollowsRole(t *testing.T) {
	fx := startRelay(t, t.TempDir())
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	var created struct {
		Token  string `json:"token"`
		Client struct {
			KeyHash string `json:"key_hash"`
		} `json:"client"`
	}
	mustPost(t, fx.base+"/api/clients", map[string]any{"label": "adm", "role": "admin"}, &created)
	if !strings.HasPrefix(created.Token, "adm_") {
		t.Fatalf("admin machine token: %q", created.Token)
	}
	mustPost(t, fx.base+"/api/clients", map[string]any{"label": "box"}, &created)
	if !strings.HasPrefix(created.Token, "cl_") {
		t.Fatalf("client machine token: %q", created.Token)
	}
	hash := created.Client.KeyHash
	regen := func() string {
		t.Helper()
		var out struct {
			Token  string `json:"token"`
			Client struct {
				KeyHash string `json:"key_hash"`
			} `json:"client"`
		}
		mustPost(t, fx.base+"/api/clients/"+hash+"/regenerate", map[string]any{}, &out)
		hash = out.Client.KeyHash // a machine is identified by its token hash
		return out.Token
	}
	setRole := func(role string) {
		t.Helper()
		resp := patchJSON(t, fx.base+"/api/clients/"+hash, map[string]any{"role": role})
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			t.Fatalf("set role %s: %d", role, resp.StatusCode)
		}
	}
	setRole("admin")
	if tok := regen(); !strings.HasPrefix(tok, "adm_") {
		t.Fatalf("regenerated token of a promoted machine: %q", tok)
	}
	setRole("client")
	if tok := regen(); !strings.HasPrefix(tok, "cl_") {
		t.Fatalf("regenerated token of a demoted machine: %q", tok)
	}
}
