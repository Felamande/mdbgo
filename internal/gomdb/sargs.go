package gomdb

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SargNode is a node in the search argument tree (WHERE clause).
type SargNode struct {
	Op       int
	Col      *MdbColumn
	ValType  int // Type flag for the stored value
	Value    MdbAny
	Parent   *SargNode // Internal: used during SQL parsing
	Children []*SargNode
	Left     *SargNode
	Right    *SargNode

	ilikePattern    string
	ilikePatternSet bool
	patternBytes    []byte // precomputed []byte form of Value.S for string sargs

	// IN list state, precomputed once per query by resolveInValues so the
	// per-row evaluator never allocates or re-parses literals.
	InValues    []MdbAny // typed elements: I for int/bool, D for float/date, S for text/memo
	InElemTypes []int    // parallel literal origin: TypeText (quoted), TypeDouble (number), TypeInt (TRUE/FALSE)
	InBytes     [][]byte // UTF-8 forms for text IN lists; sorted+deduped when inSorted
	inSorted    bool     // InValues/InBytes sorted, evaluate with binary search
}

// inLinearSearchMax is the IN list size threshold. Larger lists are sorted
// and deduplicated once at resolve time and evaluated with binary search;
// smaller lists use a cache-friendly linear scan.
const inLinearSearchMax = 8

// TestSargs evaluates the sarg tree against the provided fields.
// Returns true if the row passes all conditions.
func TestSargs(mdb *MdbHandle, table *MdbTableDef, fields []MdbField) bool {
	if table.SargTree == nil {
		return true
	}
	return testSargNode(mdb, table.SargTree, fields) != 0
}

// testSargNode recursively evaluates a sarg node.
func testSargNode(mdb *MdbHandle, node *SargNode, fields []MdbField) int {
	return testSargNodeScratch(mdb, node, fields, mdb.pgBuf, nil)
}

// testSargNodeScratch evaluates a sarg node against fields cracked from an
// explicit page. When scratch is non-nil (fast scan workers), decoding uses
// the worker's buffers and pure memo page access instead of handle state.
func testSargNodeScratch(mdb *MdbHandle, node *SargNode, fields []MdbField, page []byte, s *decodeScratch) int {
	if node == nil {
		return 1
	}

	if IsRelationalOp(node.Op) {
		col := node.Col
		if col == nil {
			// Constant comparison result stored in value
			return node.Value.I
		}

		// CrackRow assigns fields[i].ColNum == i, so the field for a column
		// is found by index rather than a linear scan.
		if col.ColNum < 0 || col.ColNum >= len(fields) {
			return 0
		}

		if testSargScratch(mdb, col, node, &fields[col.ColNum], page, s) {
			return 1
		}
		return 0
	}

	// Logical operators
	switch node.Op {
	case OpNot:
		if node.Left != nil {
			return 1 - testSargNodeScratch(mdb, node.Left, fields, page, s)
		}
		return 0

	case OpAnd:
		if node.Left != nil && !toBool(testSargNodeScratch(mdb, node.Left, fields, page, s)) {
			return 0
		}
		if node.Right != nil {
			return testSargNodeScratch(mdb, node.Right, fields, page, s)
		}
		return 1

	case OpOr:
		if node.Left != nil && toBool(testSargNodeScratch(mdb, node.Left, fields, page, s)) {
			return 1
		}
		if node.Right != nil {
			return testSargNodeScratch(mdb, node.Right, fields, page, s)
		}
		return 0
	}

	return 1
}

func toBool(v int) bool { return v != 0 }

// testSarg tests a single sarg condition against a field.
func testSarg(mdb *MdbHandle, col *MdbColumn, node *SargNode, field *MdbField) bool {
	return testSargScratch(mdb, col, node, field, mdb.pgBuf, nil)
}

