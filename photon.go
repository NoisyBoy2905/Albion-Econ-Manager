package main

import "encoding/binary"

// Photon is the network library Albion uses. One UDP packet looks like:
//
//	header (12 bytes): peer id, flags, command count, timestamp, challenge
//	command, command, ...
//
// Each command has its own 12-byte header and carries a message
// (a request, a response or an event). Big messages are split into
// fragments across several packets, which we glue back together.

const (
	headerLen   = 12
	cmdHeadLen  = 12
	fragHeadLen = 20

	cmdReliable   = 6
	cmdUnreliable = 7
	cmdFragment   = 8

	msgRequest     = 2
	msgResponse    = 3
	msgEvent       = 4
	msgResponseAlt = 7
	msgEncrypted   = 131

	flagEncrypted = 1
	flagCRC       = 0xCC
)

type Message struct {
	Kind   byte // msgRequest, msgResponse or msgEvent
	Code   byte
	Params map[byte]any
	Extra  any // responses can carry one extra value before the params
}

type fragment struct {
	data    []byte
	written int
}

type Photon struct {
	fragments   map[uint32]*fragment
	OnMessage   func(Message)
	OnEncrypted func()
}

func NewPhoton() *Photon {
	return &Photon{fragments: map[uint32]*fragment{}}
}

// Feed takes the data part of one UDP packet.
func (p *Photon) Feed(b []byte) {
	// Sometimes several Photon packets arrive stuck together, so keep going
	// while there's enough left for another header.
	for len(b) >= headerLen {
		used := p.packet(b)
		if used <= 0 {
			return
		}
		b = b[used:]
	}
}

func (p *Photon) packet(b []byte) int {
	flags := b[2]
	commands := int(b[3])
	pos := headerLen

	if flags == flagEncrypted {
		p.encrypted()
		return 0
	}
	if flags == flagCRC {
		pos += 4 // skip the checksum
	}
	if pos > len(b) {
		return 0
	}

	for i := 0; i < commands; i++ {
		if len(b)-pos < cmdHeadLen {
			return 0
		}
		cmdType := b[pos]
		length := int(binary.BigEndian.Uint32(b[pos+4:]))
		if length < cmdHeadLen || pos+length > len(b) {
			return 0
		}
		body := b[pos+cmdHeadLen : pos+length]
		pos += length

		switch cmdType {
		case cmdReliable:
			p.message(body)
		case cmdUnreliable:
			if len(body) >= 4 {
				p.message(body[4:]) // skip the unreliable sequence number
			}
		case cmdFragment:
			p.fragment(body)
		}
	}
	return pos
}

func (p *Photon) fragment(body []byte) {
	if len(body) < fragHeadLen {
		return
	}
	start := binary.BigEndian.Uint32(body[0:])
	total := int(binary.BigEndian.Uint32(body[12:]))
	offset := int(binary.BigEndian.Uint32(body[16:]))
	chunk := body[fragHeadLen:]

	f, ok := p.fragments[start]
	if !ok {
		if total <= 0 || total > 8<<20 {
			return
		}
		if len(p.fragments) > 64 { // lost fragments would pile up forever
			p.fragments = map[uint32]*fragment{}
		}
		f = &fragment{data: make([]byte, total)}
		p.fragments[start] = f
	}
	if offset < 0 || offset+len(chunk) > len(f.data) {
		return
	}
	copy(f.data[offset:], chunk)
	f.written += len(chunk)

	if f.written >= len(f.data) {
		delete(p.fragments, start)
		p.message(f.data)
	}
}

// message: 1 signal byte, 1 type byte, then the message itself
func (p *Photon) message(b []byte) {
	if len(b) < 3 {
		return
	}
	kind := b[1]
	r := &reader{data: b[2:]}

	switch kind {
	case msgEncrypted:
		p.encrypted()
		return
	case msgRequest, msgEvent:
		code := r.byte()
		params := r.params()
		p.emit(Message{Kind: kind, Code: code, Params: params})
	case msgResponse, msgResponseAlt:
		code := r.byte()
		r.u16LE() // return code
		var extra any
		if r.left() > 0 {
			extra = r.value(r.byte())
		}
		params := map[byte]any{}
		if r.left() > 0 {
			params = r.params()
		}
		p.emit(Message{Kind: msgResponse, Code: code, Params: params, Extra: extra})
	}
}

func (p *Photon) emit(m Message) {
	if p.OnMessage != nil {
		p.OnMessage(m)
	}
}

func (p *Photon) encrypted() {
	if p.OnEncrypted != nil {
		p.OnEncrypted()
	}
}
