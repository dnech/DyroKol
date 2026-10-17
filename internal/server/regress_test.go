package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/dnech/DyroKol/internal/agent"
	"github.com/dnech/DyroKol/internal/protocol"
	"github.com/dnech/DyroKol/internal/transport"
)

// tcpEcho is a raw TCP echo server counting accepted connections.
type tcpEcho struct {
	port    int
	accepts atomic.Int32
}

func startTCPEcho(t *testing.T) *tcpEcho {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	e := &tcpEcho{port: ln.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			e.accepts.Add(1)
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()
	return e
}

// rawSession connects to the relay like an agent but hands back the yamux
// session, so a test can open arbitrary streams.
func rawSession(t *testing.T, fx *relayFixture, token, password string) *yamux.Session {
	t.Helper()
	conn, err := transport.DialTLS(fx.relayAddr, "", fx.pinsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.Handshake(conn, protocol.Hello{
		Purpose: protocol.PurposeConnect, Token: token, DBPassword: password,
	}); err != nil {
		t.Fatal(err)
	}
	mux, err := yamux.Client(conn, transport.MuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mux.Close() })
	return mux
}

func createID(t *testing.T, url string, payload any) int64 {
	t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	mustPost(t, url, payload, &out)
	return out.ID
}

// An agent may only open route streams for ingress ports it owns; otherwise
// it could exit through any endpoint of the mesh.
func TestRouteStreamRequiresOwnership(t *testing.T) {
	fx := startRelay(t, t.TempDir())
	_, password, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath)
	if err != nil {
		t.Fatal(err)
	}
	token, hash := newMachine(t, fx.base, "intruder")
	target := startTCPEcho(t)

	ep := createID(t, fx.base+"/api/endpoints", map[string]any{"port": target.port})
	foreign := createID(t, fx.base+"/api/inports", map[string]any{"port": freePort(t)})
	own := createID(t, fx.base+"/api/inports", map[string]any{"port": freePort(t), "machine": hash})
	mustPost(t, fx.base+"/api/routes", map[string]any{"inport_id": foreign, "endpoint_id": ep}, nil)
	mustPost(t, fx.base+"/api/routes", map[string]any{"inport_id": own, "endpoint_id": ep}, nil)

	mux := rawSession(t, fx, token, password)
	roundtrip := func(inportID int64) error {
		stream, err := mux.OpenStream()
		if err != nil {
			return err
		}
		defer stream.Close()
		protocol.WriteMsg(stream, protocol.StreamHeader{T: protocol.StreamRoute, InPortID: inportID})
		stream.Write([]byte("ping"))
		stream.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 4)
		_, err = io.ReadFull(stream, buf)
		return err
	}
	if err := roundtrip(foreign); err == nil {
		t.Fatal("route stream for a foreign ingress port reached the endpoint")
	}
	if n := target.accepts.Load(); n != 0 {
		t.Fatalf("target dialed %d times for a foreign port", n)
	}
	if err := roundtrip(own); err != nil {
		t.Fatalf("route stream for an owned port: %v", err)
	}
}

// Agent-side traffic must be accounted exactly: every report carries data.
func TestAgentStatsAreExact(t *testing.T) {
	fx := startRelay(t, t.TempDir())
	_, password, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath)
	if err != nil {
		t.Fatal(err)
	}
	token, hash := newMachine(t, fx.base, "nas")
	startAgent(t, fx, agent.ServerEntry{Addr: fx.relayAddr, Token: token, DBPassword: password})
	target := startTCPEcho(t)
	port := freePort(t)

	in := createID(t, fx.base+"/api/inports", map[string]any{"port": port, "machine": hash})
	ep := createID(t, fx.base+"/api/endpoints", map[string]any{"port": target.port, "machine": hash})
	mustPost(t, fx.base+"/api/routes", map[string]any{"inport_id": in, "endpoint_id": ep}, nil)

	var sent int64
	send := func(n int) error {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
		if err != nil {
			return err
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		payload := []byte(strings.Repeat("x", n))
		if _, err := c.Write(payload); err != nil {
			return err
		}
		if _, err := io.ReadFull(c, payload); err != nil {
			return err
		}
		sent += int64(n)
		return nil
	}
	waitFor(t, 15*time.Second, "ingress bound", func() (bool, error) { return send(1000) == nil, nil })
	// Spread connections over several report ticks.
	for _, n := range []int{2345, 777} {
		time.Sleep(reportTickForTests)
		if err := send(n); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 30*time.Second, "exact totals", func() (bool, error) {
		st, err := fetchStatus(fx.base)
		if err != nil {
			return false, err
		}
		for _, p := range st.InPorts {
			if p.ID == in {
				return p.BytesIn == sent && p.BytesOut == sent && p.Conns == 3,
					fmt.Errorf("in=%d out=%d conns=%d, want %d/%d/3", p.BytesIn, p.BytesOut, p.Conns, sent, sent)
			}
		}
		return false, fmt.Errorf("ingress missing")
	})
}

