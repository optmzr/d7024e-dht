package network

import (
	"net"

	"github.com/optmzr/d7024e-dht/node"
	"github.com/optmzr/d7024e-dht/route"
	"github.com/optmzr/d7024e-dht/store"
)

const Size256 = 256 / 8

type SessionID [Size256]byte

type PingResult struct {
	From      route.Contact
	Challenge []byte
}

type PongRequest struct {
	From      route.Contact
	SessionID SessionID
	Challenge []byte
}

type FindNodesResult struct {
	from    route.Contact
	closest []route.Contact
}

type FindValueResult struct {
	from      route.Contact
	SessionID SessionID
	closest   []route.Contact
	Key       store.Key
	value     string
}

type FindNodesRequest struct {
	SessionID SessionID
	sender    route.Contact
}

type FindValueRequest struct {
	key       store.Key
	sessionID SessionID
	sender    route.Contact
}

type Network interface {
	Ping(addr net.UDPAddr) (chan *PingResult, error)
	Pong(challenge []byte, sessionID SessionID, addr net.UDPAddr) error
	FindNodes(target node.ID, addr net.UDPAddr) (chan Result, error)
	Store(key store.Key, value string, addr net.UDPAddr) error
	FindValue(key store.Key, addr net.UDPAddr) (chan Result, error)
	SendValue(key store.Key, value string, closets []route.Contact, sessionID SessionID, addr net.UDPAddr) error
}

type Result interface {
	From() route.Contact
	Closest() []route.Contact
	Value() string
}

func (r *FindNodesResult) From() route.Contact {
	return r.from
}

func (r *FindNodesResult) Closest() []route.Contact {
	return r.closest
}

func (r *FindNodesResult) Value() string {
	return ""
}

func (r *FindValueResult) From() route.Contact {
	return r.from
}

func (r *FindValueResult) Closest() []route.Contact {
	return r.closest
}

func (r *FindValueResult) Value() string {
	return r.value
}
