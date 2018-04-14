package msgp

import (
	"bytes"
	"encoding/binary"
	"math"
	"time"
)

var big = binary.BigEndian

// NextType returns the type of the next object in the slice. If the length of the input is zero,
// it returns InvalidType.
func NextType(b []byte) Type {
	if len(b) == 0 {
		return InvalidType
	}
	spec := sizes[b[0]]
	t := spec.typ
	if t == ExtensionType && len(b) > int(spec.size) {
		var tp int8
		if spec.extra == constsize {
			tp = int8(b[1])
		} else {
			tp = int8(b[spec.size-1])
		}
		switch tp {
		case TimeExtension:
			return TimeType
		case Complex128Extension:
			return Complex128Type
		case Complex64Extension:
			return Complex64Type
		default:
			return ExtensionType
		}
	}
	return t
}

// IsNil returns true if len(b)>0 and the leading byte is a "nil" MessagePack byte (0xc0).
func IsNil(b []byte) bool {
	return len(b) > 0 && b[0] == mnil
}

// Raw is raw encoded MessagePack. It implements Marshaler, Unmarshaler, Encoder, Decoder, and Sizer.
// Raw allows you to read and write data without interpreting the contents.
type Raw []byte

// MarshalMsg implements msgp.Marshaler. It appends the raw contents of r to the provided byte slice.
// If r is empty, then "nil" (0xc0) is appended instead.
func (r Raw) MarshalMsg(b []byte) ([]byte, error) {
	i := len(r)
	if i == 0 {
		return AppendNil(b), nil
	}
	o, l := ensure(b, i)
	copy(o[l:], []byte(r))
	return o, nil
}

// UnmarshalMsg implements msgp.Unmarshaler. It sets the contents of r to be the next object in the
// provided byte slice.
func (r *Raw) UnmarshalMsg(b []byte) ([]byte, error) {
	l := len(b)
	out, err := Skip(b)
	if err != nil {
		return b, err
	}
	rlen := l - len(out)
	if cap(*r) < rlen {
		*r = make(Raw, rlen)
	} else {
		*r = (*r)[0:rlen]
	}
	copy(*r, b[:rlen])
	return out, nil
}

// EncodeMsg implements msgp.Encoder. It writes the raw bytes to the writer. If r is empty, then "nil" (0xc0) is
// written instead.
func (r Raw) EncodeMsg(w *Writer) error {
	if len(r) == 0 {
		return w.WriteNil()
	}
	_, err := w.Write([]byte(r))
	return err
}

// DecodeMsg implements msgp.Decoder. It sets the value of r to be the next object on the wire.
func (r *Raw) DecodeMsg(f *Reader) error {
	*r = (*r)[:0]
	return appendNext(f, (*[]byte)(r))
}

// Msgsize implements msgp.Sizer
func (r Raw) Msgsize() int {
	l := len(r)
	if l == 0 {
		return 1 // for 'nil'
	}
	return l
}

func appendNext(f *Reader, d *[]byte) error {
	amt, o, err := getNextSize(f.R)
	if err != nil {
		return err
	}
	var i int
	*d, i = ensure(*d, int(amt))
	_, err = f.R.ReadFull((*d)[i:])
	if err != nil {
		return err
	}
	for o > 0 {
		err = appendNext(f, d)
		if err != nil {
			return err
		}
		o--
	}
	return nil
}

// MarshalJSON implements json.Marshaler.
func (r *Raw) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	_, err := UnmarshalAsJSON(&buf, []byte(*r))
	return buf.Bytes(), err
}

// ReadMapHeaderBytes reads a map header size from b and returns the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a map)
func ReadMapHeaderBytes(b []byte) (sz uint32, o []byte, err error) {
	l := len(b)
	if l < 1 {
		err = ErrShortBytes
		return
	}

	lead := b[0]
	if isfixmap(lead) {
		sz = uint32(rfixmap(lead))
		o = b[1:]
		return
	}

	switch lead {
	case mmap16:
		if l < 3 {
			err = ErrShortBytes
			return
		}
		sz = uint32(big.Uint16(b[1:]))
		o = b[3:]
		return

	case mmap32:
		if l < 5 {
			err = ErrShortBytes
			return
		}
		sz = big.Uint32(b[1:])
		o = b[5:]
		return

	default:
		err = badPrefix(MapType, lead)
		return
	}
}

