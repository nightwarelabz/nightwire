// sparta-rtc — WebRTC DataChannel <-> UDP datagram pipe for
// thisissparta2!
//
// The WebSocket bridge (bridge/sparta-bridge.py) carries web players to
// the DarkPlaces server over TCP, so a lost or delayed snapshot stalls
// everything queued behind it. This gateway is the same dumb pipe over
// an unreliable transport instead:
//
//     wasm client ──RTCDataChannel──► sparta-rtc ──UDP──► 127.0.0.1:26000
//
// One signaling WebSocket == one RTCPeerConnection == one DataChannel
// == one ephemeral, *connected* UDP socket to the game server: one
// session == one client, as far as the server can tell. DataChannel
// messages become UDP datagrams and UDP datagrams become DataChannel
// messages, byte-identical, 1:1, with no protocol translation,
// inspection or mutation of payloads — including the Emscripten SOCKFS
// "\xff\xff\xff\xffport" first packet, which is forwarded verbatim like
// every other datagram.
//
// Signaling is JSON over a plain WebSocket on 127.0.0.1:26003 (any
// path; Tailscale Funnel terminates TLS in front of it in production
// and mounts it as /rtc):
//
//	client -> {type:"offer", sdp}
//	server -> {type:"answer", sdp}
//	both   -> {type:"ice", candidate, sdpMid, sdpMLineIndex}  (trickle)
//
// The CLIENT creates the DataChannel (label "sparta",
// {ordered:false, maxRetransmits:0}); the gateway accepts it via
// OnDataChannel and only then dials the game server.
//
// Host-side duties mirror the bridge exactly: an Origin allowlist for
// the signaling socket (the site origin, or no Origin at all; anything
// else is 403 before upgrade), a session cap, a per-session
// client->server packet-rate sanity limit (300 pps sliding window,
// excess dropped, session closed after 10 abusive drops), an idle
// reaper (~120 s without traffic either way), a 65507-byte message
// ceiling, and structured logging (metadata only — payload bytes are
// never logged).
//
// ICE: one STUN server (default stun:stun.l.google.com:19302), no
// TURN. The ICE UDP port range is pinned to 26100-26299 via
// SettingEngine.SetEphemeralUDPPortRange so a host firewall only has
// to open that range inbound-UDP (plus the Funnel-facing signaling
// port, which is loopback-only by default).
//
// Usage:
//	sparta-rtc [--bind 127.0.0.1] [--port 26003]
//	           [--server-host 127.0.0.1] [--server-port 26000]
//	           [--max-sessions 64] [--idle-timeout 120]
//	           [--max-pps 300] [--stun stun:stun.l.google.com:19302]
//	           [--allow-origin ORIGIN ...] [--log-file PATH]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

// One DataChannel message == one UDP datagram. 65507 is the largest
// possible IPv4 UDP payload; anything bigger can never be forwarded,
// so it ends the session instead of being truncated.
const maxMessage = 65507

// Pinned ICE ephemeral UDP port range (see header comment).
const icePortMin = 26100
const icePortMax = 26299

// ---------------------------------------------------------------- logging

var (
	logMu   sync.Mutex
	logFile *os.File
)

// log writes one structured line: ts, event, key=value pairs. Never payloads.
func logEvent(event string, fields ...any) {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	parts := []string{ts, event}
	for i := 0; i+1 < len(fields); i += 2 {
		parts = append(parts, fmt.Sprintf("%v=%v", fields[i], fields[i+1]))
	}
	line := strings.Join(parts, " ")
	logMu.Lock()
	fmt.Println(line)
	if logFile != nil {
		logFile.WriteString(line + "\n")
	}
	logMu.Unlock()
}

// ---------------------------------------------------------------- config

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type config struct {
	bind         string
	port         int
	serverHost   string
	serverPort   int
	maxSessions  int
	idleTimeout  float64 // seconds
	maxPPS       int
	stun         string
	allowOrigins map[string]bool
	allowAny     bool
}

// ---------------------------------------------------------------- registry

type registry struct {
	mu       sync.Mutex
	sessions map[int]*session
	nextID   int
	max      int
}

func newRegistry(max int) *registry {
	return &registry{sessions: make(map[int]*session), max: max}
}

