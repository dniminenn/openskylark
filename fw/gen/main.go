// gen turns a CT7601 firmware image into sdas8051 assembly that assembles
// back to the same bytes. Code is found by following control flow from the
// reset and interrupt vectors plus the entries listed in the notes file.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

type flow int

const (
	next   flow = iota // falls through
	jump               // unconditional, absolute or relative target
	branch             // conditional, target plus fall through
	call
	ret
	indirect // jmp @a+dptr
	bad
)

type inst struct {
	size   int
	flow   flow
	target int    // for jump, branch and call
	text   string // operands use %T for the target label
}

var regOps = map[int]string{
	0x00: "inc %s", 0x10: "dec %s", 0x20: "add a,%s", 0x30: "addc a,%s",
	0x40: "orl a,%s", 0x50: "anl a,%s", 0x60: "xrl a,%s", 0x90: "subb a,%s",
	0xc0: "xch a,%s", 0xe0: "mov a,%s", 0xf0: "mov %s,a",
}

var single = map[byte]string{
	0x00: "nop", 0x03: "rr a", 0x13: "rrc a", 0x23: "rl a", 0x33: "rlc a",
	0x04: "inc a", 0x14: "dec a", 0x84: "div ab", 0xa4: "mul ab",
	0xc4: "swap a", 0xd4: "da a", 0xe4: "clr a", 0xf4: "cpl a",
	0x83: "movc a,@a+pc", 0x93: "movc a,@a+dptr", 0xa3: "inc dptr",
	0xb3: "cpl c", 0xc3: "clr c", 0xd3: "setb c",
	0xe0: "movx a,@dptr", 0xf0: "movx @dptr,a",
	0xe2: "movx a,@r0", 0xe3: "movx a,@r1", 0xf2: "movx @r0,a", 0xf3: "movx @r1,a",
}

var immA = map[byte]string{0x24: "add", 0x34: "addc", 0x44: "orl", 0x54: "anl", 0x64: "xrl", 0x74: "mov", 0x94: "subb"}
var dirA = map[byte]string{0x25: "add", 0x35: "addc", 0x45: "orl", 0x55: "anl", 0x65: "xrl", 0x95: "subb", 0xc5: "xch", 0xe5: "mov"}
var bitOps = map[byte]string{0x72: "orl c,%s", 0x82: "anl c,%s", 0x92: "mov %s,c", 0xa2: "mov c,%s", 0xb2: "cpl %s", 0xc2: "clr %s", 0xd2: "setb %s", 0xa0: "orl c,/%s", 0xb0: "anl c,/%s"}
var relOps = map[byte]string{0x40: "jc", 0x50: "jnc", 0x60: "jz", 0x70: "jnz"}
var bitRel = map[byte]string{0x10: "jbc", 0x20: "jb", 0x30: "jnb"}

func hex8(b byte) string { return fmt.Sprintf("0x%02x", b) }

// Standard 8051 registers. The CT7601's own SFRs are not documented and stay
// numeric.
var sfr = map[byte]string{
	0x80: "p0", 0x81: "sp", 0x82: "dpl", 0x83: "dph", 0x87: "pcon", 0x88: "tcon",
	0x89: "tmod", 0x8a: "tl0", 0x8b: "tl1", 0x8c: "th0", 0x8d: "th1", 0x90: "p1",
	0x98: "scon", 0x99: "sbuf", 0xa0: "p2", 0xa8: "ie", 0xb0: "p3", 0xb8: "ip",
	0xd0: "psw", 0xe0: "acc", 0xf0: "b",
}

func direct(b byte) string {
	if n, ok := sfr[b]; ok {
		return n
	}
	return hex8(b)
}

func bit(b byte) string {
	if n, ok := sfr[b&0xf8]; ok {
		return fmt.Sprintf("%s.%d", n, b&7)
	}
	return hex8(b)
}

