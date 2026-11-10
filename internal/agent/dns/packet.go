package dns

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

type dnsPacket struct {
	version  uint8
	src, dst net.IP
	srcAddr  netip.Addr
	dstAddr  netip.Addr
	srcPort  uint16
	dstPort  uint16
	proto    uint8
	payload  []byte // DNS message without TCP length prefix
	raw      []byte
}

type dnsMessage struct {
	id         uint16
	query      bool
	qname      string
	qtype      uint16
	rcode      uint8
	answers    []netip.Addr
	ttls       []uint32
	clientIP   netip.Addr
	clientPort uint16
}

var errMalformed = errors.New("malformed dns packet")

func parsePacket(raw []byte) (*dnsPacket, *dnsMessage, error) {
	if len(raw) < 1 {
		return nil, nil, errMalformed
	}
	var first gopacket.LayerType
	switch raw[0] >> 4 {
	case 4:
		first = layers.LayerTypeIPv4
	case 6:
		first = layers.LayerTypeIPv6
	default:
		return nil, nil, errMalformed
	}
	packet := gopacket.NewPacket(raw, first, gopacket.DecodeOptions{Lazy: false, NoCopy: true})
	dp := &dnsPacket{raw: raw}
	var network gopacket.NetworkLayer
	if v4, ok := packet.NetworkLayer().(*layers.IPv4); ok {
		dp.version = 4
		dp.src = v4.SrcIP
		dp.dst = v4.DstIP
		network = v4
	} else if v6, ok := packet.NetworkLayer().(*layers.IPv6); ok {
		dp.version = 6
		dp.src = v6.SrcIP
		dp.dst = v6.DstIP
		network = v6
	} else {
		return nil, nil, errMalformed
	}
	srcAddr, ok := netip.AddrFromSlice(dp.src)
	if !ok {
		return nil, nil, errMalformed
	}
	dstAddr, ok := netip.AddrFromSlice(dp.dst)
	if !ok {
		return nil, nil, errMalformed
	}
	dp.srcAddr, dp.dstAddr = srcAddr.Unmap(), dstAddr.Unmap()
	_ = network

	if udp, ok := packet.TransportLayer().(*layers.UDP); ok {
		dp.proto = 17
		dp.srcPort = uint16(udp.SrcPort)
		dp.dstPort = uint16(udp.DstPort)
		dp.payload = udp.Payload
	} else if tcp, ok := packet.TransportLayer().(*layers.TCP); ok {
		dp.proto = 6
		dp.srcPort = uint16(tcp.SrcPort)
		dp.dstPort = uint16(tcp.DstPort)
		payload := tcp.Payload
		if len(payload) < 2 {
			return nil, nil, errMalformed
		}
		length := int(binary.BigEndian.Uint16(payload[:2]))
		if length > len(payload)-2 {
			length = len(payload) - 2
		}
		dp.payload = payload[2 : 2+length]
	} else {
		return nil, nil, errMalformed
	}
	msg, err := parseMessage(dp.payload)
	if err != nil {
		return nil, nil, err
	}
	return dp, msg, nil
}

func parseMessage(payload []byte) (*dnsMessage, error) {
	if len(payload) < 12 {
		return nil, errMalformed
	}
	msg := &dnsMessage{
		id:    binary.BigEndian.Uint16(payload[0:2]),
		query: payload[2]&0x80 == 0,
		rcode: payload[3] & 0x0f,
	}
	dns := &layers.DNS{}
	if err := dns.DecodeFromBytes(payload, gopacket.NilDecodeFeedback); err != nil {
		return nil, err
	}
	if len(dns.Questions) == 0 {
		return nil, errMalformed
	}
	msg.qname = normalizeName(string(dns.Questions[0].Name))
	msg.qtype = uint16(dns.Questions[0].Type)
	for _, answer := range dns.Answers {
		switch answer.Type {
		case layers.DNSTypeA, layers.DNSTypeAAAA:
			if addr, ok := netip.AddrFromSlice(answer.IP); ok {
				msg.answers = append(msg.answers, addr.Unmap())
				msg.ttls = append(msg.ttls, answer.TTL)
			}
		}
	}
	return msg, nil
}

func normalizeName(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == 0 || c == '.' {
			out = append(out, '.')
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	for len(out) > 0 && out[len(out)-1] == '.' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// buildNXDomain replaces a DNS query with an authoritative-looking NXDOMAIN
// response, preserving the question section.
func buildNXDomain(dp *dnsPacket) ([]byte, error) {
	payload := dp.payload
	if len(payload) < 12 {
		return nil, errMalformed
	}
	qd := binary.BigEndian.Uint16(payload[4:6])
	off := 12
	for i := 0; i < int(qd); i++ {
		next, err := skipName(payload, off)
		if err != nil {
			return nil, err
		}
		off = next + 4
		if off > len(payload) {
			return nil, errMalformed
		}
	}
	resp := make([]byte, off)
	copy(resp, payload[:off])
	flags := binary.BigEndian.Uint16(payload[2:4])
	flags = (flags & 0x0100) | 0x8000 | 0x0080 | 3 // RD preserved, QR, RA, NXDOMAIN
	binary.BigEndian.PutUint16(resp[2:4], flags)
	binary.BigEndian.PutUint16(resp[4:6], qd)
	binary.BigEndian.PutUint16(resp[6:8], 0)
	binary.BigEndian.PutUint16(resp[8:10], 0)
	binary.BigEndian.PutUint16(resp[10:12], 0)

	udp := &layers.UDP{
		SrcPort: layers.UDPPort(dp.dstPort),
		DstPort: layers.UDPPort(dp.srcPort),
	}
	var network gopacket.NetworkLayer
	var ipLayer gopacket.SerializableLayer
	if dp.version == 4 {
		ip := &layers.IPv4{
			Version: 4, IHL: 5, TTL: 64, Protocol: layers.IPProtocolUDP,
			SrcIP: dp.dst, DstIP: dp.src,
		}
		network = ip
		ipLayer = ip
	} else {
		ip := &layers.IPv6{
			Version: 6, HopLimit: 64, NextHeader: layers.IPProtocolUDP,
			SrcIP: dp.dst, DstIP: dp.src,
		}
		network = ip
		ipLayer = ip
	}
	udp.SetNetworkLayerForChecksum(network)
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := gopacket.SerializeLayers(buf, opts, ipLayer, udp, gopacket.Payload(resp)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func skipName(msg []byte, off int) (int, error) {
	for off < len(msg) {
		length := int(msg[off])
		if length == 0 {
			return off + 1, nil
		}
		if length&0xc0 == 0xc0 {
			if off+2 > len(msg) {
				return 0, errMalformed
			}
			return off + 2, nil
		}
		if length&0xc0 != 0 {
			return 0, errMalformed
		}
		off += 1 + length
	}
	return 0, errMalformed
}
