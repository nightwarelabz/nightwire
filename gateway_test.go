package main

// Guard-logic tests for sparta-rtc: the rate window and registry cap
// are exercised directly, and the Origin law + session cap are
// exercised end-to-end over real WebSocket upgrades against the real
// handler (no ICE needed — the cap and the Origin check both bite
// before any peer connection exists).
//
// The DataChannel rate_limit close and the DC<->UDP pipe cannot be
// exercised in the lab sandbox at all (its egress policy returns EPERM
// for every send on an unconnected UDP socket, which is how ICE sends
// STUN checks — see proof/results.md), so the window arithmetic that
// drives that close is pinned here instead.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

func testConfig() *config {
	cfg := &config{
		serverHost:       "127.0.0.1",
		serverPort:       26099,
		port:             26003,
		maxSessions:      64,
		idleTimeout:      3600,
		setupTimeout:     10 * time.Second,
		maxBufferedBytes: 256 << 10,
		maxPPS:           300,
		allowOrigins:     map[string]bool{"https://nosuchmachine.net": true},
	}
	return cfg
}

func TestRateWindow(t *testing.T) {
	s := &session{cfg: testConfig()}
	for i := 0; i < 300; i++ {
		if !s.rateOK() {
			t.Fatalf("packet %d within 300 pps was dropped", i+1)
		}
	}
	if s.rateOK() {
		t.Fatal("packet 301 within the same second was not dropped")
	}
	// Slide the window past 1s: forged timestamps age out.
	s.rateMu.Lock()
	for i := range s.rateWindow {
		s.rateWindow[i] = time.Now().Add(-2 * time.Second)
	}
	s.rateMu.Unlock()
	if !s.rateOK() {
		t.Fatal("packet after window slide was dropped")
	}
}

func TestRegistryCap(t *testing.T) {
	r := newRegistry(2)
	if id := r.open(); id != 1 {
		t.Fatalf("first open = %d, want 1", id)
	}
	if id := r.open(); id != 2 {
		t.Fatalf("second open = %d, want 2", id)
	}
	if id := r.open(); id != -1 {
		t.Fatalf("third open at cap = %d, want -1", id)
	}
	r.close(1)
	if id := r.open(); id != 3 {
		t.Fatalf("open after close = %d, want 3", id)
	}
}

func dial(t *testing.T, srv *httptest.Server, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/rtc"
	return websocket.DefaultDialer.Dial(url, hdr)
}

func TestOriginLawAndCap(t *testing.T) {
	cfg := testConfig()
	cfg.maxSessions = 1
	reg := newRegistry(cfg.maxSessions)
	api := webrtc.NewAPI()
	srv := httptest.NewServer(makeHandler(cfg, reg, api))
	defer srv.Close()

	// Foreign Origin -> 403 before upgrade.
	_, resp, err := dial(t, srv, "https://evil.example")
	if err == nil {
		t.Fatal("foreign origin was upgraded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin status = %v, want 403", resp)
	}

	// Absent Origin (non-browser client) -> accepted.
	c1, _, err := dial(t, srv, "")
	if err != nil {
		t.Fatalf("absent origin refused: %v", err)
	}
	defer c1.Close()

	// Session cap reached (1) -> next upgrade refused 503, even with
	// the site Origin.
	_, resp, err = dial(t, srv, "https://nosuchmachine.net")
	if err == nil {
		t.Fatal("second session over the cap was upgraded")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("over-cap status = %v, want 503", resp)
	}
}