// open reserves a session slot; returns the session id or -1 at the cap.
func (r *registry) open() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sessions) >= r.max {
		return -1
	}
	r.nextID++
	r.sessions[r.nextID] = nil
	return r.nextID
}

func (r *registry) attach(sid int, s *session) {
	r.mu.Lock()
	r.sessions[sid] = s
	r.mu.Unlock()
}

func (r *registry) close(sid int) {
	r.mu.Lock()
	delete(r.sessions, sid)
	r.mu.Unlock()
}

func (r *registry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

func (r *registry) closeAll() {
	r.mu.Lock()
	ss := make([]*session, 0, len(r.sessions))
	for _, s := range r.sessions {
		if s != nil {
			ss = append(ss, s)
		}
	}
	r.mu.Unlock()
	for _, s := range ss {
		s.terminate("shutdown")
	}
}

// ---------------------------------------------------------------- signaling

type signalMsg struct {
	Type          string  `json:"type"`
	SDP           string  `json:"sdp,omitempty"`
	Candidate     string  `json:"candidate,omitempty"`
	SDPMid        *string `json:"sdpMid,omitempty"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex,omitempty"`
}

// ---------------------------------------------------------------- session

type session struct {
	id     int
	peer   string
	cfg    *config
	reg    *registry
	ws     *websocket.Conn
	wsMu   sync.Mutex // gorilla allows only one concurrent writer
	pc     *webrtc.PeerConnection
	dc     *webrtc.DataChannel
	udp    *net.UDPConn
	api    *webrtc.API

	started      time.Time
	lastActivity atomic.Int64 // unix nanos, traffic either way
	pktsToServer atomic.Int64
	bytesToServer atomic.Int64
	pktsToClient atomic.Int64
	bytesToClient atomic.Int64
	rateDrops    atomic.Int64
	udpErrors    atomic.Int64

	rateMu     sync.Mutex
	rateWindow []time.Time // monotonic timestamps, last 1s, client->server

	iceMu       sync.Mutex
	haveRemote  bool
	pendingICE  []webrtc.ICECandidateInit

	closeOnce sync.Once
	reasonMu  sync.Mutex
	reason    string
	done      chan struct{}
}

func (s *session) touch() { s.lastActivity.Store(time.Now().UnixNano()) }

func (s *session) setReason(r string) {
	s.reasonMu.Lock()
	if s.reason == "" {
		s.reason = r
	}
	s.reasonMu.Unlock()
}

func (s *session) getReason() string {
	s.reasonMu.Lock()
	defer s.reasonMu.Unlock()
	if s.reason == "" {
		return "eof"
	}
	return s.reason
}

// terminate ends the session exactly once, from any goroutine.
func (s *session) terminate(reason string) {
	s.setReason(reason)
	s.closeOnce.Do(func() {
		close(s.done)
		if s.dc != nil {
			s.dc.Close()
		}
		if s.udp != nil {
			s.udp.Close()
		}
		if s.pc != nil {
			s.pc.Close()
		}
		s.ws.Close()
		s.reg.close(s.id)
		logEvent("session_close",
			"session", s.id, "peer", s.peer,
			"reason", s.getReason(),
			"seconds", fmt.Sprintf("%.1f", time.Since(s.started).Seconds()),
			"to_server_pkts", s.pktsToServer.Load(),
			"to_server_bytes", s.bytesToServer.Load(),
			"to_client_pkts", s.pktsToClient.Load(),
			"to_client_bytes", s.bytesToClient.Load(),
			"rate_drops", s.rateDrops.Load(),
			"udp_errors", s.udpErrors.Load())
	})
}

func (s *session) sendSignal(m signalMsg) {
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	s.wsMu.Lock()
	s.ws.WriteMessage(websocket.TextMessage, data)
	s.wsMu.Unlock()
}

// rateOK mirrors the bridge's sliding 1-second packet window for the
// client->server direction.
func (s *session) rateOK() bool {
	now := time.Now()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	cutoff := now.Add(-time.Second)
	i := 0
	for i < len(s.rateWindow) && !s.rateWindow[i].After(cutoff) {
		i++
	}
	s.rateWindow = append([]time.Time{}, s.rateWindow[i:]...)
	if len(s.rateWindow) >= s.cfg.maxPPS {
		return false
	}
	s.rateWindow = append(s.rateWindow, now)
	return true
}

func (s *session) udpError(err error) {
	n := s.udpErrors.Add(1)
	if n == 1 || n%100 == 0 {
		logEvent("udp_error", "session", s.id, "count", n,
			"error", fmt.Sprintf("%T", err))
	}
}

// -- signaling ------------------------------------------------------

func (s *session) handleOffer(m signalMsg) {
	if s.pc == nil {
		pcCfg := webrtc.Configuration{}
		if s.cfg.stun != "" {
			pcCfg.ICEServers = []webrtc.ICEServer{{URLs: []string{s.cfg.stun}}}
		}
		pc, err := s.api.NewPeerConnection(pcCfg)
		if err != nil {
			logEvent("pc_create_failed", "session", s.id,
				"error", fmt.Sprintf("%T", err))
			s.terminate("pc_create_failed")
			return
		}
		s.pc = pc
		pc.OnICECandidate(func(c *webrtc.ICECandidate) {
			if c == nil {
				return
			}
			init := c.ToJSON()
			s.sendSignal(signalMsg{
				Type:          "ice",
				Candidate:     init.Candidate,
				SDPMid:        init.SDPMid,
				SDPMLineIndex: init.SDPMLineIndex,
			})
		})
		pc.OnDataChannel(func(dc *webrtc.DataChannel) {
			s.onDataChannel(dc)
		})
		pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
			logEvent("pc_state", "session", s.id, "state", state.String())
			if state == webrtc.PeerConnectionStateFailed {
				s.terminate("pc_failed")
			}
		})
	}
	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP}
	if err := s.pc.SetRemoteDescription(offer); err != nil {
		logEvent("set_remote_failed", "session", s.id,
			"error", fmt.Sprintf("%T", err))
		s.terminate("set_remote_failed")
		return
	}
	// Flush any ICE that trickled in before the remote description.
	s.iceMu.Lock()
	s.haveRemote = true
	pending := s.pendingICE
	s.pendingICE = nil
	s.iceMu.Unlock()
	for _, cand := range pending {
		if err := s.pc.AddICECandidate(cand); err != nil {
			logEvent("add_ice_failed", "session", s.id,
				"error", fmt.Sprintf("%T", err))
		}
	}
	answer, err := s.pc.CreateAnswer(nil)
	if err != nil {
		logEvent("create_answer_failed", "session", s.id,
			"error", fmt.Sprintf("%T", err))
		s.terminate("create_answer_failed")
		return
	}
	if err := s.pc.SetLocalDescription(answer); err != nil {
		logEvent("set_local_failed", "session", s.id,
			"error", fmt.Sprintf("%T", err))
		s.terminate("set_local_failed")
		return
	}
	s.sendSignal(signalMsg{Type: "answer", SDP: answer.SDP})
}