// decode reads one instruction at pc. ok is false past the end of the image
// or on the one undefined opcode.
func decode(img []byte, pc int) (in inst, ok bool) {
	if pc >= len(img) {
		return inst{flow: bad}, false
	}
	op := img[pc]
	arg := func(i int) byte {
		if pc+i < len(img) {
			return img[pc+i]
		}
		ok = false
		return 0
	}
	ok = true
	rel := func(size int) int { return pc + size + int(int8(arg(size-1))) }
	low := op & 0x0f

	switch {
	case op == 0xa5:
		return inst{flow: bad}, false
	case low == 1: // ajmp, acall
		t := (pc+2)&0xf800 | int(op>>5)<<8 | int(arg(1))
		if op&0x10 != 0 {
			in = inst{2, call, t, "acall %T"}
		} else {
			in = inst{2, jump, t, "ajmp %T"}
		}
	case op == 0x02:
		in = inst{3, jump, int(arg(1))<<8 | int(arg(2)), "ljmp %T"}
	case op == 0x12:
		in = inst{3, call, int(arg(1))<<8 | int(arg(2)), "lcall %T"}
	case op == 0x22:
		in = inst{1, ret, 0, "ret"}
	case op == 0x32:
		in = inst{1, ret, 0, "reti"}
	case op == 0x73:
		in = inst{1, indirect, 0, "jmp @a+dptr"}
	case op == 0x80:
		in = inst{2, jump, rel(2), "sjmp %T"}
	case relOps[op] != "":
		in = inst{2, branch, rel(2), relOps[op] + " %T"}
	case bitRel[op] != "":
		in = inst{3, branch, rel(3), bitRel[op] + " " + bit(arg(1)) + ",%T"}
	case op == 0x90:
		in = inst{3, next, 0, fmt.Sprintf("mov dptr,#0x%02x%02x", arg(1), arg(2))}
	case op == 0xc0:
		in = inst{2, next, 0, "push " + direct(arg(1))}
	case op == 0xd0:
		in = inst{2, next, 0, "pop " + direct(arg(1))}
	case bitOps[op] != "":
		in = inst{2, next, 0, fmt.Sprintf(bitOps[op], bit(arg(1)))}
	case single[op] != "":
		in = inst{1, next, 0, single[op]}
	case immA[op] != "":
		in = inst{2, next, 0, immA[op] + " a,#" + hex8(arg(1))}
	case dirA[op] != "":
		in = inst{2, next, 0, dirA[op] + " a," + direct(arg(1))}
	case op == 0x05:
		in = inst{2, next, 0, "inc " + direct(arg(1))}
	case op == 0x15:
		in = inst{2, next, 0, "dec " + direct(arg(1))}
	case op == 0x42, op == 0x52, op == 0x62:
		in = inst{2, next, 0, map[byte]string{0x42: "orl", 0x52: "anl", 0x62: "xrl"}[op] + " " + direct(arg(1)) + ",a"}
	case op == 0x43, op == 0x53, op == 0x63:
		in = inst{3, next, 0, map[byte]string{0x43: "orl", 0x53: "anl", 0x63: "xrl"}[op] + " " + direct(arg(1)) + ",#" + hex8(arg(2))}
	case op == 0x75:
		in = inst{3, next, 0, "mov " + direct(arg(1)) + ",#" + hex8(arg(2))}
	case op == 0x85: // encoded source first
		in = inst{3, next, 0, "mov " + direct(arg(2)) + "," + direct(arg(1))}
	case op == 0xf5:
		in = inst{2, next, 0, "mov " + direct(arg(1)) + ",a"}
	case op == 0xb4:
		in = inst{3, branch, rel(3), "cjne a,#" + hex8(arg(1)) + ",%T"}
	case op == 0xb5:
		in = inst{3, branch, rel(3), "cjne a," + direct(arg(1)) + ",%T"}
	case op == 0xd5:
		in = inst{3, branch, rel(3), "djnz " + direct(arg(1)) + ",%T"}
	case low >= 6: // @r0, @r1, r0..r7
		r := fmt.Sprintf("r%d", low-8)
		if low < 8 {
			r = fmt.Sprintf("@r%d", low-6)
		}
		switch hi := int(op & 0xf0); hi {
		case 0x70:
			in = inst{2, next, 0, "mov " + r + ",#" + hex8(arg(1))}
		case 0x80:
			in = inst{2, next, 0, "mov " + direct(arg(1)) + "," + r}
		case 0xa0:
			in = inst{2, next, 0, "mov " + r + "," + direct(arg(1))}
		case 0xb0:
			in = inst{3, branch, rel(3), "cjne " + r + ",#" + hex8(arg(1)) + ",%T"}
		case 0xd0:
			if low < 8 {
				in = inst{1, next, 0, "xchd a," + r}
			} else {
				in = inst{2, branch, rel(2), "djnz " + r + ",%T"}
			}
		default:
			in = inst{1, next, 0, fmt.Sprintf(regOps[hi], r)}
		}
	default:
		return inst{flow: bad}, false
	}
	return in, ok
}

