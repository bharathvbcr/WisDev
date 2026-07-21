package stackconfig

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// AutoPortEnabled is true when WISDEV_AUTO_PORT requests dynamic local port
// selection (preferred manifest port if free, otherwise the next free port).
func AutoPortEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WISDEV_AUTO_PORT"))) {
	case "1", "true", "yes", "on", "auto":
		return true
	default:
		return false
	}
}

// IsAutoPortSpec reports whether an env port value means "pick automatically".
func IsAutoPortSpec(spec string) bool {
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case "", "auto", "0":
		return true
	default:
		return false
	}
}

// PickListenPort returns preferred when it is free on loopback; otherwise it
// reserves and returns an ephemeral port.
func PickListenPort(preferred int) (int, error) {
	if preferred > 0 && portAvailable(preferred) {
		return preferred, nil
	}
	return ReserveTCPPort()
}

// ReserveTCPPort binds to 127.0.0.1:0 and returns the assigned port.
func ReserveTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve tcp port: %w", err)
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 {
		return 0, fmt.Errorf("reserve tcp port: invalid listener address")
	}
	return addr.Port, nil
}

func portAvailable(port int) bool {
	if port <= 0 {
		return false
	}
	// Match server bind addresses (`:port`), not just loopback.
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func closeListeners(listeners []net.Listener) {
	for _, ln := range listeners {
		if ln != nil {
			_ = ln.Close()
		}
	}
}

func bindAndHold(port int, listeners *[]net.Listener) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	*listeners = append(*listeners, ln)
	return nil
}

func reserveUniqueTCPPort(used map[int]struct{}, listeners *[]net.Listener) (int, error) {
	for attempt := 0; attempt < 64; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, fmt.Errorf("reserve tcp port: %w", err)
		}
		addr, ok := ln.Addr().(*net.TCPAddr)
		if !ok || addr.Port <= 0 {
			_ = ln.Close()
			continue
		}
		if _, exists := used[addr.Port]; exists {
			_ = ln.Close()
			continue
		}
		used[addr.Port] = struct{}{}
		*listeners = append(*listeners, ln)
		return addr.Port, nil
	}
	return 0, fmt.Errorf("reserve unique tcp port: exceeded attempt limit")
}

func pickStackPort(preferred int, used map[int]struct{}, listeners *[]net.Listener) (int, error) {
	if preferred <= 0 {
		return reserveUniqueTCPPort(used, listeners)
	}
	if _, exists := used[preferred]; exists {
		return 0, fmt.Errorf("port %d already allocated in this stack", preferred)
	}

	if AutoPortEnabled() {
		if portAvailable(preferred) {
			if err := bindAndHold(preferred, listeners); err == nil {
				used[preferred] = struct{}{}
				return preferred, nil
			}
		}
		return reserveUniqueTCPPort(used, listeners)
	}

	if !portAvailable(preferred) {
		return 0, fmt.Errorf("port %d is occupied; set WISDEV_AUTO_PORT=1 or free the port", preferred)
	}
	if err := bindAndHold(preferred, listeners); err != nil {
		return 0, fmt.Errorf("bind port %d: %w", preferred, err)
	}
	used[preferred] = struct{}{}
	return preferred, nil
}

// StackPorts holds the local dev stack listen ports.
type StackPorts struct {
	OrchestratorHTTP    int
	OrchestratorMetrics int
	OrchestratorGRPC    int
	SidecarHTTP         int
	SidecarGRPC         int
}

func allocateLocalStackPorts() (StackPorts, []net.Listener, error) {
	goSvc, ok := Manifest.Services["go_orchestrator"]
	if !ok {
		return StackPorts{}, nil, fmt.Errorf("go_orchestrator missing from stack manifest")
	}
	pySvc, ok := Manifest.Services["python_sidecar"]
	if !ok {
		return StackPorts{}, nil, fmt.Errorf("python_sidecar missing from stack manifest")
	}

	used := make(map[int]struct{})
	var holds []net.Listener

	httpPort, err := pickStackPort(goSvc.ListenPorts["http"], used, &holds)
	if err != nil {
		closeListeners(holds)
		return StackPorts{}, nil, err
	}
	metricsPort, err := pickStackPort(goSvc.ListenPorts["metrics"], used, &holds)
	if err != nil {
		closeListeners(holds)
		return StackPorts{}, nil, err
	}
	grpcPort, err := pickStackPort(goSvc.ListenPorts["grpc"], used, &holds)
	if err != nil {
		closeListeners(holds)
		return StackPorts{}, nil, err
	}
	sidecarHTTP, err := pickStackPort(pySvc.ListenPorts["http"], used, &holds)
	if err != nil {
		closeListeners(holds)
		return StackPorts{}, nil, err
	}
	sidecarGRPC, err := pickStackPort(pySvc.ListenPorts["grpc"], used, &holds)
	if err != nil {
		closeListeners(holds)
		return StackPorts{}, nil, err
	}

	return StackPorts{
		OrchestratorHTTP:    httpPort,
		OrchestratorMetrics: metricsPort,
		OrchestratorGRPC:    grpcPort,
		SidecarHTTP:         sidecarHTTP,
		SidecarGRPC:         sidecarGRPC,
	}, holds, nil
}

// AllocateLocalStackPorts picks ports for the open-source local stack. When
// WISDEV_AUTO_PORT is off, manifest defaults are kept when still available.
func AllocateLocalStackPorts() (StackPorts, error) {
	ports, holds, err := allocateLocalStackPorts()
	closeListeners(holds)
	return ports, err
}

// AllocateLocalStackPortsAndWrite allocates unique stack ports, holds listeners
// until ports.env is written, then releases them.
func AllocateLocalStackPortsAndWrite(path string) (StackPorts, error) {
	ports, holds, err := allocateLocalStackPorts()
	if err != nil {
		return StackPorts{}, err
	}
	defer closeListeners(holds)
	if err := WritePortsEnv(path, ports); err != nil {
		return StackPorts{}, err
	}
	return ports, nil
}

// Env returns the canonical env vars for this port allocation.
func (p StackPorts) Env() map[string]string {
	orchestratorURL := fmt.Sprintf("http://127.0.0.1:%d", p.OrchestratorHTTP)
	sidecarURL := fmt.Sprintf("http://127.0.0.1:%d", p.SidecarHTTP)
	return map[string]string{
		"PORT":                         strconv.Itoa(p.OrchestratorHTTP),
		"PYTHON_SIDECAR_PORT":          strconv.Itoa(p.SidecarHTTP),
		"INTERNAL_METRICS_PORT":        strconv.Itoa(p.OrchestratorMetrics),
		"GO_INTERNAL_GRPC_ADDR":        fmt.Sprintf("127.0.0.1:%d", p.OrchestratorGRPC),
		"PYTHON_SIDECAR_HTTP_URL":      sidecarURL,
		"PYTHON_SIDECAR_GRPC_ADDR":     fmt.Sprintf("127.0.0.1:%d", p.SidecarGRPC),
		"WISDEV_ORCHESTRATOR_URL":      orchestratorURL,
		"PYTHON_SIDECAR_LLM_TRANSPORT": "grpc",
	}
}
