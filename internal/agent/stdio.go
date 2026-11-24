package agent

import (
	"io"
	"net"
	"time"
)

// stdioConn adapts a pair of streams (normally stdin/stdout of a
// `kubessh agent --stdio` process) to net.Conn so ssh.NewServerConn can run
// the SSH transport directly over a Kubernetes exec stream.
type stdioConn struct {
	reader io.Reader
	writer io.Writer
}

// NewStdioConn returns a net.Conn backed by r and w.
func NewStdioConn(r io.Reader, w io.Writer) net.Conn {
	return &stdioConn{reader: r, writer: w}
}

func (c *stdioConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *stdioConn) Write(p []byte) (int, error) {
	return c.writer.Write(p)
}

func (c *stdioConn) Close() error { return nil }

func (c *stdioConn) LocalAddr() net.Addr              { return stdioAddr("stdin") }
func (c *stdioConn) RemoteAddr() net.Addr             { return stdioAddr("stdout") }
func (c *stdioConn) SetDeadline(time.Time) error      { return nil }
func (c *stdioConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stdioConn) SetWriteDeadline(time.Time) error { return nil }

type stdioAddr string

func (a stdioAddr) Network() string { return "stdio" }
func (a stdioAddr) String() string  { return string(a) }
