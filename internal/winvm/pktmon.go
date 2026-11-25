package winvm

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Reading pktmon's output without its text, which Windows translates: the
// component list as JSON, and each component's packets as pcapng.

// pktComp is one pktmon component.
type pktComp struct {
	ID      int
	Name    string
	IfIndex int
}

// maxPktmonList caps the component list.
const maxPktmonList = 1 << 20

// parsePktmonComponents reads `pktmon list --json`. It does not rely on the
// document's nesting: any object with a numeric Id and a Name is a component,
// and its ifIndex is a field or a Properties entry whose name is ifIndex,
// compared without case. A component with no ifIndex keeps 0, so its class is
// unknown and a green step that sees a probe there fails closed.
func parsePktmonComponents(b []byte) (map[int]pktComp, error) {
	if len(b) > maxPktmonList {
		return nil, fmt.Errorf("the pktmon component list is %d bytes, over the cap", len(b))
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("could not read the pktmon component list: %w", err)
	}
	out := map[int]pktComp{}
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		if depth > 16 {
			return
		}
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e, depth+1)
			}
		case map[string]any:
			var c pktComp
			hasID := false
			for k, e := range x {
				switch strings.ToLower(k) {
				case "id":
					if n, ok := jsonInt(e); ok {
						c.ID, hasID = n, true
					}
				case "name":
					c.Name, _ = e.(string)
				case "ifindex":
					c.IfIndex, _ = jsonInt(e)
				case "properties":
					if ps, ok := e.([]any); ok {
						for _, p := range ps {
							m, ok := p.(map[string]any)
							if !ok {
								continue
							}
							name, _ := m["Name"].(string)
							if strings.EqualFold(name, "ifindex") {
								c.IfIndex, _ = jsonInt(m["Value"])
							}
						}
					}
				}
			}
			if hasID && c.Name != "" && c.ID > 0 {
				out[c.ID] = c
			}
			for _, e := range x {
				walk(e, depth+1)
			}
		}
	}
	walk(doc, 0)
	if len(out) == 0 {
		return nil, errors.New("the pktmon component list names no component")
	}
	return out, nil
}

func jsonInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		if x == float64(int(x)) {
			return int(x), true
		}
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	}
	return 0, false
}

// pktInfo is the part of one packet the proof reads.
type pktInfo struct {
	Src, Dst netip.Addr
	Proto    string
	Port     uint16
}

// pcapng block types and link types.
const (
	pcapSHB       = 0x0A0D0D0A
	pcapIDB       = 0x00000001
	pcapSPB       = 0x00000003
	pcapEPB       = 0x00000006
	linkEthernet  = 1
	linkRaw       = 101
	maxPcapBlocks = 1 << 20
)

// parsePcapng reads the IPv4 and IPv6 TCP and UDP packets of a pcapng file,
// in either byte order. It skips any other packet and refuses a broken file.
func parsePcapng(b []byte) ([]pktInfo, error) {
	var out []pktInfo
	var bo binary.ByteOrder = binary.LittleEndian
	var links []uint16
	for n := 0; len(b) > 0; n++ {
		if n > maxPcapBlocks || len(b) < 12 {
			return nil, errors.New("the capture is cut short")
		}
		if binary.LittleEndian.Uint32(b) == pcapSHB {
			switch binary.LittleEndian.Uint32(b[8:]) {
			case 0x1A2B3C4D:
				bo = binary.LittleEndian
			case 0x4D3C2B1A:
				bo = binary.BigEndian
			default:
				return nil, errors.New("the capture has an unknown byte order")
			}
			links = nil
		}
		typ, ln := bo.Uint32(b), int(bo.Uint32(b[4:]))
		if ln < 12 || ln%4 != 0 || ln > len(b) {
			return nil, errors.New("the capture has a broken block")
		}
		body := b[8 : ln-4]
		switch typ {
		case pcapIDB:
			if len(body) < 8 {
				return nil, errors.New("the capture has a broken interface block")
			}
			links = append(links, bo.Uint16(body))
		case pcapEPB:
			if len(body) < 20 {
				return nil, errors.New("the capture has a broken packet block")
			}
			ifc, capLen := int(bo.Uint32(body)), int(bo.Uint32(body[12:]))
			if ifc >= len(links) || capLen > len(body)-20 {
				return nil, errors.New("the capture has a packet with no interface or a bad length")
			}
			if p, ok := parseFrame(links[ifc], body[20:20+capLen]); ok {
				out = append(out, p)
			}
		case pcapSPB:
			if len(body) < 4 || len(links) == 0 {
				return nil, errors.New("the capture has a broken simple packet block")
			}
			if p, ok := parseFrame(links[0], body[4:]); ok {
				out = append(out, p)
			}
		}
		b = b[ln:]
	}
	return out, nil
}

// parseFrame reads one Ethernet or raw IP frame.
func parseFrame(link uint16, f []byte) (pktInfo, bool) {
	switch link {
	case linkEthernet:
		if len(f) < 14 {
			return pktInfo{}, false
		}
		et, off := binary.BigEndian.Uint16(f[12:]), 14
		for et == 0x8100 || et == 0x88A8 {
			if len(f) < off+4 {
				return pktInfo{}, false
			}
			et, off = binary.BigEndian.Uint16(f[off+2:]), off+4
		}
		switch et {
		case 0x0800, 0x86DD:
			return parseIP(f[off:])
		}
		return pktInfo{}, false
	case linkRaw:
		return parseIP(f)
	}
	return pktInfo{}, false
}

// parseIP reads the addresses, the protocol and the destination port.
func parseIP(p []byte) (pktInfo, bool) {
	if len(p) < 1 {
		return pktInfo{}, false
	}
	var k pktInfo
	var proto byte
	var l4 []byte
	switch p[0] >> 4 {
	case 4:
		ihl := int(p[0]&0x0f) * 4
		if ihl < 20 || len(p) < ihl {
			return pktInfo{}, false
		}
		k.Src, k.Dst = netip.AddrFrom4([4]byte(p[12:16])), netip.AddrFrom4([4]byte(p[16:20]))
		proto, l4 = p[9], p[ihl:]
	case 6:
		if len(p) < 40 {
			return pktInfo{}, false
		}
		k.Src, k.Dst = netip.AddrFrom16([16]byte(p[8:24])), netip.AddrFrom16([16]byte(p[24:40]))
		proto, l4 = p[6], p[40:]
	default:
		return pktInfo{}, false
	}
	switch proto {
	case 6:
		k.Proto = "TCP"
	case 17:
		k.Proto = "UDP"
	default:
		return k, true
	}
	if len(l4) >= 4 {
		k.Port = binary.BigEndian.Uint16(l4[2:])
	}
	return k, true
}
