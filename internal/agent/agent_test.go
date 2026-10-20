package agent

import (
	"io"
	"log"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dnech/DyroKol/internal/protocol"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func listening(port int) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func ingressPush(version int64, ports map[int64]int) *protocol.ConfigPush {
	push := &protocol.ConfigPush{Version: version}
	for id, port := range ports {
		push.In = append(push.In, protocol.Ingress{
			InPortID: id, Port: port,
			Egress: protocol.EgressSpec{Machine: "relay", Type: protocol.TypeDirect, Host: "127.0.0.1", Port: 1},
		})
	}
	return push
}

func newTestAgent(t *testing.T) *Agent {
	a := New(Config{}, Options{Logger: log.New(io.Discard, "", 0)})
	t.Cleanup(func() { a.closeIngress(func(*ingress) bool { return true }) })
	return a
}

// Ingress ids are only unique per relay: two relays using the same id must
// not overwrite each other's port.
func TestIngressIDsArePerRelay(t *testing.T) {
	a := newTestAgent(t)
	r1 := &session{addr: "relay-1", keyHash: "k1"}
	r2 := &session{addr: "relay-2", keyHash: "k2"}
	p1, p2 := freePort(t), freePort(t)

	for _, step := range []struct {
		sess *session
		push *protocol.ConfigPush
	}{
		{r1, ingressPush(1, map[int64]int{1: p1})},
		{r2, ingressPush(1, map[int64]int{1: p2})},
	} {
		errs, err := a.applyConfig(step.sess, step.push)
		if err != nil || len(errs) != 0 {
			t.Fatalf("apply: %v %v", errs, err)
		}
	}
	if !listening(p1) || !listening(p2) {
		t.Fatalf("both relays' ports must be bound (p1=%v p2=%v)", listening(p1), listening(p2))
	}

	// Removing relay-1's port leaves relay-2's alone.
	if _, err := a.applyConfig(r1, ingressPush(2, nil)); err != nil {
		t.Fatal(err)
	}
	if listening(p1) || !listening(p2) {
		t.Fatalf("after removal: p1=%v p2=%v", listening(p1), listening(p2))
	}

	// A port held by another relay is a bind error, not a takeover.
	errs, err := a.applyConfig(r1, ingressPush(3, map[int64]int{7: p2}))
	if err != nil || len(errs) != 1 || errs[0].InPortID != 7 {
		t.Fatalf("conflicting port: %+v %v", errs, err)
	}
}

func TestApplyConfigMovesPort(t *testing.T) {
	a := newTestAgent(t)
	r := &session{addr: "relay", keyHash: "k"}
	p1, p2 := freePort(t), freePort(t)
	a.applyConfig(r, ingressPush(1, map[int64]int{1: p1}))
	a.applyConfig(r, ingressPush(2, map[int64]int{1: p2}))
	if listening(p1) || !listening(p2) {
		t.Fatalf("port not moved: p1=%v p2=%v", listening(p1), listening(p2))
	}
}

func TestApplyConfigRejectsMalformedPush(t *testing.T) {
	a := newTestAgent(t)
	r := &session{addr: "relay"}
	p := freePort(t)
	for name, push := range map[string]*protocol.ConfigPush{
		"duplicate port": ingressPush(1, map[int64]int{1: p, 2: p}),
		"bad port":       ingressPush(1, map[int64]int{1: 70000}),
		"bad filter":     {In: []protocol.Ingress{{InPortID: 1, Port: p, Deny: []string{"bad[x"}}}},
	} {
		if _, err := a.applyConfig(r, push); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if listening(p) {
		t.Fatal("rejected push bound a port")
	}
}

func TestReportDrainsCounters(t *testing.T) {
	a := newTestAgent(t)
	r := &session{addr: "relay"}
	p := freePort(t)
	a.applyConfig(r, ingressPush(1, map[int64]int{5: p}))
	in := a.ingress[ingressKey{"relay", 5}]
	in.Counters.In.Add(10)
	in.Counters.Out.Add(20)

	rep := a.report("relay")
	if len(rep.Stats) != 1 || rep.Stats[0].BytesIn != 10 || rep.Stats[0].BytesOut != 20 {
		t.Fatalf("first report: %+v", rep.Stats)
	}
	rep = a.report("relay")
	if len(rep.Stats) != 1 || rep.Stats[0].BytesIn != 0 {
		t.Fatalf("second report must carry only new deltas: %+v", rep.Stats)
	}
	if len(a.report("other").Stats) != 0 {
		t.Fatal("report leaked ports of another relay")
	}
}

func TestNormalizeAddr(t *testing.T) {
	for in, want := range map[string]string{
		"relay.example.com":           "relay.example.com:7443",
		"relay.example.com:5000":      "relay.example.com:5000",
		"203.0.113.5":                 "203.0.113.5:7443",
		"203.0.113.5:7443":            "203.0.113.5:7443",
		" https://relay.example.com/": "relay.example.com:7443",
		"tcp://relay.example.com:81":  "relay.example.com:81",
		"::1":                         "[::1]:7443",
		"[::1]:9":                     "[::1]:9",
		"relay.example.com:":          "relay.example.com:7443",
		"":                            "",
	} {
		if got := NormalizeAddr(in); got != want {
			t.Errorf("NormalizeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "dyrokol.yaml")
	created, err := WriteTemplate(path)
	if err != nil || !created {
		t.Fatalf("write template: %v %v", created, err)
	}
	if again, err := WriteTemplate(path); err != nil || again {
		t.Fatalf("an existing config must be left alone: %v %v", again, err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("the template must parse: %v", err)
	}
	if err := CheckConfig(cfg); err == nil {
		t.Fatal("a config with placeholders must not be runnable")
	}
	// registering a real machine replaces the stub instead of keeping it
	if err := SaveServer(path, ServerEntry{Addr: "relay.example.org", Token: "adm_x", DBPassword: "p"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = LoadConfig(path)
	if len(cfg.Servers) != 1 || cfg.Servers[0].Addr != "relay.example.org:7443" {
		t.Fatalf("stub not replaced: %+v", cfg.Servers)
	}
	if err := CheckConfig(cfg); err != nil {
		t.Fatalf("filled config rejected: %v", err)
	}
	if CheckConfig(Config{}) == nil || CheckConfig(Config{Servers: []ServerEntry{{Addr: "h:1"}}}) == nil {
		t.Fatal("empty config / missing token must be rejected")
	}
}
