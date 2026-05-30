// Package puredb is a pure Go implementation of an MDB (Microsoft Access) file reader.
// It provides the same capabilities as the C-based cmdb driver but without CGo dependencies.
//
// The public API mirrors cmdb.Query for easy driver integration.
package puredb

import (
	"strconv"
	"time"
)

// Query represents an open query on an MDB database.
// It provides methods to iterate over rows and extract column values.
type Query struct {
	sql *SQL
}

// OpenQuery opens an MDB database and executes a SQL query.
// It returns a Query handle or an error.
func OpenQuery(path, query string) (*Query, error) {
	mdb, err := OpenMDB(path)
	if err != nil {
		return nil, err
	}

	sql, err := mdb.OpenQuery(query)
	if err != nil {
		mdb.Close()
		return nil, err
	}

	if sql.ColumnCount() == 0 && sql.CurTable == nil {
		mdb.Close()
		return nil, &Error{Msg: "mdb: query produced no columns"}
	}

	return &Query{sql: sql}, nil
}

// Close frees resources associated with the query.
func (q *Query) Close() error {
	if q.sql == nil {
		return nil
	}
	if q.sql.Mdb != nil {
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
	return q.sql.Value(i)
}

// IsNull returns true if column i is NULL in the current row.
func (q *Query) IsNull(i int) bool {
	if q.sql == nil {
		return true
	}
	return q.sql.IsNull(i)
}

// BinaryValue returns the raw bytes for a binary column in the current row.
func (q *Query) BinaryValue(i int) []byte {
	if q.sql == nil {
		return nil
	}
	return q.sql.BinaryValue(i)
}

// DateTimeValue returns the time.Time value for a DateTime column in the current row.
func (q *Query) DateTimeValue(i int) (time.Time, bool) {
	if q.sql == nil {
		return time.Time{}, false
	}
	return q.sql.DateTimeValue(i)
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

// Error implements the error interface for puredb errors.
type Error struct {
	Msg string
}

func (e *Error) Error() string {
	return e.Msg
}