// ReadMapKeyZC attempts to read a map key from b and returns the key bytes and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a str or bin)
func ReadMapKeyZC(b []byte) ([]byte, []byte, error) {
	o, b, err := ReadStringZC(b)
	if err != nil {
		if tperr, ok := err.(TypeError); ok && tperr.Encoded == BinType {
			return ReadBytesZC(b)
		}
		return nil, b, err
	}
	return o, b, nil
}

// ReadArrayHeaderBytes attempts to read the array header size off of b and return the size
// and remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not an array)
func ReadArrayHeaderBytes(b []byte) (sz uint32, o []byte, err error) {
	if len(b) < 1 {
		return 0, nil, ErrShortBytes
	}
	lead := b[0]
	if isfixarray(lead) {
		sz = uint32(rfixarray(lead))
		o = b[1:]
		return
	}

	switch lead {
	case marray16:
		if len(b) < 3 {
			err = ErrShortBytes
			return
		}
		sz = uint32(big.Uint16(b[1:]))
		o = b[3:]
		return

	case marray32:
		if len(b) < 5 {
			err = ErrShortBytes
			return
		}
		sz = big.Uint32(b[1:])
		o = b[5:]
		return

	default:
		err = badPrefix(ArrayType, lead)
		return
	}
}

// ReadNilBytes tries to read a "nil" byte off of b and return the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a 'nil')
// - InvalidPrefixError
func ReadNilBytes(b []byte) ([]byte, error) {
	if len(b) < 1 {
		return nil, ErrShortBytes
	}
	if b[0] != mnil {
		return b, badPrefix(NilType, b[0])
	}
	return b[1:], nil
}

// ReadFloat64Bytes tries to read a float64 from b and return the value and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a float64)
func ReadFloat64Bytes(b []byte) (f float64, o []byte, err error) {
	if len(b) < 9 {
		if len(b) >= 5 && b[0] == mfloat32 {
			var tf float32
			tf, o, err = ReadFloat32Bytes(b)
			f = float64(tf)
			return
		}
		err = ErrShortBytes
		return
	}

	if b[0] != mfloat64 {
		if b[0] == mfloat32 {
			var tf float32
			tf, o, err = ReadFloat32Bytes(b)
			f = float64(tf)
			return
		}
		err = badPrefix(Float64Type, b[0])
		return
	}

	f = math.Float64frombits(getMuint64(b))
	o = b[9:]
	return
}

// ReadFloat32Bytes tries to read a float64 from b and return the value and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a float32)
func ReadFloat32Bytes(b []byte) (f float32, o []byte, err error) {
	if len(b) < 5 {
		err = ErrShortBytes
		return
	}

	if b[0] != mfloat32 {
		err = TypeError{Method: Float32Type, Encoded: getType(b[0])}
		return
	}

	f = math.Float32frombits(getMuint32(b))
	o = b[5:]
	return
}

// ReadBoolBytes tries to read a float64 from b and return the value and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a bool)
func ReadBoolBytes(b []byte) (bool, []byte, error) {
	if len(b) < 1 {
		return false, b, ErrShortBytes
	}
	switch b[0] {
	case mtrue:
		return true, b[1:], nil
	case mfalse:
		return false, b[1:], nil
	default:
		return false, b, badPrefix(BoolType, b[0])
	}
}

// ReadInt64Bytes reads an int64 from b and return the value and the remaining bytes.
// Errors that can be returned include ErrShortBytes, UintOverflow, InvalidPrefixError, and TypeError.
func ReadInt64Bytes(b []byte) (int64, []byte, error) {

	l := len(b)
	if l < 1 {
		return 0, nil, ErrShortBytes
	}

	lead := b[0]
	if isfixint(lead) {
		return int64(rfixint(lead)), b[1:], nil
	}
	if isnfixint(lead) {
		return int64(rnfixint(lead)), b[1:], nil
	}

	switch lead {
	case mint8, muint8:
		if l < 2 {
			return 0, b, ErrShortBytes
		}
		if lead == mint8 {
			return int64(getMint8(b)), b[2:], nil
		}
		return int64(getMuint8(b)), b[2:], nil
	case mint16, muint16:
		if l < 3 {
			return 0, b, ErrShortBytes
		}
		if lead == mint16 {
			return int64(getMint16(b)), b[3:], nil
		}
		return int64(getMuint16(b)), b[3:], nil
	case mint32, muint32:
		if l < 5 {
			return 0, b, ErrShortBytes
		}
		if lead == mint32 {
			return int64(getMint32(b)), b[5:], nil
		}
		return int64(getMint32(b)), b[5:], nil
	case mint64, muint64:
		if l < 9 {
			return 0, b, ErrShortBytes
		}
		if lead == mint64 {
			return getMint64(b), b[9:], nil
		}
		num := getMuint64(b)
		// Only checking for overflow with uint64 because all other (smaller) unsigned
		// integers can fit into an int64.
		if num > math.MaxInt64 {
			return 0, b, UintOverflow{num, 64}
		}
		return int64(num), b[9:], nil
	}

	return 0, b, badPrefix(IntType, lead)

}

