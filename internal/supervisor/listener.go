package supervisor

import (
	"net"
	"sync"
)

const MaxConnections = 64

type boundedListener struct {
	net.Listener
	slots chan struct{}
}
type boundedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *boundedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
func (l *boundedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &boundedConn{Conn: c, release: func() { <-l.slots }}, nil
		default:
			_ = c.Close()
		}
	}
}