// testSargScratch is the page/scratch-aware form of testSarg.
func testSargScratch(mdb *MdbHandle, col *MdbColumn, node *SargNode, field *MdbField, page []byte, s *decodeScratch) bool {
	if node.Op == OpIsNull {
		return field.IsNull
	}
	if node.Op == OpNotNull {
		return !field.IsNull
	}
	if node.Op == OpIn {
		return testSargIn(mdb, col, node, field, page, s)
	}

	switch col.ColType {
	case TypeBool:
		val := 1
		if field.IsNull {
			val = 0
		}
		return testInt(node.Op, node.Value.I, val)

	case TypeByte:
		if field.IsNull || field.Siz < 1 {
			return false
		}
		val := int(field.Value[0])
		return testInt(node.Op, node.Value.I, val)

	case TypeInt:
		if field.IsNull || field.Siz < 2 {
			return false
		}
		val := GetInt16(field.Value, 0)
		return testInt(node.Op, node.Value.I, val)

	case TypeLongInt:
		if field.IsNull || field.Siz < 4 {
			return false
		}
		val := GetInt32(field.Value, 0)
		return testInt(node.Op, node.Value.I, val)

	case TypeFloat:
		if field.IsNull || field.Siz < 4 {
			return false
		}
		nodeVal := node.Value.D
		if node.ValType == TypeInt {
			nodeVal = float64(node.Value.I)
		}
		fieldVal := float64(GetSingle(field.Value, 0))
		return testDouble(node.Op, nodeVal, fieldVal)

	case TypeDouble:
		if field.IsNull || field.Siz < 8 {
			return false
		}
		nodeVal := node.Value.D
		if node.ValType == TypeInt {
			nodeVal = float64(node.Value.I)
		}
		fieldVal := GetDouble(field.Value, 0)
		return testDouble(node.Op, nodeVal, fieldVal)

	case TypeText:
		if field.IsNull {
			return false
		}
		if s != nil {
			return testSargStringIn(mdb, node, field.Value, s)
		}
		return mdb.testSargString(node, field.Value)

	case TypeMemo, TypeRepID:
		if field.IsNull {
			return false
		}
		var val string
		if s != nil {
			val = colToStringIn(mdb, col, field, page, s)
		} else {
			val = mdb.valueFromField(col, field)
		}
		return testString(node, val)

	case TypeDateTime:
		if field.IsNull || field.Siz < 8 {
			return false
		}
		nodeVal := poorMansTrunc(node.Value.D)
		fieldVal := poorMansTrunc(GetDouble(field.Value, 0))
		return testDouble(node.Op, nodeVal, fieldVal)

	default:
		return true
	}
}

// testSargIn evaluates an IN predicate against a single field. Typed element
// lists are precomputed, so the hot path is a tight loop (or binary search)
// over plain values with no allocation.
func testSargIn(mdb *MdbHandle, col *MdbColumn, node *SargNode, field *MdbField, page []byte, s *decodeScratch) bool {
	switch col.ColType {
	case TypeBool:
		// Mirrors the existing bool comparison semantics: a null bool field
		// compares as 0 (FALSE).
		val := 0
		if !field.IsNull {
			val = 1
		}
		return testIntList(node, val)

	case TypeByte:
		if field.IsNull || field.Siz < 1 {
			return false
		}
		return testIntList(node, int(field.Value[0]))

	case TypeInt:
		if field.IsNull || field.Siz < 2 {
			return false
		}
		return testIntList(node, GetInt16(field.Value, 0))

	case TypeLongInt, TypeComplex:
		if field.IsNull || field.Siz < 4 {
			return false
		}
		return testIntList(node, GetInt32(field.Value, 0))

	case TypeFloat:
		if field.IsNull || field.Siz < 4 {
			return false
		}
		return testDoubleList(node, float64(GetSingle(field.Value, 0)))

	case TypeDouble:
		if field.IsNull || field.Siz < 8 {
			return false
		}
		return testDoubleList(node, GetDouble(field.Value, 0))

	case TypeText:
		if field.IsNull {
			return false
		}
		if s != nil {
			return testInTextScratch(mdb, node, field.Value, s)
		}
		return mdb.testInText(node, field.Value)

	case TypeMemo, TypeRepID:
		if field.IsNull {
			return false
		}
		var val string
		if s != nil {
			val = colToStringIn(mdb, col, field, page, s)
		} else {
			val = mdb.valueFromField(col, field)
		}
		return testStringList(node, val)

	case TypeDateTime:
		if field.IsNull || field.Siz < 8 {
			return false
		}
		return testDoubleList(node, poorMansTrunc(GetDouble(field.Value, 0)))

	default:
		// Column types without comparison support never match IN.
		return false
	}
}

