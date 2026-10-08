package scan

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf16"
)

// A reader for .NET assembly metadata (ECMA-335, partition II). A managed
// DLL can only call code outside itself through entries in its metadata:
// type references (TypeRef), member references (MemberRef), native imports
// (ImplMap + ModuleRef), and other assemblies (AssemblyRef). Its string
// literals are in the #US heap. Read without running anything, these list
// everything the DLL can reach directly.

// CLRInfo is what a DLL's metadata says it uses.
type CLRInfo struct {
	Assembly     string   // its own name
	ILOnly       bool     // no native code (the CLI header's ILONLY flag)
	AssemblyRefs []string // other assemblies it references
	TypeRefs     []string // "Namespace.Type" (nested: "Namespace.Outer/Inner")
	MemberRefs   []MemberRef
	PInvokes     []string          // "module!function"
	Strings      []string          // string literals (#US)
	TypeRefAsm   map[string]string // TypeRef -> the assembly it comes from ("" if unknown)
}

// MemberRef is a method or field the DLL calls or reads on another type.
type MemberRef struct {
	Type, Name string
}

var errNotCLR = errors.New("not a .NET assembly")

// ReadCLR reads the metadata of a .NET DLL.
func ReadCLR(b []byte) (info *CLRInfo, err error) {
	defer func() {
		if recover() != nil {
			info, err = nil, errors.New("malformed .NET metadata")
		}
	}()
	md, flags, err := metadataBlob(b)
	if err != nil {
		return nil, err
	}
	in, err := parseMetadata(md)
	if in != nil {
		in.ILOnly = flags&1 != 0
	}
	return in, err
}

type peSection struct{ va, vsize, raw, rawSize uint32 }

// metadataBlob finds the metadata root through the PE and CLI headers.
func metadataBlob(b []byte) ([]byte, uint32, error) {
	if len(b) < 0x40 || b[0] != 'M' || b[1] != 'Z' {
		return nil, 0, errNotCLR
	}
	pe := int(binary.LittleEndian.Uint32(b[0x3c:]))
	if pe+24 > len(b) || string(b[pe:pe+4]) != "PE\x00\x00" {
		return nil, 0, errNotCLR
	}
	coff := pe + 4
	nsec := int(binary.LittleEndian.Uint16(b[coff+2:]))
	optSize := int(binary.LittleEndian.Uint16(b[coff+16:]))
	opt := coff + 20
	if opt+optSize > len(b) || optSize < 2 {
		return nil, 0, errNotCLR
	}
	var dirs int
	switch binary.LittleEndian.Uint16(b[opt:]) {
	case 0x10b:
		dirs = opt + 96
	case 0x20b:
		dirs = opt + 112
	default:
		return nil, 0, errNotCLR
	}
	cliDir := dirs + 14*8
	if cliDir+8 > opt+optSize {
		return nil, 0, errNotCLR
	}
	cliRVA := binary.LittleEndian.Uint32(b[cliDir:])
	if cliRVA == 0 {
		return nil, 0, errNotCLR // a native DLL
	}
	var secs []peSection
	for i, at := 0, opt+optSize; i < nsec; i, at = i+1, at+40 {
		if at+40 > len(b) {
			return nil, 0, errNotCLR
		}
		secs = append(secs, peSection{binary.LittleEndian.Uint32(b[at+12:]), binary.LittleEndian.Uint32(b[at+8:]),
			binary.LittleEndian.Uint32(b[at+20:]), binary.LittleEndian.Uint32(b[at+16:])})
	}
	off := func(rva uint32) (int, bool) {
		for _, s := range secs {
			size := s.vsize
			if s.rawSize > size {
				size = s.rawSize
			}
			if rva >= s.va && rva < s.va+size {
				o := int(rva - s.va + s.raw)
				return o, o < len(b)
			}
		}
		return 0, false
	}
	cli, ok := off(cliRVA)
	if !ok || cli+20 > len(b) {
		return nil, 0, errNotCLR
	}
	flags := binary.LittleEndian.Uint32(b[cli+16:])
	mdRVA := binary.LittleEndian.Uint32(b[cli+8:])
	mdSize := int(binary.LittleEndian.Uint32(b[cli+12:]))
	md, ok := off(mdRVA)
	if !ok || md+mdSize > len(b) || mdSize < 16 {
		return nil, 0, errNotCLR
	}
	return b[md : md+mdSize], flags, nil
}

