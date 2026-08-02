package gomdb

// MdbColumn represents a column in an Access table.
type MdbColumn struct {
	Table    *MdbTableDef
	Name     string
	ColType  int
	ColSize  int
	ColNum   int
	ColPrec  int
	ColScale int

	IsFixed     bool
	IsLongAuto  bool
	IsUUIDAuto  bool
	FixedOffset int
	VarColNum   int
	RowColNum   int

	// Sargs for this column
	Sargs []*MdbSarg

	// Current row value state
	CurValueStart  int
	CurValueLen    int
	CurValueIsNull bool
	CurBlobPgRow   uint32
	ChunkSize      int

	// CurValueText is populated only when a caller asks for the legacy string
	// representation. Native driver getters can therefore avoid formatting.
	CurValueText      string
	CurValueTextValid bool

	// Binding
	BindPtr []byte
	LenPtr  *int
	BindLen int

	// Column properties
	Props *Properties
}

// MdbSarg holds a single search argument on a column.
type MdbSarg struct {
	Op    int
	Value MdbAny
}

// MdbAny is a union type for sarg values.
type MdbAny struct {
	I int
	D float64
	S string
}
