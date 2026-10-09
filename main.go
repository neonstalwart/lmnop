package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"time"
)

func main() {
	// Define CLI flags with sensible defaults
	localPort := flag.Int("local-port", 9000, "Port the proxy listens on locally for LiveProfessor")
	m32Host := flag.String("m32-host", "192.168.0.60", "IP address of the Behringer M32 console")
	m32Port := flag.Int("m32-port", 10023, "Port of the M32 console")
	lpHost := flag.String("lp-host", "127.0.0.1", "IP address where LiveProfessor is listening")
	lpPort := flag.Int("lp-port", 10024, "Port LiveProfessor is listening on for feedback")
	heartbeatSec := flag.Int("heartbeat", 8, "Interval in seconds to send automated /xremote keep-alive")
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

	log.Printf("Proxy started on :%d", *localPort)
	log.Printf("  -> Forwarding M32 feedback to: %s", lpAddr.String())
	log.Printf("  -> Forwarding LiveProfessor traffic to: %s", m32Addr.String())

	// Background goroutine for the /xremote keep-alive heartbeat
	go func() {
		ticker := time.NewTicker(time.Duration(*heartbeatSec) * time.Second)
		defer ticker.Stop()

		// OSC strings must be null-terminated and padded to 4-byte boundaries.
		// "/xremote\0\0\0\0" is exactly 12 bytes.
		xremotePacket := []byte("/xremote\x00\x00\x00\x00")

		for range ticker.C {
			_, err := conn.WriteToUDP(xremotePacket, m32Addr)
			if err != nil {
				log.Printf("Error sending automated /xremote heartbeat: %v", err)
			} else {
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
			_, err = conn.WriteToUDP(buf[:n], lpAddr)
			if err != nil {
				log.Printf("Failed to relay packet to LiveProfessor: %v", err)
			}
		} else {
			// Message came from LiveProfessor (or anything else) -> Relay to M32
			_, err = conn.WriteToUDP(buf[:n], m32Addr)
			if err != nil {
				log.Printf("Failed to relay packet to M32: %v", err)
			}
		}
	}
}
