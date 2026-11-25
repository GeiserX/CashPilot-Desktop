//go:build windows

package winvm

import (
	"context"
	"errors"
	"strings"
	"sync"
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
	ws2      = windows.NewLazySystemDLL("ws2_32.dll")
	procBind = ws2.NewProc("bind")
	procAcc  = ws2.NewProc("accept")
	procRecv = ws2.NewProc("recv")
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
	mu       sync.Mutex
	received []string
	once     sync.Once
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
	l := &hvListener{done: make(chan struct{})}
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
		l.key = name
		if err != nil {
			l.unregister()
			return nil, err
		}
	}
	s, err := windows.Socket(afHyperV, windows.SOCK_STREAM, hvProtocolRaw)
	if err != nil {
		l.unregister()
		return nil, err
	}
	l.s = s
	sa := sockaddrHV{Family: afHyperV, VMID: vm, ServiceID: svc}
	if r, _, e := procBind.Call(uintptr(s), uintptr(unsafe.Pointer(&sa)), unsafe.Sizeof(sa)); r != 0 {
		windows.Closesocket(s)
		l.unregister()
		return nil, e
	}
	if err := windows.Listen(s, 4); err != nil {
		windows.Closesocket(s)
		l.unregister()
		return nil, err
	}
	go l.serve()
	return l, nil
}

func (l *hvListener) unregister() {
	if l.key != "" {
		registry.DeleteKey(registry.LOCAL_MACHINE, l.key)
	}
}

func (l *hvListener) serve() {
	defer close(l.done)
	for {
		var sa sockaddrHV
		n := int32(unsafe.Sizeof(sa))
		r, _, _ := procAcc.Call(uintptr(l.s), uintptr(unsafe.Pointer(&sa)), uintptr(unsafe.Pointer(&n)))
		c := windows.Handle(r)
		if c == windows.InvalidHandle {
			return
		}
		go func() {
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
			l.received = append(l.received, tok)
			l.mu.Unlock()
		}()
	}
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
	if e := registry.DeleteKey(registry.LOCAL_MACHINE, l.key); e != nil && !errors.Is(e, registry.ErrNotExist) {
		return e
	}
	l.key = ""
	return nil
}

func (l *hvListener) Close() error {
	var err error
	l.once.Do(func() {
		err = windows.Closesocket(l.s)
		<-l.done
		err = errors.Join(err, l.Unregister())
	})
	return err
}
