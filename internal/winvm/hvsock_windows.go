//go:build windows

package winvm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// An hv_sock (AF_HYPERV) listener on the host for the proof's VMBus steps,
// through ws2_32 directly: golang.org/x/sys has no AF_HYPERV address.

const (
	afHyperV      = 34
	hvProtocolRaw = 1
	gcsKey        = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices`
)

var (
	ws2       = windows.NewLazySystemDLL("ws2_32.dll")
	procBind  = ws2.NewProc("bind")
	procAcc   = ws2.NewProc("accept")
	procRecv  = ws2.NewProc("recv")
	procIoctl = ws2.NewProc("ioctlsocket")
)

// sockaddrHV is SOCKADDR_HV.
type sockaddrHV struct {
	Family    uint16
	Reserved  uint16
	VMID      windows.GUID
	ServiceID windows.GUID
}

type hvListener struct {
	s        windows.Handle
	key      string // the GCS registration this listener made, "" for none
	element  string
	mu       sync.Mutex
	received []string
	once     sync.Once
	readers  sync.WaitGroup
	closeErr error
	serveErr error // published by closing done
	stop     chan struct{}
	done     chan struct{}
}

// openHVListener binds an hv_sock listener to bindVMID (the wildcard takes
// every VM) and service, after registering service under
// GuestCommunicationServices when register is set. Close removes what it
// registered.
func openHVListener(_ context.Context, bindVMID, service string, register bool, element string) (HVListener, error) {
	vm, err := windows.GUIDFromString("{" + bindVMID + "}")
	if err != nil {
		return nil, err
	}
	svc, err := windows.GUIDFromString("{" + service + "}")
	if err != nil {
		return nil, err
	}
	l := &hvListener{done: make(chan struct{}), stop: make(chan struct{})}
	if register {
		name := gcsKey + `\` + strings.ToUpper(service)
		k, existed, err := registry.CreateKey(registry.LOCAL_MACHINE, name, registry.SET_VALUE)
		if err != nil {
			return nil, err
		}
		if existed {
			k.Close()
			return nil, errors.New("the hv_sock test service is already registered")
		}
		err = k.SetStringValue("ElementName", element)
		k.Close()
		if err != nil {
			return nil, err
		}
		l.key, l.element = name, element
	}
	s, err := windows.Socket(afHyperV, windows.SOCK_STREAM, hvProtocolRaw)
	if err != nil {
		l.Unregister()
		return nil, err
	}
	l.s = s
	sa := sockaddrHV{Family: afHyperV, VMID: vm, ServiceID: svc}
	if r, _, e := procBind.Call(uintptr(s), uintptr(unsafe.Pointer(&sa)), unsafe.Sizeof(sa)); r != 0 {
		windows.Closesocket(s)
		l.Unregister()
		return nil, e
	}
	if err := windows.Listen(s, 4); err != nil {
		windows.Closesocket(s)
		l.Unregister()
		return nil, err
	}
	if err := hvNonblocking(s, 1); err != nil {
		windows.Closesocket(s)
		l.Unregister()
		return nil, err
	}
	go l.serve()
	return l, nil
}

func hvNonblocking(s windows.Handle, enable uint32) error {
	const fionbio = 0x8004667e
	if r, _, err := procIoctl.Call(uintptr(s), fionbio, uintptr(unsafe.Pointer(&enable))); r != 0 {
		return err
	}
	return nil
}

func (l *hvListener) serve() {
	defer close(l.done)
	l.serveErr = hvAcceptLoop(l.stop, l.poll, func() error { return windows.Closesocket(l.s) })
	l.readers.Wait()
}

func (l *hvListener) poll() (bool, error) {
	var sa sockaddrHV
	n := int32(unsafe.Sizeof(sa))
	r, _, err := procAcc.Call(uintptr(l.s), uintptr(unsafe.Pointer(&sa)), uintptr(unsafe.Pointer(&n)))
	c := windows.Handle(r)
	if c == windows.InvalidHandle {
		if errors.Is(err, windows.WSAEWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	l.mu.Lock()
	slot := len(l.received)
	l.received = append(l.received, "connected")
	l.mu.Unlock()
	if hvNonblocking(c, 0) != nil || windows.SetsockoptInt(c, windows.SOL_SOCKET, windows.SO_RCVTIMEO, 1000) != nil {
		windows.Closesocket(c)
		return true, nil
	}
	l.readers.Go(func() {
		defer windows.Closesocket(c)
		buf := make([]byte, tokenRead)
		got, _, _ := procRecv.Call(uintptr(c), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
		tok := "connected"
		if int32(got) > 0 {
			if t := strings.TrimSpace(string(buf[:int32(got)])); canaryToken.MatchString(t) {
				tok = t
			}
		}
		l.mu.Lock()
		l.received[slot] = tok
		l.mu.Unlock()
	})
	return true, nil
}

func (l *hvListener) Received() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.received...)
}

// Unregister deletes the registration and leaves the listener bound.
func (l *hvListener) Unregister() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.key == "" {
		return nil
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, l.key, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		l.key = ""
		return nil
	}
	if err != nil {
		return err
	}
	element, _, err := k.GetStringValue("ElementName")
	k.Close()
	if err != nil || element != l.element {
		return errors.New("the hv_sock registration no longer has this proof's owner tag")
	}
	if e := registry.DeleteKey(registry.LOCAL_MACHINE, l.key); e != nil && !errors.Is(e, registry.ErrNotExist) {
		return e
	}
	l.key = ""
	return nil
}

func (l *hvListener) Close() error {
	l.once.Do(func() {
		close(l.stop)
		l.closeErr = waitHVShutdown(l.done, 2*time.Second)
		if l.closeErr == nil {
			l.closeErr = l.serveErr
		}
	})
	return errors.Join(l.closeErr, l.Unregister())
}
