package signaling

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/httplog/v2"
)

var errUnimplementedTurnSecret = errors.New("signaling: TurnSecret is not implemented; it needs a matching turn.Authenticator in the turn package")

// Hub owns the pairing state for every live session. It never touches a
// socket: peers are reached through a buffered channel and a close func, so a
// notification cannot block and the whole registry fits behind one mutex.
type Hub struct {
	cfg *Config
	log *httplog.Logger

	mu       sync.Mutex
	sessions map[string]*session

	baseCtx   context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// peer is one connected client as the state machine sees it.
type peer struct {
	role      string
	sessionID string // lowercased lookup key
	label     string // session id as the client wrote it, for logs
	imei      string
	addr      string // client address, for logs only

	out       chan []byte
	closeFn   func(reason string)
	closeOnce sync.Once
}

type session struct {
	id        string
	label     string
	imei      string
	device    *peer
	phone     *peer
	turnToken string
	readySent bool
	lonely    *time.Timer
}

func New(cfg *Config) (*Hub, error) {
	if cfg == nil {
		return nil, errors.New("signaling: nil config")
	}
	if cfg.TurnSecret != "" {
		return nil, errUnimplementedTurnSecret
	}
	if cfg.SendBuffer <= 0 {
		cfg.SendBuffer = defaultSendBuffer
	}
	if cfg.PingTimeout <= 0 {
		cfg.PingTimeout = defaultPingTimeout
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{
		cfg:      cfg,
		log:      cfg.Log,
		sessions: make(map[string]*session),
		baseCtx:  ctx,
		cancel:   cancel,
	}, nil
}

// Close cancels every live connection and waits for their goroutines. It must
// run before http.Server.Shutdown, which neither waits for nor closes hijacked
// connections.
func (s *Hub) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.wg.Wait()

		// Connection goroutines drop their own sessions as they unwind. Anything
		// still here had no connection behind it, so clear it explicitly rather
		// than leaving the active-session gauge permanently high.
		s.mu.Lock()
		remaining := len(s.sessions)
		for _, sess := range s.sessions {
			sess.stopLonely()
		}
		s.sessions = make(map[string]*session)
		s.mu.Unlock()
		sessionsActive.Add(int64(-remaining))
	})
}

func (s *Hub) sessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (p *peer) send(msg []byte) {
	select {
	case p.out <- msg:
	default:
		slowPeerDrops.Inc()
		p.close("send buffer full")
	}
}

func (p *peer) close(reason string) {
	p.closeOnce.Do(func() { p.closeFn(reason) })
}

func (s *session) slot(role string) *peer {
	if role == roleDevice {
		return s.device
	}
	return s.phone
}

func (s *session) setSlot(role string, p *peer) {
	if role == roleDevice {
		s.device = p
	} else {
		s.phone = p
	}
}

func (s *session) other(role string) *peer {
	if role == roleDevice {
		return s.phone
	}
	return s.device
}

func (s *session) isEmpty() bool { return s.device == nil && s.phone == nil }

func (s *session) stopLonely() {
	if s.lonely != nil {
		s.lonely.Stop()
		s.lonely = nil
	}
}

// join validates a join frame and registers the peer. A returned error means
// the caller must close the socket without a reply.
func (s *Hub) join(m joinMessage, addr string, closeFn func(reason string)) (*peer, error) {
	if m.Session == "" {
		return nil, errors.New("join: empty session id")
	}
	if m.Role != roleDevice && m.Role != rolePhone {
		return nil, fmt.Errorf("join: unknown role %q", m.Role)
	}

	p := &peer{
		role:      m.Role,
		sessionID: strings.ToLower(m.Session),
		label:     m.Session,
		imei:      m.Imei,
		addr:      addr,
		out:       make(chan []byte, s.cfg.SendBuffer),
		closeFn:   closeFn,
	}

	s.mu.Lock()
	sess := s.sessions[p.sessionID]
	if sess == nil {
		sess = &session{id: p.sessionID, label: m.Session, turnToken: newTurnToken()}
		s.sessions[p.sessionID] = sess
		sessionsActive.Add(1)
	}
	if m.Imei != "" {
		sess.imei = m.Imei
	}

	replaced := sess.slot(p.role)
	sess.setSlot(p.role, p)
	if replaced != nil {
		sess.readySent = false
	}

	p.send(mustMarshal(joinedMessage{Type: typeJoined, Mode: modeForward}))

	if sess.device != nil && sess.phone != nil {
		sess.stopLonely()
		if !sess.readySent {
			sess.readySent = true
			sess.device.send(s.readyFrame(sess, false))
			sess.phone.send(s.readyFrame(sess, true))
		}
	} else if p.role == roleDevice && s.cfg.NoReceiverTimeout > 0 {
		sess.lonely = time.AfterFunc(s.cfg.NoReceiverTimeout, func() { s.expireLonely(p) })
	}

	s.announceJoin(p, m.Session)
	s.mu.Unlock()

	if replaced != nil {
		replaced.close("replaced by a new " + p.role)
	}
	countPeerJoined(p.role)
	s.log.Info("signaling peer joined", "role", p.role, "session", p.label, "imei", p.imei, "remoteAddr", p.addr)
	return p, nil
}