// ReadInt32Bytes tries to read an int32 from b and return the value and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a int)
// - IntOverflow{} (value doesn't fit in int32)
func ReadInt32Bytes(b []byte) (int32, []byte, error) {
	i, o, err := ReadInt64Bytes(b)
	if i > math.MaxInt32 || i < math.MinInt32 {
		return 0, o, IntOverflow{Value: i, FailedBitsize: 32}
	}
	return int32(i), o, err
}

// ReadInt16Bytes tries to read an int16 from b and return the value and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a int)
// - IntOverflow{} (value doesn't fit in int16)
func ReadInt16Bytes(b []byte) (int16, []byte, error) {
	i, o, err := ReadInt64Bytes(b)
	if i > math.MaxInt16 || i < math.MinInt16 {
		return 0, o, IntOverflow{Value: i, FailedBitsize: 16}
	}
	return int16(i), o, err
}

// ReadInt8Bytes tries to read an int16 from b and return the value and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a int)
// - IntOverflow{} (value doesn't fit in int8)
func ReadInt8Bytes(b []byte) (int8, []byte, error) {
	i, o, err := ReadInt64Bytes(b)
	if i > math.MaxInt8 || i < math.MinInt8 {
		return 0, o, IntOverflow{Value: i, FailedBitsize: 8}
	}
	return int8(i), o, err
}

// ReadIntBytes tries to read an int from b and return the value and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a int)
// - IntOverflow{} (value doesn't fit in int; 32-bit platforms only)
func ReadIntBytes(b []byte) (int, []byte, error) {
	if smallint {
		i, b, err := ReadInt32Bytes(b)
		return int(i), b, err
	}
	i, b, err := ReadInt64Bytes(b)
	return int(i), b, err
}

// ReadUint64Bytes tries to read a uint64 from b and return the value and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a uint)
func ReadUint64Bytes(b []byte) (u uint64, o []byte, err error) {
	l := len(b)
	if l < 1 {
		return 0, nil, ErrShortBytes
	}

	lead := b[0]
	if isfixint(lead) {
		u = uint64(rfixint(lead))
		o = b[1:]
		return
	}

	switch lead {
	case muint8:
		if l < 2 {
			err = ErrShortBytes
			return
		}
		u = uint64(getMuint8(b))
		o = b[2:]
		return

	case muint16:
		if l < 3 {
			err = ErrShortBytes
			return
		}
		u = uint64(getMuint16(b))
		o = b[3:]
		return

	case muint32:
		if l < 5 {
			err = ErrShortBytes
			return
		}
		u = uint64(getMuint32(b))
		o = b[5:]
		return

	case muint64:
		if l < 9 {
			err = ErrShortBytes
			return
		}
		u = getMuint64(b)
		o = b[9:]
		return

	default:
		err = badPrefix(UintType, lead)
		return
	}
}

// ReadUint32Bytes tries to read a uint32 from b and return the value and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a uint)
// - UintOverflow{} (value too large for uint32)
func ReadUint32Bytes(b []byte) (uint32, []byte, error) {
	v, o, err := ReadUint64Bytes(b)
	if v > math.MaxUint32 {
		return 0, nil, UintOverflow{Value: v, FailedBitsize: 32}
	}
	return uint32(v), o, err
}

// ReadUint16Bytes tries to read a uint16 from b and return the value and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a uint)
// - UintOverflow{} (value too large for uint16)
func ReadUint16Bytes(b []byte) (uint16, []byte, error) {
	v, o, err := ReadUint64Bytes(b)
	if v > math.MaxUint16 {
		return 0, nil, UintOverflow{Value: v, FailedBitsize: 16}
	}
	return uint16(v), o, err
}

// ReadUint8Bytes tries to read a uint8 from b and return the value and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a uint)
// - UintOverflow{} (value too large for uint8)
func ReadUint8Bytes(b []byte) (uint8, []byte, error) {
	v, o, err := ReadUint64Bytes(b)
	if v > math.MaxUint8 {
		return 0, nil, UintOverflow{Value: v, FailedBitsize: 8}
	}
	return uint8(v), o, err
}