type notes struct {
	entries []int
	names   map[int]string
	ccase   map[int]bool // compiler switch helpers that read a table after the call
	inline  map[int]int  // helpers that read N constant bytes after the call, then return past them
}

func readNotes(path string) (notes, error) {
	n := notes{names: map[int]string{}, ccase: map[int]bool{}, inline: map[int]int{}}
	f, err := os.Open(path)
	if err != nil {
		return n, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		fields := strings.Fields(strings.SplitN(sc.Text(), "#", 2)[0])
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 {
			return n, fmt.Errorf("%s:%d: want KIND ADDR [NAME]", path, line)
		}
		addr, err := strconv.ParseInt(fields[1], 0, 32)
		if err != nil {
			return n, fmt.Errorf("%s:%d: %v", path, line, err)
		}
		switch fields[0] {
		case "entry":
			n.entries = append(n.entries, int(addr))
		case "ccase":
			n.ccase[int(addr)] = true
			n.entries = append(n.entries, int(addr))
		case "inline": // ADDR COUNT
			if len(fields) != 3 {
				return n, fmt.Errorf("%s:%d: want inline ADDR COUNT", path, line)
			}
			count, err := strconv.Atoi(fields[2])
			if err != nil {
				return n, fmt.Errorf("%s:%d: %v", path, line, err)
			}
			n.inline[int(addr)] = count
			n.entries = append(n.entries, int(addr))
			continue
		case "jumptable": // ADDR COUNT: a run of ljmp instructions reached by jmp @a+dptr
			if len(fields) != 3 {
				return n, fmt.Errorf("%s:%d: want jumptable ADDR COUNT", path, line)
			}
			count, err := strconv.Atoi(fields[2])
			if err != nil {
				return n, fmt.Errorf("%s:%d: %v", path, line, err)
			}
			for i := 0; i < count; i++ {
				n.entries = append(n.entries, int(addr)+3*i)
			}
			continue
		case "name":
		default:
			return n, fmt.Errorf("%s:%d: unknown kind %q", path, line, fields[0])
		}
		if len(fields) > 2 {
			n.names[int(addr)] = fields[2]
		}
	}
	return n, sc.Err()
}

type caseTable struct {
	rows  [][2]int // target, value
	deflt int
	size  int
}

// readCase parses the table a ccase helper expects after its call:
// {target16, value8}... then 0x0000 and the default target.
func readCase(img []byte, pc int) (caseTable, bool) {
	var t caseTable
	for p := pc; p+3 < len(img); p += 3 {
		target := int(img[p])<<8 | int(img[p+1])
		if target == 0 {
			t.deflt = int(img[p+2])<<8 | int(img[p+3])
			t.size = p + 4 - pc
			return t, true
		}
		t.rows = append(t.rows, [2]int{target, int(img[p+2])})
	}
	return t, false
}