// announceJoin tells every other connected client that a peer joined, naming
// the session it joined. It is what lets a client discover a session id it was
// never told out of band. The joiner is skipped: it already knows.
//
// Callers must hold s.mu. Sending is a buffered channel write, so this cannot
// block however many peers are connected.
func (s *Hub) announceJoin(joiner *peer, session string) {
	frame := mustMarshal(remoteJoinedMessage{Type: typeRemoteJoined, Session: session})
	for _, sess := range s.sessions {
		for _, other := range []*peer{sess.device, sess.phone} {
			if other != nil && other != joiner {
				other.send(frame)
			}
		}
	}
}

// leave removes a peer and tells its counterpart. It acts only if the session
// still points at this exact peer, so a peer that was already replaced cleans
// up to a no-op without needing a flag.
func (s *Hub) leave(p *peer) {
	s.mu.Lock()
	sess := s.sessions[p.sessionID]
	if sess == nil || sess.slot(p.role) != p {
		s.mu.Unlock()
		return
	}
	sess.setSlot(p.role, nil)
	sess.stopLonely()
	sess.readySent = false
	other := sess.other(p.role)
	if sess.isEmpty() {
		delete(s.sessions, p.sessionID)
		sessionsActive.Add(-1)
	}
	s.mu.Unlock()

	if other != nil {
		other.send(mustMarshal(byeMessage{Type: typeBye}))
	}
	s.log.Info("signaling peer left", "role", p.role, "session", p.label, "remoteAddr", p.addr)
}

// forward passes a frame to the peer on the other side of the session,
// unchanged. Clients send fields this package does not model.
func (s *Hub) forward(p *peer, frameType string, raw []byte) {
	s.mu.Lock()
	sess := s.sessions[p.sessionID]
	if sess == nil || sess.slot(p.role) != p {
		s.mu.Unlock()
		return
	}
	other := sess.other(p.role)
	s.mu.Unlock()

	if other == nil {
		s.log.Debug("signaling frame dropped: no counterpart", "role", p.role, "session", p.label, "bytes", len(raw))
		return
	}
	countMessageForwarded(frameType)
	other.send(raw)
}

func (s *Hub) expireLonely(device *peer) {
	s.mu.Lock()
	sess := s.sessions[device.sessionID]
	if sess == nil || sess.device != device || sess.phone != nil {
		s.mu.Unlock()
		return
	}
	sess.lonely = nil
	sess.device = nil
	if sess.isEmpty() {
		delete(s.sessions, device.sessionID)
		sessionsActive.Add(-1)
	}
	s.mu.Unlock()

	noReceiverTimeouts.Inc()
	s.log.Info("signaling device had no receiver", "session", device.label, "remoteAddr", device.addr, "timeout", s.cfg.NoReceiverTimeout)
	device.send(mustMarshal(byeMessage{Type: typeBye}))
	device.close("no receiver")
}

func (s *Hub) readyFrame(sess *session, initiator bool) []byte {
	return mustMarshal(readyMessage{
		Type:      typeReady,
		Initiator: initiator,
		Stun:      s.cfg.StunURL,
		Turn:      s.credentialFor(sess),
	})
}