func (s *session) handleICE(m signalMsg) {
	cand := webrtc.ICECandidateInit{
		Candidate:     m.Candidate,
		SDPMid:        m.SDPMid,
		SDPMLineIndex: m.SDPMLineIndex,
	}
	s.iceMu.Lock()
	if !s.haveRemote || s.pc == nil {
		s.pendingICE = append(s.pendingICE, cand)
		s.iceMu.Unlock()
		return
	}
	s.iceMu.Unlock()
	if err := s.pc.AddICECandidate(cand); err != nil {
		logEvent("add_ice_failed", "session", s.id,
			"error", fmt.Sprintf("%T", err))
	}
}

// signalingLoop reads JSON signaling messages until the WS dies.
func (s *session) signalingLoop() {
	defer s.terminate("eof")
	for {
		_, data, err := s.ws.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure,
				websocket.CloseGoingAway) {
				s.setReason("client_close")
			}
			return
		}
		s.touch()
		var m signalMsg
		if err := json.Unmarshal(data, &m); err != nil {
			logEvent("signal_bad_json", "session", s.id, "bytes", len(data))
			s.setReason("protocol_error")
			return
		}
		switch m.Type {
		case "offer":
			s.handleOffer(m)
		case "ice":
			s.handleICE(m)
		default:
			logEvent("signal_unknown_type", "session", s.id, "type", m.Type)
		}
	}
}

