package main

import (
	"math"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

func testGateway(t *testing.T, cfg *config, api *webrtc.API) (*registry, *httptest.Server) {
	t.Helper()
	reg := newRegistry(cfg.maxSessions)
	srv := httptest.NewServer(makeHandler(cfg, reg, api))
	t.Cleanup(func() {
		reg.closeAll()
		srv.Close()
	})
	return reg, srv
}

func waitFor(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(time.Millisecond)
	}
}

func testSession(t *testing.T, cfg *config) (*websocket.Conn, *session) {
	t.Helper()
	reg, srv := testGateway(t, cfg, webrtc.NewAPI())
	c, _, err := dial(t, srv, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	var s *session
	waitFor(t, "session attachment", func() bool {
		reg.mu.Lock()
		defer reg.mu.Unlock()
		s = reg.sessions[1]
		return s != nil
	})
	return c, s
}

func waitClosed(t *testing.T, s *session, reason string) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		t.Fatalf("session did not close (%s)", reason)
	}
	if got := s.getReason(); got != reason {
		t.Fatalf("close reason = %q, want %q", got, reason)
	}
	waitFor(t, "session slot release", func() bool { return s.reg.count() == 0 })
}

func TestSilentNegotiationExpires(t *testing.T) {
	cfg := testConfig()
	cfg.setupTimeout = 40 * time.Millisecond
	_, s := testSession(t, cfg)
	waitClosed(t, s, "setup_timeout")
	if id := s.reg.open(); id < 0 {
		t.Fatal("expired negotiation did not release its slot")
	} else {
		s.reg.close(id)
	}
}

func TestSignalingDoesNotExtendSetup(t *testing.T) {
	cfg := testConfig()
	cfg.setupTimeout = 100 * time.Millisecond
	c, s := testSession(t, cfg)
	for i := 0; i < 4; i++ {
		if err := c.WriteJSON(signalMsg{Type: "ice"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(15 * time.Millisecond)
	}
	waitClosed(t, s, "setup_timeout")
}

func TestSignalingLimits(t *testing.T) {
	tests := []struct {
		name, data, reason string
		kind               int
	}{
		{"oversized_frame", strings.Repeat("x", maxSignalMessage+1), "signal_too_large", websocket.TextMessage},
		{"oversized_candidate", `{"type":"ice","candidate":"` + strings.Repeat("x", maxICECandidateBytes+1) + `"}`, "candidate_too_large", websocket.TextMessage},
		{"binary_signal", `{"type":"ice"}`, "protocol_error", websocket.BinaryMessage},
		{"unknown_type", `{"type":"unexpected"}`, "protocol_error", websocket.TextMessage},
		{"empty_offer", `{"type":"offer"}`, "protocol_error", websocket.TextMessage},
		{"bad_json", `{`, "protocol_error", websocket.TextMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := testSession(t, testConfig())
			if err := c.WriteMessage(tt.kind, []byte(tt.data)); err != nil {
				t.Fatal(err)
			}
			waitClosed(t, s, tt.reason)
		})
	}
}

func TestPreOfferCandidatesAreBounded(t *testing.T) {
	c, s := testSession(t, testConfig())
	for i := 0; i <= maxICECandidates; i++ {
		if err := c.WriteJSON(signalMsg{Type: "ice", Candidate: "candidate:queued"}); err != nil {
			t.Fatal(err)
		}
	}
	waitClosed(t, s, "candidate_limit")
	s.iceMu.Lock()
	defer s.iceMu.Unlock()
	if len(s.pendingICE) != maxICECandidates {
		t.Fatalf("queued %d candidates, want bounded %d", len(s.pendingICE), maxICECandidates)
	}
}

func testChannel(t *testing.T, label string, init *webrtc.DataChannelInit) *webrtc.DataChannel {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	dc, err := pc.CreateDataChannel(label, init)
	if err != nil {
		t.Fatal(err)
	}
	return dc
}

func datagramChannel() *webrtc.DataChannelInit {
	ordered, retransmits := false, uint16(0)
	return &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &retransmits}
}

func TestChannelContract(t *testing.T) {
	tests := []struct {
		name, label string
		init        *webrtc.DataChannelInit
	}{
		{"wrong_label", "other", datagramChannel()},
		{"reliable_ordered", "sparta", nil},
		{"reliable_unordered", "sparta", &webrtc.DataChannelInit{Ordered: datagramChannel().Ordered}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, s := testSession(t, testConfig())
			s.onDataChannel(testChannel(t, tt.label, tt.init))
			waitClosed(t, s, "invalid_channel")
		})
	}
}

