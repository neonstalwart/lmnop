package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

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
		"--heartbeat=1", // Fast heartbeat for test speed
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

	// 4. Test Case A: LiveProfessor -> Proxy -> M32 Relay
	proxyEndpoint, err := net.ResolveUDPAddr("udp", "127.0.0.1:18025")
	if err != nil {
		t.Fatalf("Failed to resolve proxy endpoint: %v", err)
	}

	clientConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("Failed to create client socket: %v", err)
	}
	defer clientConn.Close()

	testOSCPacket := []byte("/ch/01/mix/fader\x00\x00\x00,f\x00\x00\x3f\x80\x00\x00")
	_, err = clientConn.WriteToUDP(testOSCPacket, proxyEndpoint)
	if err != nil {
		t.Fatalf("Failed to send test packet to proxy: %v", err)
	}

	// Assert mock M32 received it
	buf := make([]byte, 1024)
	if err := m32Conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("Failed to set read deadline: %v", err)
	}
	n, _, err := m32Conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("TIMEOUT: Mock M32 did not receive relayed packet: %v", err)
	}
	if n != len(testOSCPacket) {
		t.Errorf("Expected packet size %d, got %d", len(testOSCPacket), n)
	}

	// 5. Test Case B: M32 -> Proxy -> LiveProfessor Relay
	// Note: To send *from* the mock M32 IP/port, we write back through m32Conn to the proxy's local port (18025)
	_, err = m32Conn.WriteToUDP([]byte("/meters/1\x00\x00\x00"), proxyEndpoint)
	if err != nil {
		t.Fatalf("Failed to write mock feedback from M32: %v", err)
	}

	// Assert LiveProfessor listener received it
	if err := lpListener.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("Failed to set read deadline: %v", err)
	}
	n, _, err = lpListener.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("TIMEOUT: Mock LiveProfessor listener did not receive M32 feedback: %v", err)
	}
	expectedFeedback := "/meters/1\x00\x00\x00"
	if string(buf[:n]) != expectedFeedback {
		t.Errorf("Expected feedback %q, got %q", expectedFeedback, string(buf[:n]))
	}

	// 6. Test Case C: Automated /xremote Heartbeat Verification
	if err := m32Conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("Failed to set read deadline: %v", err)
	}
	n, _, err = m32Conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("TIMEOUT: Did not receive automated /xremote heartbeat ticker: %v", err)
	}
	expectedHeartbeat := string([]byte("/xremote\x00\x00\x00\x00"))
	receivedStr := string(buf[:n])
	if receivedStr != expectedHeartbeat {
		t.Errorf("Expected heartbeat %q, got %q", expectedHeartbeat, receivedStr)
	}
}
