package steam

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

type DialFn func(network, address string) (net.Conn, error)

// Server represents a Source engine game server.
type Server struct {
	addr string

	dial DialFn

	rconPassword string

	usock          *udpSocket
	udpInitialized bool

	rsock           *rconSocket
	rconInitialized bool

	mu sync.Mutex
}

// ConnectOptions describes the various connections options.
type ConnectOptions struct {
	// Default will use net.Dialer.Dial. You can override the same by
	// providing your own.
	Dial DialFn

	// RCON password.
	RCONPassword string
}

// Connect to the source server.
func Connect(addr string, os ...*ConnectOptions) (_ *Server, err error) {
	s := &Server{
		addr: addr,
	}
	if len(os) > 0 {
		o := os[0]
		s.dial = o.Dial
		s.rconPassword = o.RCONPassword
	}
	if s.dial == nil {
		s.dial = (&net.Dialer{
			Timeout: 1 * time.Second,
		}).Dial
	}
	if err := s.init(); err != nil {
		return nil, err
	}
	if s.rconPassword == "" {
		return s, nil
	}
	defer func() {
		if err != nil {
			s.usock.close()
		}
	}()
	if err := s.initRCON(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) String() string {
	return s.addr
}

func (s *Server) init() error {
	if s.addr == "" {
		return errors.New("steam: server needs a address")
	}
	var err error
	if s.usock, err = newUDPSocket(s.dial, s.addr); err != nil {
		return fmt.Errorf("steam: could not open udp socket (%v)", err)
	}
	return nil
}

func (s *Server) initRCON() (err error) {
	if s.addr == "" {
		return errors.New("steam: server needs a address")
	}
	if s.rsock, err = newRCONSocket(s.dial, s.addr); err != nil {
		return fmt.Errorf("steam: could not open tcp socket (%v)", err)
	}
	defer func() {
		if err != nil {
			s.rsock.close()
		}
	}()
	if err := s.authenticate(); err != nil {
		return fmt.Errorf("steam: could not authenticate (%v)", err)
	}
	s.rconInitialized = true
	return nil
}

func (s *Server) authenticate() error {
	req := newRCONRequest(rrtAuth, s.rconPassword)
	data, _ := req.marshalBinary()
	if err := s.rsock.send(data); err != nil {
		return err
	}
	// Receive the empty response value
	data, err := s.rsock.receive()
	if err != nil {
		return err
	}
	var resp rconResponse
	if err := resp.unmarshalBinary(data); err != nil {
		return err
	}
	if resp.typ != rrtRespValue || resp.id != req.id {
		return ErrInvalidResponseID
	}
	if resp.id != req.id {
		return ErrInvalidResponseType
	}
	// Receive the actual auth response
	data, err = s.rsock.receive()
	if err != nil {
		return err
	}
	if err := resp.unmarshalBinary(data); err != nil {
		return err
	}
	if resp.typ != rrtAuthResp || resp.id != req.id {
		return ErrRCONAuthFailed
	}
	return nil
}

// Close releases the resources associated with this server.
func (s *Server) Close() {
	if s.rconInitialized {
		s.rsock.close()
	}
	s.usock.close()
}

// Ping returns the RTT (round-trip time) to the server.
func (s *Server) Ping() (time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, _ := infoRequest{}.marshalBinary()
	start := time.Now()
	_ = s.usock.send(req)
	if _, err := s.usock.receive(); err != nil {
		return 0, err
	}
	elapsed := time.Since(start)
	return elapsed, nil
}

// Info retrieves server information.
func (s *Server) Info() (*InfoResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, _ := infoRequest{}.marshalBinary()
	if err := s.usock.send(req); err != nil {
		return nil, err
	}
	data, err := s.usock.receive()
	if err != nil {
		return nil, fmt.Errorf("steam: could not receive info response (%v)", err)
	}

	if isChallengeResponse(data) {
		// Parse the challenge response
		var challangeRes ChallengeResponse
		if err := challangeRes.unmarshalBinary(data); err != nil {
			return nil, err
		}
		// Send a new request with the proper challenge number
		req, _ = infoRequest{challangeRes.Challenge}.marshalBinary()
		if err := s.usock.send(req); err != nil {
			return nil, err
		}
		data, err = s.usock.receive()
		if err != nil {
			return nil, err
		}
	}

	var res InfoResponse
	if err := res.unmarshalBinary(data); err != nil {
		return nil, fmt.Errorf("steam: could not unmarshal info response (%v)", err)
	}
	return &res, nil
}

// PlayersInfo retrieves player information from the server.
func (s *Server) PlayersInfo() (*PlayersInfoResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Send the challenge request
	req, _ := playersInfoRequest{}.marshalBinary()
	if err := s.usock.send(req); err != nil {
		return nil, err
	}
	data, err := s.usock.receive()
	if err != nil {
		return nil, err
	}
	if isChallengeResponse(data) {
		// Parse the challenge response
		var challangeRes ChallengeResponse
		if err := challangeRes.unmarshalBinary(data); err != nil {
			return nil, err
		}
		// Send a new request with the proper challenge number
		req, _ = playersInfoRequest{challangeRes.Challenge}.marshalBinary()
		if err := s.usock.send(req); err != nil {
			return nil, err
		}
		data, err = s.usock.receive()
		if err != nil {
			return nil, err
		}
	}
	// Parse the return value
	var res PlayersInfoResponse
	if err := res.unmarshalBinary(data); err != nil {
		return nil, err
	}
	return &res, nil
}

// Send RCON command to the server.
func (s *Server) Send(cmd string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.rconInitialized {
		return "", ErrRCONNotInitialized
	}
	req := newRCONRequest(rrtExecCmd, cmd)
	data, _ := req.marshalBinary()
	if err := s.rsock.send(data); err != nil {
		return "", fmt.Errorf("steam: sending rcon request (%v)", err)
	}
	// Send the mirror packet.
	reqMirror := newRCONRequest(rrtRespValue, "")
	data, _ = reqMirror.marshalBinary()
	if err := s.rsock.send(data); err != nil {
		return "", fmt.Errorf("steam: sending rcon mirror request (%v)", err)
	}
	var (
		buf       bytes.Buffer
		sawMirror bool
	)
	// Start receiving data.
	for {
		data, err := s.rsock.receive()
		if err != nil {
			return "", fmt.Errorf("steam: receiving rcon response (%v)", err)
		}
		var resp rconResponse
		if err := resp.unmarshalBinary(data); err != nil {
			return "", fmt.Errorf("steam: decoding response (%v)", err)
		}
		if resp.typ != rrtRespValue {
			return "", ErrInvalidResponseType
		}
		if !sawMirror && resp.id == reqMirror.id {
			sawMirror = true
			continue
		}
		if sawMirror {
			if bytes.Compare(resp.body, trailer) == 0 {
				break
			}
			return "", ErrInvalidResponseTrailer
		}
		if req.id != resp.id {
			return "", ErrInvalidResponseID
		}
		_, err = buf.Write(resp.body)
		if err != nil {
			return "", err
		}
	}
	return buf.String(), nil
}

var (
	trailer = []byte{0x00, 0x01, 0x00, 0x00}

	ErrRCONAuthFailed = errors.New("steam: authentication failed")

	ErrRCONNotInitialized     = errors.New("steam: rcon is not initialized")
	ErrInvalidResponseType    = errors.New("steam: invalid response type from server")
	ErrInvalidResponseID      = errors.New("steam: invalid response id from server")
	ErrInvalidResponseTrailer = errors.New("steam: invalid response trailer from server")
)