func TestExtraChannelCannotReplaceResources(t *testing.T) {
	_, s := testSession(t, testConfig())
	first := testChannel(t, "sparta", datagramChannel())
	s.onDataChannel(first)
	s.onDataChannel(testChannel(t, "sparta", datagramChannel()))
	waitClosed(t, s, "extra_channel")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dc != first || s.udp != nil {
		t.Fatal("extra channel replaced the accepted transport")
	}
}

func TestConcurrentOpenAndTerminate(t *testing.T) {
	for i := 0; i < 20; i++ {
		_, s := testSession(t, testConfig())
		dc := testChannel(t, "sparta", datagramChannel())
		s.onDataChannel(dc)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); s.openUDP(dc) }()
		go func() { defer wg.Done(); s.terminate("test_close") }()
		wg.Wait()
		waitClosed(t, s, "test_close")
		if s.openUDP(dc) != nil {
			t.Fatal("closed session published a late UDP socket")
		}
		s.mu.Lock()
		udp := s.udp
		s.mu.Unlock()
		if udp != nil {
			if _, err := udp.Write([]byte("closed")); err == nil {
				t.Fatal("published UDP socket remained open after termination")
			}
		}
	}
}

func TestRegistryShutdownRefusesLateAttachment(t *testing.T) {
	r := newRegistry(1)
	id := r.open()
	r.closeAll()
	if r.attach(id, &session{}) {
		t.Fatal("shutdown accepted a late session")
	}
	if r.open() != -1 {
		t.Fatal("shutdown accepted a new reservation")
	}
	r.close(id)
}

func TestRateWindowBoundaryAndWrap(t *testing.T) {
	cfg := testConfig()
	cfg.maxPPS = 3
	s := &session{cfg: cfg}
	now := time.Now()
	for _, ms := range []int{0, 200, 400} {
		if !s.rateOKAt(now.Add(time.Duration(ms) * time.Millisecond)) {
			t.Fatal("packet below limit was rejected")
		}
	}
	for _, tt := range []struct {
		ms      int
		allowed bool
	}{{999, false}, {1000, true}, {1001, false}, {1200, true}, {1400, true}} {
		if got := s.rateOKAt(now.Add(time.Duration(tt.ms) * time.Millisecond)); got != tt.allowed {
			t.Fatalf("at %d ms: allowed=%v, want %v", tt.ms, got, tt.allowed)
		}
	}
}

func TestConfigRejectsInvalidLimits(t *testing.T) {
	for _, mutate := range []func(*config){
		func(c *config) { c.maxPPS = 0 },
		func(c *config) { c.maxSessions = 0 },
		func(c *config) { c.setupTimeout = 0 },
		func(c *config) { c.idleTimeout = math.NaN() },
		func(c *config) { c.idleTimeout = math.Inf(1) },
		func(c *config) { c.port = 65536 },
		func(c *config) { c.maxBufferedBytes = maxMessage - 1 },
	} {
		cfg := testConfig()
		mutate(cfg)
		if err := cfg.validate(); err == nil {
			t.Fatal("invalid configuration was accepted")
		}
	}
	if err := testConfig().validate(); err != nil {
		t.Fatalf("valid configuration refused: %v", err)
	}
}

func BenchmarkRateWindow(b *testing.B) {
	s := &session{cfg: testConfig()}
	now := time.Now()
	s.rateOKAt(now)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now = now.Add(4 * time.Millisecond)
		s.rateOKAt(now)
	}
}

func TestBufferBudgetBoundaries(t *testing.T) {
	const limit = 256 << 10
	for _, tt := range []struct {
		buffered, message uint64
		allowed           bool
	}{
		{0, maxMessage, true},
		{limit - maxMessage, maxMessage, true},
		{limit - maxMessage + 1, maxMessage, false},
		{limit, 1, false},
		{math.MaxUint64, 1, false},
	} {
		if got := bufferAvailable(tt.buffered, tt.message, limit); got != tt.allowed {
			t.Fatalf("buffered=%d message=%d: allowed=%v, want %v", tt.buffered, tt.message, got, tt.allowed)
		}
	}
}
