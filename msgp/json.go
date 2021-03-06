package msgp

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"strconv"
	"unicode/utf8"
)

var (
	null = []byte("null")
	hex  = []byte("0123456789abcdef")
)

// jsWriter is the interface used to write JSON.
type jsWriter interface {
	io.Writer
	io.ByteWriter
	WriteString(string) (int, error)
}

// CopyToJSON reads MessagePack from src and copies it as JSON to dst until EOF.
func CopyToJSON(dst io.Writer, src io.Reader) (int64, error) {
	r := NewReader(src)
	return r.WriteToJSON(dst)
}

// WriteToJSON translates MessagePack from r and writes it as JSON to w until the underlying
// reader returns io.EOF. WriteToJSON returns the number of bytes written. An error is returned
// only if reading stops before io.EOF.
func (r *Reader) WriteToJSON(w io.Writer) (n int64, err error) {
	var j jsWriter
	var bf *bufio.Writer
	if jsw, ok := w.(jsWriter); ok {
		j = jsw
	} else {
		bf = bufio.NewWriter(w)
		j = bf
	}
	var nn int
	for err == nil {
		nn, err = rwNext(j, r)
		n += int64(nn)
	}
	if err != io.EOF {
		if bf != nil {
			bf.Flush()
		}
		return
	}
	err = nil
	if bf != nil {
		err = bf.Flush()
	}
	return
}

func rwNext(w jsWriter, src *Reader) (int, error) {
	t, err := src.NextType()
	if err != nil {
		return 0, err
	}
	switch t {
	case StrType:
		return rwString(w, src)
	case BinType:
		return rwBytes(w, src)
	case MapType:
		return rwMap(w, src)
	case ArrayType:
		return rwArray(w, src)
	case Float64Type:
		return rwFloat64(w, src)
	case Float32Type:
		return rwFloat32(w, src)
	case BoolType:
		return rwBool(w, src)
	case IntType:
		return rwInt(w, src)
	case UintType:
		return rwUint(w, src)
	case NilType:
		return rwNil(w, src)
	case ExtensionType:
		return rwExtension(w, src)
	case Complex64Type:
		return rwExtension(w, src)
	case Complex128Type:
		return rwExtension(w, src)
	case TimeType:
		return rwTime(w, src)
	default:
		return 0, err
	}
}

func rwMap(dst jsWriter, src *Reader) (int, error) {

	sz, err := src.ReadMapHeader()
	if err != nil {
		return 0, err
	}

	if sz == 0 {
		return dst.WriteString("{}")
	}

	var n int

	err = dst.WriteByte('{')
	if err != nil {
		return n, err
	}
	n++

	var comma bool
	for i := uint32(0); i < sz; i++ {

		if comma {
			err = dst.WriteByte(',')
			if err != nil {
				return n, err
			}
			n++
		}
		comma = true

		field, err := src.ReadMapKeyPtr()
		if err != nil {
			return n, err
		}
		nn, err := rwQuoted(dst, field)
		n += nn
		if err != nil {
			return n, err
		}

		err = dst.WriteByte(':')
		if err != nil {
			return n, err
		}
		n++
		nn, err = rwNext(dst, src)
		n += nn
		if err != nil {
			return n, err
		}

	}

	err = dst.WriteByte('}')
	if err != nil {
		return n, err
	}
	n++

	return n, nil

}

func rwArray(dst jsWriter, src *Reader) (n int, err error) {
	err = dst.WriteByte('[')
	if err != nil {
		return
	}
	var sz uint32
	sz, err = src.ReadArrayHeader()
	if err != nil {
		return
	}
	var nn int
	comma := false
	for i := uint32(0); i < sz; i++ {
		if comma {
			err = dst.WriteByte(',')
			if err != nil {
				return
			}
			n++
		}
		nn, err = rwNext(dst, src)
		n += nn
		if err != nil {
			return
		}
		comma = true
	}

	err = dst.WriteByte(']')
	if err != nil {
		return
	}
	n++
	return
}

func rwNil(dst jsWriter, src *Reader) (int, error) {
	err := src.ReadNil()
	if err != nil {
		return 0, err
	}
	return dst.Write(null)
}

func rwFloat32(dst jsWriter, src *Reader) (int, error) {
	f, err := src.ReadFloat32()
	if err != nil {
		return 0, err
	}
	src.scratch = strconv.AppendFloat(src.scratch[:0], float64(f), 'f', -1, 64)
	return dst.Write(src.scratch)
}

func rwFloat64(dst jsWriter, src *Reader) (int, error) {
	f, err := src.ReadFloat64()
	if err != nil {
		return 0, err
	}
	src.scratch = strconv.AppendFloat(src.scratch[:0], f, 'f', -1, 32)
	return dst.Write(src.scratch)
}

func rwInt(dst jsWriter, src *Reader) (int, error) {
	i, err := src.ReadInt64()
	if err != nil {
		return 0, err
	}
	src.scratch = strconv.AppendInt(src.scratch[:0], i, 10)
	return dst.Write(src.scratch)
}

func rwUint(dst jsWriter, src *Reader) (int, error) {
	u, err := src.ReadUint64()
	if err != nil {
		return 0, err
	}
	src.scratch = strconv.AppendUint(src.scratch[:0], u, 10)
	return dst.Write(src.scratch)
}

func rwBool(dst jsWriter, src *Reader) (int, error) {
	b, err := src.ReadBool()
	if err != nil {
		return 0, err
	}
	if b {
		return dst.WriteString("true")
	}
	return dst.WriteString("false")
}

