package traffic

import (
	"net"
	"testing"
)

func TestCountersDrain(t *testing.T) {
	var c Counters
	c.In.Add(5)
	c.Conns.Add(1)
	c.Active.Add(2)
	if d := c.Drain(); d != (Delta{In: 5, Conns: 1, Active: 2}) {
		t.Fatalf("first drain: %+v", d)
	}
	if d := c.Drain(); d != (Delta{Active: 2}) {
		t.Fatalf("second drain keeps only the gauge: %+v", d)
	}
}

func TestPeersDrain(t *testing.T) {
	var p Peers
	k := PeerKey{IP: "1.2.3.4", InPortID: 1}
	p.Add(k, PeerDelta{Conns: 1})
	p.Add(k, PeerDelta{In: 10, Out: 20})
	p.Add(PeerKey{InPortID: 1}, PeerDelta{Conns: 1}) // no ip: ignored
	got := p.Drain()
	if len(got) != 1 || got[k] != (PeerDelta{Conns: 1, In: 10, Out: 20}) {
		t.Fatalf("drain: %+v", got)
	}
	if len(p.Drain()) != 0 {
		t.Fatal("drain must reset")
	}
}

func TestRelayAccountsAndShutRefuses(t *testing.T) {
	var port Port
	port.ID = 3
	var peers Peers

	visitor, visitorPeer := net.Pipe()
	remote, remotePeer := net.Pipe()
	done := make(chan struct{})
	go func() {
		port.Relay(visitor, remote, &peers)
		close(done)
	}()
	go func() {
		buf := make([]byte, 4)
		remotePeer.Read(buf)
		remotePeer.Write([]byte("pong!"))
		remotePeer.Close()
	}()
	visitorPeer.Write([]byte("ping"))
	buf := make([]byte, 5)
	visitorPeer.Read(buf)
	<-done
	visitorPeer.Close()

	if d := port.Counters.Drain(); d.In != 4 || d.Out != 5 || d.Conns != 1 || d.Active != 0 {
		t.Fatalf("counters: %+v", d)
	}
	if port.Live.Len() != 0 {
		t.Fatal("finished connection still live")
	}

	port.Live.Shut()
	a, b := net.Pipe()
	defer b.Close()
	if port.Live.Add(a) {
		t.Fatal("shut set accepted a connection")
	}
}
