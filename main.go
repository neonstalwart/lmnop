package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

func formatOSCPreview(data []byte) string {
	// Extract printable characters up to the first null or unprintable byte sequence
	idx := bytes.IndexByte(data, 0)
	if idx > 0 {
		return string(data[:idx])
	}
	cleaned := strings.Map(func(r rune) rune {
		if r >= 32 && r <= 126 {
			return r
		}
		return '.'
	}, string(data))
	if len(cleaned) > 50 {
		return cleaned[:50] + "..."
	}
	return cleaned
}

func main() {
	// Define CLI flags with sensible defaults
	localPort := flag.Int("local-port", 9001, "Port the proxy listens on locally for LiveProfessor")
	m32Host := flag.String("m32-host", "192.168.0.60", "IP address of the Behringer M32 console")
	m32Port := flag.Int("m32-port", 10023, "Port of the M32 console")
	lpHost := flag.String("lp-host", "127.0.0.1", "IP address where LiveProfessor is listening")
	lpPort := flag.Int("lp-port", 9000, "Port LiveProfessor is listening on for feedback")
	heartbeatSec := flag.Int("heartbeat", 8, "Interval in seconds to send automated /xremote keep-alive")
	verbose := flag.Bool("verbose", false, "Enable verbose packet logging")
	help := flag.Bool("help", false, "Show help message")

	flag.Parse()

	if *help {
		fmt.Println("LMNOP - LiveProfessor M32 Network OSC Proxy")
		fmt.Println("Usage:")
		flag.PrintDefaults()
		os.Exit(0)
	}

	// Resolve network addresses
	localAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", *localPort))
	if err != nil {
		log.Fatalf("Failed to resolve local address: %v", err)
	}

	m32Addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", *m32Host, *m32Port))
	if err != nil {
		log.Fatalf("Failed to resolve M32 address: %v", err)
	}

	lpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", *lpHost, *lpPort))
	if err != nil {
		log.Fatalf("Failed to resolve LiveProfessor address: %v", err)
	}

	// Start listening on the local UDP port
	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		log.Fatalf("Failed to bind to local port %d: %v", *localPort, err)
	}
	defer conn.Close()

	log.Printf("LMNOP Proxy started on :%d", *localPort)
	log.Printf("  -> Forwarding M32 feedback to: %s", lpAddr.String())
	log.Printf("  -> Forwarding LiveProfessor traffic to: %s", m32Addr.String())
	if *verbose {
		log.Printf("  -> Verbose packet logging enabled")
	}

	// Background goroutine for the /xremote keep-alive heartbeat
	go func() {
		ticker := time.NewTicker(time.Duration(*heartbeatSec) * time.Second)
		defer ticker.Stop()

		// OSC format: address pattern padded to 4 bytes (12 bytes) + type tag string ",\0\0\0" (4 bytes).
		// Exactly 16 bytes matching standard OSC 1.0 and M32-Edit format.
		xremotePacket := []byte("/xremote\x00\x00\x00\x00,\x00\x00\x00")

		for range ticker.C {
			_, err := conn.WriteToUDP(xremotePacket, m32Addr)
			if err != nil {
				log.Printf("Error sending automated /xremote heartbeat: %v", err)
			} else if *verbose {
				log.Println("Sent automated /xremote heartbeat to M32")
			}
		}
	}()

	// Main packet relay loop
	buf := make([]byte, 4096)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("Read error: %v", err)
			continue
		}

		// Check who sent the packet
		if remoteAddr.IP.Equal(m32Addr.IP) && remoteAddr.Port == m32Addr.Port {
			// Message came from the M32 -> Relay to LiveProfessor's listening port
			if *verbose {
				log.Printf("[M32 -> LP] %d bytes (%q) relayed to %s", n, formatOSCPreview(buf[:n]), lpAddr.String())
			}
			_, err = conn.WriteToUDP(buf[:n], lpAddr)
			if err != nil {
				log.Printf("Failed to relay packet to LiveProfessor: %v", err)
			}
		} else {
			// Message came from LiveProfessor (or anything else) -> Relay to M32
			if *verbose {
				log.Printf("[LP -> M32] %d bytes (%q) from %s relayed to %s", n, formatOSCPreview(buf[:n]), remoteAddr.String(), m32Addr.String())
			}
			_, err = conn.WriteToUDP(buf[:n], m32Addr)
			if err != nil {
				log.Printf("Failed to relay packet to M32: %v", err)
			}
		}
	}
}
