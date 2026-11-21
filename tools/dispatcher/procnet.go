package main

import (
	"bufio"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
)

// listenPorts maps every TCP port in LISTEN state to whether it is reachable
// from outside loopback (bound to anything but 127.0.0.0/8 or ::1).
func listenPorts() (map[int]bool, error) {
	out := map[int]bool{}
	readAny := false
	for _, p := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		readAny = true
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 4 || fields[3] != "0A" {
				continue
			}
			addr, portHex, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			port, err := strconv.ParseUint(portHex, 16, 16)
			if err != nil {
				continue
			}
			ip := parseProcIP(addr)
			if ip == nil {
				continue
			}
			out[int(port)] = out[int(port)] || !ip.IsLoopback()
		}
		f.Close()
	}
	if !readAny {
		return nil, errors.New("cannot read /proc/net/tcp")
	}
	return out, nil
}

// parseProcIP decodes /proc/net/tcp addresses: each 32-bit word is printed in host (little-endian) order.
func parseProcIP(s string) net.IP {
	b, err := hex.DecodeString(s)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return nil
	}
	for i := 0; i+4 <= len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return net.IP(b)
}

// containerIP is the IPv4 address of the interface carrying the default route.
func containerIP() (string, error) {
	if data, err := os.ReadFile("/proc/net/route"); err == nil {
		for _, line := range strings.Split(string(data), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) < 2 || f[1] != "00000000" {
				continue
			}
			if iface, err := net.InterfaceByName(f[0]); err == nil {
				if ip := ifaceIPv4(iface); ip != "" {
					return ip, nil
				}
			}
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if ip := ifaceIPv4(&iface); ip != "" {
			return ip, nil
		}
	}
	return "", errors.New("no container IPv4 address found")
}

func ifaceIPv4(iface *net.Interface) string {
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	return ""
}