// -- data plane -------------------------------------------------------

// onDataChannel fires when the client-created channel arrives. The UDP
// side is dialed when the channel opens, mirroring the bridge (which
// dials when the WS upgrade completes).
func (s *session) onDataChannel(dc *webrtc.DataChannel) {
	s.dc = dc
	logEvent("dc_arrive", "session", s.id, "label", dc.Label())
	dc.OnOpen(func() {
		addr := &net.UDPAddr{
			IP:   net.ParseIP(s.cfg.serverHost),
			Port: s.cfg.serverPort,
		}
		udp, err := net.DialUDP("udp", nil, addr)
		if err != nil {
			logEvent("udp_connect_failed", "session", s.id, "peer", s.peer,
				"error", fmt.Sprintf("%T", err))
			s.terminate("udp_connect_failed")
			return
		}
		s.udp = udp
		logEvent("dc_open", "session", s.id, "label", dc.Label(),
			"server", fmt.Sprintf("%s:%d", s.cfg.serverHost, s.cfg.serverPort))
		go s.udpPump()
		go s.idleReaper()
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		data := msg.Data
		if len(data) == 0 {
			return // bridge drops empty payloads; the session lives
		}
		if len(data) > maxMessage {
			s.terminate("message_too_large")
			return
		}
		if s.udp == nil {
			return
		}
		if !s.rateOK() {
			n := s.rateDrops.Add(1)
			if n == 1 || n == 10 {
				logEvent("rate_drop", "session", s.id, "drops", n)
			}
			if n >= 10 {
				s.terminate("rate_limit")
			}
			return
		}
		if _, err := s.udp.Write(data); err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			s.udpError(err)
			return
		}
		s.touch()
		s.pktsToServer.Add(1)
		s.bytesToServer.Add(int64(len(data)))
	})
	dc.OnClose(func() {
		s.terminate("dc_close")
	})
}

// udpPump: server datagrams -> DataChannel messages, 1:1.
func (s *session) udpPump() {
	buf := make([]byte, 65535)
	for {
		n, err := s.udp.Read(buf)
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			// e.g. ECONNREFUSED from an ICMP port-unreachable on a
			// connected socket when the server is down. The pipe
			// stays up; the client's own netcode retries. (Bridge
			// semantics, same comment.)
			s.udpError(err)
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if n == 0 {
			continue
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		s.touch()
		if err := s.dc.Send(data); err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			s.terminate("client_write_failed")
			return
		}
		s.pktsToClient.Add(1)
		s.bytesToClient.Add(int64(n))
	}
}

// idleReaper ends the session after idleTimeout with no traffic either way.
func (s *session) idleReaper() {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	limit := time.Duration(s.cfg.idleTimeout * float64(time.Second))
	for {
		select {
		case <-s.done:
			return
		case now := <-t.C:
			if now.Sub(time.Unix(0, s.lastActivity.Load())) > limit {
				s.terminate("idle")
				return
			}
		}
	}
}

// ---------------------------------------------------------------- handler

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Origin is checked by hand before upgrade (house law below), so
	// the refusal is a logged, plain 403 — not an upgrade failure.
	CheckOrigin: func(r *http.Request) bool { return true },
}