const reportTickForTests = 2600 * time.Millisecond

func TestEventsReachTheLog(t *testing.T) {
	fx := startRelay(t, t.TempDir())
	token, password, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath)
	if err != nil {
		t.Fatal(err)
	}
	startAgent(t, fx, agent.ServerEntry{Addr: fx.relayAddr, Token: token, DBPassword: password,
		AdminListen: "127.0.0.1:" + strconv.Itoa(freePort(t))})
	waitFor(t, 20*time.Second, "connect event logged", func() (bool, error) {
		body, err := get(fx.base + "/api/logs")
		return err == nil && strings.Contains(body, "connected") && strings.Contains(body, "database initialized"), err
	})
}

func TestLastAdminCannotBeDeleted(t *testing.T) {
	fx := startRelay(t, t.TempDir())
	token, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath)
	if err != nil {
		t.Fatal(err)
	}
	req := newAPIRequest(t, http.MethodDelete, fx.base+"/api/clients/"+protocol.KeyHash(token), nil)
	resp, err := noKeepAlive.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete last admin: got %d, want 400", resp.StatusCode)
	}
}

func TestRouteCacheIgnoresStaleLoads(t *testing.T) {
	var c routeCache
	// A load that races with an invalidation must not be cached.
	c.get(1, func(int64) *route {
		c.invalidate()
		return &route{found: true}
	})
	loads := 0
	r := c.get(1, func(int64) *route { loads++; return &route{} })
	if loads != 1 || r.found {
		t.Fatalf("stale route served from cache (loads=%d)", loads)
	}
	c.get(1, func(int64) *route { loads++; return &route{} })
	if loads != 1 {
		t.Fatal("fresh route not cached")
	}
}

func TestSameOrigin(t *testing.T) {
	for _, tc := range []struct {
		origin, host string
		want         bool
	}{
		{"http://127.0.0.1:9180", "127.0.0.1:9180", true},
		{"http://localhost:9180", "127.0.0.1:9180", true},
		{"http://[::1]:9180", "localhost:9180", true},
		{"https://admin.example:443", "admin.example:443", true},
		{"http://evil.example", "127.0.0.1:9180", false},
		{"http://127.0.0.1.evil.example", "127.0.0.1:9180", false},
	} {
		if got := sameOrigin(tc.origin, tc.host); got != tc.want {
			t.Errorf("sameOrigin(%q, %q) = %v, want %v", tc.origin, tc.host, got, tc.want)
		}
	}
}

// Two machines can both expose port 8080; the API only refuses a duplicate on
// the same machine.
func TestSamePortOnDifferentMachines(t *testing.T) {
	fx := startRelay(t, t.TempDir())
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatal(err)
	}
	_, nas := newMachine(t, fx.base, "nas")
	_, pc := newMachine(t, fx.base, "pc")
	const port = 8080

	createID(t, fx.base+"/api/inports", map[string]any{"port": port, "machine": nas})
	createID(t, fx.base+"/api/inports", map[string]any{"port": port, "machine": pc})
	dup := postJSON(t, fx.base+"/api/inports", map[string]any{"port": port, "machine": pc})
	dup.Body.Close()
	if dup.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate on one machine: got %d, want 409", dup.StatusCode)
	}
}

// Several ingress ports may route to the same egress endpoint (fan-in).
func TestManyIngressToOneEgress(t *testing.T) {
	fx := startRelay(t, t.TempDir())
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatal(err)
	}
	_, echoAddr, echoPort := echoServer(t)
	echoHost, _, _ := net.SplitHostPort(echoAddr)
	ep := createID(t, fx.base+"/api/endpoints", map[string]any{"host": echoHost, "port": echoPort})
	ports := []int{freePort(t), freePort(t), freePort(t)}
	for _, p := range ports {
		in := createID(t, fx.base+"/api/inports", map[string]any{"port": p})
		mustPost(t, fx.base+"/api/routes", map[string]any{"inport_id": in, "endpoint_id": ep}, nil)
	}
	for _, p := range ports {
		p := p
		waitFor(t, 10*time.Second, fmt.Sprintf("port %d forwards", p), func() (bool, error) {
			body, err := get(fmt.Sprintf("http://127.0.0.1:%d/", p))
			return body == "pong", err
		})
	}
}

