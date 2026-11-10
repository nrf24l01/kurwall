package dns

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

func buildQuery(t *testing.T, name string, id uint16) []byte {
	t.Helper()
	question := []byte{}
	for _, label := range splitName(name) {
		question = append(question, byte(len(label)))
		question = append(question, label...)
	}
	question = append(question, 0x00, 0x00, 0x01, 0x00, 0x01)

	payload := make([]byte, 12)
	binary.BigEndian.PutUint16(payload[0:2], id)
	binary.BigEndian.PutUint16(payload[2:4], 0x0100)
	binary.BigEndian.PutUint16(payload[4:6], 1)
	payload = append(payload, question...)

	ip := &layers.IPv4{
		Version: 4, IHL: 5, TTL: 64, Protocol: layers.IPProtocolUDP,
		SrcIP: net.ParseIP("10.0.0.2").To4(), DstIP: net.ParseIP("8.8.8.8").To4(),
	}
	udp := &layers.UDP{SrcPort: 40000, DstPort: 53}
	udp.SetNetworkLayerForChecksum(ip)
	buf := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
		ip, udp, gopacket.Payload(payload)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func splitName(name string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			out = append(out, name[start:i])
			start = i + 1
		}
	}
	return out
}

func TestParseAndNXDomain(t *testing.T) {
	raw := buildQuery(t, "example.com", 0x1234)
	dp, msg, err := parsePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !msg.query || msg.qname != "example.com" || msg.qtype != 1 || msg.id != 0x1234 {
		t.Fatalf("unexpected query: %+v", msg)
	}
	if dp.srcAddr.String() != "10.0.0.2" || dp.dstAddr.String() != "8.8.8.8" {
		t.Fatalf("unexpected addresses: %v -> %v", dp.srcAddr, dp.dstAddr)
	}

	response, err := buildNXDomain(dp)
	if err != nil {
		t.Fatal(err)
	}
	rp, rmsg, err := parsePacket(response)
	if err != nil {
		t.Fatal(err)
	}
	if rmsg.query {
		t.Fatal("expected a response")
	}
	if rmsg.rcode != 3 || rmsg.qname != "example.com" || rmsg.id != 0x1234 {
		t.Fatalf("unexpected response: %+v", rmsg)
	}
	if rp.srcAddr.String() != "8.8.8.8" || rp.dstAddr.String() != "10.0.0.2" {
		t.Fatalf("addresses were not swapped: %v -> %v", rp.srcAddr, rp.dstAddr)
	}
	if rp.srcPort != 53 || rp.dstPort != 40000 {
		t.Fatalf("ports were not swapped: %d -> %d", rp.srcPort, rp.dstPort)
	}
}

func TestBuildNXDomainTruncated(t *testing.T) {
	raw := buildQuery(t, "example.com", 1)
	dp, _, err := parsePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	dp.payload = dp.payload[:10]
	if _, err := buildNXDomain(dp); err == nil {
		t.Fatal("expected an error for a truncated payload")
	}
}
