package gen

import (
	"fmt"
	"io"
	"strconv"

	"github.com/dchenk/msgp/msgp"
)

type sizeState uint8

const (
	// need to write "s = ..."
	assign sizeState = iota

	// need to write "s += ..."
	add

	// can just append "+ ..."
	expr
)

func sizes(w io.Writer) *sizeGen {
	return &sizeGen{
		p:     printer{w: w},
		state: assign,
	}
}

type sizeGen struct {
	passes
	p     printer
	state sizeState
}

func (s *sizeGen) Method() Method { return Size }

func (s *sizeGen) Apply(dirs []string) error {
	return nil
}

func builtinSize(typ string) string {
	return "msgp." + typ + "Size"
}

// this lets us chain together addition operations where possible
func (s *sizeGen) addConstant(sz string) {
	if !s.p.ok() {
		return
	}

	switch s.state {
	case assign:
		s.p.print("\ns = " + sz)
		s.state = expr
		return
	case add:
		s.p.print("\ns += " + sz)
		s.state = expr
		return
	case expr:
		s.p.print(" + " + sz)
		return
	}

	panic("unknown size state")
}

func (s *sizeGen) Execute(p Elem) error {
	if !s.p.ok() {
		return s.p.err
	}

	p = s.applyAll(p)
	if p == nil || !isPrintable(p) {
		return nil
	}

	s.p.comment("Msgsize returns an upper bound estimate of the number of bytes occupied by the serialized message")

	s.p.printf("\nfunc (%s %s) Msgsize() (s int) {", p.Varname(), imutMethodReceiver(p))
	s.state = assign
	next(s, p)
	s.p.nakedReturn()
	return s.p.err
}

func (s *sizeGen) gStruct(st *Struct) {
	if !s.p.ok() {
		return
	}

	nfields := uint32(len(st.Fields))

	if st.AsTuple {
		data := msgp.AppendArrayHeader(nil, nfields)
		s.addConstant(strconv.Itoa(len(data)))
		for i := range st.Fields {
			if !s.p.ok() {
				return
			}
			next(s, st.Fields[i].fieldElem)
		}
	} else {
		data := msgp.AppendMapHeader(nil, nfields)
		s.addConstant(strconv.Itoa(len(data)))
		for i := range st.Fields {
			data = data[:0]
			data = msgp.AppendString(data, st.Fields[i].fieldTag)
			s.addConstant(strconv.Itoa(len(data)))
			next(s, st.Fields[i].fieldElem)
		}
	}
}

func (s *sizeGen) gPtr(p *Ptr) {
	s.state = add // inner must use add
	s.p.printf("\nif %s == nil {\ns += msgp.NilSize\n} else {", p.Varname())
	next(s, p.Value)
	s.state = add // closing block; reset to add
	s.p.closeBlock()
}

func (s *sizeGen) gSlice(sl *Slice) {
	if !s.p.ok() {
		return
	}

	s.addConstant(builtinSize(arrayHeader))

	// if the slice's element is a fixed size
	// (e.g. float64, [32]int, etc.), then
	// print the length times the element size directly
	if str, ok := fixedSizeExpr(sl.Els); ok {
		s.addConstant(fmt.Sprintf("(%s * (%s))", lenExpr(sl), str))
		return
	}

	// add inside the range block, and immediately after
	s.state = add
	s.p.rangeBlock(sl.Index, sl.Varname(), s, sl.Els)
	s.state = add
}

func (s *sizeGen) gArray(a *Array) {
	if !s.p.ok() {
		return
	}

	s.addConstant(builtinSize(arrayHeader))

	// If the array's children are a fixed size, we can compile
	// an expression that always represents the array's wire size.
	if str, ok := fixedSizeExpr(a); ok {
		s.addConstant(str)
		return
	}

	s.state = add
	s.p.rangeBlock(a.Index, a.Varname(), s, a.Els)
	s.state = add
}

func (s *sizeGen) gMap(m *Map) {
	s.addConstant(builtinSize(mapHeader))
	s.p.printf("\nif %s != nil {", m.Varname())
	s.p.printf("\nfor %s, %s := range %s {", m.KeyIndx, m.ValIndx, m.Varname())
	s.p.printf("\n_ = %s", m.ValIndx) // we may not use the value
	s.p.printf("\ns += msgp.StringPrefixSize + len(%s)", m.KeyIndx)
	s.state = expr
	next(s, m.Value)
	s.p.closeBlock()
	s.p.closeBlock()
	s.state = add
}

func (s *sizeGen) gBase(b *BaseElem) {
	if !s.p.ok() {
		return
	}
	if b.Convert && b.ShimMode == Convert {
		s.state = add
		vname := randIdent()
		s.p.declare(vname, b.BaseType())

		// Ensure we don't get "unused variable" errors from outer slice iterations.
		s.p.print("\n_ = " + b.Varname())

		s.p.printf("\ns += %s", baseSizeExpr(b.Value, vname, b.BaseName()))
		s.state = expr

	} else {
		vname := b.Varname()
		if b.Convert {
			vname = b.toBaseConvert()
		}
		s.addConstant(baseSizeExpr(b.Value, vname, b.BaseName()))
	}
}

// lenExpr returns "len(sliceName)"
func lenExpr(sl *Slice) string {
	return "len(" + sl.Varname() + ")"
}

// fixedSize says if a given primitive is always the same (max) size on the wire.
func fixedSize(p primitive) bool {
	return p != Intf && p != Ext && p != IDENT && p != Bytes && p != String
}

// stripRef strips the address operator "&" from s.
func stripRef(s string) string {
	if s[0] == '&' {
		return s[1:]
	}
	return s
}

// return a fixed-size expression, if possible.
// only possible for *BaseElem and *Array.
// returns (expr, ok)
func fixedSizeExpr(e Elem) (string, bool) {
	switch e := e.(type) {
	case *Array:
		if str, ok := fixedSizeExpr(e.Els); ok {
			return fmt.Sprintf("(%s * (%s))", e.Size, str), true
		}
	case *BaseElem:
		if fixedSize(e.Value) {
			return builtinSize(e.BaseName()), true
		}
	case *Struct:
		var str string
		for _, f := range e.Fields {
			if fs, ok := fixedSizeExpr(f.fieldElem); ok {
				if str == "" {
					str = fs
				} else {
					str += "+" + fs
				}
			} else {
				return "", false
			}
		}
		var hdrlen int
		mhdr := msgp.AppendMapHeader(nil, uint32(len(e.Fields)))
		hdrlen += len(mhdr)
		var strbody []byte
		for _, f := range e.Fields {
			strbody = msgp.AppendString(strbody[:0], f.fieldTag)
			hdrlen += len(strbody)
		}
		return fmt.Sprintf("%d + %s", hdrlen, str), true
	}
	return "", false
}

// print size expression of a variable name
func baseSizeExpr(value primitive, vname, basename string) string {
	switch value {
	case Ext:
		return "msgp.ExtensionPrefixSize + " + stripRef(vname) + ".Len()"
	case Intf:
		return "msgp.GuessSize(" + vname + ")"
	case IDENT:
		return vname + ".Msgsize()"
	case Bytes:
		return "msgp.BytesPrefixSize + len(" + vname + ")"
	case String:
		return "msgp.StringPrefixSize + len(" + vname + ")"
	default:
		return builtinSize(basename)
	}
}