// PUT /api/endpoints/{id}/routes sets all ingress ports of an endpoint at once
// and closes connections of ports that lost their route.
func TestSetEndpointRoutesAPI(t *testing.T) {
	fx := startRelay(t, t.TempDir())
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatal(err)
	}
	_, echoAddr, echoPort := echoServer(t)
	echoHost, _, _ := net.SplitHostPort(echoAddr)
	ep := createID(t, fx.base+"/api/endpoints", map[string]any{"host": echoHost, "port": echoPort})
	p1, p2 := freePort(t), freePort(t)
	in1 := createID(t, fx.base+"/api/inports", map[string]any{"port": p1})
	in2 := createID(t, fx.base+"/api/inports", map[string]any{"port": p2})

	put := func(ids []int64) *http.Response {
		data, _ := json.Marshal(map[string]any{"inport_ids": ids})
		req := newAPIRequest(t, http.MethodPut, fmt.Sprintf("%s/api/endpoints/%d/routes", fx.base, ep), bytes.NewReader(data))
		resp, err := noKeepAlive.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	forwards := func(p int) bool {
		body, err := get(fmt.Sprintf("http://127.0.0.1:%d/", p))
		return err == nil && body == "pong"
	}

	if r := put([]int64{in1, in2}); r.StatusCode != http.StatusNoContent {
		t.Fatalf("put: %d", r.StatusCode)
	}
	waitFor(t, 10*time.Second, "both ports forward", func() (bool, error) {
		return forwards(p1) && forwards(p2), nil
	})

	if r := put([]int64{in1}); r.StatusCode != http.StatusNoContent {
		t.Fatalf("shrink: %d", r.StatusCode)
	}
	waitFor(t, 10*time.Second, "second port stops forwarding", func() (bool, error) {
		return forwards(p1) && !forwards(p2), nil
	})

	if r := put([]int64{in1, 12345}); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown ingress: got %d, want 404", r.StatusCode)
	}
}

func statusCode(resp *http.Response) int {
	resp.Body.Close()
	return resp.StatusCode
}

// Changing the number of a relay-owned ingress port rebinds it; an unusable
// number is refused and the old one keeps working.
func TestEditRelayPortNumber(t *testing.T) {
	fx := startRelay(t, t.TempDir())
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatal(err)
	}
	_, echoAddr, echoPort := echoServer(t)
	echoHost, _, _ := net.SplitHostPort(echoAddr)
	ep := createID(t, fx.base+"/api/endpoints", map[string]any{"host": echoHost, "port": echoPort})
	p1, p2 := freePort(t), freePort(t)
	in := createID(t, fx.base+"/api/inports", map[string]any{"port": p1})
	createID(t, fx.base+"/api/inports", map[string]any{"port": freePort(t)}) // a neighbour on another port
	mustPost(t, fx.base+"/api/routes", map[string]any{"inport_id": in, "endpoint_id": ep}, nil)
	url := fmt.Sprintf("%s/api/inports/%d", fx.base, in)
	forwards := func(p int) bool {
		body, err := get(fmt.Sprintf("http://127.0.0.1:%d/", p))
		return err == nil && body == "pong"
	}
	waitFor(t, 10*time.Second, "initial port forwards", func() (bool, error) { return forwards(p1), nil })

	if code := statusCode(patchJSON(t, url, map[string]any{"port": p2})); code != http.StatusNoContent {
		t.Fatalf("change port: got %d", code)
	}
	waitFor(t, 10*time.Second, "traffic moves to the new port", func() (bool, error) {
		return forwards(p2) && !forwards(p1), nil
	})

	// a number that is busy on the host is refused and rolled back
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port
	// the relay binds on all interfaces, so occupy that address too
	wild, err := net.Listen("tcp", fmt.Sprintf(":%d", busyPort))
	if err == nil {
		defer wild.Close()
	}
	if code := statusCode(patchJSON(t, url, map[string]any{"port": busyPort})); code != http.StatusConflict {
		t.Fatalf("busy port: got %d, want 409", code)
	}
	waitFor(t, 10*time.Second, "old number still works after the refused change", func() (bool, error) {
		return forwards(p2), nil
	})
	st, err := fetchStatus(fx.base)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.InPorts {
		if p.ID == in && p.Port != p2 {
			t.Fatalf("port not rolled back: %d", p.Port)
		}
	}

	// another ingress port of the same machine already has that number
	st, _ = fetchStatus(fx.base)
	var neighbour int
	for _, p := range st.InPorts {
		if p.ID != in {
			neighbour = p.Port
		}
	}
	if code := statusCode(patchJSON(t, url, map[string]any{"port": neighbour})); code != http.StatusConflict {
		t.Fatalf("duplicate port: got %d, want 409", code)
	}
}

