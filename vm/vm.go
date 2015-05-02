package vm

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

// ϡtheProgram is the variable that holds the program generated by the
// builder for the input PEG.
var ϡtheProgram *ϡprogram

//+pigeon following code is part of the generated parser

// ϡsentinel is a type used to define sentinel values that shouldn't
// be equal to something else.
type ϡsentinel int

const (
	// ϡmatchFailed is a sentinel value used to indicate a match failure.
	ϡmatchFailed ϡsentinel = iota - 1
)

const (
	// stack IDs, used in PUSH and POP's first argument
	ϡpstackID = iota + 1
	ϡlstackID
	ϡvstackID
	ϡistackID
	ϡastackID

	// special V stack values
	ϡvValNil    = 0
	ϡvValFailed = 1
	ϡvValEmpty  = 2
)

var (
	ϡstackNm = []string{
		ϡpstackID: "P",
		ϡlstackID: "L",
		ϡvstackID: "V",
		ϡistackID: "I",
		ϡastackID: "A",
	}
)

// special values that may be pushed on the V stack.
var ϡvSpecialValues = []interface{}{
	nil,
	ϡmatchFailed,
	[]interface{}(nil),
}

type ϡmemoizedResult struct {
	v  interface{}
	pt ϡsvpt
}

// ϡprogram is the data structure that is generated by the builder
// based on an input PEG. It contains the program information required
// to execute the grammar using the vm.
type ϡprogram struct {
	instrs []ϡinstr

	// lists
	ms []ϡmatcher
	as []func(*ϡvm) (interface{}, error)
	bs []func(*ϡvm) (bool, error)
	ss []string

	// instrToRule is the mapping of an instruction index to a rule
	// identifier (or display name) in the ss list:
	//
	// ss[instrToRule[instrIndex]] == name of the rule
	//
	// Since instructions are limited to 65535, the size of this slice
	// is bounded.
	instrToRule []int
}

// String formats the program's instructions in a human-readable format.
func (pg ϡprogram) String() string {
	var buf bytes.Buffer
	var n int

	for i, instr := range pg.instrs {
		if n > 0 {
			n -= 4
			continue
		}
		_, n, _, _, _ = instr.decode()
		n -= 3

		buf.WriteString(fmt.Sprintf("[%3d]: %s\n", i, pg.instrToString(instr, i)))
	}
	return buf.String()
}

// instrToString formats an instruction in a human-readable format, in the
// context of the program.
func (pg ϡprogram) instrToString(instr ϡinstr, ix int) string {
	var buf bytes.Buffer

	op, _, a0, a1, a2 := instr.decode()
	rule := pg.ruleNameAt(ix)
	if rule == "" {
		rule = "<none>"
	}
	stdFmt := "%s.%s"
	switch op {
	case ϡopCall, ϡopCumulOrF, ϡopReturn, ϡopExit, ϡopRestore,
		ϡopRestoreIfF, ϡopNilIfF, ϡopNilIfT:
		buf.WriteString(fmt.Sprintf(stdFmt, rule, op))
	case ϡopCallA, ϡopCallB, ϡopJump, ϡopJumpIfT, ϡopJumpIfF, ϡopPopVJumpIfF, ϡopTakeLOrJump:
		buf.WriteString(fmt.Sprintf(stdFmt+" %d", rule, op, a0))
	case ϡopPush:
		buf.WriteString(fmt.Sprintf(stdFmt+" %s %d %d", rule, op, ϡstackNm[a0], a1, a2))
	case ϡopPop:
		buf.WriteString(fmt.Sprintf(stdFmt+" %s", rule, op, ϡstackNm[a0]))
	case ϡopMatch:
		buf.WriteString(fmt.Sprintf(stdFmt+" %d (%s)", rule, op, a0, pg.ms[a0]))
	case ϡopStoreIfT:
		buf.WriteString(fmt.Sprintf(stdFmt+" %d (%s)", rule, op, a0, pg.ss[a0]))
	default:
		buf.WriteString(fmt.Sprintf(stdFmt+" %d %d", rule, op, a0, a1))
	}
	return buf.String()
}

