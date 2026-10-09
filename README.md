# LMNOP (LiveProfessor M32 Network OSC Proxy)

**LMNOP** is a lightweight, cross-platform UDP network proxy designed to bridge **LiveProfessor** and **Behringer M32 / X32** mixing consoles. It manages asymmetric UDP port routing, automated console keep-alive heartbeats, and bidirectional fader curve translation.

---

## Features

- **Asymmetric UDP Routing:** Bridges LiveProfessor's fixed local listening port with the M32's bi-directional OSC communication.
- **Automated `/xremote` Keep-Alive:** Sends an OSC 1.0 compliant 16-byte `/xremote` packet every 8 seconds (configurable) to maintain active console metering and feedback subscription.
- **Fader Curve & Address Translation:**
  - Converts non-linear M32 fader positions $[0.0, 1.0]$ into LiveProfessor logarithmic audio taper values ($x = 10^{(\text{dB}-10)/40}$) matching desk markings across the range.
  - Automatically translates addresses ending in `/.../fader` $\leftrightarrow$ `/.../fader/db`.
- **Transparent Passthrough:** Relays all other OSC traffic (meters, mutes, names, colors, headamps) untouched in both directions.
- **Zero Dependencies:** Single static binary with minimal resource footprint.

---

## Downloads

Download the latest precompiled static binary for your operating system:

| Platform | Architecture | Download |
| :--- | :--- | :--- |
| **macOS** | Apple Silicon (M1/M2/M3/M4) | [lmnop-darwin-arm64](https://github.com/neonstalwart/lmnop/releases/latest/download/lmnop-darwin-arm64) |
| **macOS** | Intel (x86_64) | [lmnop-darwin-amd64](https://github.com/neonstalwart/lmnop/releases/latest/download/lmnop-darwin-amd64) |
| **Windows** | 64-bit (x64) | [lmnop-windows-amd64.exe](https://github.com/neonstalwart/lmnop/releases/latest/download/lmnop-windows-amd64.exe) |
| **Linux** | 64-bit (x64) | [lmnop-linux-amd64](https://github.com/neonstalwart/lmnop/releases/latest/download/lmnop-linux-amd64) |

---

## Quick Start

### 1. Launch the Proxy

```bash
# Example connecting to physical M32 console at 192.168.0.60
./lmnop --m32-host=192.168.0.60 --verbose

# Example connecting to local M32 emulator on 127.0.0.1:10023
./lmnop --m32-host=127.0.0.1 --m32-port=10023 --verbose
```

### 2. Configure LiveProfessor

1. In LiveProfessor, go to **Options $\to$ Hardware Controllers $\to$ OSC**.
2. Set the **Output IP Address** to `127.0.0.1` and **Output Port** to `9001` (proxy listening port).
3. Set the **Input Port** to `9000` (LiveProfessor feedback port).
4. For fader controls, bind your controller items to `/ch/XX/mix/fader/db` (or `/dca/X/fader/db`, `/bus/XX/mix/fader/db`).

---

## CLI Options

```text
Usage of lmnop:
  --heartbeat int
        Interval in seconds to send automated /xremote keep-alive (default 8)
  --help
        Show help message
  --local-port int
        Port the proxy listens on locally for LiveProfessor (default 9001)
  --lp-host string
        IP address where LiveProfessor is listening (default "127.0.0.1")
  --lp-port int
        Port LiveProfessor is listening on for feedback (default 9000)
  --m32-host string
        IP address of the Behringer M32 console (default "192.168.0.60")
  --m32-port int
        Port of the M32 console (default 10023)
  --verbose
        Enable verbose packet logging
  --version
        Show version information
```

---

## License

MIT
