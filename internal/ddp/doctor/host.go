package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/platform"
)

const hostCheckTimeout = 3 * time.Second

// Docker checks whether the Docker daemon answers a read-only info request.
func Docker(ctx context.Context) platform.Result {
	ctx, cancel := context.WithTimeout(ctx, hostCheckTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return platform.Result{State: "unknown", Severity: "critical", Message: "Docker availability could not be determined within 3 seconds. Check the Docker service and host responsiveness."}
		}
		if errors.Is(err, exec.ErrNotFound) {
			return platform.Result{State: "unknown", Severity: "critical", Message: "Docker is not installed or is not on PATH. Install Docker and ensure the docker command is available."}
		}
		return platform.Result{State: "failing", Severity: "critical", Message: "Docker is unavailable. Start the Docker service and check its daemon configuration."}
	}
	return platform.Result{State: "ok", Severity: "critical", Message: "Docker daemon is reachable."}
}

// Port checks the configured local serving port and, when occupied, its readiness endpoint.
func Port(ctx context.Context, addr string) platform.Result {
	host, port, probeAddr, ok := localAddress(addr)
	if !ok {
		return platform.Result{State: "unknown", Severity: "critical", Message: "The configured serving address is invalid or non-local. Set serving.addr to a local TCP address."}
	}
	if err := ctx.Err(); err != nil {
		return platform.Result{State: "unknown", Severity: "critical", Message: "The serving port check was cancelled before it completed."}
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err == nil {
		_ = listener.Close()
		return platform.Result{State: "ok", Severity: "critical", Message: "The serving port is available for the DDP API; no service readiness was observed."}
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		return platform.Result{State: "unknown", Severity: "critical", Message: "The configured serving port could not be checked. Verify the local address and host permissions."}
	}

	checkCtx, cancel := context.WithTimeout(ctx, hostCheckTimeout)
	defer cancel()
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, "http://"+probeAddr+"/readyz", nil)
	if err != nil {
		return platform.Result{State: "unknown", Severity: "critical", Message: "The configured serving address could not be probed. Set serving.addr to a local TCP address."}
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(checkCtx.Err(), context.Canceled) || errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
			return platform.Result{State: "unknown", Severity: "critical", Message: "The serving readiness check timed out or was cancelled. Check the API service."}
		}
		return platform.Result{State: "unknown", Severity: "critical", Message: "The serving port is occupied, but its readiness endpoint could not be checked. Verify the API service."}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		return platform.Result{State: "unknown", Severity: "critical", Message: "The serving readiness response could not be read. Verify the API service."}
	}
	var ready struct {
		OK bool `json:"ok"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &ready) != nil || !ready.OK {
		return platform.Result{State: "failing", Severity: "critical", Message: "The serving port is occupied, but /readyz is not reporting a ready DDP API. Check database and migration readiness."}
	}
	return platform.Result{State: "ok", Severity: "critical", Message: "The DDP API is listening and ready on the serving port."}
}

func localAddress(addr string) (host, port, probe string, ok bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "", "", "", false
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", "", "", false
	}
	if host == "" {
		host = "0.0.0.0"
	}
	if strings.EqualFold(host, "localhost") {
		return host, port, net.JoinHostPort(host, port), true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "", "", "", false
	}
	if !ip.IsLoopback() && !ip.IsUnspecified() {
		addresses, err := net.InterfaceAddrs()
		if err != nil {
			return "", "", "", false
		}
		local := false
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err == nil && prefix.Addr().Unmap() == ip.WithZone("").Unmap() {
				local = true
				break
			}
		}
		if !local {
			return "", "", "", false
		}
	}
	probeHost := host
	if ip.IsUnspecified() {
		if ip.Is6() {
			probeHost = "::1"
		} else {
			probeHost = "127.0.0.1"
		}
	}
	return host, port, net.JoinHostPort(probeHost, port), true
}
