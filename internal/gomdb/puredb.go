// Package gomdb is a pure Go implementation of an MDB (Microsoft Access) file reader.
// It provides the same capabilities as the C-based cmdb driver but without CGo dependencies.
//
// The public API mirrors cmdb.Query for easy driver integration.
package gomdb

import (
	"encoding/binary"
	"strconv"
	"time"
)

// Query represents an open query on an MDB database.
// It provides methods to iterate over rows and extract column values.
type Query struct {
	sql     *SQL
	ownsMdb bool
	fast    *fastScan
}

// OpenQuery opens an MDB database and executes a SQL query.
// The returned Query owns the MdbHandle and closes it when Close() is called.
func OpenQuery(path, query string) (*Query, error) {
	mdb, err := OpenMDB(path)
	if err != nil {
		return nil, err
	}

	q, err := openQueryOnMdb(mdb, query)
	if err != nil {
		mdb.Close()
		return nil, err
	}
	q.ownsMdb = true
	return q, nil
}

// OpenQueryOnHandle executes a SQL query on an already-open MdbHandle.
// The returned Query does NOT own the MdbHandle — Close() on the Query
// will not close the underlying file. The caller is responsible for the
// MdbHandle's lifecycle.
func OpenQueryOnHandle(mdb *MdbHandle, query string) (*Query, error) {
	q, err := openQueryOnMdb(mdb, query)
	if err != nil {
		return nil, err
	}
	q.ownsMdb = false
	return q, nil
}

func openQueryOnMdb(mdb *MdbHandle, query string) (*Query, error) {
	sql, err := mdb.OpenQuery(query)
	if err != nil {
		return nil, err
	}

	if sql.ColumnCount() == 0 && sql.CurTable == nil {
		return nil, &Error{Msg: "mdb: query produced no columns"}
	}

	q := &Query{sql: sql}
	if canFastScan(mdb, sql) {
		q.fast = newFastScan(mdb, sql)
	}
	return q, nil
}

// Close frees resources associated with the query.
// If the Query owns its MdbHandle (created via OpenQuery), the handle is closed.
// If created via OpenQueryOnHandle, the MdbHandle is left open.
func (q *Query) Close() error {
	if q.sql == nil {
		return nil
	}
	if q.fast != nil {
		q.fast.close()
		q.fast = nil
	}
	if q.ownsMdb && q.sql.Mdb != nil {
		q.sql.Mdb.Close()
	}
	q.sql = nil
	return nil
}

// Next advances to the next row in the result set.
// Returns (true, nil) if a row is available, (false, nil) if no more rows,
// or (false, error) on error.
func (q *Query) Next() (bool, error) {
	if q.sql == nil {
		return false, nil
	}
	if q.fast != nil {
		return q.fast.nextRow()
	}
	return q.sql.FetchRow()
}

// Columns returns the column names.
func (q *Query) Columns() []string {
	if q.sql == nil {
		return nil
	}
	cols := make([]string, q.sql.NumColumns)
	for i := 0; i < q.sql.NumColumns; i++ {
		cols[i] = q.sql.ColumnName(i)
	}
	return cols
}

// ColumnInfo returns metadata about the result columns.
func (q *Query) ColumnInfo() []Column {
	if q.sql == nil {
		return nil
	}
	return q.sql.ColumnInfo()
}

// Value returns the string value for column i in the current row.
func (q *Query) Value(i int) string {
	if q.sql == nil {
		return ""
	}
	if q.fast != nil {
		return q.fast.value(i)
	}
	return q.sql.Value(i)
}