// The metadata tables, by number.
const (
	tModule = iota
	tTypeRef
	tTypeDef
	tFieldPtr
	tField
	tMethodPtr
	tMethodDef
	tParamPtr
	tParam
	tInterfaceImpl
	tMemberRef
	tConstant
	tCustomAttribute
	tFieldMarshal
	tDeclSecurity
	tClassLayout
	tFieldLayout
	tStandAloneSig
	tEventMap
	tEventPtr
	tEvent
	tPropertyMap
	tPropertyPtr
	tProperty
	tMethodSemantics
	tMethodImpl
	tModuleRef
	tTypeSpec
	tImplMap
	tFieldRVA
	tEncLog
	tEncMap
	tAssembly
	tAssemblyProcessor
	tAssemblyOS
	tAssemblyRef
	tAssemblyRefProcessor
	tAssemblyRefOS
	tFile
	tExportedType
	tManifestResource
	tNestedClass
	tGenericParam
	tMethodSpec
	tGenericParamConstraint
	tCount
)

// Column kinds of a table row.
const (
	c2     = iota + 1 // 2-byte constant
	c4                // 4-byte constant
	cStr              // #Strings index
	cGUID             // #GUID index
	cBlob             // #Blob index
	cIdx              // index into one table (with the table in the column)
	cCoded            // coded index (with the coded kind in the column)
)

type col struct{ kind, arg int }

// Coded index kinds: their tables, in tag order (-1: unused tag).
var coded = [][]int{
	{tTypeDef, tTypeRef, tTypeSpec}, // 0 TypeDefOrRef
	{tField, tParam, tProperty},     // 1 HasConstant
	{tMethodDef, tField, tTypeRef, tTypeDef, tParam, tInterfaceImpl, tMemberRef, tModule, tDeclSecurity, tProperty, tEvent,
		tStandAloneSig, tModuleRef, tTypeSpec, tAssembly, tAssemblyRef, tFile, tExportedType, tManifestResource,
		tGenericParam, tGenericParamConstraint, tMethodSpec}, // 2 HasCustomAttribute
	{tField, tParam},                                        // 3 HasFieldMarshal
	{tTypeDef, tMethodDef, tAssembly},                       // 4 HasDeclSecurity
	{tTypeDef, tTypeRef, tModuleRef, tMethodDef, tTypeSpec}, // 5 MemberRefParent
	{tEvent, tProperty},                                     // 6 HasSemantics
	{tMethodDef, tMemberRef},                                // 7 MethodDefOrRef
	{tField, tMethodDef},                                    // 8 MemberForwarded
	{tFile, tAssemblyRef, tExportedType},                    // 9 Implementation
	{-1, -1, tMethodDef, tMemberRef, -1},                    // 10 CustomAttributeType
	{tModule, tModuleRef, tAssemblyRef, tTypeRef},           // 11 ResolutionScope
	{tTypeDef, tMethodDef},                                  // 12 TypeOrMethodDef
}

const (
	kTypeDefOrRef = iota
	kHasConstant
	kHasCustomAttribute
	kHasFieldMarshal
	kHasDeclSecurity
	kMemberRefParent
	kHasSemantics
	kMethodDefOrRef
	kMemberForwarded
	kImplementation
	kCustomAttributeType
	kResolutionScope
	kTypeOrMethodDef
)