func testIntList(node *SargNode, val int) bool {
	vals := node.InValues
	if len(vals) == 0 {
		return false
	}
	if node.inSorted {
		i := sort.Search(len(vals), func(i int) bool { return vals[i].I >= val })
		return i < len(vals) && vals[i].I == val
	}
	for i := range vals {
		if vals[i].I == val {
			return true
		}
	}
	return false
}

func testDoubleList(node *SargNode, val float64) bool {
	vals := node.InValues
	if len(vals) == 0 {
		return false
	}
	if node.inSorted {
		i := sort.Search(len(vals), func(i int) bool { return vals[i].D >= val })
		return i < len(vals) && vals[i].D == val
	}
	for i := range vals {
		if vals[i].D == val {
			return true
		}
	}
	return false
}

func testStringList(node *SargNode, val string) bool {
	for i := range node.InValues {
		if node.InValues[i].S == val {
			return true
		}
	}
	return false
}

// testInTextScratch evaluates a text IN predicate with caller-owned scratch.
// The field is decoded once and then compared against the precomputed
// patterns, so a large list never re-decodes or allocates per element.
func testInTextScratch(mdb *MdbHandle, node *SargNode, src []byte, s *decodeScratch) bool {
	if body, ok := unicodeFastPath(src, mdb.IsJet4()); ok {
		return testInBytes(node, body)
	}
	buf := appendUnicodeUTF8(s.unicode[:0], src, mdb.IsJet4())
	s.unicode = buf
	return testInBytes(node, buf)
}

// testInText evaluates a text IN predicate using the handle's reusable
// unicode buffer (synchronous path).
func (mdb *MdbHandle) testInText(node *SargNode, src []byte) bool {
	if body, ok := unicodeFastPath(src, mdb.IsJet4()); ok {
		return testInBytes(node, body)
	}
	buf := appendUnicodeUTF8(mdb.unicodeBuf[:0], src, mdb.IsJet4())
	mdb.unicodeBuf = buf
	return testInBytes(node, buf)
}

// testInBytes compares decoded text against the precomputed IN patterns.
func testInBytes(node *SargNode, s []byte) bool {
	ps := node.InBytes
	if len(ps) == 0 {
		return false
	}
	if node.inSorted {
		i := sort.Search(len(ps), func(i int) bool { return bytes.Compare(ps[i], s) >= 0 })
		return i < len(ps) && bytes.Equal(ps[i], s)
	}
	for i := range ps {
		if bytes.Equal(ps[i], s) {
			return true
		}
	}
	return false
}

