package gomdb

import (
	"bytes"
	"strings"
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
}

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

		if testSarg(mdb, col, node, &fields[col.ColNum]) {
			return 1
		}
		return 0
	}

	// Logical operators
	switch node.Op {
	case OpNot:
		if node.Left != nil {
			return 1 - testSargNode(mdb, node.Left, fields)
		}
		return 0

	case OpAnd:
		if node.Left != nil && !toBool(testSargNode(mdb, node.Left, fields)) {
			return 0
		}
		if node.Right != nil {
			return testSargNode(mdb, node.Right, fields)
		}
		return 1

	case OpOr:
		if node.Left != nil && toBool(testSargNode(mdb, node.Left, fields)) {
			return 1
		}
		if node.Right != nil {
			return testSargNode(mdb, node.Right, fields)
		}
		return 0
	}

	return 1
}

func toBool(v int) bool { return v != 0 }

// testSarg tests a single sarg condition against a field.
func testSarg(mdb *MdbHandle, col *MdbColumn, node *SargNode, field *MdbField) bool {
	if node.Op == OpIsNull {
		return field.IsNull
	}
	if node.Op == OpNotNull {
		return !field.IsNull
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
		return mdb.testSargString(node, field.Value)

	case TypeMemo, TypeRepID:
		if field.IsNull {
			return false
		}
		val := mdb.valueFromField(col, field)
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
