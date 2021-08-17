package steam

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"time"
)

type rconSocket struct {
	conn net.Conn
}

func newRCONSocket(dial DialFn, addr string) (*rconSocket, error) {
	conn, err := dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &rconSocket{conn}, nil
}

func (s *rconSocket) close() {
	_ = s.conn.Close()
}

func (s *rconSocket) send(p []byte) error {
	if err := s.conn.SetWriteDeadline(time.Now().Add(400 * time.Millisecond)); err != nil {
		return err
	}
	_, err := s.conn.Write(p)
	if err != nil {
		return err
	}
	return nil
}

func (s *rconSocket) receive() (_ []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()
	buf := new(bytes.Buffer)
	tr := io.TeeReader(s.conn, buf)
	total := int(readLong(tr))
	for total > 0 {
		b := make([]byte, total)
		if err := s.conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond)); err != nil {
			return nil, err
		}
		n, err := s.conn.Read(b)
		if n > 0 {
			_, err := buf.Write(b)
			if err != nil {
				return nil, err
			}
			total -= n
		}
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("steam: could not receive data (%v)", err)
			}
		}
	}
	return buf.Bytes(), nil
}
