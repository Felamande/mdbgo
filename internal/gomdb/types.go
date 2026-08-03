// Package gomdb is a pure Go implementation of an MDB (Microsoft Access) file reader.
// It provides the same capabilities as the C-based mdbtools but without CGo dependencies.
package gomdb

// Page types
const (
	PageDB    = 0
	PageData  = 1
	PageTable = 2
	PageIndex = 3
	PageLeaf  = 4
	PageMap   = 5
)

// Jet version constants
const (
	Jet3      = 0
	Jet4      = 1
	Accdb2007 = 2
	Accdb2010 = 3
	Accdb2013 = 4
	Accdb2016 = 5
	Accdb2019 = 6
)

// Object types
const (
	ObjForm             = 0
	ObjTable            = 1
	ObjMacro            = 2
	ObjSystemTable      = 3
	ObjReport           = 4
	ObjQuery            = 5
	ObjLinkedTable      = 6
	ObjModule           = 7
	ObjRelationship     = 8
	ObjUnknown09        = 9
	ObjUnknown0A        = 10
	ObjDatabaseProperty = 11
	ObjAny              = -1
)

// Column data types
const (
	TypeBool     = 0x01
	TypeByte     = 0x02
	TypeInt      = 0x03
	TypeLongInt  = 0x04
	TypeMoney    = 0x05
	TypeFloat    = 0x06
	TypeDouble   = 0x07
	TypeDateTime = 0x08
	TypeBinary   = 0x09
	TypeText     = 0x0a
	TypeOLE      = 0x0b
	TypeMemo     = 0x0c
	TypeRepID    = 0x0f
	TypeNumeric  = 0x10
	TypeComplex  = 0x12
)

// Search argument operators
const (
	OpOr      = 1
	OpAnd     = 2
	OpNot     = 3
	OpEqual   = 4
	OpGT      = 5
	OpLT      = 6
	OpGTEQ    = 7
	OpLTEQ    = 8
	OpLike    = 9
	OpIsNull  = 10
	OpNotNull = 11
	OpILike   = 12
	OpNEQ     = 13
)

// Index-related
const (
	IdxUnique      = 0x01
	IdxIgnoreNulls = 0x02
	IdxRequired    = 0x08

	Asc  = 0
	Desc = 1

	MaxIdxCols   = 10
	MaxObjName   = 256
	MaxCols      = 256
	PageSize     = 4096
	MemoOverhead = 12
	BindSize     = 16384

	OffsetMask = 0x1fff
)

// Scan strategies
const (
	TableScan = iota
	LeafScan
	IndexScan
)

// UUID format
const (
	UUIDBraces4228   = 0
	UUIDNoBraces4226 = 1
)

// MdbFormatConstants holds Jet-version-dependent offsets for reading table/page metadata.
type MdbFormatConstants struct {
	PgSize             int
	RowCountOffset     int
	TabNumRowsOffset   int
	TabNumColsOffset   int
	TabNumIdxsOffset   int
	TabNumRidxsOffset  int
	TabUsageMapOffset  int
	TabFirstDpgOffset  int
	TabColsStartOffset int
	TabRidxEntrySize   int
	ColScaleOffset     int
	ColPrecOffset      int
	ColFlagsOffset     int
	ColSizeOffset      int
	ColNumOffset       int
	TabColEntrySize    int
	TabFreeMapOffset   int
	TabColOffsetVar    int
	TabColOffsetFixed  int
	TabRowColNumOffset int
}

// Jet4FormatConstants are the format offsets for Jet 4 (Access 2000+) databases.
var Jet4FormatConstants = MdbFormatConstants{
	PgSize:             4096,
	RowCountOffset:     0x0c,
	TabNumRowsOffset:   16,
	TabNumColsOffset:   45,
	TabNumIdxsOffset:   47,
	TabNumRidxsOffset:  51,
	TabUsageMapOffset:  55,
	TabFirstDpgOffset:  56,
	TabColsStartOffset: 63,
	TabRidxEntrySize:   12,
	ColScaleOffset:     11,
	ColPrecOffset:      12,
	ColFlagsOffset:     15,
	ColSizeOffset:      23,
	ColNumOffset:       5,
	TabColEntrySize:    25,
	TabFreeMapOffset:   59,
	TabColOffsetVar:    7,
	TabColOffsetFixed:  21,
	TabRowColNumOffset: 9,
}

// Jet3FormatConstants are the format offsets for Jet 3 (Access 97) databases.
var Jet3FormatConstants = MdbFormatConstants{
	PgSize:             2048,
	RowCountOffset:     0x08,
	TabNumRowsOffset:   12,
	TabNumColsOffset:   25,
	TabNumIdxsOffset:   27,
	TabNumRidxsOffset:  31,
	TabUsageMapOffset:  35,
	TabFirstDpgOffset:  36,
	TabColsStartOffset: 43,
	TabRidxEntrySize:   8,
	ColScaleOffset:     9,
	ColPrecOffset:      10,
	ColFlagsOffset:     13,
	ColSizeOffset:      16,
	ColNumOffset:       1,
	TabColEntrySize:    18,
	TabFreeMapOffset:   39,
	TabColOffsetVar:    3,
	TabColOffsetFixed:  14,
	TabRowColNumOffset: 5,
}

// Data type names used by the Access backend
var accessTypeNames = map[int]string{
	TypeBool:     "Boolean",
	TypeByte:     "Byte",
	TypeInt:      "Integer",
	TypeLongInt:  "Long Integer",
	TypeMoney:    "Currency",
	TypeFloat:    "Single",
	TypeDouble:   "Double",
	TypeDateTime: "DateTime",
	TypeBinary:   "Binary",
	TypeText:     "Text",
	TypeOLE:      "OLE",
	TypeMemo:     "Memo/Hyperlink",
	TypeRepID:    "Replication ID",
	TypeNumeric:  "Numeric",
	TypeComplex:  "Complex",
}

// Object type name strings
var objTypeNames = map[int]string{
	ObjForm:             "Form",
	ObjTable:            "Table",
	ObjMacro:            "Macro",
	ObjSystemTable:      "System Table",
	ObjReport:           "Report",
	ObjQuery:            "Query",
	ObjLinkedTable:      "Linked Table",
	ObjModule:           "Module",
	ObjRelationship:     "Relationship",
	ObjUnknown09:        "Unknown 0x09",
	ObjUnknown0A:        "Unknown 0x0A",
	ObjDatabaseProperty: "Database",
}

// Column represents metadata about a single column in a query result.
type Column struct {
	Name         string
	DatabaseType string
	Type         int
	Size         int64
}

// IsLogicalOp returns true if the operator is a logical connective (AND, OR, NOT).
func IsLogicalOp(op int) bool {
	return op == OpOr || op == OpAnd || op == OpNot
}

// IsRelationalOp returns true if the operator is a comparison operator.
func IsRelationalOp(op int) bool {
	return op == OpEqual || op == OpGT || op == OpLT || op == OpGTEQ || op == OpLTEQ ||
		op == OpNEQ || op == OpLike || op == OpILike || op == OpIsNull || op == OpNotNull
}

// ColTypeName returns the Access type name for a column type constant.
func ColTypeName(typ int) string {
	if name, ok := accessTypeNames[typ]; ok {
		return name
	}
	return ""
}

// ObjTypeName returns the human-readable name for an object type constant.
func ObjTypeName(typ int) string {
	if name, ok := objTypeNames[typ]; ok {
		return name
	}
	return ""
}
