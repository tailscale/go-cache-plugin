package revproxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
)

// A Connector is a minimal implementation of the HTTP CONNECT protocol.
// It implements [net.Listener] by hijacking the connection of a valid request
// and forwarding it to a caller of the Accept method.
type Connector struct {
	// Addrs define the host:port combinations the Connector will accept as
	// targets for a CONNECT request. At least one must be defined.
	Addrs []string

	initOnce sync.Once
	queue    chan clientConn // channels waiting to be Accepted
	stopped  chan struct{}   // closed when the Connector is closed
}

func (c *Connector) init() {
	c.initOnce.Do(func() {
		c.stopped = make(chan struct{})
		c.queue = make(chan clientConn, 1)
	})
}

// Accept implements part of [net.Listener]. It blocks until a connection is
// posted to the queue, or until c is closed. If a connection is not available
// before c closes, it reports [net.ErrClosed].
func (c *Connector) Accept() (net.Conn, error) {
	c.init()
	select {
	case <-c.stopped:
		// fall through
	case conn, ok := <-c.queue:
		if ok {
			return conn, nil
		}
	}
	return nil, net.ErrClosed
}

// Close implements part of [net.Listener]. It must not be called concurrently
// from multiple goroutines.
func (c *Connector) Close() error {
	c.init()
	select {
	case <-c.stopped:
		return net.ErrClosed
	default:
		close(c.stopped)
		return nil
	}
}

// Addr implements part of [net.Listener].
func (c *Connector) Addr() net.Addr {
	c.init()
	if len(c.Addrs) == 0 {
		return addrStub("<invalid>")
	}
	return addrStub(c.Addrs[0])
}

func (c *Connector) push(conn net.Conn) (<-chan struct{}, error) {
	cc := clientConn{Conn: conn, done: make(chan struct{})}
	select {
	case <-c.stopped:
		return nil, errors.New("connection unavailable")
	case c.queue <- cc:
		return cc.done, nil
	}
}

func (c *Connector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.init()

	// This endpoint allows only "CONNECT" requests.
	if r.Method != http.MethodConnect {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	} else if r.URL.Path != "" {

		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}

	h, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, http.StatusText(http.StatusNotImplemented), http.StatusNotImplemented)
		return
	}

	if !hostMatchesTarget(r.URL.Host, c.Addrs) {
		http.Error(w, fmt.Sprintf("target address %q not recognized", r.URL.Host), http.StatusForbidden)
		return
	}

	conn, bw, err := h.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	done, err := c.push(conn)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	bw.Flush()
	fmt.Fprintf(conn, "%s 200 OK\r\n\r\n", r.Proto)
	<-done
}

type clientConn struct {
	net.Conn
	done chan struct{}
}

func (c clientConn) Close() error {
	defer close(c.done)
	return c.Conn.Close()
}

// addrStub implements the [net.Addr] interface for a fake address.
type addrStub string

func (a addrStub) Network() string { return "tcp" }
func (a addrStub) String() string  { return string(a) }
