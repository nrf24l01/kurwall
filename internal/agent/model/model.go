// Package model contains the agent's transport-neutral network events.
package model

import (
	"net/netip"
	"time"
)

// Flow is one consolidated completed connection.
type Flow struct {
	ID            string
	StartedAt     time.Time
	EndedAt       time.Time
	SrcIP         netip.Addr
	DstIP         netip.Addr
	SrcPort       uint16
	DstPort       uint16
	Protocol      uint8
	Domain        string
	SNI           string
	ContainerID   string
	ContainerName string
	Network       string
	BytesIn       uint64
	BytesOut      uint64
	PacketsIn     uint64
	PacketsOut    uint64
	Blocked       bool
	RuleID        string
	Reason        string
}

// DNS is one DNS request observed on the wire.
type DNS struct {
	ID            string
	At            time.Time
	ClientIP      netip.Addr
	ClientPort    string
	QName         string
	QType         uint16
	RCode         uint8
	Answers       []netip.Addr
	Blocked       bool
	RuleID        string
	ContainerID   string
	ContainerName string
	LatencyMS     uint32
}

// Block is one blocking decision.
type Block struct {
	ID            string
	At            time.Time
	Kind          string
	Target        string
	SrcIP         netip.Addr
	DstIP         netip.Addr
	SrcPort       uint16
	DstPort       uint16
	Protocol      uint8
	Domain        string
	ContainerID   string
	ContainerName string
	RuleID        string
	Reason        string
}
