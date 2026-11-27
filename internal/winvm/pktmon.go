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

// pktComp is one pktmon component. Name is for messages only: pktmon list
// and the ETL describe the same component with different names (live run 5).
type pktComp struct {
	ID      int
	Name    string
	Driver  string
	IfIndex int
	Type    int
	// Twin is a NIC's NDIS view under its SecondaryId. The trace can name
	// it (setup 1i), but every packet it sees also passes the NIC's own
	// entry, so the proof neither requires nor exports it.
	Twin bool
}

// maxPktmonList caps the component list.
const maxPktmonList = 1 << 20

// pktmonNICType uses the types in the PktMon ETW component event. pktmon list
// names them in words: "Host vNic", "Extension Miniport", "NetVsc" (live run
// 5), beside the names in the ETW manifest.
func pktmonNICType(v any) int {
	if n, ok := jsonInt(v); ok {
		return n
	}
	s, _ := v.(string)
	switch strings.ToLower(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(s)) {
	case "miniport":
		return 2
	case "vmsvmnic", "vmnic":
		return 5
	case "vmsminiport", "hostvnic":
		return 6
	case "vmsextminiport", "extensionminiport":
		return 7
	case "vmsprotocolnic":
		return 8
	case "netvscminiport", "netvsc":
		return 9
	}
	return 0
}

func (c pktComp) isNIC() bool {
	return c.Type == 2 || (c.Type >= 5 && c.Type <= 9)
}

// parsePktmonComponents reads the NICs in pktmon list --json.
func parsePktmonComponents(b []byte) (map[int]pktComp, error) {
	if len(b) > maxPktmonList {
		return nil, fmt.Errorf("the pktmon component list is %d bytes, over the cap", len(b))
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("could not read the pktmon component list: %w", err)
	}
	out := map[int]pktComp{}
	// pktmon list gives a component a second entry, its NDIS view: type
	// Miniport, with a SecondaryId. The ETL describes the first entry, so a
	// twin counts only for a component that has no other entry.
	twin := map[int]bool{}
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
			hasID, secondary, secondaryID := false, false, 0
			for k, e := range x {
				switch strings.ToLower(k) {
				case "id":
					if n, ok := jsonInt(e); ok {
						c.ID, hasID = n, true
					}
				case "type", "componenttype":
					c.Type = pktmonNICType(e)
				case "name":
					c.Name, _ = e.(string)
				case "drivername":
					c.Driver, _ = e.(string)
				case "secondaryid":
					secondary = true
					secondaryID, _ = jsonInt(e)
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
			if hasID && c.Name != "" && c.ID > 0 && c.isNIC() {
				if prev, seen := out[c.ID]; !seen || prev.Twin || (twin[c.ID] && !secondary) {
					out[c.ID] = c
					twin[c.ID] = secondary
				}
				// A NIC's own entry, or another NIC, always wins over a twin.
				if secondary && secondaryID > 0 && secondaryID != c.ID {
					if _, seen := out[secondaryID]; !seen {
						out[secondaryID] = pktComp{ID: secondaryID, Name: c.Name, Driver: c.Driver, IfIndex: c.IfIndex, Type: 2, Twin: true}
					}
				}
			}
			for _, e := range x {
				walk(e, depth+1)
			}
		}
	}
	walk(doc, 0)
	if len(out) == 0 {
		return nil, errors.New("the pktmon component list names no NIC")
	}
	return out, nil
}

// reconcilePktmon checks the ETL's own component metadata before any packet
// from an export can be used as evidence.
func reconcilePktmon(want map[int]pktComp, actual []pktComp) error {
	seen := make(map[int]bool, len(want))
	for _, got := range actual {
		if !got.isNIC() {
			continue
		}
		expected, ok := want[got.ID]
		if !ok || got.Type != expected.Type || !strings.EqualFold(got.Driver, expected.Driver) || got.IfIndex != expected.IfIndex {
			return fmt.Errorf("pktmon trace NIC %d (%s, type %d, %s, interface %d) differs from the pre-capture inventory (%s, type %d, %s, interface %d)",
				got.ID, got.Name, got.Type, got.Driver, got.IfIndex, expected.Name, expected.Type, expected.Driver, expected.IfIndex)
		}
		seen[got.ID] = true
	}
	for id := range want {
		if !seen[id] && !want[id].Twin {
			return fmt.Errorf("pktmon trace is missing NIC %d (%s)", id, want[id].Name)
		}
	}
	return nil
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
// in either byte order. It skips other packet types when it reads addresses.
// Every export must show packet data: headers alone cannot prove that a quiet
// component was captured completely.
func parsePcapng(b []byte) ([]pktInfo, error) {
	if len(b) == 0 {
		return nil, errors.New("empty capture export")
	}
	if len(b) < 28 || binary.LittleEndian.Uint32(b) != pcapSHB {
		return nil, errors.New("missing capture section header")
	}
	var out []pktInfo
	var bo binary.ByteOrder = binary.LittleEndian
	var links []uint16
	hasInterface, hasPacketData := false, false
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
		if bo.Uint32(b[ln-4:]) != uint32(ln) || (typ == pcapSHB && ln < 28) {
			return nil, errors.New("the capture has an invalid block length")
		}
		body := b[8 : ln-4]
		switch typ {
		case pcapIDB:
			if len(body) < 8 {
				return nil, errors.New("the capture has a broken interface block")
			}
			link := bo.Uint16(body)
			if link != linkEthernet && link != linkRaw {
				return nil, fmt.Errorf("unsupported capture interface link type %d", link)
			}
			if bo.Uint16(body[2:]) != 0 {
				return nil, errors.New("invalid capture interface header")
			}
			hasInterface = true
			links = append(links, link)
		case pcapEPB:
			if len(body) < 20 {
				return nil, errors.New("the capture has a broken packet block")
			}
			ifc, capLen := int(bo.Uint32(body)), int(bo.Uint32(body[12:]))
			if ifc >= len(links) || capLen > len(body)-20 {
				return nil, errors.New("the capture has a packet with no interface or a bad length")
			}
			hasPacketData = hasPacketData || capLen > 0
			if p, ok := parseFrame(links[ifc], body[20:20+capLen]); ok {
				out = append(out, p)
			}
		case pcapSPB:
			if len(body) < 4 || len(links) == 0 {
				return nil, errors.New("the capture has a broken simple packet block")
			}
			hasPacketData = hasPacketData || (bo.Uint32(body) > 0 && len(body) > 4)
			if p, ok := parseFrame(links[0], body[4:]); ok {
				out = append(out, p)
			}
		}
		b = b[ln:]
	}
	if !hasInterface {
		return nil, errors.New("capture has no interface evidence")
	}
	if !hasPacketData {
		return nil, errors.New("capture has no packet data to verify the export")
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