func makeHandler(cfg *config, reg *registry, api *webrtc.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peer := r.RemoteAddr
		if r.Method != http.MethodGet {
			logEvent("reject", "status", 405, "reason", "method", "peer", peer)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if !websocket.IsWebSocketUpgrade(r) {
			logEvent("reject", "status", 400, "reason", "not_a_websocket_upgrade", "peer", peer)
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		// Origin: the house rule — the site origin, or no Origin at
		// all (non-browser clients). Anything else is refused BEFORE
		// the upgrade. (Identical law to the bridge.)
		origin := r.Header.Get("Origin")
		if origin != "" && !cfg.allowAny && !cfg.allowOrigins[origin] {
			logEvent("reject_origin", "peer", peer, "origin", origin)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		sid := reg.open()
		if sid < 0 {
			logEvent("reject_cap", "peer", peer, "sessions", reg.count())
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		// Subprotocol echo, bridge-style: prefer "sparta", else echo
		// the first offered token. The token is not interpreted.
		var proto string
		offered := websocket.Subprotocols(r)
		for _, p := range offered {
			if p == "sparta" {
				proto = "sparta"
				break
			}
		}
		if proto == "" && len(offered) > 0 {
			proto = offered[0]
		}
		hdr := http.Header{}
		if proto != "" {
			hdr.Set("Sec-WebSocket-Protocol", proto)
		}
		ws, err := upgrader.Upgrade(w, r, hdr)
		if err != nil {
			reg.close(sid)
			logEvent("upgrade_failed", "peer", peer,
				"error", fmt.Sprintf("%T", err))
			return
		}
		logEvent("session_open", "session", sid, "peer", peer,
			"origin", orDash(origin), "path", r.URL.Path,
			"subprotocol", orDash(proto))
		s := &session{
			id: sid, peer: peer, cfg: cfg, reg: reg, ws: ws, api: api,
			started: time.Now(), done: make(chan struct{}),
		}
		s.touch()
		reg.attach(sid, s)
		s.signalingLoop()
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ---------------------------------------------------------------- main

func main() {
	cfg := &config{}
	var allowOrigin stringList
	var logPath string
	flag.StringVar(&cfg.bind, "bind", "127.0.0.1",
		"signaling listen address (default 127.0.0.1; Funnel fronts it)")
	flag.IntVar(&cfg.port, "port", 26003, "signaling listen port")
	flag.StringVar(&cfg.serverHost, "server-host", "127.0.0.1",
		"DarkPlaces server host")
	flag.IntVar(&cfg.serverPort, "server-port", 26000,
		"DarkPlaces server UDP port")
	flag.IntVar(&cfg.maxSessions, "max-sessions", 64,
		"maximum simultaneous sessions")
	flag.Float64Var(&cfg.idleTimeout, "idle-timeout", 120,
		"seconds without traffic before a session is reaped")
	flag.IntVar(&cfg.maxPPS, "max-pps", 300,
		"per-session client->server packet rate sanity limit, packets/second")
	flag.StringVar(&cfg.stun, "stun", "stun:stun.l.google.com:19302",
		"STUN server URL (empty disables STUN; no TURN by design)")
	flag.Var(&allowOrigin, "allow-origin",
		"allowed Origin value; repeatable. Default: https://nosuchmachine.net. "+
			"Use '*' to allow any origin (not recommended). Absent Origin "+
			"headers (non-browser clients) are always accepted.")
	flag.StringVar(&logPath, "log-file", "", "also append log lines to this file")
	flag.Parse()

	if len(allowOrigin) == 0 {
		allowOrigin = stringList{"https://nosuchmachine.net"}
	}
	cfg.allowOrigins = make(map[string]bool)
	for _, o := range allowOrigin {
		if o == "*" {
			cfg.allowAny = true
		}
		cfg.allowOrigins[o] = true
	}
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sparta-rtc: open log file: %v\n", err)
			os.Exit(1)
		}
		logFile = f
		defer f.Close()
	}

	// ICE: pinned ephemeral UDP port range 26100-26299.
	se := webrtc.SettingEngine{}
	if err := se.SetEphemeralUDPPortRange(icePortMin, icePortMax); err != nil {
		fmt.Fprintf(os.Stderr, "sparta-rtc: port range: %v\n", err)
		os.Exit(1)
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))

	reg := newRegistry(cfg.maxSessions)
	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.bind, cfg.port),
		Handler: makeHandler(cfg, reg, api),
	}
	go func() {
		logEvent("listen", "bind", cfg.bind, "port", cfg.port,
			"server", fmt.Sprintf("%s:%d", cfg.serverHost, cfg.serverPort),
			"max_sessions", cfg.maxSessions,
			"idle_timeout", cfg.idleTimeout, "max_pps", cfg.maxPPS,
			"stun", orDash(cfg.stun),
			"ice_ports", fmt.Sprintf("%d-%d", icePortMin, icePortMax),
			"origins", strings.Join(allowOrigin, ","))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "sparta-rtc: listen: %v\n", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	sig := <-stop
	logEvent("signal", "signum", fmt.Sprintf("%v", sig))
	logEvent("shutdown", "sessions", reg.count())
	reg.closeAll()
	srv.Close()
}