// ReadUintBytes tries to read a uint from b and return the value and the remaining bytes.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a uint)
// - UintOverflow{} (value too large for uint; 32-bit platforms only)
func ReadUintBytes(b []byte) (uint, []byte, error) {
	if smallint {
		u, b, err := ReadUint32Bytes(b)
		return uint(u), b, err
	}
	u, b, err := ReadUint64Bytes(b)
	return uint(u), b, err
}

// ReadByteBytes is analogous to ReadUint8Bytes
func ReadByteBytes(b []byte) (byte, []byte, error) {
	return ReadUint8Bytes(b)
}

// ReadBytesBytes reads a 'bin' object from b and returns its value and the remaining bytes in b.
// Possible errors:
// - ErrShortBytes (too few bytes)
// - TypeError{} (not a 'bin' object)
func ReadBytesBytes(b []byte, scratch []byte) (v []byte, o []byte, err error) {
	return readBytesBytes(b, scratch, false)
}

func readBytesBytes(b []byte, scratch []byte, zc bool) (v []byte, o []byte, err error) {
	l := len(b)
	if l < 1 {
		return nil, nil, ErrShortBytes
	}

	lead := b[0]
	var read int
	switch lead {
	case mbin8:
		if l < 2 {
			err = ErrShortBytes
			return
		}

		read = int(b[1])
		b = b[2:]

	case mbin16:
		if l < 3 {
			err = ErrShortBytes
			return
		}
		read = int(big.Uint16(b[1:]))
		b = b[3:]

	case mbin32:
		if l < 5 {
			err = ErrShortBytes
			return
		}
		read = int(big.Uint32(b[1:]))
		b = b[5:]

	default:
		err = badPrefix(BinType, lead)
		return
	}

	if len(b) < read {
		err = ErrShortBytes
		return
	}

	// zero-copy
	if zc {
		v = b[0:read]
		o = b[read:]
		return
	}

	if cap(scratch) >= read {
		v = scratch[0:read]
	} else {
		v = make([]byte, read)
	}

	o = b[copy(v, b):]
	return
}

// ReadBytesZC extracts the MessagePack-encoded binary field without copying. The returned []byte
// points to the same memory as the input slice.
// Possible errors:
// - ErrShortBytes (b not long enough)
// - TypeError{} (object not 'bin')
func ReadBytesZC(b []byte) (v []byte, o []byte, err error) {
	return readBytesBytes(b, nil, true)
}

func ReadExactBytes(b []byte, into []byte) (o []byte, err error) {
	l := len(b)
	if l < 1 {
		err = ErrShortBytes
		return
	}

	lead := b[0]
	var read uint32
	var skip int
	switch lead {
	case mbin8:
		if l < 2 {
			err = ErrShortBytes
			return
		}

		read = uint32(b[1])
		skip = 2

	case mbin16:
		if l < 3 {
			err = ErrShortBytes
			return
		}
		read = uint32(big.Uint16(b[1:]))
		skip = 3

	case mbin32:
		if l < 5 {
			err = ErrShortBytes
			return
		}
		read = uint32(big.Uint32(b[1:]))
		skip = 5

	default:
		err = badPrefix(BinType, lead)
		return
	}

	if read != uint32(len(into)) {
		err = ArrayError{Wanted: uint32(len(into)), Got: read}
		return
	}

	o = b[skip+copy(into, b[skip:]):]
	return
}

// ReadStringZC reads a messagepack string field
// without copying. The returned []byte points
// to the same memory as the input slice.
// Possible errors:
// - ErrShortBytes (b not long enough)
// - TypeError{} (object not 'str')
func ReadStringZC(b []byte) (v []byte, o []byte, err error) {
	l := len(b)
	if l < 1 {
		return nil, nil, ErrShortBytes
	}

	lead := b[0]
	var read int

	if isfixstr(lead) {
		read = int(rfixstr(lead))
		b = b[1:]
	} else {
		switch lead {
		case mstr8:
			if l < 2 {
				err = ErrShortBytes
				return
			}
			read = int(b[1])
			b = b[2:]

		case mstr16:
			if l < 3 {
				err = ErrShortBytes
				return
			}
			read = int(big.Uint16(b[1:]))
			b = b[3:]

		case mstr32:
			if l < 5 {
				err = ErrShortBytes
				return
			}
			read = int(big.Uint32(b[1:]))
			b = b[5:]

		default:
			err = TypeError{Method: StrType, Encoded: getType(lead)}
			return
		}
	}

	if len(b) < read {
		err = ErrShortBytes
		return
	}

	v = b[0:read]
	o = b[read:]
	return
}

