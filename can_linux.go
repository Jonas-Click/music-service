//go:build linux

package main

// A hand-rolled SocketCAN (PF_CAN/CAN_RAW) binding, using raw syscalls
// instead of cgo so cross-compiling for the MDB's ARM target from any host
// stays a plain `go build` — same zero-dependency posture as resp.go's
// hand-written RESP client.
//
// Struct layouts below mirror <linux/can.h> exactly:
//
//	struct can_frame {
//	    canid_t can_id;   // 32-bit ID, EFF/RTR/ERR flags in the top bits
//	    __u8    can_dlc;
//	    __u8    __pad, __res0, __res1;
//	    __u8    data[8] __attribute__((aligned(8)));
//	};  // 16 bytes total — data already falls on an 8-byte boundary, so Go's
//	    // struct layout for the fields below needs no explicit padding.
//
//	struct sockaddr_can {
//	    sa_family_t can_family; // 2 bytes
//	    int         can_ifindex;
//	    union { ... } can_addr; // unused for CAN_RAW; zeroed
//	};
import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

const sendQueueFullTimeout = 500 * time.Millisecond

const (
	afCAN  = 29 // AF_CAN / PF_CAN — not exported by the syscall package
	canRaw = 1  // CAN_RAW protocol

	sizeofSockaddrCAN = 16 // family(2) + pad(2) + ifindex(4) + addr union(8), see sockaddrCAN below
	sizeofCANFrame    = 16

	ifNameSize   = 16
	sizeofIfreq  = 40 // struct ifreq on 64-bit Linux; 32-bit only needs the first 20 bytes we touch
	siocgifindex = 0x8933

	canEFFFlag = 0x80000000
	canRTRFlag = 0x40000000
	canErrFlag = 0x20000000
	canSFFMask = 0x000007FF
	canEFFMask = 0x1FFFFFFF
)

type sockaddrCAN struct {
	Family  uint16
	_       uint16 // alignment padding before the int32 ifindex
	Ifindex int32
	_       [8]byte // can_addr union, unused for CAN_RAW
}

type canFrameWire struct {
	ID   uint32
	DLC  uint8
	_    uint8
	_    uint8
	_    uint8
	Data [8]byte
}

type socketCAN struct {
	fd int
}

func openSocketCAN(iface string) (*socketCAN, error) {
	fd, err := syscall.Socket(afCAN, syscall.SOCK_RAW, canRaw)
	if err != nil {
		return nil, fmt.Errorf("socket(AF_CAN): %w", err)
	}

	idx, err := ifIndex(fd, iface)
	if err != nil {
		syscall.Close(fd)
		return nil, err
	}

	addr := sockaddrCAN{Family: afCAN, Ifindex: idx}
	_, _, errno := syscall.Syscall(syscall.SYS_BIND, uintptr(fd),
		uintptr(unsafe.Pointer(&addr)), uintptr(sizeofSockaddrCAN))
	if errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("bind(%s): %w", iface, errno)
	}

	return &socketCAN{fd: fd}, nil
}

func ifIndex(fd int, iface string) (int32, error) {
	if len(iface) >= ifNameSize {
		return 0, fmt.Errorf("interface name %q too long", iface)
	}
	var req [sizeofIfreq]byte
	copy(req[:ifNameSize], iface)

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		uintptr(siocgifindex), uintptr(unsafe.Pointer(&req[0])))
	if errno != 0 {
		return 0, fmt.Errorf("SIOCGIFINDEX(%s): %w — is the interface up?", iface, errno)
	}
	idx := *(*int32)(unsafe.Pointer(&req[ifNameSize]))
	return idx, nil
}

func (s *socketCAN) send(id uint32, data []byte) error {
	if len(data) > 8 {
		return fmt.Errorf("CAN payload of %d bytes exceeds 8", len(data))
	}
	var frame canFrameWire
	frame.ID = id & canSFFMask // standard 11-bit frame — matches every ID music.sh/app.lisp use
	frame.DLC = uint8(len(data))
	copy(frame.Data[:], data)

	buf := (*[sizeofCANFrame]byte)(unsafe.Pointer(&frame))[:]
	// ENOBUFS means the interface's transmit queue (qlen 10 on the MDB, at
	// 125 kbit/s) is full, not that the bus is broken: wait for it to drain.
	var n int
	var err error
	for wait := time.Duration(0); ; wait += time.Millisecond {
		n, err = syscall.Write(s.fd, buf)
		if err != syscall.ENOBUFS || wait >= sendQueueFullTimeout {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if n != sizeofCANFrame {
		return fmt.Errorf("short write: %d of %d bytes", n, sizeofCANFrame)
	}
	return nil
}

func (s *socketCAN) recv() (uint32, []byte, error) {
	var buf [sizeofCANFrame]byte
	n, err := syscall.Read(s.fd, buf[:])
	if err != nil {
		return 0, nil, fmt.Errorf("read: %w", err)
	}
	if n != sizeofCANFrame {
		return 0, nil, fmt.Errorf("short read: %d of %d bytes", n, sizeofCANFrame)
	}
	frame := (*canFrameWire)(unsafe.Pointer(&buf[0]))

	rawID := frame.ID
	if rawID&canErrFlag != 0 {
		return 0, nil, fmt.Errorf("CAN error frame: 0x%X", rawID)
	}
	var id uint32
	if rawID&canEFFFlag != 0 {
		id = rawID & canEFFMask
	} else {
		id = rawID & canSFFMask
	}
	dlc := int(frame.DLC)
	if dlc > 8 {
		dlc = 8
	}
	data := make([]byte, dlc)
	copy(data, frame.Data[:dlc])
	return id, data, nil
}

func (s *socketCAN) describe() string { return "socketcan" }

func (s *socketCAN) close() { syscall.Close(s.fd) }