func rwTime(dst jsWriter, src *Reader) (int, error) {
	t, err := src.ReadTime()
	if err != nil {
		return 0, err
	}
	bts, err := t.MarshalJSON()
	if err != nil {
		return 0, err
	}
	return dst.Write(bts)
}

func rwExtension(dst jsWriter, src *Reader) (int, error) {

	et, err := src.peekExtensionType()
	if err != nil {
		return 0, err // must not be shadowed
	}

	// Registered extensions can override the JSON encoding.
	if j, ok := extensionReg[et]; ok {
		e := j()
		err = src.ReadExtension(e)
		if err != nil {
			return 0, err
		}
		bts, err := json.Marshal(e)
		if err != nil {
			return 0, err
		}
		return dst.Write(bts)
	}

	e := RawExtension{}
	e.Type = et
	err = src.ReadExtension(&e)
	if err != nil {
		return 0, err
	}

	var n int
	err = dst.WriteByte('{')
	if err != nil {
		return n, err
	}
	n++

	var nn int
	nn, err = dst.WriteString(`"type:"`)
	n += nn
	if err != nil {
		return n, err
	}

	src.scratch = strconv.AppendInt(src.scratch[0:0], int64(e.Type), 10)
	nn, err = dst.Write(src.scratch)
	n += nn
	if err != nil {
		return n, err
	}

	nn, err = dst.WriteString(`,"data":"`)
	n += nn
	if err != nil {
		return n, err
	}

	enc := base64.NewEncoder(base64.StdEncoding, dst)

	nn, err = enc.Write(e.Data)
	n += nn
	if err != nil {
		return n, err
	}
	err = enc.Close()
	if err != nil {
		return n, err
	}
	nn, err = dst.WriteString(`"}`)
	n += nn
	return n, err

}

func rwString(dst jsWriter, src *Reader) (int, error) {

	p, err := src.R.Peek(1)
	if err != nil {
		return 0, err
	}

	lead := p[0]
	var read int

	if isfixstr(lead) {
		read = int(rfixstr(lead))
		src.R.Skip(1)
	} else {
		switch lead {
		case mstr8:
			p, err = src.R.Next(2)
			if err != nil {
				return 0, err
			}
			read = int(uint8(p[1]))
		case mstr16:
			p, err = src.R.Next(3)
			if err != nil {
				return 0, err
			}
			read = int(big.Uint16(p[1:]))
		case mstr32:
			p, err = src.R.Next(5)
			if err != nil {
				return 0, err
			}
			read = int(big.Uint32(p[1:]))
		default:
			return 0, badPrefix(StrType, lead)
		}
	}

	p, err = src.R.Next(read)
	if err != nil {
		return 0, err
	}
	return rwQuoted(dst, p)

}

func rwBytes(dst jsWriter, src *Reader) (int, error) {
	var n int
	err := dst.WriteByte('"')
	if err != nil {
		return n, err
	}
	n++
	src.scratch, err = src.ReadBytes(src.scratch[:0])
	if err != nil {
		return n, err
	}
	enc := base64.NewEncoder(base64.StdEncoding, dst)
	nn, err := enc.Write(src.scratch)
	n += nn
	if err != nil {
		return n, err
	}
	err = enc.Close()
	if err != nil {
		return n, err
	}
	err = dst.WriteByte('"')
	if err != nil {
		return n, err
	}
	n++
	return n, nil
}

func rwQuoted(dst jsWriter, s []byte) (n int, err error) {
	err = dst.WriteByte('"')
	if err != nil {
		return
	}
	n++
	var nn int
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if 0x20 <= b && b != '\\' && b != '"' && b != '<' && b != '>' && b != '&' {
				i++
				continue
			}
			if start < i {
				nn, err = dst.Write(s[start:i])
				n += nn
				if err != nil {
					return
				}
			}
			switch b {
			case '\\', '"':
				err = dst.WriteByte('\\')
				if err != nil {
					return
				}
				n++
				err = dst.WriteByte(b)
				if err != nil {
					return
				}
				n++
			case '\n':
				err = dst.WriteByte('\\')
				if err != nil {
					return
				}
				n++
				err = dst.WriteByte('n')
				if err != nil {
					return
				}
				n++
			case '\r':
				err = dst.WriteByte('\\')
				if err != nil {
					return
				}
				n++
				err = dst.WriteByte('r')
				if err != nil {
					return
				}
				n++
			default:
				nn, err = dst.WriteString(`\u00`)
				n += nn
				if err != nil {
					return
				}
				err = dst.WriteByte(hex[b>>4])
				if err != nil {
					return
				}
				n++
				err = dst.WriteByte(hex[b&0xF])
				if err != nil {
					return
				}
				n++
			}
			i++
			start = i
			continue
		}
		c, size := utf8.DecodeRune(s[i:])
		if c == utf8.RuneError && size == 1 {
			if start < i {
				nn, err = dst.Write(s[start:i])
				n += nn
				if err != nil {
					return
				}
				nn, err = dst.WriteString(`\ufffd`)
				n += nn
				if err != nil {
					return
				}
				i += size
				start = i
				continue
			}
		}
		if c == '\u2028' || c == '\u2029' {
			if start < i {
				nn, err = dst.Write(s[start:i])
				n += nn
				if err != nil {
					return
				}
				nn, err = dst.WriteString(`\u202`)
				n += nn
				if err != nil {
					return
				}
				err = dst.WriteByte(hex[c&0xF])
				if err != nil {
					return
				}
				n++
			}
		}
		i += size
	}
	if start < len(s) {
		nn, err = dst.Write(s[start:])
		n += nn
		if err != nil {
			return
		}
	}
	err = dst.WriteByte('"')
	if err != nil {
		return
	}
	n++
	return
}
