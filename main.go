package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"strings"
	"time"
)

// formatOSCPreview formats raw OSC bytes for human-readable logging.
func formatOSCPreview(data []byte) string {
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

// faderToDB converts normalized M32 fader position [0.0, 1.0] to true dB [-90.0, +10.0].
func faderToDB(f float32) float32 {
	switch {
	case f <= 0.0:
		return -90.0
	case f <= 0.0625:
		return -90.0 + f*480.0
	case f <= 0.25:
		return -60.0 + (f-0.0625)*160.0
	case f <= 0.50:
		return -30.0 + (f-0.25)*80.0
	default:
		if f > 1.0 {
			f = 1.0
		}
		return -10.0 + (f-0.50)*40.0
	}
}

// dbToFader converts true dB [-90.0, +10.0] to normalized M32 fader position [0.0, 1.0].
func dbToFader(db float32) float32 {
	switch {
	case db <= -90.0:
		return 0.0
	case db <= -60.0:
		return (db + 90.0) / 480.0
	case db <= -30.0:
		return 0.0625 + (db+60.0)/160.0
	case db <= -10.0:
		return 0.25 + (db+30.0)/80.0
	default:
		if db > 10.0 {
			db = 10.0
		}
		return 0.50 + (db+10.0)/40.0
	}
}

// faderToNormDB maps M32 fader [0.0, 1.0] to normalized linear dB [0.0, 1.0] (-90dB = 0.0, +10dB = 1.0, 0dB = 0.9).
func faderToNormDB(f float32) float32 {
	db := faderToDB(f)
	norm := (db + 90.0) / 100.0
	if norm < 0.0 {
		return 0.0
	}
	if norm > 1.0 {
		return 1.0
	}
	return norm
}

// normDBToFader maps normalized linear dB [0.0, 1.0] back to M32 fader position [0.0, 1.0].
func normDBToFader(norm float32) float32 {
	if norm < 0.0 {
		norm = 0.0
	}
	if norm > 1.0 {
		norm = 1.0
	}
	db := norm*100.0 - 90.0
	return dbToFader(db)
}

// parseOSCSingleFloat extracts the OSC address and single float32 value from a standard OSC message.
func parseOSCSingleFloat(data []byte) (string, float32, bool) {
	nullIdx := bytes.IndexByte(data, 0)
	if nullIdx <= 0 {
		return "", 0, false
	}
	addr := string(data[:nullIdx])
	typeTagOffset := ((nullIdx / 4) + 1) * 4
	if len(data) < typeTagOffset+8 {
		return "", 0, false
	}
	// Check type tag for single float: ",f\0\0"
	if data[typeTagOffset] != ',' || data[typeTagOffset+1] != 'f' || data[typeTagOffset+2] != 0 || data[typeTagOffset+3] != 0 {
		return "", 0, false
	}
	bits := binary.BigEndian.Uint32(data[typeTagOffset+4 : typeTagOffset+8])
	return addr, math.Float32frombits(bits), true
}

// buildOSCSingleFloat encodes an OSC single-float message with 4-byte padding alignment.
func buildOSCSingleFloat(address string, val float32) []byte {
	addrBytes := []byte(address)
	padLen := 4 - (len(addrBytes) % 4)
	if padLen == 0 {
		padLen = 4
	}
	addrPadded := append(addrBytes, make([]byte, padLen)...)
	typeTag := []byte{',', 'f', 0, 0}
	valBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(valBytes, math.Float32bits(val))

	msg := make([]byte, 0, len(addrPadded)+len(typeTag)+len(valBytes))
	msg = append(msg, addrPadded...)
	msg = append(msg, typeTag...)
	msg = append(msg, valBytes...)
	return msg
}

// translateM32ToLP translates M32 `/.../fader` messages to `/.../fader/db` with normalized dB values.
func translateM32ToLP(data []byte) ([]byte, bool) {
	addr, val, ok := parseOSCSingleFloat(data)
	if ok && strings.HasSuffix(addr, "/fader") {
		normDB := faderToNormDB(val)
		return buildOSCSingleFloat(addr+"/db", normDB), true
	}
	return data, false
}

// translateLPToM32 translates LiveProfessor `/.../fader/db` messages to `/.../fader` with M32 fader positions.
func translateLPToM32(data []byte) ([]byte, bool) {
	addr, val, ok := parseOSCSingleFloat(data)
	if ok && strings.HasSuffix(addr, "/fader/db") {
		baseAddr := strings.TrimSuffix(addr, "/db")
		faderPos := normDBToFader(val)
		return buildOSCSingleFloat(baseAddr, faderPos), true
	}
	return data, false
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

		rawPacket := buf[:n]

		// Check who sent the packet
		if remoteAddr.IP.Equal(m32Addr.IP) && remoteAddr.Port == m32Addr.Port {
			// Message came from the M32 -> Translate fader and relay to LiveProfessor
			outPacket, translated := translateM32ToLP(rawPacket)
			if *verbose {
				if translated {
					log.Printf("[M32 -> LP (translated)] %s -> %s", formatOSCPreview(rawPacket), formatOSCPreview(outPacket))
				} else {
					log.Printf("[M32 -> LP] %d bytes (%q) relayed to %s", len(outPacket), formatOSCPreview(outPacket), lpAddr.String())
				}
			}
			_, err = conn.WriteToUDP(outPacket, lpAddr)
			if err != nil {
				log.Printf("Failed to relay packet to LiveProfessor: %v", err)
			}
		} else {
			// Message came from LiveProfessor -> Translate fader/db and relay to M32
			outPacket, translated := translateLPToM32(rawPacket)
			if *verbose {
				if translated {
					log.Printf("[LP -> M32 (translated)] %s -> %s", formatOSCPreview(rawPacket), formatOSCPreview(outPacket))
				} else {
					log.Printf("[LP -> M32] %d bytes (%q) from %s relayed to %s", len(outPacket), formatOSCPreview(outPacket), remoteAddr.String(), m32Addr.String())
				}
			}
			_, err = conn.WriteToUDP(outPacket, m32Addr)
			if err != nil {
				log.Printf("Failed to relay packet to M32: %v", err)
			}
		}
	}
}
