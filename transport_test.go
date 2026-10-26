package main

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
	"github.com/pion/webrtc/v4"
)

// Real Pion peers, real signaling WS, and a real UDP echo server. No STUN,
// browser, external server, or WAN connectivity is needed for this test.
// Advertise a fixed loopback interface while retaining standard OS sockets.
// This avoids NIC enumeration, which restricted runners may prohibit.
type loopbackNet struct{ *stdnet.Net }

func (n loopbackNet) Interfaces() ([]*transport.Interface, error) {
	iface := transport.NewInterface(net.Interface{Index: 1, MTU: 65536, Name: "proof-loopback", Flags: net.FlagUp | net.FlagLoopback})
	iface.AddAddress(&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(8, 32)})
	return []*transport.Interface{iface}, nil
}

func (n loopbackNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	ifs, _ := n.Interfaces()
	if index != ifs[0].Index {
		return nil, transport.ErrInterfaceNotFound
	}
	return ifs[0], nil
}

func (n loopbackNet) InterfaceByName(name string) (*transport.Interface, error) {
	ifs, _ := n.Interfaces()
	if name != ifs[0].Name {
		return nil, transport.ErrInterfaceNotFound
	}
	return ifs[0], nil
}

func loopbackAPI() *webrtc.API {
	se := webrtc.SettingEngine{}
	se.SetNet(loopbackNet{Net: &stdnet.Net{}})
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetIncludeLoopbackCandidate(true)
	se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	return webrtc.NewAPI(webrtc.WithSettingEngine(se))
}

func udpEcho(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 65535)
		for {
			n, peer, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if _, err := c.WriteToUDP(buf[:n], peer); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { c.Close(); <-done })
	return c.LocalAddr().(*net.UDPAddr).Port
}

func rtcSession(t *testing.T, cfg *config) (*webrtc.PeerConnection, *webrtc.DataChannel, *session, <-chan []byte, *websocket.Conn) {
	t.Helper()
	reg, srv := testGateway(t, cfg, loopbackAPI())
	ws, _, err := dial(t, srv, "https://nosuchmachine.net")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	pc, err := loopbackAPI().NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	dc, err := pc.CreateDataChannel("sparta", datagramChannel())
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{})
	received := make(chan []byte, 512)
	dc.OnOpen(func() { close(opened) })
	dc.OnMessage(func(m webrtc.DataChannelMessage) { received <- append([]byte(nil), m.Data...) })
	signalErrors := make(chan error, 1)
	signalDone := make(chan struct{})
	go func() {
		defer close(signalDone)
		var pending []webrtc.ICECandidateInit
		remoteSet := false
		for {
			var msg signalMsg
			if err := ws.ReadJSON(&msg); err != nil {
				return
			}
			var err error
			switch msg.Type {
			case "answer":
				err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: msg.SDP})
				if err == nil {
					remoteSet = true
					for _, cand := range pending {
						if err = pc.AddICECandidate(cand); err != nil {
							break
						}
					}
					pending = nil
				}
			case "ice":
				cand := webrtc.ICECandidateInit{Candidate: msg.Candidate, SDPMid: msg.SDPMid, SDPMLineIndex: msg.SDPMLineIndex}
				if remoteSet {
					err = pc.AddICECandidate(cand)
				} else {
					pending = append(pending, cand)
				}
			}
			if err != nil {
				signalErrors <- err
				return
			}
		}
	}()
	t.Cleanup(func() { ws.Close(); <-signalDone })
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		t.Fatal("local ICE gathering did not complete")
	}
	if err := ws.WriteJSON(signalMsg{Type: "offer", SDP: pc.LocalDescription().SDP}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-opened:
	case err := <-signalErrors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatalf("DataChannel did not open: pc=%s ice=%s", pc.ConnectionState(), pc.ICEConnectionState())
	}
	var s *session
	waitFor(t, "gateway session", func() bool {
		reg.mu.Lock()
		defer reg.mu.Unlock()
		s = reg.sessions[1]
		return s != nil
	})
	return pc, dc, s, received, ws
}

func TestDatagramRoundTrip(t *testing.T) {
	cfg := testConfig()
	cfg.serverPort = udpEcho(t)
	_, dc, s, received, _ := rtcSession(t, cfg)
	payloads := [][]byte{[]byte("\xff\xff\xff\xffport"), {0}, bytes.Repeat([]byte{0, 255, 7, 128}, 300), bytes.Repeat([]byte{42}, 16384), bytes.Repeat([]byte{173}, maxMessage)}
	var total int64
	for i, data := range payloads {
		if err := dc.Send(data); err != nil {
			t.Fatal(err)
		}
		select {
		case back := <-received:
			if !bytes.Equal(back, data) {
				t.Fatalf("datagram %d: got %d bytes, want %d byte-exact", i, len(back), len(data))
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no echo for datagram %d (%d bytes)", i, len(data))
		}
		total += int64(len(data))
	}
	waitFor(t, "traffic counters", func() bool {
		return s.pktsToServer.Load() == int64(len(payloads)) && s.pktsToClient.Load() == int64(len(payloads))
	})
	if s.bytesToServer.Load() != total || s.bytesToClient.Load() != total {
		t.Fatal("byte counters did not preserve datagrams")
	}
}

func TestDataChannelRateLimitCloses(t *testing.T) {
	cfg := testConfig()
	cfg.serverPort = udpEcho(t)
	cfg.maxPPS = 3
	_, dc, s, _, _ := rtcSession(t, cfg)
	for i := 0; i < 20; i++ {
		if err := dc.Send([]byte(fmt.Sprintf("packet-%d", i))); err != nil {
			break
		}
	}
	waitClosed(t, s, "rate_limit")
	if got := s.pktsToServer.Load(); got != 3 {
		t.Fatalf("forwarded=%d, want 3", got)
	}
	if got := s.rateDrops.Load(); got != 10 {
		t.Fatalf("rate drops=%d, want 10", got)
	}
}

func TestOversizedDatagramCloses(t *testing.T) {
	cfg := testConfig()
	cfg.serverPort = udpEcho(t)
	_, dc, s, _, _ := rtcSession(t, cfg)
	if err := dc.Send(bytes.Repeat([]byte{1}, maxMessage+1)); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, s, "message_too_large")
	if s.pktsToServer.Load() != 0 {
		t.Fatal("oversized datagram reached the game server")
	}
}

func TestDuplicateOfferCloses(t *testing.T) {
	cfg := testConfig()
	cfg.serverPort = udpEcho(t)
	pc, _, s, _, ws := rtcSession(t, cfg)
	if err := ws.WriteJSON(signalMsg{Type: "offer", SDP: pc.LocalDescription().SDP}); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, s, "protocol_error")
}

func TestSignalingDoesNotKeepIdleMediaAlive(t *testing.T) {
	cfg := testConfig()
	cfg.serverPort = udpEcho(t)
	cfg.idleTimeout = 0.15
	_, _, s, _, ws := rtcSession(t, cfg)
	for i := 0; i < 4; i++ {
		if err := ws.WriteJSON(signalMsg{Type: "ice"}); err != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	waitClosed(t, s, "idle")
}
