package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPassword = "password-12345678"

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "db.enc")
	s, err := Bootstrap(path, testPassword, "adminhash")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return s, path
}

func mustClient(t *testing.T, s *Store, key string) {
	t.Helper()
	if _, err := s.CreateClient(key, "client", key); err != nil {
		t.Fatalf("create client %s: %v", key, err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestBootstrapRoundtripAndEncryption(t *testing.T) {
	s, path := newStore(t)
	if _, err := Bootstrap(path, testPassword, "adminhash"); !errors.Is(err, ErrExists) {
		t.Fatalf("second bootstrap: got %v, want ErrExists", err)
	}
	mustClient(t, s, "clienthash")

	ip, err := s.CreateInPort(InPort{Port: 21000, Label: "nas-http"})
	if err != nil || ip.Machine != MachineRelay {
		t.Fatalf("create inport: %+v, %v", ip, err)
	}
	if _, err := s.CreateInPort(InPort{Port: 21000}); !errors.Is(err, ErrPortInUse) || !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate port: got %v, want ErrPortInUse", err)
	}
	if _, err := s.CreateInPort(InPort{Machine: "unknown", Port: 21001}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown machine: got %v, want ErrNotFound", err)
	}
	ep, err := s.CreateEndpoint(Endpoint{Machine: "clienthash", Port: 8080, Label: "web"})
	if err != nil || ep.Host != "127.0.0.1" || ep.Type != TypeDirect {
		t.Fatalf("create endpoint: %+v, %v", ep, err)
	}
	if _, err := s.CreateEndpoint(Endpoint{Machine: "clienthash", Host: "127.0.0.1", Port: 8080}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate endpoint: got %v, want ErrDuplicate", err)
	}
	if _, err := s.CreateRoute(ip.ID, ep.ID); err != nil {
		t.Fatalf("create route: %v", err)
	}
	if _, err := s.CreateRoute(ip.ID, ep.ID); !errors.Is(err, ErrAlreadyRouted) {
		t.Fatalf("second route: got %v, want ErrAlreadyRouted", err)
	}
	now := time.Now().Unix()
	if err := s.Record(Flush{
		TS:     now,
		Stats:  []StatUpdate{{InPortID: ip.ID, BytesIn: 100, BytesOut: 200, Conns: 3}},
		Peers:  []Peer{{IP: "203.0.113.9", InPortID: ip.ID, Conns: 2, BytesIn: 60, BytesOut: 40}},
		Online: []Seen{{KeyHash: "clienthash", IP: "198.51.100.7"}},
		Logs:   []LogEntry{{TS: now, Level: "info", Source: "test", Message: "hello"}},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Nothing but ciphertext is left on disk.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("CREATE TABLE")) || bytes.Contains(raw, []byte("clienthash")) || bytes.Contains(raw, []byte("SQLite")) {
			t.Fatalf("%s contains plaintext", e.Name())
		}
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected files on disk: %v", entries)
	}

	s2, err := Open(path, testPassword)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s2.Close()
	topo, err := s2.Topology()
	if err != nil {
		t.Fatal(err)
	}
	if len(topo.Clients) != 2 || len(topo.InPorts) != 1 || len(topo.Endpoints) != 1 || len(topo.Routes) != 1 {
		t.Fatalf("topology after reopen: %+v", topo)
	}
	if topo.Version < 5 {
		t.Fatalf("config version not bumped per change: %d", topo.Version)
	}
	in, routed, err := s2.Resolve(ip.ID)
	if err != nil || routed == nil || routed.ID != ep.ID || in.Port != 21000 {
		t.Fatalf("resolve: %+v %+v %v", in, routed, err)
	}
	totals, _ := s2.Totals()
	if totals[ip.ID] != (Totals{BytesIn: 100, BytesOut: 200, Conns: 3}) {
		t.Fatalf("totals: %+v", totals)
	}
	presence, _ := s2.Presence()
	if presence["clienthash"] != (Presence{LastSeen: now, LastIP: "198.51.100.7"}) {
		t.Fatalf("presence: %+v", presence)
	}
	peers, _ := s2.ListPeers(50)
	if len(peers) != 1 || peers[0].BytesIn != 60 {
		t.Fatalf("peers: %+v", peers)
	}
	samples, _ := s2.SamplesSince(now - 1)
	if len(samples) != 1 || samples[0].InPortID != ip.ID {
		t.Fatalf("samples: %+v", samples)
	}
	logs, _ := s2.ListLogs(10)
	if len(logs) != 1 || logs[0].Message != "hello" {
		t.Fatalf("logs: %+v", logs)
	}
}

func TestOpenErrors(t *testing.T) {
	s, path := newStore(t)
	s.Close()
	if _, err := Open(path, "wrong-password"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("got %v, want ErrBadPassword", err)
	}
	if _, err := Open(filepath.Join(t.TempDir(), "missing.enc"), "x"); !errors.Is(err, ErrNoDB) {
		t.Fatalf("got %v, want ErrNoDB", err)
	}
	// The data database is disposable: it is recreated when missing.
	if err := os.Remove(DataPath(path)); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path, testPassword)
	if err != nil {
		t.Fatalf("open without data db: %v", err)
	}
	s2.Close()
}

func TestDeleteCascades(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	mustClient(t, s, "clienthash")
	own, _ := s.CreateInPort(InPort{Machine: "clienthash", Port: 22000})
	relayIn, _ := s.CreateInPort(InPort{Port: 22001})
	ep, _ := s.CreateEndpoint(Endpoint{Machine: "clienthash", Port: 8080})
	s.CreateRoute(own.ID, ep.ID)
	s.CreateRoute(relayIn.ID, ep.ID)

	if err := s.DeleteClient("clienthash"); err != nil {
		t.Fatalf("delete client: %v", err)
	}
	topo, _ := s.Topology()
	if len(topo.InPorts) != 1 || topo.InPorts[0].ID != relayIn.ID || len(topo.Endpoints) != 0 || len(topo.Routes) != 0 {
		t.Fatalf("after delete: %+v", topo)
	}
	if err := s.DeleteClient("clienthash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}

	// Deleting an endpoint reports the ingress ports that lost their route.
	ep2, _ := s.CreateEndpoint(Endpoint{Port: 9090})
	s.CreateRoute(relayIn.ID, ep2.ID)
	unrouted, err := s.DeleteEndpoint(ep2.ID)
	if err != nil || len(unrouted) != 1 || unrouted[0] != relayIn.ID {
		t.Fatalf("delete endpoint: %v, %v", unrouted, err)
	}
}

func TestLastAdminIsProtected(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	if err := s.UpdateClient("adminhash", ClientPatch{Role: ptr("client")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("demote last admin: %v", err)
	}
	if err := s.DeleteClient("adminhash"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("delete last admin: %v", err)
	}
	mustClient(t, s, "second")
	if err := s.UpdateClient("second", ClientPatch{Role: ptr("admin")}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteClient("adminhash"); err != nil {
		t.Fatalf("delete one of two admins: %v", err)
	}
}

func TestEndpointValidation(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	mustClient(t, s, "clienthash")
	for name, e := range map[string]Endpoint{
		"host with port":    {Machine: "clienthash", Host: "127.0.0.1:8080", Port: 80},
		"unknown type":      {Machine: "clienthash", Type: "udp", Host: "x", Port: 1},
		"socks5 login only": {Machine: "clienthash", Type: TypeSocks5, Socks5User: "u"},
		"bad filter":        {Machine: "clienthash", Host: "h", Port: 1, Deny: []string{"bad[pattern"}},
		"port out of range": {Machine: "clienthash", Port: 70000},
		"label too long":    {Machine: "clienthash", Port: 1, Label: string(make([]byte, MaxLabel+1))},
	} {
		if _, err := s.CreateEndpoint(e); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.CreateEndpoint(Endpoint{Machine: "nope", Port: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown machine: %v", err)
	}

	socks, err := s.CreateEndpoint(Endpoint{
		Machine: "clienthash", Type: TypeSocks5, Host: "ignored", Port: 443,
		Socks5User: "u", Socks5Pass: "p",
	})
	if err != nil {
		t.Fatalf("create socks5: %v", err)
	}
	if socks.Host != "" || socks.Port != 0 {
		t.Fatalf("socks5 endpoint keeps a target: %+v", socks)
	}
	if err := s.UpdateEndpoint(socks.ID, EndpointPatch{Patch: Patch{Deny: &[]string{"*.blocked.example"}, Label: ptr("proxied")}}); err != nil {
		t.Fatalf("update endpoint: %v", err)
	}
	got, _ := s.GetEndpoint(socks.ID)
	if got.Label != "proxied" || len(got.Deny) != 1 || got.Socks5Pass != "p" {
		t.Fatalf("endpoint after update: %+v", got)
	}
	if err := s.UpdateEndpoint(socks.ID, EndpointPatch{Patch: Patch{Allow: &[]string{"bad[x"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid patch: %v", err)
	}
}

func TestSourceFilterRejectsPorts(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	if _, err := s.CreateInPort(InPort{Port: 9000, Deny: []string{"*:25"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("source filter with a port: got %v, want ErrInvalid", err)
	}
	ip, err := s.CreateInPort(InPort{Port: 9000, Deny: []string{"10.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateInPort(ip.ID, InPortPatch{Patch: Patch{Allow: &[]string{"1.2.3.4:80"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("patch with a port: got %v, want ErrInvalid", err)
	}
	// a destination filter may carry a port
	if _, err := s.CreateEndpoint(Endpoint{Port: 25, Deny: []string{"*:25"}}); err != nil {
		t.Fatalf("destination filter with a port: %v", err)
	}
}

func TestRotateClientKey(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	mustClient(t, s, "clienthash")
	mustClient(t, s, "otherhash")
	ip, _ := s.CreateInPort(InPort{Machine: "clienthash", Port: 21000})
	ep, _ := s.CreateEndpoint(Endpoint{Machine: "clienthash", Host: "h", Port: 80})

	if _, err := s.RotateClientKey("clienthash", "otherhash"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rotate onto existing key: %v", err)
	}
	c, err := s.RotateClientKey("clienthash", "newhash")
	if err != nil || c.KeyHash != "newhash" {
		t.Fatalf("rotate: %+v, %v", c, err)
	}
	if _, err := s.GetClient("clienthash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old key still present: %v", err)
	}
	if got, _ := s.GetInPort(ip.ID); got.Machine != "newhash" {
		t.Fatalf("inport not repointed: %+v", got)
	}
	if got, _ := s.GetEndpoint(ep.ID); got.Machine != "newhash" {
		t.Fatalf("endpoint not repointed: %+v", got)
	}
}

func TestBackupRoundtrip(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	mustClient(t, s, "clienthash")
	ip, _ := s.CreateInPort(InPort{Port: 21000, Allow: []string{"192.168.0.0/16"}})
	ep, _ := s.CreateEndpoint(Endpoint{
		Machine: "clienthash", Type: TypeSocks5,
		Socks5User: "u", Socks5Pass: "secret",
	})
	s.CreateRoute(ip.ID, ep.ID)
	if err := s.SaveSettings(Settings{Autostart: true}); err != nil {
		t.Fatal(err)
	}

	redacted, _ := s.Export(false)
	if redacted.Endpoints[0].Socks5Pass != "" {
		t.Fatal("password leaked in secretless export")
	}
	full, err := s.Export(true)
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(full)
	parsed, err := ParseSnapshot(blob)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	s2, _ := newStore(t)
	defer s2.Close()
	before, _ := s2.ConfigVersion()
	if err := s2.Import(parsed); err != nil {
		t.Fatalf("import: %v", err)
	}
	topo, _ := s2.Topology()
	if len(topo.Clients) != 2 || len(topo.InPorts) != 1 || len(topo.Endpoints) != 1 || len(topo.Routes) != 1 {
		t.Fatalf("after import: %+v", topo)
	}
	if topo.Routes[0].InPortID != topo.InPorts[0].ID || topo.Routes[0].EndpointID != topo.Endpoints[0].ID {
		t.Fatalf("route not remapped: %+v", topo)
	}
	if topo.Endpoints[0].Socks5Pass != "secret" || topo.InPorts[0].Allow[0] != "192.168.0.0/16" {
		t.Fatalf("fields lost: %+v", topo)
	}
	if topo.Version <= before {
		t.Fatalf("config version did not grow: %d → %d", before, topo.Version)
	}
	if st, _ := s2.GetSettings(); !st.Autostart {
		t.Fatal("settings not imported")
	}

	for name, snap := range map[string]Snapshot{
		"no admin":   {Format: SnapshotFormat, Schema: schemaVersion},
		"bad format": {Format: "nope"},
		"bad route": {Format: SnapshotFormat, Schema: schemaVersion,
			Clients: []Client{{KeyHash: "a", Role: "admin"}},
			Routes:  []Route{{InPortID: 1, EndpointID: 1}}},
	} {
		if err := s2.Import(&snap); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if _, err := ParseSnapshot([]byte(`{"format":"dyrokol-config","unknown":1}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown field accepted: %v", err)
	}
}

// The same port number may be used on different machines; on one machine it
// is unique.
func TestIngressPortIsUniquePerMachine(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	mustClient(t, s, "nas")
	mustClient(t, s, "pc")

	for _, machine := range []string{MachineRelay, "nas", "pc"} {
		if _, err := s.CreateInPort(InPort{Machine: machine, Port: 8080}); err != nil {
			t.Fatalf("port 8080 on %s: %v", machine, err)
		}
	}
	if _, err := s.CreateInPort(InPort{Machine: "nas", Port: 8080}); !errors.Is(err, ErrPortInUse) {
		t.Fatalf("same port on the same machine: got %v, want ErrPortInUse", err)
	}

	// backup and restore keep all three, and still reject a real duplicate
	snap, err := s.Export(true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Import(snap); err != nil {
		t.Fatalf("import of per-machine ports: %v", err)
	}
	if ports, _ := s.ListInPorts(); len(ports) != 3 {
		t.Fatalf("ports after import: %+v", ports)
	}
	snap.InPorts = append(snap.InPorts, InPort{ID: 99, Machine: "pc", Port: 8080})
	if err := s.Import(snap); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate port on one machine in a snapshot: got %v, want ErrInvalid", err)
	}
}

func TestSetEndpointRoutes(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	a, _ := s.CreateInPort(InPort{Port: 1001})
	b, _ := s.CreateInPort(InPort{Port: 1002})
	c, _ := s.CreateInPort(InPort{Port: 1003})
	e1, _ := s.CreateEndpoint(Endpoint{Port: 5001})
	e2, _ := s.CreateEndpoint(Endpoint{Port: 5002})
	routedTo := func(ep int64) []int64 {
		var out []int64
		rs, _ := s.ListRoutes()
		for _, r := range rs {
			if r.EndpointID == ep {
				out = append(out, r.InPortID)
			}
		}
		return out
	}

	// fan-in: three ingress ports feed one endpoint
	if _, err := s.SetEndpointRoutes(e1.ID, []int64{a.ID, b.ID, c.ID}); err != nil {
		t.Fatal(err)
	}
	if got := routedTo(e1.ID); len(got) != 3 {
		t.Fatalf("e1 routes: %v", got)
	}

	// a port listed for another endpoint is moved, and reported as affected
	affected, err := s.SetEndpointRoutes(e2.ID, []int64{c.ID})
	if err != nil || len(affected) != 1 || affected[0] != c.ID {
		t.Fatalf("move c to e2: %v, %v", affected, err)
	}
	if len(routedTo(e1.ID)) != 2 || len(routedTo(e2.ID)) != 1 {
		t.Fatalf("after move: e1=%v e2=%v", routedTo(e1.ID), routedTo(e2.ID))
	}

	// ports not listed lose their route to this endpoint only
	affected, err = s.SetEndpointRoutes(e1.ID, []int64{a.ID})
	if err != nil || len(affected) != 1 || affected[0] != b.ID {
		t.Fatalf("shrink e1: %v, %v", affected, err)
	}
	if len(routedTo(e2.ID)) != 1 {
		t.Fatal("shrinking e1 touched e2")
	}

	// no-op spends no config version
	before, _ := s.ConfigVersion()
	if _, err := s.SetEndpointRoutes(e1.ID, []int64{a.ID}); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.ConfigVersion(); after != before {
		t.Fatalf("no-op bumped the config version: %d → %d", before, after)
	}

	// unknown ids are rejected and change nothing
	if _, err := s.SetEndpointRoutes(e1.ID, []int64{a.ID, 999}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown ingress: %v", err)
	}
	if _, err := s.SetEndpointRoutes(999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown endpoint: %v", err)
	}
	if len(routedTo(e1.ID)) != 1 {
		t.Fatal("a rejected call changed routes")
	}
	// clearing
	if _, err := s.SetEndpointRoutes(e1.ID, nil); err != nil || len(routedTo(e1.ID)) != 0 {
		t.Fatalf("clear: %v %v", err, routedTo(e1.ID))
	}
}

func TestEditPortNumbers(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	mustClient(t, s, "nas")
	a, _ := s.CreateInPort(InPort{Port: 8080})
	b, _ := s.CreateInPort(InPort{Port: 8081})
	onNas, _ := s.CreateInPort(InPort{Machine: "nas", Port: 8080})

	if err := s.UpdateInPort(a.ID, InPortPatch{Port: ptr(9090)}); err != nil {
		t.Fatalf("change port: %v", err)
	}
	if got, _ := s.GetInPort(a.ID); got.Port != 9090 {
		t.Fatalf("port not changed: %+v", got)
	}
	if err := s.UpdateInPort(b.ID, InPortPatch{Port: ptr(9090)}); !errors.Is(err, ErrPortInUse) {
		t.Fatalf("port taken on the same machine: %v", err)
	}
	// the same number on another machine is fine
	if err := s.UpdateInPort(onNas.ID, InPortPatch{Port: ptr(8081)}); err != nil {
		t.Fatalf("same number on another machine: %v", err)
	}
	if err := s.UpdateInPort(a.ID, InPortPatch{Port: ptr(70000)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad port: %v", err)
	}
	if got, _ := s.GetInPort(a.ID); got.Port != 9090 {
		t.Fatalf("a rejected edit changed the port: %+v", got)
	}
}

func TestEditEndpointTarget(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	direct, _ := s.CreateEndpoint(Endpoint{Host: "db.internal", Port: 5432})
	other, _ := s.CreateEndpoint(Endpoint{Host: "db.internal", Port: 5433})
	socks, _ := s.CreateEndpoint(Endpoint{Type: TypeSocks5})

	if err := s.UpdateEndpoint(direct.ID, EndpointPatch{Host: ptr("db2.internal"), Port: ptr(6432)}); err != nil {
		t.Fatalf("change target: %v", err)
	}
	if got, _ := s.GetEndpoint(direct.ID); got.Host != "db2.internal" || got.Port != 6432 {
		t.Fatalf("target not changed: %+v", got)
	}
	if err := s.UpdateEndpoint(other.ID, EndpointPatch{Host: ptr("db2.internal"), Port: ptr(6432)}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("identical target: %v", err)
	}
	if err := s.UpdateEndpoint(direct.ID, EndpointPatch{Host: ptr("bad:80")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("host with a port: %v", err)
	}
	if err := s.UpdateEndpoint(direct.ID, EndpointPatch{Socks5User: ptr("u")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("socks5 field on a direct endpoint: %v", err)
	}
	if err := s.UpdateEndpoint(socks.ID, EndpointPatch{Host: ptr("x"), Port: ptr(1)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("target on a socks5 endpoint: %v", err)
	}
	if err := s.UpdateEndpoint(socks.ID, EndpointPatch{Socks5User: ptr("u"), Socks5Pass: ptr("pw")}); err != nil {
		t.Fatalf("change login: %v", err)
	}
	if got, _ := s.GetEndpoint(socks.ID); got.Socks5User != "u" || got.Socks5Pass != "pw" {
		t.Fatalf("proxy not changed: %+v", got)
	}
}

func TestUpdateRoute(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	in1, _ := s.CreateInPort(InPort{Port: 1})
	in2, _ := s.CreateInPort(InPort{Port: 2})
	e1, _ := s.CreateEndpoint(Endpoint{Port: 11})
	e2, _ := s.CreateEndpoint(Endpoint{Port: 12})
	r1, _ := s.CreateRoute(in1.ID, e1.ID)
	s.CreateRoute(in2.ID, e1.ID)

	before, after, err := s.UpdateRoute(r1.ID, RoutePatch{EndpointID: &e2.ID})
	if err != nil || before.EndpointID != e1.ID || after.EndpointID != e2.ID || after.InPortID != in1.ID {
		t.Fatalf("change endpoint: %+v %+v %v", before, after, err)
	}
	if _, _, err := s.UpdateRoute(r1.ID, RoutePatch{InPortID: &in2.ID}); !errors.Is(err, ErrAlreadyRouted) {
		t.Fatalf("ingress port already routed: %v", err)
	}
	in3, _ := s.CreateInPort(InPort{Port: 3})
	if _, after, err := s.UpdateRoute(r1.ID, RoutePatch{InPortID: &in3.ID}); err != nil || after.InPortID != in3.ID {
		t.Fatalf("change ingress: %+v %v", after, err)
	}
	ver, _ := s.ConfigVersion()
	if _, _, err := s.UpdateRoute(r1.ID, RoutePatch{InPortID: &in3.ID, EndpointID: &e2.ID}); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.ConfigVersion(); v != ver {
		t.Fatal("a no-op update bumped the config version")
	}
	missing := int64(999)
	if _, _, err := s.UpdateRoute(r1.ID, RoutePatch{EndpointID: &missing}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown endpoint: %v", err)
	}
	if _, _, err := s.UpdateRoute(999, RoutePatch{EndpointID: &e1.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown route: %v", err)
	}
}

func TestMapLayout(t *testing.T) {
	s, path := newStore(t)
	in, _ := s.CreateInPort(InPort{Port: 8080})
	ep, _ := s.CreateEndpoint(Endpoint{Port: 5000})

	if l, err := s.GetMapLayout(); err != nil || len(l.Nodes) != 0 || len(l.Items) != 0 {
		t.Fatalf("empty layout: %+v %v", l, err)
	}
	ver, _ := s.ConfigVersion()
	layout := MapLayout{
		Nodes: map[string]MapPoint{fmt.Sprintf("in:%d", in.ID): {X: 40, Y: 60}, fmt.Sprintf("eg:%d", ep.ID): {X: 500, Y: 80}},
		Items: []MapItem{
			{ID: "f1", Kind: "frame", X: 10, Y: 10, W: 300, H: 200, Text: "Home", Color: "blue"},
			{ID: "t1", Kind: "text", X: 20, Y: 20, Text: "note"},
		},
	}
	if err := s.SaveMapLayout(layout); err != nil {
		t.Fatalf("save: %v", err)
	}
	if v, _ := s.ConfigVersion(); v != ver {
		t.Fatal("saving the layout bumped the config version (agents would be re-synced for nothing)")
	}
	got, _ := s.GetMapLayout()
	if got.Items[1].Size != "M" || got.Items[1].Color != "neutral" || got.Items[0].Color != "blue" {
		t.Fatalf("defaults not applied: %+v", got.Items)
	}

	// invalid layouts are rejected and change nothing
	for name, bad := range map[string]MapLayout{
		"bad key":    {Nodes: map[string]MapPoint{"x:1": {}}},
		"bad kind":   {Items: []MapItem{{ID: "a", Kind: "circle"}}},
		"bad color":  {Items: []MapItem{{ID: "a", Kind: "text", Color: "pink"}}},
		"dup id":     {Items: []MapItem{{ID: "a", Kind: "text"}, {ID: "a", Kind: "text"}}},
		"tiny frame": {Items: []MapItem{{ID: "a", Kind: "frame", W: 5, H: 5}}},
		"far away":   {Nodes: map[string]MapPoint{"in:1": {X: 1 << 30}}},
		"long text":  {Items: []MapItem{{ID: "a", Kind: "text", Text: strings.Repeat("x", 300)}}},
	} {
		if err := s.SaveMapLayout(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if again, _ := s.GetMapLayout(); len(again.Items) != 2 {
		t.Fatal("a rejected layout replaced the saved one")
	}

	// it survives a restart and a backup/restore with re-created ids
	snap, err := s.Export(true)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if persisted, _ := s2.GetMapLayout(); len(persisted.Items) != 2 || len(persisted.Nodes) != 2 {
		t.Fatalf("layout lost on reopen: %+v", persisted)
	}
	// make the ids differ after the restore
	s2.CreateInPort(InPort{Port: 1})
	if err := s2.Import(snap); err != nil {
		t.Fatalf("import: %v", err)
	}
	restored, _ := s2.GetMapLayout()
	ports, _ := s2.ListInPorts()
	eps, _ := s2.ListEndpoints()
	if restored.Nodes[fmt.Sprintf("in:%d", ports[0].ID)] != (MapPoint{X: 40, Y: 60}) ||
		restored.Nodes[fmt.Sprintf("eg:%d", eps[0].ID)] != (MapPoint{X: 500, Y: 80}) || len(restored.Items) != 2 {
		t.Fatalf("layout not remapped on restore: %+v (ports %+v)", restored, ports)
	}
}

func TestRouteEnabled(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	ip, _ := s.CreateInPort(InPort{Port: 21500})
	ep, _ := s.CreateEndpoint(Endpoint{Host: "127.0.0.1", Port: 80})
	rt, err := s.CreateRoute(ip.ID, ep.ID)
	if err != nil || !rt.Enabled {
		t.Fatalf("new route must be on: %+v %v", rt, err)
	}
	off := false
	before, after, err := s.UpdateRoute(rt.ID, RoutePatch{Enabled: &off})
	if err != nil || !before.Enabled || after.Enabled {
		t.Fatalf("switch off: %+v %+v %v", before, after, err)
	}
	if _, got, _ := s.Resolve(ip.ID); got != nil {
		t.Fatalf("a switched-off route must not resolve: %+v", got)
	}
	// the route survives a backup round trip with its state
	snap, _ := s.Export(true)
	if len(snap.Routes) != 1 || snap.Routes[0].Enabled {
		t.Fatalf("export lost the state: %+v", snap.Routes)
	}
	on := true
	if _, _, err := s.UpdateRoute(rt.ID, RoutePatch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	if _, got, _ := s.Resolve(ip.ID); got == nil {
		t.Fatal("route did not resolve after switching on")
	}
	var old Route
	if err := json.Unmarshal([]byte(`{"id":1,"inport_id":1,"endpoint_id":1}`), &old); err != nil || !old.Enabled {
		t.Fatalf("a route from an old backup must be on: %+v %v", old, err)
	}
}

func TestSettingsNameAndPublicAddr(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	if err := s.SaveSettings(Settings{Autostart: true, Name: "  Frankfurt  ", PublicAddr: "https://Relay.Example.com/"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSettings()
	if got.Name != "Frankfurt" || got.PublicAddr != "Relay.Example.com" || !got.Autostart {
		t.Fatalf("settings: %+v", got)
	}
	for _, addr := range []string{"relay.example.com:7443", "203.0.113.5", "203.0.113.5:80", "[::1]:7443", ""} {
		if err := s.SaveSettings(Settings{PublicAddr: addr}); err != nil {
			t.Errorf("public address %q rejected: %v", addr, err)
		}
	}
	for _, addr := range []string{"bad host", "host:99999", "host:abc", ":7443", "a/b"} {
		if err := s.SaveSettings(Settings{PublicAddr: addr}); !errors.Is(err, ErrInvalid) {
			t.Errorf("public address %q: got %v, want ErrInvalid", addr, err)
		}
	}
	if err := s.SaveSettings(Settings{Name: string(make([]byte, MaxLabel+1))}); !errors.Is(err, ErrInvalid) {
		t.Errorf("long name: %v", err)
	}
	// a patch that only touches one field keeps the others
	on := true
	merged := SettingsPatch{Autostart: &on}.Apply(Settings{Name: "n", PublicAddr: "h"})
	if merged.Name != "n" || merged.PublicAddr != "h" || !merged.Autostart {
		t.Fatalf("patch: %+v", merged)
	}
}
