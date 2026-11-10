package tlsinspect

// Status describes how far a TLS ClientHello could be parsed.
type Status int

const (
	// NotTLS means the payload does not look like a TLS handshake record.
	NotTLS Status = iota
	// Incomplete means the payload is a truncated ClientHello.
	Incomplete
	// NoSNI means a complete ClientHello has no server_name extension (ECH or
	// a non-TLS protocol wrapped in a handshake).
	NoSNI
	// Found means an SNI was extracted.
	Found
)

// InspectClientHello parses as much of a TLS ClientHello as is available.
func InspectClientHello(payload []byte) (string, Status) {
	if len(payload) < 5 || payload[0] != 0x16 || payload[1] != 0x03 {
		return "", NotTLS
	}
	recordLen := int(payload[3])<<8 | int(payload[4])
	if recordLen < 4 {
		return "", NotTLS
	}
	if 5+recordLen > len(payload) {
		return "", Incomplete
	}
	rec := payload[5 : 5+recordLen]
	if rec[0] != 0x01 { // ClientHello
		return "", NoSNI
	}
	hsLen := int(rec[1])<<16 | int(rec[2])<<8 | int(rec[3])
	if hsLen > len(rec)-4 {
		return "", Incomplete
	}
	body := rec[4 : 4+hsLen]
	if len(body) < 34 {
		return "", Incomplete
	}
	off := 34
	if off >= len(body) {
		return "", Incomplete
	}
	sessionLen := int(body[off])
	off += 1 + sessionLen
	if off+2 > len(body) {
		return "", Incomplete
	}
	cipherLen := int(body[off])<<8 | int(body[off+1])
	off += 2 + cipherLen
	if off >= len(body) {
		return "", Incomplete
	}
	compLen := int(body[off])
	off += 1 + compLen
	if off+2 > len(body) {
		return "", Incomplete
	}
	extLen := int(body[off])<<8 | int(body[off+1])
	off += 2
	end := off + extLen
	if end > len(body) {
		return "", Incomplete
	}
	for off+4 <= end {
		extType := int(body[off])<<8 | int(body[off+1])
		length := int(body[off+2])<<8 | int(body[off+3])
		off += 4
		if off+length > end {
			return "", Incomplete
		}
		if extType == 0 {
			if name, ok := parseServerName(body[off : off+length]); ok {
				return name, Found
			}
			return "", NoSNI
		}
		off += length
	}
	return "", NoSNI
}

// ExtractSNI is a convenience wrapper used by tests.
func ExtractSNI(payload []byte) (string, bool) {
	name, status := InspectClientHello(payload)
	return name, status == Found
}

func parseServerName(data []byte) (string, bool) {
	if len(data) < 2 {
		return "", false
	}
	listLen := int(data[0])<<8 | int(data[1])
	if listLen > len(data)-2 {
		return "", false
	}
	off := 2
	end := 2 + listLen
	for off+3 <= end {
		nameType := data[off]
		nameLen := int(data[off+1])<<8 | int(data[off+2])
		off += 3
		if off+nameLen > end {
			return "", false
		}
		if nameType == 0 && nameLen > 0 {
			return normalizeName(string(data[off : off+nameLen])), true
		}
		off += nameLen
	}
	return "", false
}

func normalizeName(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
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