func TestPatchRouteAPI(t *testing.T) {
	fx := startRelay(t, t.TempDir())
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatal(err)
	}
	_, echoAddr, echoPort := echoServer(t)
	echoHost, _, _ := net.SplitHostPort(echoAddr)
	dead := createID(t, fx.base+"/api/endpoints", map[string]any{"host": echoHost, "port": freePort(t)})
	live := createID(t, fx.base+"/api/endpoints", map[string]any{"host": echoHost, "port": echoPort})
	port := freePort(t)
	in := createID(t, fx.base+"/api/inports", map[string]any{"port": port})
	var rt struct {
		ID int64 `json:"id"`
	}
	mustPost(t, fx.base+"/api/routes", map[string]any{"inport_id": in, "endpoint_id": dead}, &rt)
	url := fmt.Sprintf("%s/api/routes/%d", fx.base, rt.ID)

	if code := statusCode(patchJSON(t, url, map[string]any{"endpoint_id": live})); code != http.StatusNoContent {
		t.Fatalf("re-point route: got %d", code)
	}
	waitFor(t, 10*time.Second, "route points at the live endpoint", func() (bool, error) {
		body, err := get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		return body == "pong", err
	})
	if code := statusCode(patchJSON(t, url, map[string]any{"endpoint_id": 4242})); code != http.StatusNotFound {
		t.Fatalf("unknown endpoint: got %d, want 404", code)
	}
	other := createID(t, fx.base+"/api/inports", map[string]any{"port": freePort(t)})
	mustPost(t, fx.base+"/api/routes", map[string]any{"inport_id": other, "endpoint_id": live}, nil)
	if code := statusCode(patchJSON(t, url, map[string]any{"inport_id": other})); code != http.StatusConflict {
		t.Fatalf("ingress already routed: got %d, want 409", code)
	}
}

func TestMapAPI(t *testing.T) {
	fx := startRelay(t, t.TempDir())
	if _, _, err := agent.Bootstrap(fx.relayAddr, fx.pinsPath); err != nil {
		t.Fatal(err)
	}
	body, err := get(fx.base + "/api/map")
	if err != nil || !strings.Contains(body, `"nodes":{}`) || !strings.Contains(body, `"items":[]`) {
		t.Fatalf("empty map: %q %v", body, err)
	}
	layout := `{"nodes":{"in:1":{"x":40,"y":60}},"items":[{"id":"f1","kind":"frame","x":0,"y":0,"w":300,"h":200,"text":"Home","color":"blue"}]}`
	put := func(payload string) int {
		req := newAPIRequest(t, http.MethodPut, fx.base+"/api/map", strings.NewReader(payload))
		resp, err := noKeepAlive.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return statusCode(resp)
	}
	if code := put(layout); code != http.StatusNoContent {
		t.Fatalf("save: got %d", code)
	}
	body, _ = get(fx.base + "/api/map")
	if !strings.Contains(body, `"Home"`) || !strings.Contains(body, `"x":40`) {
		t.Fatalf("saved layout not returned: %s", body)
	}
	for name, bad := range map[string]string{
		"bad kind": `{"items":[{"id":"a","kind":"circle"}]}`,
		"bad key":  `{"nodes":{"zz":{"x":1,"y":1}}}`,
		"extra":    `{"unknown":1}`,
		"not json": `nope`,
	} {
		if code := put(bad); code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, code)
		}
	}
	if body, _ = get(fx.base + "/api/map"); !strings.Contains(body, `"Home"`) {
		t.Fatal("a rejected layout replaced the saved one")
	}
}
