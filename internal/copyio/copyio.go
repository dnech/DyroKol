// Package copyio moves bytes on every relayed path: a pooled-buffer copy, so
// large transfers do not allocate a fresh buffer per stream, and a
// bidirectional pipe with optional live byte counters.
package copyio

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
)

const bufSize = 128 * 1024

var pool = sync.Pool{
	New: func() any {
		b := make([]byte, bufSize)
		return &b
	},
}

// Copy behaves like io.Copy but reuses a large shared buffer.
func Copy(dst io.Writer, src io.Reader) (int64, error) {
	bp := pool.Get().(*[]byte)
	defer pool.Put(bp)
	return io.CopyBuffer(dst, src, *bp)
}

// Pipe relays a ⇄ b until either direction finishes, then closes both ends
// and waits for the other direction. ab and ba, when non-nil, are advanced
// as bytes flow (so long-lived connections report traffic while open). The
// totals of each direction are returned.
func Pipe(a, b net.Conn, ab, ba *atomic.Int64) (nab, nba int64) {
	done := make(chan struct{}, 2)
	go func() {
		nab, _ = Copy(countingWriter{b, ab}, a)
		done <- struct{}{}
	}()
	go func() {
		nba, _ = Copy(countingWriter{a, ba}, b)
		done <- struct{}{}
	}()
	<-done
	a.Close()
	b.Close()
	<-done
	return nab, nba
}

type countingWriter struct {
	dst io.Writer
	ctr *atomic.Int64
}

func (w countingWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 && w.ctr != nil {
		w.ctr.Add(int64(n))
	}
	return n, err
}