// ReadStringBytes reads a 'str' object
// from 'b' and returns its value and the
// remaining bytes in 'b'.
// Possible errors:
// - ErrShortBytes (b not long enough)
// - TypeError{} (not 'str' type)
// - InvalidPrefixError
func ReadStringBytes(b []byte) (string, []byte, error) {
	v, o, err := ReadStringZC(b)
	return string(v), o, err
}

// ReadStringAsBytes reads a 'str' object into a slice of bytes. The data read is the first slice returned,
// which may be written to the memory held by the scratch slice if it is large enough (scratch may be nil).
// The second slice returned contains the remaining bytes in b. Possible errors are ErrShortBytes (b not 
// long enough), TypeError{} (not 'str' type), and InvalidPrefixError (unknown type marker).
func ReadStringAsBytes(b []byte, scratch []byte) ([]byte, []byte, error) {
	tmp, o, err := ReadStringZC(b)
	return append(scratch[:0], tmp...), o, err
}

// ReadComplex128Bytes reads a complex128
// extension object from 'b' and returns the
// remaining bytes.
// Possible errors:
// - ErrShortBytes (not enough bytes in 'b')
// - TypeError{} (object not a complex128)
// - InvalidPrefixError
// - ExtensionTypeError{} (object an extension of the correct size, but not a complex128)
func ReadComplex128Bytes(b []byte) (c complex128, o []byte, err error) {
	if len(b) < 18 {
		err = ErrShortBytes
		return
	}
	if b[0] != mfixext16 {
		err = badPrefix(Complex128Type, b[0])
		return
	}
	if int8(b[1]) != Complex128Extension {
		err = errExt(int8(b[1]), Complex128Extension)
		return
	}
	c = complex(math.Float64frombits(big.Uint64(b[2:])),
		math.Float64frombits(big.Uint64(b[10:])))
	o = b[18:]
	return
}

// ReadComplex64Bytes reads a complex64
// extension object from 'b' and returns the
// remaining bytes.
// Possible errors:
// - ErrShortBytes (not enough bytes in 'b')
// - TypeError{} (object not a complex64)
// - ExtensionTypeError{} (object an extension of the correct size, but not a complex64)
func ReadComplex64Bytes(b []byte) (c complex64, o []byte, err error) {
	if len(b) < 10 {
		err = ErrShortBytes
		return
	}
	if b[0] != mfixext8 {
		err = badPrefix(Complex64Type, b[0])
		return
	}
	if b[1] != Complex64Extension {
		err = errExt(int8(b[1]), Complex64Extension)
		return
	}
	c = complex(math.Float32frombits(big.Uint32(b[2:])),
		math.Float32frombits(big.Uint32(b[6:])))
	o = b[10:]
	return
}

// ReadTimeBytes reads a time.Time
// extension object from 'b' and returns the
// remaining bytes.
// Possible errors:
// - ErrShortBytes (not enough bytes in 'b')
// - TypeError{} (object not a complex64)
// - ExtensionTypeError{} (object an extension of the correct size, but not a time.Time)
func ReadTimeBytes(b []byte) (t time.Time, o []byte, err error) {
	if len(b) < 15 {
		err = ErrShortBytes
		return
	}
	if b[0] != mext8 || b[1] != 12 {
		err = badPrefix(TimeType, b[0])
		return
	}
	if int8(b[2]) != TimeExtension {
		err = errExt(int8(b[2]), TimeExtension)
		return
	}
	sec, nsec := getUnix(b[3:])
	t = time.Unix(sec, int64(nsec)).Local()
	o = b[15:]
	return
}

// ReadMapStrIntfBytes reads a map[string]interface{} out of b and returns the map and any remaining bytes.
// If map old is not nil, it will be cleared and used so that a map does not need to be created.
func ReadMapStrIntfBytes(b []byte, old map[string]interface{}) (map[string]interface{}, []byte, error) {

	sz, o, err := ReadMapHeaderBytes(b)
	if err != nil {
		return old, o, err
	}

	if old != nil {
		for key := range old {
			delete(old, key)
		}
	} else {
		old = make(map[string]interface{}, int(sz))
	}

	for z := uint32(0); z < sz; z++ {
		if len(o) < 1 {
			return old, o, ErrShortBytes
		}
		var key []byte
		key, o, err = ReadMapKeyZC(o)
		if err != nil {
			return old, o, err
		}
		var val interface{}
		val, o, err = ReadIntfBytes(o)
		if err != nil {
			return old, o, err
		}
		old[string(key)] = val
	}

	return old, o, err

}