var schema = [tCount][]col{
	tModule:                 {{c2, 0}, {cStr, 0}, {cGUID, 0}, {cGUID, 0}, {cGUID, 0}},
	tTypeRef:                {{cCoded, kResolutionScope}, {cStr, 0}, {cStr, 0}},
	tTypeDef:                {{c4, 0}, {cStr, 0}, {cStr, 0}, {cCoded, kTypeDefOrRef}, {cIdx, tField}, {cIdx, tMethodDef}},
	tFieldPtr:               {{cIdx, tField}},
	tField:                  {{c2, 0}, {cStr, 0}, {cBlob, 0}},
	tMethodPtr:              {{cIdx, tMethodDef}},
	tMethodDef:              {{c4, 0}, {c2, 0}, {c2, 0}, {cStr, 0}, {cBlob, 0}, {cIdx, tParam}},
	tParamPtr:               {{cIdx, tParam}},
	tParam:                  {{c2, 0}, {c2, 0}, {cStr, 0}},
	tInterfaceImpl:          {{cIdx, tTypeDef}, {cCoded, kTypeDefOrRef}},
	tMemberRef:              {{cCoded, kMemberRefParent}, {cStr, 0}, {cBlob, 0}},
	tConstant:               {{c2, 0}, {cCoded, kHasConstant}, {cBlob, 0}},
	tCustomAttribute:        {{cCoded, kHasCustomAttribute}, {cCoded, kCustomAttributeType}, {cBlob, 0}},
	tFieldMarshal:           {{cCoded, kHasFieldMarshal}, {cBlob, 0}},
	tDeclSecurity:           {{c2, 0}, {cCoded, kHasDeclSecurity}, {cBlob, 0}},
	tClassLayout:            {{c2, 0}, {c4, 0}, {cIdx, tTypeDef}},
	tFieldLayout:            {{c4, 0}, {cIdx, tField}},
	tStandAloneSig:          {{cBlob, 0}},
	tEventMap:               {{cIdx, tTypeDef}, {cIdx, tEvent}},
	tEventPtr:               {{cIdx, tEvent}},
	tEvent:                  {{c2, 0}, {cStr, 0}, {cCoded, kTypeDefOrRef}},
	tPropertyMap:            {{cIdx, tTypeDef}, {cIdx, tProperty}},
	tPropertyPtr:            {{cIdx, tProperty}},
	tProperty:               {{c2, 0}, {cStr, 0}, {cBlob, 0}},
	tMethodSemantics:        {{c2, 0}, {cIdx, tMethodDef}, {cCoded, kHasSemantics}},
	tMethodImpl:             {{cIdx, tTypeDef}, {cCoded, kMethodDefOrRef}, {cCoded, kMethodDefOrRef}},
	tModuleRef:              {{cStr, 0}},
	tTypeSpec:               {{cBlob, 0}},
	tImplMap:                {{c2, 0}, {cCoded, kMemberForwarded}, {cStr, 0}, {cIdx, tModuleRef}},
	tFieldRVA:               {{c4, 0}, {cIdx, tField}},
	tEncLog:                 {{c4, 0}, {c4, 0}},
	tEncMap:                 {{c4, 0}},
	tAssembly:               {{c4, 0}, {c2, 0}, {c2, 0}, {c2, 0}, {c2, 0}, {c4, 0}, {cBlob, 0}, {cStr, 0}, {cStr, 0}},
	tAssemblyProcessor:      {{c4, 0}},
	tAssemblyOS:             {{c4, 0}, {c4, 0}, {c4, 0}},
	tAssemblyRef:            {{c2, 0}, {c2, 0}, {c2, 0}, {c2, 0}, {c4, 0}, {cBlob, 0}, {cStr, 0}, {cStr, 0}, {cBlob, 0}},
	tAssemblyRefProcessor:   {{c4, 0}, {cIdx, tAssemblyRef}},
	tAssemblyRefOS:          {{c4, 0}, {c4, 0}, {c4, 0}, {cIdx, tAssemblyRef}},
	tFile:                   {{c4, 0}, {cStr, 0}, {cBlob, 0}},
	tExportedType:           {{c4, 0}, {c4, 0}, {cStr, 0}, {cStr, 0}, {cCoded, kImplementation}},
	tManifestResource:       {{c4, 0}, {c4, 0}, {cStr, 0}, {cCoded, kImplementation}},
	tNestedClass:            {{cIdx, tTypeDef}, {cIdx, tTypeDef}},
	tGenericParam:           {{c2, 0}, {c2, 0}, {cCoded, kTypeOrMethodDef}, {cStr, 0}},
	tMethodSpec:             {{cCoded, kMethodDefOrRef}, {cBlob, 0}},
	tGenericParamConstraint: {{cIdx, tGenericParam}, {cCoded, kTypeDefOrRef}},
}

type metadata struct {
	strings, blob, us        []byte
	rows                     [tCount]int
	tables                   [tCount][]byte
	rowSize                  [tCount]int
	colSize                  [tCount][]int
	strIdx, blobIdx, guidIdx int
}