// resolveInValues converts the raw IN literals captured by the parser into a
// typed list for the target column. Conversion happens once per query;
// elements that cannot represent a value of the column type are dropped
// (they can never match). Lists larger than inLinearSearchMax are sorted and
// deduplicated for binary-search evaluation.
func resolveInValues(node *SargNode, col *MdbColumn) {
	vals := node.InValues

	switch col.ColType {
	case TypeText, TypeMemo, TypeRepID:
		// Every literal compares as text: quoted strings as-is, numeric
		// literals as their raw text, TRUE/FALSE as "1"/"0".
		node.ValType = TypeText
		node.InBytes = make([][]byte, 0, len(vals))
		for i := range vals {
			node.InBytes = append(node.InBytes, []byte(vals[i].S))
		}
		if len(node.InBytes) > inLinearSearchMax {
			sort.Slice(node.InBytes, func(i, j int) bool {
				return bytes.Compare(node.InBytes[i], node.InBytes[j]) < 0
			})
			dedup := node.InBytes[:1]
			for _, p := range node.InBytes[1:] {
				if bytes.Compare(dedup[len(dedup)-1], p) != 0 {
					dedup = append(dedup, p)
				}
			}
			node.InBytes = dedup
			node.inSorted = true
		}

	case TypeBool, TypeByte, TypeInt, TypeLongInt, TypeComplex:
		node.ValType = TypeInt
		out := vals[:0]
		for i := range vals {
			if node.InElemTypes[i] == TypeInt {
				out = append(out, MdbAny{I: vals[i].I})
				continue
			}
			if iv, err := strconv.Atoi(vals[i].S); err == nil {
				out = append(out, MdbAny{I: iv})
			}
		}
		node.InValues = out
		if len(node.InValues) > inLinearSearchMax {
			sort.Slice(node.InValues, func(i, j int) bool { return node.InValues[i].I < node.InValues[j].I })
			dedup := node.InValues[:1]
			for _, v := range node.InValues[1:] {
				if dedup[len(dedup)-1].I == v.I {
					continue
				}
				dedup = append(dedup, v)
			}
			node.InValues = dedup
			node.inSorted = true
		}

	case TypeFloat, TypeDouble:
		node.ValType = TypeDouble
		out := vals[:0]
		for i := range vals {
			if node.InElemTypes[i] == TypeInt {
				out = append(out, MdbAny{D: float64(vals[i].I)})
				continue
			}
			if d, err := strconv.ParseFloat(vals[i].S, 64); err == nil {
				out = append(out, MdbAny{D: d})
			}
		}
		node.InValues = out
		if len(node.InValues) > inLinearSearchMax {
			sort.Slice(node.InValues, func(i, j int) bool { return node.InValues[i].D < node.InValues[j].D })
			dedup := node.InValues[:1]
			for _, v := range node.InValues[1:] {
				if dedup[len(dedup)-1].D == v.D {
					continue
				}
				dedup = append(dedup, v)
			}
			node.InValues = dedup
			node.inSorted = true
		}

	case TypeDateTime:
		// Mirrors the single-value rule: integer literals are Unix
		// timestamps converted to date serials; float literals are serials.
		node.ValType = TypeDouble
		out := vals[:0]
		for i := range vals {
			if node.InElemTypes[i] == TypeInt {
				t := time.Unix(int64(vals[i].I), 0).UTC()
				out = append(out, MdbAny{D: TmToDate(t)})
				continue
			}
			if d, err := strconv.ParseFloat(vals[i].S, 64); err == nil {
				out = append(out, MdbAny{D: d})
			}
		}
		node.InValues = out
		if len(node.InValues) > inLinearSearchMax {
			sort.Slice(node.InValues, func(i, j int) bool { return node.InValues[i].D < node.InValues[j].D })
			dedup := node.InValues[:1]
			for _, v := range node.InValues[1:] {
				if dedup[len(dedup)-1].D == v.D {
					continue
				}
				dedup = append(dedup, v)
			}
			node.InValues = dedup
			node.inSorted = true
		}

	default:
		// Unsupported column type: the predicate can never match.
		node.InValues = nil
		node.InBytes = nil
		node.ValType = 0
	}
	node.InElemTypes = nil
}

// testSargStringIn evaluates a string sarg with caller-owned scratch.
func testSargStringIn(mdb *MdbHandle, node *SargNode, src []byte, s *decodeScratch) bool {
	if node.Op == OpILike {
		return testString(node, unicodeBorrow(src, mdb.IsJet4(), s))
	}
	if body, ok := unicodeFastPath(src, mdb.IsJet4()); ok {
		return testStringBytes(node, body)
	}
	buf := appendUnicodeUTF8(s.unicode[:0], src, mdb.IsJet4())
	s.unicode = buf
	return testStringBytes(node, buf)
}