// ReadIntfBytes attempts to read
// the next object out of 'b' as a raw interface{} and
// return the remaining bytes.
func ReadIntfBytes(b []byte) (i interface{}, o []byte, err error) {
	if len(b) < 1 {
		err = ErrShortBytes
		return
	}

	k := NextType(b)

	switch k {
	case MapType:
		i, o, err = ReadMapStrIntfBytes(b, nil)
		return

	case ArrayType:
		var sz uint32
		sz, o, err = ReadArrayHeaderBytes(b)
		if err != nil {
			return
		}
		j := make([]interface{}, int(sz))
		i = j
		for d := range j {
			j[d], o, err = ReadIntfBytes(o)
			if err != nil {
				return
			}
		}
		return

	case Float32Type:
		i, o, err = ReadFloat32Bytes(b)
		return

	case Float64Type:
		i, o, err = ReadFloat64Bytes(b)
		return

	case IntType:
		i, o, err = ReadInt64Bytes(b)
		return

	case UintType:
		i, o, err = ReadUint64Bytes(b)
		return

	case BoolType:
		i, o, err = ReadBoolBytes(b)
		return

	case TimeType:
		i, o, err = ReadTimeBytes(b)
		return

	case Complex64Type:
		i, o, err = ReadComplex64Bytes(b)
		return

	case Complex128Type:
		i, o, err = ReadComplex128Bytes(b)
		return

	case ExtensionType:
		var t int8
		t, err = peekExtension(b)
		if err != nil {
			return
		}
		// use a user-defined extension,
		// if it's been registered
		f, ok := extensionReg[t]
		if ok {
			e := f()
			o, err = ReadExtensionBytes(b, e)
			i = e
			return
		}
		// last resort is a raw extension
		e := RawExtension{}
		e.Type = int8(t)
		o, err = ReadExtensionBytes(b, &e)
		i = &e
		return

	case NilType:
		o, err = ReadNilBytes(b)
		return

	case BinType:
		i, o, err = ReadBytesBytes(b, nil)
		return

	case StrType:
		i, o, err = ReadStringBytes(b)
		return

	default:
		err = InvalidPrefixError(b[0])
		return
	}
}

// Skip skips the next object in 'b' and
// returns the remaining bytes. If the object
// is a map or array, all of its elements
// will be skipped.
// Possible Errors:
// - ErrShortBytes (not enough bytes in b)
// - InvalidPrefixError (bad encoding)
func Skip(b []byte) ([]byte, error) {
	sz, asz, err := getSize(b)
	if err != nil {
		return b, err
	}
	if uintptr(len(b)) < sz {
		return b, ErrShortBytes
	}
	b = b[sz:]
	for asz > 0 {
		b, err = Skip(b)
		if err != nil {
			return b, err
		}
		asz--
	}
	return b, nil
}

// returns (skip N bytes, skip M objects, error)
func getSize(b []byte) (uintptr, uintptr, error) {
	l := len(b)
	if l == 0 {
		return 0, 0, ErrShortBytes
	}
	lead := b[0]
	spec := &sizes[lead] // get type information
	size, mode := spec.size, spec.extra
	if size == 0 {
		return 0, 0, InvalidPrefixError(lead)
	}
	if mode >= 0 { // fixed composites
		return uintptr(size), uintptr(mode), nil
	}
	if l < int(size) {
		return 0, 0, ErrShortBytes
	}
	switch mode {
	case extra8:
		return uintptr(size) + uintptr(b[1]), 0, nil
	case extra16:
		return uintptr(size) + uintptr(big.Uint16(b[1:])), 0, nil
	case extra32:
		return uintptr(size) + uintptr(big.Uint32(b[1:])), 0, nil
	case map16v:
		return uintptr(size), 2 * uintptr(big.Uint16(b[1:])), nil
	case map32v:
		return uintptr(size), 2 * uintptr(big.Uint32(b[1:])), nil
	case array16v:
		return uintptr(size), uintptr(big.Uint16(b[1:])), nil
	case array32v:
		return uintptr(size), uintptr(big.Uint32(b[1:])), nil
	default:
		return 0, 0, fatal
	}
}