// DriverValue returns the native database/sql value for a result column in
// the current row: nil, bool, int64, float64, time.Time, []byte, or string.
// It resolves the bound column once and converts in a single switch, avoiding
// the per-column IsNull/getter/value lookup chain used by the legacy API.
func (q *Query) DriverValue(i int) any {
	if q.sql == nil {
		return nil
	}
	if q.fast != nil {
		return q.fast.driverValue(i)
	}
	sql := q.sql
	if i < 0 || i >= sql.NumColumns || sql.CurTable == nil {
		return nil
	}
	col := sql.boundColumn(i)
	if col == nil || col.CurValueIsNull {
		return nil
	}
	mdb := sql.Mdb
	if mdb == nil {
		return nil
	}

	buf := mdb.pgBuf
	start := col.CurValueStart
	switch col.ColType {
	case TypeBool:
		return col.CurValueLen != 0
	case TypeByte:
		if col.CurValueLen >= 1 {
			return int64(buf[start])
		}
	case TypeInt:
		if col.CurValueLen >= 2 {
			return int64(GetInt16(buf, start))
		}
	case TypeLongInt, TypeComplex:
		if col.CurValueLen >= 4 {
			return int64(GetInt32(buf, start))
		}
	case TypeFloat:
		if col.CurValueLen >= 4 {
			return compatibilityFloat64(float64(GetSingle(buf, start)), 8, 32)
		}
	case TypeDouble:
		if col.CurValueLen >= 8 {
			return compatibilityFloat64(GetDouble(buf, start), 16, 64)
		}
	case TypeMoney:
		if col.CurValueLen >= 8 {
			return float64(int64(binary.LittleEndian.Uint64(buf[start:]))) / 10000
		}
	case TypeDateTime:
		return DateToTime(GetDouble(buf, start))
	case TypeBinary:
		return mdb.BinaryValue(col)
	default:
		return sql.Value(i)
	}
	return sql.Value(i)
}

// DriverRow returns the preformatted native values for the current row on
// fast-scan queries, or nil when the synchronous path is in use. Callers
// should fall back to DriverValue when nil is returned.
func (q *Query) DriverRow() []any {
	if q.fast != nil {
		return q.fast.driverRow()
	}
	return nil
}

// IsNull returns true if column i is NULL in the current row.
func (q *Query) IsNull(i int) bool {
	if q.sql == nil {
		return true
	}
	if q.fast != nil {
		return q.fast.isNull(i)
	}
	return q.sql.IsNull(i)
}

// BinaryValue returns the raw bytes for a binary column in the current row.
func (q *Query) BinaryValue(i int) []byte {
	if q.sql == nil {
		return nil
	}
	if q.fast != nil {
		return q.fast.binaryValue(i)
	}
	return q.sql.BinaryValue(i)
}

// DateTimeValue returns the time.Time value for a DateTime column in the current row.
func (q *Query) DateTimeValue(i int) (time.Time, bool) {
	if q.sql == nil {
		return time.Time{}, false
	}
	if q.fast != nil {
		return q.fast.dateTimeValue(i)
	}
	return q.sql.DateTimeValue(i)
}

// BoolValue returns a native Boolean value for column i.
func (q *Query) BoolValue(i int) (bool, bool) {
	if q.sql == nil {
		return false, false
	}
	if q.fast != nil {
		return q.fast.boolValue(i)
	}
	return q.sql.BoolValue(i)
}

// Int64Value returns a native integral value for column i.
func (q *Query) Int64Value(i int) (int64, bool) {
	if q.sql == nil {
		return 0, false
	}
	if q.fast != nil {
		return q.fast.int64Value(i)
	}
	return q.sql.Int64Value(i)
}

// Float64Value returns a native floating-point value for column i.
func (q *Query) Float64Value(i int) (float64, bool) {
	if q.sql == nil {
		return 0, false
	}
	if q.fast != nil {
		return q.fast.float64Value(i)
	}
	return q.sql.Float64Value(i)
}

// ParseInt parses a string as an int64. Returns (value, true) on success.
func ParseInt(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
}

// ParseFloat parses a string as a float64. Returns (value, true) on success.
func ParseFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// Error implements the error interface for gomdb errors.
type Error struct {
	Msg string
}

func (e *Error) Error() string {
	return e.Msg
}