func main() {
	notesPath := flag.String("notes", "", "annotation file")
	indexPath := flag.String("index", "", "also write a per-subroutine summary here")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: gen [-notes FILE] IMAGE > listing.asm")
		os.Exit(2)
	}
	img, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var nt notes
	nt.names, nt.ccase, nt.inline = map[int]string{}, map[int]bool{}, map[int]int{}
	if *notesPath != "" {
		if nt, err = readNotes(*notesPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	const (
		isCode = 1 << iota // first byte of an instruction
		isBody             // later byte of an instruction
		isTable
		isInline
	)
	kind := make([]byte, len(img))
	insts := map[int]inst{}
	tables := map[int]caseTable{}
	calls := map[int]bool{}
	callers := map[int]int{}
	jumps := map[int]bool{}
	var unresolved []int

	work := append([]int{0x0000, 0x0003, 0x000b, 0x0013, 0x001b, 0x0023, 0x002b}, nt.entries...)
	// Only vectors that hold a jump are real; the gaps between them hold code.
	isVector := func(a int) bool { return a == 0 || img[a] == 0x02 }
	for i := 0; i < len(work); i++ {
		pc := work[i]
		if pc < 0x30 && i < 7 && !isVector(pc) {
			continue
		}
		for pc < len(img) && kind[pc] == 0 {
			in, ok := decode(img, pc)
			if !ok {
				break
			}
			clash := false
			for j := 1; j < in.size; j++ {
				clash = clash || kind[pc+j] != 0
			}
			if clash {
				break
			}
			kind[pc] = isCode
			for j := 1; j < in.size; j++ {
				kind[pc+j] = isBody
			}
			insts[pc] = in
			switch in.flow {
			case jump:
				jumps[in.target] = true
				work = append(work, in.target)
			case branch:
				jumps[in.target] = true
				work = append(work, in.target)
			case call:
				calls[in.target] = true
				callers[in.target]++
				work = append(work, in.target)
				if nt.ccase[in.target] {
					if t, ok := readCase(img, pc+in.size); ok {
						tables[pc+in.size] = t
						for j := 0; j < t.size; j++ {
							kind[pc+in.size+j] = isTable
						}
						for _, r := range t.rows {
							jumps[r[0]] = true
							work = append(work, r[0])
						}
						jumps[t.deflt] = true
						work = append(work, t.deflt)
					}
				}
			case indirect:
				unresolved = append(unresolved, pc)
			}
			if n := nt.inline[in.target]; in.flow == call && n > 0 && pc+in.size+n <= len(img) {
				for j := 0; j < n; j++ {
					kind[pc+in.size+j] = isInline
				}
				pc += n
			}
			if in.flow == jump || in.flow == ret || in.flow == indirect || nt.ccase[in.target] && in.flow == call {
				break
			}
			pc += in.size
		}
	}

	label := func(a int) string {
		if n, ok := nt.names[a]; ok {
			return n
		}
		if calls[a] {
			return fmt.Sprintf("sub_%04x", a)
		}
		return fmt.Sprintf("loc_%04x", a)
	}
	// A target inside an instruction or in data cannot carry a label.
	ref := func(a int) string {
		if a < len(img) && kind[a] == isCode {
			return label(a)
		}
		return fmt.Sprintf("0x%04x", a)
	}

	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	fmt.Fprintln(w, "; Generated by fw/gen. Edit fw/notes.txt and regenerate.")
	fmt.Fprintln(w, "\t.module fw\n\t.area CSEG (ABS,CODE)\n\t.org 0x0000")
	codeBytes := 0
	for pc := 0; pc < len(img); {
		switch kind[pc] {
		case isCode:
			in := insts[pc]
			if calls[pc] || jumps[pc] || nt.names[pc] != "" {
				if n := callers[pc]; n > 0 {
					fmt.Fprintf(w, "\n; called from %d place(s)\n", n)
				}
				fmt.Fprintf(w, "%s:\n", label(pc))
			}
			fmt.Fprintf(w, "\t%-28s ; %04x\n", strings.Replace(in.text, "%T", ref(in.target), 1), pc)
			codeBytes += in.size
			pc += in.size
		case isInline:
			end := pc
			for end < len(img) && kind[end] == isInline {
				end++
			}
			parts, val := []string{}, 0
			for _, b := range img[pc:end] {
				parts = append(parts, hex8(b))
				val = val<<8 | int(b)
			}
			fmt.Fprintf(w, "\t.db %-25s ; %04x = %d\n", strings.Join(parts, ","), pc, val)
			pc = end
		case isTable:
			t := tables[pc]
			for _, r := range t.rows {
				fmt.Fprintf(w, "\t.dw %s\n\t.db 0x%02x\n", ref(r[0]), r[1])
			}
			fmt.Fprintf(w, "\t.dw 0\n\t.dw %s\n", ref(t.deflt))
			pc += t.size
		default:
			end := pc
			for end < len(img) && kind[end] == 0 {
				end++
			}
			fmt.Fprintf(w, "\n; data %04x-%04x\n", pc, end-1)
			if nt.names[pc] != "" {
				fmt.Fprintf(w, "%s:\n", nt.names[pc])
			}
			for pc < end {
				// Printable runs become text; everything else is bytes.
				run := pc
				for run < end && img[run] >= 0x20 && img[run] < 0x7f && img[run] != '"' && img[run] != '\\' {
					run++
				}
				if run-pc >= 4 {
					fmt.Fprintf(w, "\t.ascii \"%s\"%*s ; %04x\n", img[pc:run], max(1, 44-(run-pc)), "", pc)
					pc = run
					continue
				}
				stop := min(pc+8, end)
				for i := pc + 1; i < stop; i++ {
					n := i
					for n < end && img[n] >= 0x20 && img[n] < 0x7f && img[n] != '"' && img[n] != '\\' {
						n++
					}
					if n-i >= 4 {
						stop = i
					}
				}
				parts := make([]string, stop-pc)
				for i := range parts {
					parts[i] = hex8(img[pc+i])
				}
				fmt.Fprintf(w, "\t.db %-47s ; %04x\n", strings.Join(parts, ","), pc)
				pc = stop
			}
			pc = end
		}
	}

	if *indexPath != "" {
		if err := writeIndex(*indexPath, img, kind, insts, calls, callers, label); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	sort.Ints(unresolved)
	fmt.Fprintf(os.Stderr, "code %d bytes, tables %d, data %d bytes, %d subroutines\n", codeBytes, len(tables), len(img)-codeBytes, len(calls))
	fmt.Fprintf(os.Stderr, "indirect jumps not followed: %x\n", unresolved)
}

// writeIndex summarises each subroutine: size, callers, what it calls, the
// XDATA addresses it loads into DPTR and the code strings it points at.
func writeIndex(path string, img, kind []byte, insts map[int]inst, calls map[int]bool, callers map[int]int, label func(int) string) error {
	var subs []int
	for a := range calls {
		if a < len(img) && kind[a] == 1 {
			subs = append(subs, a)
		}
	}
	sort.Ints(subs)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	text := func(a int) string {
		end := a
		for end < len(img) && img[end] >= 0x20 && img[end] < 0x7f {
			end++
		}
		if end-a < 3 {
			return ""
		}
		return string(img[a:min(end, a+28)])
	}
	uniq := func(xs []string) string {
		seen, out := map[string]bool{}, []string{}
		for _, x := range xs {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
		return strings.Join(out, " ")
	}
	for i, a := range subs {
		end := len(img)
		if i+1 < len(subs) {
			end = subs[i+1]
		}
		var callees, xdata, strs []string
		for pc := a; pc < end; pc++ {
			in, ok := insts[pc]
			if !ok {
				continue
			}
			if in.flow == call {
				callees = append(callees, label(in.target))
			}
			if img[pc] == 0x90 {
				xdata = append(xdata, fmt.Sprintf("%02x%02x", img[pc+1], img[pc+2]))
			}
			// Keil far pointer to code: mov r3,#0xff / mov r2,#hi / mov r1,#lo
			if img[pc] == 0x7b && img[pc+1] == 0xff && pc+5 < len(img) && img[pc+2] == 0x7a && img[pc+4] == 0x79 {
				if t := text(int(img[pc+3])<<8 | int(img[pc+5])); t != "" {
					strs = append(strs, strconv.Quote(t))
				}
			}
		}
		fmt.Fprintf(w, "%s size=%d callers=%d\n  calls: %s\n  xdata: %s\n  text: %s\n", label(a), end-a, callers[a], uniq(callees), uniq(xdata), uniq(strs))
	}
	return nil
}