func parseMetadata(md []byte) (*CLRInfo, error) {
	le := binary.LittleEndian
	if le.Uint32(md) != 0x424A5342 {
		return nil, errNotCLR
	}
	vlen := int(le.Uint32(md[12:]))
	p := 16 + vlen
	nstreams := int(le.Uint16(md[p+2:]))
	p += 4
	m := &metadata{}
	var tbl []byte
	for i := 0; i < nstreams; i++ {
		o, n := int(le.Uint32(md[p:])), int(le.Uint32(md[p+4:]))
		p += 8
		end := p
		for md[end] != 0 {
			end++
		}
		name := string(md[p:end])
		p = (end + 4) &^ 3
		if o+n > len(md) || o < 0 || n < 0 {
			return nil, errors.New("metadata stream out of range")
		}
		s := md[o : o+n]
		switch name {
		case "#~", "#-":
			tbl = s
		case "#Strings":
			m.strings = s
		case "#Blob":
			m.blob = s
		case "#US":
			m.us = s
		}
	}
	if tbl == nil {
		return nil, errors.New("no metadata tables")
	}
	heaps := tbl[6]
	m.strIdx, m.guidIdx, m.blobIdx = 2, 2, 2
	if heaps&1 != 0 {
		m.strIdx = 4
	}
	if heaps&2 != 0 {
		m.guidIdx = 4
	}
	if heaps&4 != 0 {
		m.blobIdx = 4
	}
	valid := le.Uint64(tbl[8:])
	q := 24
	for t := 0; t < 64; t++ {
		if valid&(1<<t) == 0 {
			continue
		}
		if t >= tCount {
			return nil, fmt.Errorf("unknown metadata table %#x", t)
		}
		m.rows[t] = int(le.Uint32(tbl[q:]))
		q += 4
	}
	if heaps&0x40 != 0 {
		q += 4 // extra data
	}
	idxSize := func(t int) int {
		if m.rows[t] < 1<<16 {
			return 2
		}
		return 4
	}
	codedSize := func(k int) int {
		ts := coded[k]
		bits := 0
		for 1<<bits < len(ts) {
			bits++
		}
		max := 0
		for _, t := range ts {
			if t >= 0 && m.rows[t] > max {
				max = m.rows[t]
			}
		}
		if max < 1<<(16-bits) {
			return 2
		}
		return 4
	}
	for t := 0; t < tCount; t++ {
		for _, c := range schema[t] {
			n := 0
			switch c.kind {
			case c2:
				n = 2
			case c4:
				n = 4
			case cStr:
				n = m.strIdx
			case cGUID:
				n = m.guidIdx
			case cBlob:
				n = m.blobIdx
			case cIdx:
				n = idxSize(c.arg)
			case cCoded:
				n = codedSize(c.arg)
			}
			m.colSize[t] = append(m.colSize[t], n)
			m.rowSize[t] += n
		}
		size := m.rowSize[t] * m.rows[t]
		if q+size > len(tbl) {
			return nil, errors.New("metadata tables truncated")
		}
		m.tables[t] = tbl[q : q+size]
		q += size
	}
	return m.info(), nil
}

// cell reads column c of row r (1-based) of table t.
func (m *metadata) cell(t, r, c int) int {
	at := (r - 1) * m.rowSize[t]
	for i := 0; i < c; i++ {
		at += m.colSize[t][i]
	}
	row := m.tables[t]
	if m.colSize[t][c] == 2 {
		return int(binary.LittleEndian.Uint16(row[at:]))
	}
	return int(binary.LittleEndian.Uint32(row[at:]))
}

func (m *metadata) str(i int) string {
	if i <= 0 || i >= len(m.strings) {
		return ""
	}
	end := i
	for end < len(m.strings) && m.strings[end] != 0 {
		end++
	}
	return string(m.strings[i:end])
}

// decode splits a coded index into its table and row.
func decode(k, v int) (table, row int) {
	ts := coded[k]
	bits := 0
	for 1<<bits < len(ts) {
		bits++
	}
	tag := v & (1<<bits - 1)
	if tag >= len(ts) {
		return -1, 0
	}
	return ts[tag], v >> bits
}

func (m *metadata) typeRefName(r int) string { return m.typeRefNameDepth(r, 0) }

// typeRefNameDepth follows nesting at most 16 deep: a crafted file can
// make types nest in a loop, which would otherwise recurse until the stack
// overflows (a crash no recover can catch).
func (m *metadata) typeRefNameDepth(r, depth int) string {
	if r <= 0 || r > m.rows[tTypeRef] || depth > 16 {
		return ""
	}
	name := m.str(m.cell(tTypeRef, r, 1))
	ns := m.str(m.cell(tTypeRef, r, 2))
	t, outer := decode(kResolutionScope, m.cell(tTypeRef, r, 0))
	if t == tTypeRef && outer != r {
		return m.typeRefNameDepth(outer, depth+1) + "/" + name
	}
	if ns == "" {
		return name
	}
	return ns + "." + name
}

