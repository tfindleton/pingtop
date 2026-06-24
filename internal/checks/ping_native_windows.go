//go:build windows

package checks

import (
	"encoding/binary"
	"net"
	"syscall"
	"unsafe"
)

const (
	ipSuccess     = 0
	ipReqTimedOut = 11010
	invalidHandle = ^uintptr(0)
)

type ipOptionInformation struct {
	TTL         byte
	TOS         byte
	Flags       byte
	OptionsSize byte
	OptionsData uintptr
}

type icmpEchoReply struct {
	Address       uint32
	Status        uint32
	RoundTripTime uint32
	DataSize      uint16
	Reserved      uint16
	Data          uintptr
	Options       ipOptionInformation
}

var (
	modIphlpapi      = syscall.NewLazyDLL("iphlpapi.dll")
	procIcmpCreate   = modIphlpapi.NewProc("IcmpCreateFile")
	procIcmpClose    = modIphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho = modIphlpapi.NewProc("IcmpSendEcho")
)

func nativePing(ipAddress string, timeoutMS int) (bool, *float64, string, string, bool) {
	parsedIP := net.ParseIP(ipAddress)
	if parsedIP == nil {
		return false, nil, "", "", false
	}

	ipv4 := parsedIP.To4()
	if ipv4 == nil {
		return false, nil, "", "", false
	}

	handle, err := openICMPHandle()
	if err != nil {
		return false, nil, "", "", false
	}
	defer closeICMPHandle(handle)

	requestData := []byte("pingtop")
	replyBuffer := make([]byte, int(unsafe.Sizeof(icmpEchoReply{}))+len(requestData)+8)
	address := binary.LittleEndian.Uint32(ipv4)

	result, _, callErr := procIcmpSendEcho.Call(
		handle,
		uintptr(address),
		uintptr(unsafe.Pointer(&requestData[0])),
		uintptr(len(requestData)),
		0,
		uintptr(unsafe.Pointer(&replyBuffer[0])),
		uintptr(len(replyBuffer)),
		uintptr(timeoutMS),
	)
	if result == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno != 0 {
			return false, nil, "", "", false
		}
		return false, nil, "", "", false
	}

	reply := (*icmpEchoReply)(unsafe.Pointer(&replyBuffer[0]))
	switch reply.Status {
	case ipSuccess:
		latency := float64(reply.RoundTripTime)
		return true, &latency, "ok", "", true
	case ipReqTimedOut:
		return false, nil, "timeout", "Request timed out", true
	default:
		return false, nil, "ping_failure", icmpStatusText(reply.Status), true
	}
}

func openICMPHandle() (uintptr, error) {
	if err := modIphlpapi.Load(); err != nil {
		return 0, err
	}
	handle, _, err := procIcmpCreate.Call()
	if handle == 0 || handle == invalidHandle {
		if errno, ok := err.(syscall.Errno); ok && errno != 0 {
			return 0, errno
		}
		return 0, syscall.EINVAL
	}
	return handle, nil
}

func closeICMPHandle(handle uintptr) {
	if handle == 0 || handle == invalidHandle {
		return
	}
	_, _, _ = procIcmpClose.Call(handle)
}

func icmpStatusText(status uint32) string {
	switch status {
	case 11002:
		return "Destination network unreachable"
	case 11003:
		return "Destination host unreachable"
	case 11004:
		return "Destination protocol unreachable"
	case 11005:
		return "Destination port unreachable"
	case 11013:
		return "TTL expired in transit"
	default:
		return "ICMP status " + strconvUint32(status)
	}
}

func strconvUint32(value uint32) string {
	if value == 0 {
		return "0"
	}
	buffer := [10]byte{}
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + (value % 10))
		value /= 10
	}
	return string(buffer[index:])
}