// valueFromField gets the string value from a field for the given column type.
func (mdb *MdbHandle) valueFromField(col *MdbColumn, field *MdbField) string {
	// Create a temporary column copy with the field's data as the current value
	tmpCol := *col
	tmpCol.CurValueStart = field.Start
	tmpCol.CurValueLen = field.Siz
	tmpCol.CurValueIsNull = field.IsNull
	return mdb.colToString(&tmpCol, field)
}

// testSargString evaluates a string sarg against raw field bytes without
// allocating a string per row: the field is decoded into the handle's
// reusable buffer and compared byte-wise (UTF-8 comparison is
// order-preserving, so results match the legacy string comparisons).
func (mdb *MdbHandle) testSargString(node *SargNode, src []byte) bool {
	if node.Op == OpILike {
		// ILIKE folds the decoded text with Unicode case folding, which
		// needs a real string; it is not on the hot path.
		return testString(node, mdb.unicodeToUTF8(src))
	}
	if body, ok := unicodeFastPath(src, mdb.IsJet4()); ok {
		return testStringBytes(node, body)
	}
	buf := appendUnicodeUTF8(mdb.unicodeBuf[:0], src, mdb.IsJet4())
	mdb.unicodeBuf = buf
	return testStringBytes(node, buf)
}

// testStringBytes performs string comparison for sargs over byte slices.
func testStringBytes(node *SargNode, s []byte) bool {
	p := node.patternBytes
	if p == nil {
		p = []byte(node.Value.S)
	}
	switch node.Op {
	case OpLike:
		return likeCmpBytes(s, p)
	case OpILike:
		if node.ilikePatternSet {
			// patternBytes holds the pre-folded pattern for ILIKE
			return likeCmpBytes(s, node.patternBytes)
		}
		return likeCmpBytes(s, []byte(strings.ToLower(node.Value.S)))
	case OpEqual:
		return bytes.Equal(s, p)
	case OpNEQ:
		return !bytes.Equal(s, p)
	case OpGT:
		return bytes.Compare(s, p) > 0
	case OpLT:
		return bytes.Compare(s, p) < 0
	case OpGTEQ:
		return bytes.Compare(s, p) >= 0
	case OpLTEQ:
		return bytes.Compare(s, p) <= 0
	}
	return false
}

// testString performs string comparison for sargs.
func testString(node *SargNode, s string) bool {
	switch node.Op {
	case OpLike:
		return LikeCmp(s, node.Value.S)
	case OpILike:
		if node.ilikePatternSet {
			return iLikeCmpFolded(s, node.ilikePattern)
		}
		return ILikeCmp(s, node.Value.S)
	case OpEqual:
		return s == node.Value.S
	case OpNEQ:
		return s != node.Value.S
	case OpGT:
		return s > node.Value.S
	case OpLT:
		return s < node.Value.S
	case OpGTEQ:
		return s >= node.Value.S
	case OpLTEQ:
		return s <= node.Value.S
	}
	return false
}

// testInt performs integer comparison for sargs.
func testInt(op int, nodeVal, fieldVal int) bool {
	switch op {
	case OpEqual:
		return nodeVal == fieldVal
	case OpGT:
		return nodeVal < fieldVal
	case OpLT:
		return nodeVal > fieldVal
	case OpGTEQ:
		return nodeVal <= fieldVal
	case OpLTEQ:
		return nodeVal >= fieldVal
	case OpNEQ:
		return nodeVal != fieldVal
	}
	return false
}

// testDouble performs float comparison for sargs.
func testDouble(op int, nodeVal, fieldVal float64) bool {
	switch op {
	case OpEqual:
		return nodeVal == fieldVal
	case OpGT:
		return nodeVal < fieldVal
	case OpLT:
		return nodeVal > fieldVal
	case OpGTEQ:
		return nodeVal <= fieldVal
	case OpLTEQ:
		return nodeVal >= fieldVal
	case OpNEQ:
		return nodeVal != fieldVal
	}
	return false
}