// typeRefAssembly is the assembly a type reference resolves to.
func (m *metadata) typeRefAssembly(r int) string {
	for depth := 0; depth < 8 && r > 0 && r <= m.rows[tTypeRef]; depth++ {
		t, x := decode(kResolutionScope, m.cell(tTypeRef, r, 0))
		switch t {
		case tAssemblyRef:
			if x > 0 && x <= m.rows[tAssemblyRef] {
				return m.str(m.cell(tAssemblyRef, x, 6))
			}
			return ""
		case tTypeRef:
			r = x
		default:
			return ""
		}
	}
	return ""
}

// typeSpecName names a TypeSpec that instantiates a generic type
// (GENERICINST CLASS|VALUETYPE TypeDefOrRef ...); "" for others.
func (m *metadata) typeSpecName(r int) string {
	if r <= 0 || r > m.rows[tTypeSpec] {
		return ""
	}
	sig := m.blobAt(m.cell(tTypeSpec, r, 0))
	if len(sig) < 3 || sig[0] != 0x15 || (sig[1] != 0x12 && sig[1] != 0x11) {
		return ""
	}
	v, _ := compressed(sig[2:])
	t, x := decode(kTypeDefOrRef, v)
	if t == tTypeRef {
		return m.typeRefName(x)
	}
	return ""
}

func (m *metadata) blobAt(i int) []byte {
	if i <= 0 || i >= len(m.blob) {
		return nil
	}
	n, k := compressed(m.blob[i:])
	if k == 0 || i+k+n > len(m.blob) {
		return nil
	}
	return m.blob[i+k : i+k+n]
}

// compressed reads an ECMA-335 compressed unsigned integer and its length.
func compressed(b []byte) (int, int) {
	switch {
	case len(b) >= 1 && b[0]&0x80 == 0:
		return int(b[0]), 1
	case len(b) >= 2 && b[0]&0xC0 == 0x80:
		return int(b[0]&0x3F)<<8 | int(b[1]), 2
	case len(b) >= 4 && b[0]&0xE0 == 0xC0:
		return int(b[0]&0x1F)<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3]), 4
	}
	return 0, 0
}

func (m *metadata) info() *CLRInfo {
	in := &CLRInfo{TypeRefAsm: map[string]string{}}
	if m.rows[tAssembly] > 0 {
		in.Assembly = m.str(m.cell(tAssembly, 1, 7))
	}
	for r := 1; r <= m.rows[tAssemblyRef]; r++ {
		in.AssemblyRefs = append(in.AssemblyRefs, m.str(m.cell(tAssemblyRef, r, 6)))
	}
	for r := 1; r <= m.rows[tTypeRef]; r++ {
		n := m.typeRefName(r)
		in.TypeRefs = append(in.TypeRefs, n)
		in.TypeRefAsm[n] = m.typeRefAssembly(r)
	}
	for r := 1; r <= m.rows[tMemberRef]; r++ {
		t, x := decode(kMemberRefParent, m.cell(tMemberRef, r, 0))
		owner := ""
		switch t {
		case tTypeRef:
			owner = m.typeRefName(x)
		case tTypeSpec:
			owner = m.typeSpecName(x)
		}
		in.MemberRefs = append(in.MemberRefs, MemberRef{owner, m.str(m.cell(tMemberRef, r, 1))})
	}
	for r := 1; r <= m.rows[tImplMap]; r++ {
		mod := m.cell(tImplMap, r, 3)
		dll := ""
		if mod > 0 && mod <= m.rows[tModuleRef] {
			dll = m.str(m.cell(tModuleRef, mod, 0))
		}
		in.PInvokes = append(in.PInvokes, dll+"!"+m.str(m.cell(tImplMap, r, 2)))
	}
	// #US: length-prefixed UTF-16 strings (plus a trailing flag byte).
	for i := 1; i < len(m.us); {
		n, k := compressed(m.us[i:])
		if k == 0 || i+k+n > len(m.us) {
			break
		}
		if n > 1 {
			raw := m.us[i+k : i+k+n-1]
			u := make([]uint16, len(raw)/2)
			for j := range u {
				u[j] = binary.LittleEndian.Uint16(raw[2*j:])
			}
			in.Strings = append(in.Strings, string(utf16.Decode(u)))
		}
		i += k + n
	}
	return in
}
