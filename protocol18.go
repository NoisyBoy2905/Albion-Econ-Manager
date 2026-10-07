package main

import (
	"encoding/binary"
	"math"
	"reflect"
)

// reader walks through a byte slice. If it runs out of bytes it sets bad
// instead of crashing, so one broken packet can't take the program down.
type reader struct {
	data []byte
	pos  int
	bad  bool
}

func (r *reader) left() int { return len(r.data) - r.pos }

func (r *reader) take(n int) []byte {
	if n < 0 || r.left() < n {
		r.bad = true
		r.pos = len(r.data)
		return nil
	}
	b := r.data[r.pos : r.pos+n]
	r.pos += n
	return b
}

func (r *reader) byte() byte {
	b := r.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (r *reader) u16LE() uint16 {
	b := r.take(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

// varint: 7 bits per byte, top bit means "more bytes follow"
func (r *reader) varint() uint64 {
	var v uint64
	for shift := uint(0); shift < 70; shift += 7 {
		b := r.byte()
		if r.bad {
			return 0
		}
		v |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return v
		}
	}
	r.bad = true
	return 0
}

// zigzag turns 0,1,2,3,4... back into 0,-1,1,-2,2...
func zigzag(v uint64) int64 { return int64(v>>1) ^ -int64(v&1) }

func (r *reader) count() int {
	n := r.varint()
	if n > uint64(r.left())+1 {
		r.bad = true
		return 0
	}
	return int(n)
}

func (r *reader) str() string {
	n := r.count()
	return string(r.take(n))
}

// Photon "Protocol 18" type codes
const (
	tUnknown   = 0
	tBool      = 2
	tByte      = 3
	tShort     = 4
	tFloat     = 5
	tDouble    = 6
	tString    = 7
	tNull      = 8
	tVarInt    = 9
	tVarLong   = 10
	tInt1      = 11
	tInt1Neg   = 12
	tInt2      = 13
	tInt2Neg   = 14
	tLong1     = 15
	tLong1Neg  = 16
	tLong2     = 17
	tLong2Neg  = 18
	tCustom    = 19
	tDict      = 20
	tHashtable = 21
	tObjArray  = 23
	tOpRequest = 24
	tOpResp    = 25
	tEvent     = 26
	tFalse     = 27
	tTrue      = 28
	tZeroShort = 29
	tZeroInt   = 30
	tZeroLong  = 31
	tZeroFloat = 32
	tZeroDbl   = 33
	tZeroByte  = 34
	tArray     = 0x40
	tSlimBase  = 0x80
)

// value reads one value of type t. Every type has to be handled, even ones
// we don't care about, otherwise we lose our place in the packet.
func (r *reader) value(t byte) any {
	if r.bad {
		return nil
	}
	if t >= tSlimBase {
		return r.customBody()
	}
	switch t {
	case tUnknown, tNull:
		return nil
	case tBool:
		return r.byte() != 0
	case tByte:
		return r.byte()
	case tShort:
		return int16(r.u16LE())
	case tFloat:
		b := r.take(4)
		if b == nil {
			return nil
		}
		return math.Float32frombits(binary.LittleEndian.Uint32(b))
	case tDouble:
		b := r.take(8)
		if b == nil {
			return nil
		}
		return math.Float64frombits(binary.LittleEndian.Uint64(b))
	case tString:
		return r.str()
	case tVarInt, tVarLong:
		return zigzag(r.varint())
	case tInt1, tLong1:
		return int64(r.byte())
	case tInt1Neg, tLong1Neg:
		return -int64(r.byte())
	case tInt2, tLong2:
		return int64(r.u16LE())
	case tInt2Neg, tLong2Neg:
		return -int64(r.u16LE())
	case tCustom:
		r.byte() // custom type id
		return r.customBody()
	case tDict, tHashtable:
		return r.dict()
	case tObjArray:
		n := r.count()
		out := make([]any, 0, n)
		for i := 0; i < n && !r.bad; i++ {
			out = append(out, r.value(r.byte()))
		}
		return out
	case tOpRequest, tEvent:
		r.byte()
		return r.params()
	case tOpResp:
		r.byte()
		r.u16LE()
		if r.left() > 0 {
			r.value(r.byte())
		}
		return r.params()
	case tFalse:
		return false
	case tTrue:
		return true
	case tZeroShort, tZeroInt, tZeroLong, tZeroByte:
		return int64(0)
	case tZeroFloat, tZeroDbl:
		return float64(0)
	case tArray:
		n := r.count()
		et := r.byte()
		out := make([]any, 0, n)
		for i := 0; i < n && !r.bad; i++ {
			out = append(out, r.value(et))
		}
		return out
	}
	if t&tArray == tArray {
		return r.typedArray(t &^ tArray)
	}
	r.bad = true // a type we don't know: we can't safely keep reading
	return nil
}

func (r *reader) customBody() any {
	n := r.count()
	return r.take(n)
}

func (r *reader) dict() map[any]any {
	kt, vt := r.byte(), r.byte()
	n := r.count()
	out := make(map[any]any, n)
	for i := 0; i < n && !r.bad; i++ {
		k, v := kt, vt
		if k == 0 {
			k = r.byte()
		}
		if v == 0 {
			v = r.byte()
		}
		key := r.value(k)
		val := r.value(v)
		if key != nil && !reflect.TypeOf(key).Comparable() {
			continue // lists can't be used as map keys
		}
		out[key] = val
	}
	return out
}

// typedArray: every element has the same type, so the type is only sent once.
func (r *reader) typedArray(et byte) any {
	n := r.count()
	switch et {
	case tBool:
		packed := r.take((n + 7) / 8)
		out := make([]bool, n)
		for i := range out {
			if packed != nil {
				out[i] = packed[i/8]&(1<<(i%8)) != 0
			}
		}
		return out
	case tByte:
		return r.take(n)
	case tString:
		out := make([]string, 0, n)
		for i := 0; i < n && !r.bad; i++ {
			out = append(out, r.str())
		}
		return out
	case tCustom:
		r.byte()
		out := make([]any, 0, n)
		for i := 0; i < n && !r.bad; i++ {
			out = append(out, r.customBody())
		}
		return out
	}
	out := make([]any, 0, n)
	for i := 0; i < n && !r.bad; i++ {
		out = append(out, r.value(et))
	}
	return out
}

// params reads a parameter table: count, then (key, type, value) for each.
func (r *reader) params() map[byte]any {
	n := r.count()
	out := make(map[byte]any, n)
	for i := 0; i < n && !r.bad; i++ {
		key := r.byte()
		out[key] = r.value(r.byte())
	}
	return out
}
