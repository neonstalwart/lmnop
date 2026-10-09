package main

import (
	"context"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFormatOSCPreview(t *testing.T) {
	cases := []struct {
		input    []byte
		expected string
	}{
		{input: []byte("/ch/01/mix/fader\x00\x00\x00,f"), expected: "/ch/01/mix/fader"},
		{input: []byte("simple"), expected: "simple"},
	}

	for _, c := range cases {
		res := formatOSCPreview(c.input)
		if res != c.expected {
			t.Errorf("formatOSCPreview(%q) = %q, expected %q", c.input, res, c.expected)
		}
	}
}

func TestFaderMathConversions(t *testing.T) {
	testPoints := []struct {
		fader         float32
		expectedDB    float32
		expectedTaper float32
	}{
		{fader: 0.0, expectedDB: -90.0, expectedTaper: 0.0},
		{fader: 0.0625, expectedDB: -60.0, expectedTaper: 0.01778},
		{fader: 0.25, expectedDB: -30.0, expectedTaper: 0.10000},
		{fader: 0.50, expectedDB: -10.0, expectedTaper: 0.31623},
		{fader: 0.75, expectedDB: 0.0, expectedTaper: 0.56234},
		{fader: 1.0, expectedDB: 10.0, expectedTaper: 1.00000},
	}

	for _, pt := range testPoints {
		// Forward fader -> dB
		db := faderToDB(pt.fader)
		if math.Abs(float64(db-pt.expectedDB)) > 0.001 {
			t.Errorf("faderToDB(%f) = %f, expected %f", pt.fader, db, pt.expectedDB)
		}

		// Forward fader -> LiveProfessor taper
		taper := faderToLPTaper(pt.fader)
		if math.Abs(float64(taper-pt.expectedTaper)) > 0.001 {
			t.Errorf("faderToLPTaper(%f) = %f, expected %f", pt.fader, taper, pt.expectedTaper)
		}

		// Inverse LiveProfessor taper -> fader
		fBack := lpTaperToFader(pt.expectedTaper)
		if math.Abs(float64(fBack-pt.fader)) > 0.005 {
			t.Errorf("lpTaperToFader(%f) = %f, expected %f", pt.expectedTaper, fBack, pt.fader)
		}

		// Inverse dB -> fader
		fBackFromDB := dbToFader(pt.expectedDB)
		if math.Abs(float64(fBackFromDB-pt.fader)) > 0.001 {
			t.Errorf("dbToFader(%f) = %f, expected %f", pt.expectedDB, fBackFromDB, pt.fader)
		}
	}
}

func TestOSCEncodingDecoding(t *testing.T) {
	addr := "/ch/01/mix/fader"
	val := float32(0.75) // 0 dB unity

	encoded := buildOSCSingleFloat(addr, val)
	parsedAddr, parsedVal, ok := parseOSCSingleFloat(encoded)
	if !ok {
		t.Fatalf("Failed to parse encoded OSC packet")
	}
	if parsedAddr != addr {
		t.Errorf("Parsed address %q != %q", parsedAddr, addr)
	}
	if math.Abs(float64(parsedVal-val)) > 0.0001 {
		t.Errorf("Parsed value %f != %f", parsedVal, val)
	}

	// Test M32 -> LP Translation (fader 0.75 -> 0 dB -> taper ~0.5623)
	lpPacket, detailM32 := translateM32ToLP(encoded)
	if detailM32 == nil {
		t.Fatalf("Expected translateM32ToLP to translate /fader")
	}
	if math.Abs(float64(detailM32.RealDB-0.0)) > 0.001 || math.Abs(float64(detailM32.TaperVal-0.56234)) > 0.001 {
		t.Errorf("Expected RealDB=0.0 and TaperVal=0.56234, got RealDB=%f TaperVal=%f", detailM32.RealDB, detailM32.TaperVal)
	}
	lpAddr, lpVal, ok := parseOSCSingleFloat(lpPacket)
	if !ok || lpAddr != "/ch/01/mix/fader/db" {
		t.Fatalf("Expected /ch/01/mix/fader/db, got %q", lpAddr)
	}
	if math.Abs(float64(lpVal-0.56234)) > 0.001 {
		t.Errorf("Expected taper 0.56234, got %f", lpVal)
	}

	// Test LP -> M32 Translation (taper 0.56234 -> 0 dB -> fader 0.75)
	m32Packet, detailLP := translateLPToM32(lpPacket)
	if detailLP == nil {
		t.Fatalf("Expected translateLPToM32 to translate /fader/db")
	}
	if math.Abs(float64(detailLP.FaderPos-0.75)) > 0.001 {
		t.Errorf("Expected FaderPos=0.75, got %f", detailLP.FaderPos)
	}
	backAddr, backVal, ok := parseOSCSingleFloat(m32Packet)
	if !ok || backAddr != "/ch/01/mix/fader" {
		t.Fatalf("Expected /ch/01/mix/fader, got %q", backAddr)
	}
	if math.Abs(float64(backVal-0.75)) > 0.001 {
		t.Errorf("Expected fader 0.75, got %f", backVal)
	}
}

func TestProxyIntegration(t *testing.T) {
	// 1. Set up a mock "M32 Console" UDP listener
	mockM32Addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:18023")
	if err != nil {
		t.Fatalf("Failed to resolve mock M32 address: %v", err)
	}
	m32Conn, err := net.ListenUDP("udp", mockM32Addr)
	if err != nil {
		t.Fatalf("Failed to bind mock M32 listener: %v", err)
	}
	defer m32Conn.Close()

	// 2. Set up a mock "LiveProfessor Listener" UDP endpoint
	mockLPAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:18024")
	if err != nil {
		t.Fatalf("Failed to resolve mock LiveProfessor address: %v", err)
	}
	lpListener, err := net.ListenUDP("udp", mockLPAddr)
	if err != nil {
		t.Fatalf("Failed to bind mock LiveProfessor listener: %v", err)
	}
	defer lpListener.Close()

	// Build the test binary directly to avoid orphan processes from 'go run'
	binPath := filepath.Join(t.TempDir(), "lmnop-test")
	buildCmd := exec.Command("go", "build", "-o", binPath, "main.go")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build test binary: %v\nOutput: %s", err, string(out))
	}

	// 3. Start the proxy binary in a background process using test ports
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath,
		"--local-port=18025",
		"--m32-host=127.0.0.1",
		"--m32-port=18023",
		"--lp-host=127.0.0.1",
		"--lp-port=18024",
		"--heartbeat=1",
		"--verbose",
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start proxy process: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	// Allow proxy binding time
	time.Sleep(300 * time.Millisecond)

	proxyEndpoint, err := net.ResolveUDPAddr("udp", "127.0.0.1:18025")
	if err != nil {
		t.Fatalf("Failed to resolve proxy endpoint: %v", err)
	}

	clientConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("Failed to create client socket: %v", err)
	}
	defer clientConn.Close()

	// 4. Test Case A: LiveProfessor -> Proxy -> M32 (/fader/db -> /fader with taper translation)
	// LP sends 0.56234 taper (0 dB unity) to /ch/01/mix/fader/db
	testOSCPacket := buildOSCSingleFloat("/ch/01/mix/fader/db", 0.56234)
	_, err = clientConn.WriteToUDP(testOSCPacket, proxyEndpoint)
	if err != nil {
		t.Fatalf("Failed to send test packet to proxy: %v", err)
	}

	// Assert mock M32 received /ch/01/mix/fader with 0.75 (0 dB unity fader position)
	buf := make([]byte, 1024)
	if err := m32Conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("Failed to set read deadline: %v", err)
	}
	n, _, err := m32Conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("TIMEOUT: Mock M32 did not receive relayed packet: %v", err)
	}
	addr, faderVal, ok := parseOSCSingleFloat(buf[:n])
	if !ok || addr != "/ch/01/mix/fader" {
		t.Fatalf("Expected address /ch/01/mix/fader, got %q", addr)
	}
	if math.Abs(float64(faderVal-0.75)) > 0.005 {
		t.Errorf("Expected fader position 0.75, got %f", faderVal)
	}

	// 5. Test Case B: M32 -> Proxy -> LiveProfessor (/fader -> /fader/db with taper translation)
	// M32 sends fader position 0.75 (0 dB)
	m32FaderPacket := buildOSCSingleFloat("/ch/01/mix/fader", 0.75)
	_, err = m32Conn.WriteToUDP(m32FaderPacket, proxyEndpoint)
	if err != nil {
		t.Fatalf("Failed to write mock feedback from M32: %v", err)
	}

	// Assert LiveProfessor listener received /ch/01/mix/fader/db with 0.56234
	if err := lpListener.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("Failed to set read deadline: %v", err)
	}
	n, _, err = lpListener.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("TIMEOUT: Mock LiveProfessor listener did not receive M32 feedback: %v", err)
	}
	lpRecvAddr, lpRecvVal, ok := parseOSCSingleFloat(buf[:n])
	if !ok || lpRecvAddr != "/ch/01/mix/fader/db" {
		t.Fatalf("Expected /ch/01/mix/fader/db, got %q", lpRecvAddr)
	}
	if math.Abs(float64(lpRecvVal-0.56234)) > 0.001 {
		t.Errorf("Expected taper 0.56234, got %f", lpRecvVal)
	}

	// 6. Test Case C: Non-fader OSC Passthrough
	nonFaderPacket := []byte("/meters/1\x00\x00\x00")
	_, err = m32Conn.WriteToUDP(nonFaderPacket, proxyEndpoint)
	if err != nil {
		t.Fatalf("Failed to write mock non-fader from M32: %v", err)
	}
	if err := lpListener.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("Failed to set read deadline: %v", err)
	}
	n, _, err = lpListener.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("TIMEOUT: Mock LP listener did not receive passthrough message: %v", err)
	}
	if string(buf[:n]) != string(nonFaderPacket) {
		t.Errorf("Expected passthrough %q, got %q", string(nonFaderPacket), string(buf[:n]))
	}

	// 7. Test Case D: Automated /xremote Heartbeat Verification
	if err := m32Conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("Failed to set read deadline: %v", err)
	}
	n, _, err = m32Conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("TIMEOUT: Did not receive automated /xremote heartbeat ticker: %v", err)
	}
	expectedHeartbeat := string([]byte("/xremote\x00\x00\x00\x00,\x00\x00\x00"))
	receivedStr := string(buf[:n])
	if receivedStr != expectedHeartbeat {
		t.Errorf("Expected heartbeat %q, got %q", expectedHeartbeat, receivedStr)
	}
}
