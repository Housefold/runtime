package supervisor

import (
	"net"
	"testing"
	"time"
)

func TestListenerRejectsExcessAndReleasesExactlyOnce(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	bounded := &boundedListener{Listener: raw, slots: make(chan struct{}, 1)}
	first, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	held, err := bounded.Accept()
	if err != nil {
		t.Fatal(err)
	}
	excess, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer excess.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := bounded.Accept(); accepted <- c }()
	_ = excess.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err = excess.Read(b[:]); err == nil {
		t.Fatal("excess connection accepted")
	}
	_ = held.Close()
	_ = held.Close()
	next, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	select {
	case c := <-accepted:
		if c == nil {
			t.Fatal("released capacity lost")
		}
		c.Close()
	case <-time.After(time.Second):
		t.Fatal("capacity leaked")
	}
	if len(bounded.slots) != 0 {
		t.Fatal("connection token leaked")
	}
}