// ruleNameAt returns the name of the rule that contains the instruction
// index. It returns an empty string is the instruction is not part of a
// rule (bootstrap instruction, invalid index).
func (pg ϡprogram) ruleNameAt(instrIx int) string {
	if instrIx < 0 || instrIx >= len(pg.instrToRule) {
		return ""
	}
	ssIx := pg.instrToRule[instrIx]
	if ssIx < 0 || ssIx >= len(pg.ss) {
		return ""
	}
	return pg.ss[ssIx]
}

// ϡvm holds the state to execute a compiled grammar.
type ϡvm struct {
	// input
	filename string
	parser   *ϡparser

	// options
	debug   bool
	memoize bool
	recover bool
	// TODO : no bounds checking option (for stacks)? benchmark to see if it's worth it.

	// program data
	pc  int
	pg  *ϡprogram
	cur current

	// stacks
	p ϡpstack
	l ϡlstack
	v ϡvstack
	i ϡistack
	a ϡastack

	// TODO: memoization...
	// TODO: farthest failure position

	// error list
	errs errList
}

// setOptions applies the options in sequence on the vm. It returns the
// vm to allow for chaining calls.
func (v *ϡvm) setOptions(opts []Option) *ϡvm {
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// addErr adds the error at the current parser position, without rule name
// information.
func (v *ϡvm) addErr(err error) {
	v.addErrAt(err, -1, v.parser.pt.position)
}

// addErrAt adds the error at the specified position, for the instruction
// at instrIx.
func (v *ϡvm) addErrAt(err error, instrIx int, pos position) {
	var buf bytes.Buffer
	if v.filename != "" {
		buf.WriteString(v.filename)
	}
	if buf.Len() > 0 {
		buf.WriteString(":")
	}
	buf.WriteString(fmt.Sprintf("%s", pos))

	ruleNm := v.pg.ruleNameAt(instrIx)
	if ruleNm != "" {
		buf.WriteString(": ")
		buf.WriteString("rule " + ruleNm)
	}

	pe := &parserError{Inner: err, ϡprefix: buf.String()}
	v.errs.ϡadd(pe)
}

// dumpSnapshot writes a dump of the current VM state to w.
func (v *ϡvm) dumpSnapshot(w io.Writer) {
	var buf bytes.Buffer

	if v.filename != "" {
		buf.WriteString(v.filename + ":")
	}
	buf.WriteString(fmt.Sprintf("%s: %#U\n", v.parser.pt.position, v.parser.pt.rn))

	// write the next 5 instructions
	ix := v.pc - 1
	if ix > 0 {
		ix--
	}
	stdFmt := ". [%d]: %s"
	for i := 0; i < 5; i++ {
		stdFmt := stdFmt
		if ix == v.pc-1 {
			stdFmt = ">" + stdFmt[1:]
		}
		instr := v.pg.instrs[ix]
		op, n, _, _, _ := instr.decode()
		switch op {
		case ϡopCall:
			buf.WriteString(fmt.Sprintf(stdFmt+"\n", ix, v.pg.instrToString(instr, ix)))
			ix = v.i.pop() // continue with instructions at this index
			v.i.push(ix)
			continue
		default:
			buf.WriteString(fmt.Sprintf(stdFmt+"\n", ix, v.pg.instrToString(instr, ix)))
		}
		ix++
		n -= 3
		for n > 0 {
			ix++
			n -= 4
		}
		if ix >= len(v.pg.instrs) {
			break
		}
	}

	// print the stacks
	buf.WriteString("[ P: ")
	for i := 0; i < 3; i++ {
		if len(v.p) <= i {
			break
		}
		if i > 0 {
			buf.WriteString(", ")
		}
		val := v.p[len(v.p)-i-1]
		buf.WriteString(fmt.Sprintf("\"%v\"", val))
	}
	buf.WriteString(" ]\n[ V: ")
	for i := 0; i < 3; i++ {
		if len(v.v) <= i {
			break
		}
		if i > 0 {
			buf.WriteString(", ")
		}
		val := v.v[len(v.v)-i-1]
		buf.WriteString(fmt.Sprintf("%#v", val))
	}
	buf.WriteString(" ]\n[ I: ")
	for i := 0; i < 3; i++ {
		if len(v.i) <= i {
			break
		}
		if i > 0 {
			buf.WriteString(", ")
		}
		val := v.i[len(v.i)-i-1]
		buf.WriteString(fmt.Sprintf("%d", val))
	}
	buf.WriteString(" ]\n[ L: ")
	for i := 0; i < 3; i++ {
		if len(v.l) <= i {
			break
		}
		if i > 0 {
			buf.WriteString(", ")
		}
		val := v.l[len(v.l)-i-1]
		buf.WriteString(fmt.Sprintf("%v", val))
	}
	buf.WriteString(" ]\n")
	fmt.Fprintln(w, buf.String())
}

// run executes the provided program in this VM, and returns the result.
func (v *ϡvm) run(pg *ϡprogram) (interface{}, error) {
	v.pg = pg
	ret := v.dispatch()

	// if the match failed, translate that to a nil result and make
	// sure it returns an error
	if ret == ϡmatchFailed {
		ret = nil
		if len(v.errs) == 0 {
			v.addErr(errNoMatch)
		}
	}

	return ret, v.errs.ϡerr()
}

// dispatch is the proper execution method of the VM, it loops over
// the instructions and executes each opcode.
func (v *ϡvm) dispatch() interface{} {
	var instrPath []int
	if v.debug {
		fmt.Fprintln(os.Stderr, v.pg)
		defer func() {
			var buf bytes.Buffer

			buf.WriteString("Execution path:\n")
			for _, ix := range instrPath {
				buf.WriteString(fmt.Sprintf("[%3d]: %s\n", ix, v.pg.instrToString(v.pg.instrs[ix], ix)))
			}
			fmt.Fprintln(os.Stderr, buf.String())
		}()
	}

	// move to first rune before starting the loop
	v.parser.read()
	for {
		// fetch and decode the instruction
		instr := v.pg.instrs[v.pc]
		op, n, a0, a1, a2 := instr.decode()
		instrPath = append(instrPath, v.pc)

		// increment program counter
		v.pc++

		switch op {
		case ϡopCall:
			if v.debug {
				v.dumpSnapshot(os.Stderr)
			}
			ix := v.i.pop()
			v.i.push(v.pc)
			v.pc = ix

		case ϡopCallA:
			if v.debug {
				v.dumpSnapshot(os.Stderr)
			}
			v.v.pop()
			start := v.p.pop()
			v.cur.pos = start.position
			v.cur.text = v.parser.sliceFrom(start)
			if a0 >= len(v.pg.as) {
				panic(fmt.Sprintf("invalid %s argument: %d", op, a0))
			}
			fn := v.pg.as[a0]
			val, err := fn(v)
			if err != nil {
				v.addErrAt(err, v.pc-1, start.position)
			}
			v.v.push(val)

		case ϡopCallB:
			if v.debug {
				v.dumpSnapshot(os.Stderr)
			}
			v.cur.pos = v.parser.pt.position
			v.cur.text = nil
			if a0 >= len(v.pg.bs) {
				panic(fmt.Sprintf("invalid %s argument: %d", op, a0))
			}
			fn := v.pg.bs[a0]
			val, err := fn(v)
			if err != nil {
				v.addErrAt(err, v.pc-1, v.parser.pt.position)
			}
			if !val {
				v.v.push(ϡmatchFailed)
				break
			}
			v.v.push(nil)

		case ϡopCumulOrF:
			va, vb := v.v.pop(), v.v.pop()
			if va == ϡmatchFailed {
				v.v.push(ϡmatchFailed)
				break
			}
			switch vb := vb.(type) {
			case []interface{}:
				vb = append(vb, va)
				v.v.push(vb)
			case ϡsentinel:
				v.v.push([]interface{}{va})
			default:
				panic(fmt.Sprintf("invalid %s value type on the V stack: %T", op, vb))
			}

		case ϡopExit:
			return v.v.pop()

		case ϡopNilIfF:
			if top := v.v.pop(); top == ϡmatchFailed {
				v.v.push(nil)
				break
			}
			v.v.push(ϡmatchFailed)

		case ϡopNilIfT:
			if top := v.v.pop(); top != ϡmatchFailed {
				v.v.push(nil)
				break
			}
			v.v.push(ϡmatchFailed)

		case ϡopJump:
			v.pc = a0

		case ϡopJumpIfF:
			if top := v.v.peek(); top == ϡmatchFailed {
				v.pc = a0
			}

		case ϡopJumpIfT:
			if top := v.v.peek(); top != ϡmatchFailed {
				v.pc = a0
			}

		case ϡopMatch:
			start := v.parser.pt
			if a0 >= len(v.pg.ms) {
				panic(fmt.Sprintf("invalid %s argument: %d", op, a0))
			}
			m := v.pg.ms[a0]
			if ok := m.match(v.parser); ok {
				v.v.push(v.parser.sliceFrom(start))
				break
			}
			v.v.push(ϡmatchFailed)
			v.parser.pt = start

		case ϡopPop:
			switch a0 {
			case ϡlstackID:
				v.l.pop()
			case ϡpstackID:
				v.p.pop()
			case ϡastackID:
				v.a.pop()
			default:
				panic(fmt.Sprintf("invalid %s argument: %d", op, a0))
			}

		case ϡopPopVJumpIfF:
			if top := v.v.peek(); top == ϡmatchFailed {
				v.v.pop()
				v.pc = a0
			}

		case ϡopPush:
			switch a0 {
			case ϡpstackID:
				v.p.push(v.parser.pt)
			case ϡistackID:
				v.i.push(a1)
			case ϡvstackID:
				if a1 >= len(ϡvSpecialValues) {
					panic(fmt.Sprintf("invalid %s V stack argument: %d", op, a1))
				}
				v.v.push(ϡvSpecialValues[a1])
			case ϡastackID:
				v.a.push()
			case ϡlstackID:
				// n = L args to push + 1, for the lstackID
				n--
				ar := make([]int, n)
				src := []int{0, 0, a2, a1}
				for i := 0; i < n; i++ {
					lsrc := len(src)
					ar[i] = src[lsrc-1]
					src = src[:lsrc-1]
					if lsrc-1 == 0 && i < n-1 {
						// need more
						instr := v.pg.instrs[v.pc]
						a0, a1, a2, a3 := instr.decodeLs()
						src = append(src, a3, a2, a1, a0)
						v.pc++
					}
				}
				v.l.push(ar)
			default:
				panic(fmt.Sprintf("invalid %s argument: %d", op, a0))
			}

		case ϡopRestore:
			pt := v.p.pop()
			v.parser.pt = pt

		case ϡopRestoreIfF:
			pt := v.p.pop()
			if top := v.v.peek(); top == ϡmatchFailed {
				v.parser.pt = pt
			}

		case ϡopReturn:
			ix := v.i.pop()
			v.pc = ix

		case ϡopStoreIfT:
			if top := v.v.peek(); top != ϡmatchFailed {
				// get the label name
				if a0 >= len(v.pg.ss) {
					panic(fmt.Sprintf("invalid %s argument: %d", op, a0))
				}
				lbl := v.pg.ss[a0]

				// store the value
				as := v.a.peek()
				as.add(lbl, top)
			}

		case ϡopTakeLOrJump:
			ix := v.l.take()
			if ix < 0 {
				v.pc = a0
				break
			}
			v.i.push(ix)

		default:
			panic(fmt.Sprintf("unknown opcode %s", op))
		}
	}
}
