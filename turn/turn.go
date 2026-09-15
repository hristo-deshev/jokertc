// Package turn embeds a STUN+TURN server based on pion/turn. The TURN
// listener answers STUN binding requests too, so one listener covers both
// protocols. See docs/plans/stun-and-turn.md.
package turn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/go-chi/httplog/v2"
	"github.com/pion/logging"
	pion "github.com/pion/turn/v5"
)

const realm = "jokertc"

type Config struct {
	ListenUDPAddr string // e.g. ":3478"; empty = no UDP listener
	ListenTCPAddr string // e.g. ":3478"; empty = no TCP listener
	ExternalIP    string // advertised in relayed addresses; auto-detected when empty
	RelayPortMin  int
	RelayPortMax  int
	Auth          Authenticator
	Log           *httplog.Logger

	// DisableSTUN drops STUN binding requests on the UDP listener, so peers
	// cannot gather a server-reflexive candidate from this server and have to
	// fall back to a relay allocation. It is a test lever for the TURN-only
	// path, not something to run in production.
	DisableSTUN bool

	// AllowAnyCredential makes the relay accept any username with any password,
	// however they pair, by rewriting MESSAGE-INTEGRITY on each request before
	// pion checks it. It turns the relay into a fully open one and exists for
	// interop testing against a client whose credentials you do not control.
	// Off by default; never enable it on a reachable network.
	AllowAnyCredential bool
}

type Server struct {
	cfg *Config
	log *httplog.Logger

	udpConn     net.PacketConn
	tcpListener net.Listener
	srv         *pion.Server

	stopOnce sync.Once
	stopped  chan struct{}
}

func New(cfg *Config) (*Server, error) {
	if cfg.ListenUDPAddr == "" && cfg.ListenTCPAddr == "" {
		return nil, errors.New("at least one of ListenUDPAddr / ListenTCPAddr required")
	}
	if cfg.RelayPortMin <= 0 || cfg.RelayPortMax < cfg.RelayPortMin || cfg.RelayPortMax > 65535 {
		return nil, fmt.Errorf("invalid relay port range %d-%d", cfg.RelayPortMin, cfg.RelayPortMax)
	}
	if cfg.Auth == nil {
		return nil, errors.New("authenticator required")
	}

	relayIP := net.ParseIP(cfg.ExternalIP)
	if relayIP == nil {
		detected, err := externalIP()
		if err != nil {
			return nil, fmt.Errorf("no ExternalIP configured and auto-detection failed: %w", err)
		}
		relayIP = detected
	}

	s := &Server{cfg: cfg, log: cfg.Log, stopped: make(chan struct{})}

	// One generator per listener. Two generators over the same range can
	// race for a port; AllocatePacketConn retries (MaxRetries) on bind
	// failure, which absorbs the collision.
	newGen := func() pion.RelayAddressGenerator {
		return &pion.RelayAddressGeneratorPortRange{
			RelayAddress: relayIP,
			MinPort:      uint16(cfg.RelayPortMin),
			MaxPort:      uint16(cfg.RelayPortMax),
			MaxRetries:   10,
			Address:      "0.0.0.0",
		}
	}

	serverConfig := pion.ServerConfig{
		LoggerFactory: logging.NewDefaultLoggerFactory(),
		Realm:         realm,
		AuthHandler: func(ra *pion.RequestAttributes) (string, []byte, bool) {
			key, ok := cfg.Auth.Authenticate(ra.Username, ra.Realm, ra.SrcAddr)
			return ra.Username, key, ok
		},
		EventHandler: newEventHandler(cfg.Log),
	}

	if cfg.ListenUDPAddr != "" {
		var lc net.ListenConfig
		udpConn, err := lc.ListenPacket(context.Background(), "udp", cfg.ListenUDPAddr)
		if err != nil {
			return nil, fmt.Errorf("TURN UDP listener: %w", err)
		}
		s.udpConn = udpConn
		served := net.PacketConn(udpConn)
		if cfg.Log != nil {
			served = newSTUNLogConn(served, cfg.Log)
		}
		// Outside the logger on purpose: a dropped request is still reported as
		// an attempt, and only the filter says it went no further.
		if cfg.DisableSTUN {
			served = &stunFilterConn{PacketConn: served, log: cfg.Log}
			if cfg.Log != nil {
				cfg.Log.Warn("STUN binding requests are disabled on the TURN UDP listener")
			}
		}
		// Outermost, so it rewrites the request as pion is about to read it.
		// It only touches a TURN request that carries a credential, so it never
		// collides with the STUN layers below.
		if cfg.AllowAnyCredential {
			served = newAnyCredentialConn(served, realm, cfg.Log)
			if cfg.Log != nil {
				cfg.Log.Warn("TURN accepts ANY username/password — this is an open relay")
			}
		}
		serverConfig.PacketConnConfigs = append(serverConfig.PacketConnConfigs, pion.PacketConnConfig{
			PacketConn:            served,
			RelayAddressGenerator: newGen(),
		})
	}
	if cfg.ListenTCPAddr != "" {
		var lc net.ListenConfig
		l, err := lc.Listen(context.Background(), "tcp", cfg.ListenTCPAddr)
		if err != nil {
			return nil, fmt.Errorf("TURN TCP listener: %w", err)
		}
		s.tcpListener = l
		serverConfig.ListenerConfigs = append(serverConfig.ListenerConfigs, pion.ListenerConfig{
			Listener:              l,
			RelayAddressGenerator: newGen(),
		})
	}

	srv, err := pion.NewServer(serverConfig)
	if err != nil {
		return nil, fmt.Errorf("pion TURN server: %w", err)
	}
	s.srv = srv
	return s, nil
}

// Run blocks until ctx is cancelled or the server is closed. Bind errors are
// returned from New; pion handles runtime listener errors internally.
func (s *Server) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return s.Close()
	case <-s.stopped:
		return nil
	}
}

// Close stops the TURN server and closes its listeners. Idempotent.
func (s *Server) Close() error {
	var err error
	s.stopOnce.Do(func() {
		err = s.srv.Close() // also closes the PacketConn / Listener we handed it
		close(s.stopped)
	})
	return err
}

func (s *Server) UDPAddr() net.Addr {
	if s.udpConn == nil {
		return nil
	}
	return s.udpConn.LocalAddr()
}

func (s *Server) TCPAddr() net.Addr {
	if s.tcpListener == nil {
		return nil
	}
	return s.tcpListener.Addr()
}

// externalIP returns the first non-loopback IPv4 address of the host, used as
// the relay address when Config.ExternalIP is empty.
func externalIP() (net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP, nil
		}
	}
	return nil, errors.New("no non-loopback IPv4 address found")
}
