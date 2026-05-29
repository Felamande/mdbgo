package cmdb

/*
#cgo CFLAGS: -I${SRCDIR}/include -I${SRCDIR}/.. -D_CRT_SECURE_NO_WARNINGS -D_CRT_NONSTDC_NO_WARNINGS -DHAVE_DECL_PROGRAM_INVOCATION_SHORT_NAME=0
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"strconv"
	"time"
	"unsafe"
)

type Query struct {
	sql *C.MdbSQL
}

func OpenQuery(path, query string) (*Query, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	cquery := C.CString(query)
	defer C.free(unsafe.Pointer(cquery))

	sql := C.cmdb_query_open(cpath, cquery)
	if sql == nil {
		return nil, errors.New("mdb: failed to allocate query")
	}
	if msg := C.cmdb_query_error(sql); msg != nil && C.cmdb_query_has_error(sql) != 0 {
		err := errors.New(C.GoString(msg))
		C.cmdb_query_close(sql)
		return nil, err
	}
	if C.cmdb_query_column_count(sql) == 0 {
		C.cmdb_query_close(sql)
		return nil, errors.New("mdb: query produced no columns")
	}
	return &Query{sql: sql}, nil
}

func (q *Query) Close() error {
	if q.sql == nil {
		return nil
	}
	C.cmdb_query_close(q.sql)
	q.sql = nil
	return nil
}

func (q *Query) Columns() []string {
	n := int(C.cmdb_query_column_count(q.sql))
	cols := make([]string, n)
	for i := 0; i < n; i++ {
		cols[i] = C.GoString(C.cmdb_query_column_name(q.sql, C.uint(i)))
	}
	return cols
}

type Column struct {
	Name         string
	DatabaseType string
	Type         int
	Size         int64
}

const (
	TypeBool     = int(C.MDB_BOOL)
	TypeByte     = int(C.MDB_BYTE)
	TypeInt      = int(C.MDB_INT)
	TypeLongInt  = int(C.MDB_LONGINT)
	TypeMoney    = int(C.MDB_MONEY)
	TypeFloat    = int(C.MDB_FLOAT)
	TypeDouble   = int(C.MDB_DOUBLE)
	TypeDateTime = int(C.MDB_DATETIME)
	TypeBinary   = int(C.MDB_BINARY)
	TypeText     = int(C.MDB_TEXT)
	TypeOLE      = int(C.MDB_OLE)
	TypeMemo     = int(C.MDB_MEMO)
	TypeRepID    = int(C.MDB_REPID)
	TypeNumeric  = int(C.MDB_NUMERIC)
	TypeComplex  = int(C.MDB_COMPLEX)
)

func (q *Query) ColumnInfo() []Column {
	n := int(C.cmdb_query_column_count(q.sql))
	cols := make([]Column, n)
	for i := 0; i < n; i++ {
		cols[i] = Column{
			Name:         C.GoString(C.cmdb_query_column_name(q.sql, C.uint(i))),
			DatabaseType: C.GoString(C.cmdb_query_column_database_type(q.sql, C.uint(i))),
			Type:         int(C.cmdb_query_column_type(q.sql, C.uint(i))),
			Size:         int64(C.cmdb_query_column_size(q.sql, C.uint(i))),
		}
	}
	return cols
}

func (q *Query) Next() (bool, error) {
	rc := C.cmdb_query_next(q.sql)
	if rc < 0 {
		return false, fmt.Errorf("mdb: %s", C.GoString(C.cmdb_query_error(q.sql)))
	}
	return rc != 0, nil
}

func (q *Query) Value(i int) string {
	if i < 0 || i >= int(C.cmdb_query_column_count(q.sql)) {
		return ""
	}
	return C.GoString(C.cmdb_query_value(q.sql, C.uint(i)))
}

func (q *Query) IsNull(i int) bool {
	if i < 0 || i >= int(C.cmdb_query_column_count(q.sql)) {
		return true
	}
	return C.cmdb_query_column_is_null(q.sql, C.uint(i)) != 0
}

func (q *Query) BinaryValue(i int) []byte {
	if i < 0 || i >= int(C.cmdb_query_column_count(q.sql)) {
		return nil
	}
	var size C.int
	ptr := C.cmdb_query_binary_value(q.sql, C.uint(i), &size)
	if ptr == nil || size <= 0 {
		return nil
	}
	return C.GoBytes(ptr, size)
}

func (q *Query) DateTimeValue(i int) (time.Time, bool) {
	if i < 0 || i >= int(C.cmdb_query_column_count(q.sql)) {
		return time.Time{}, false
	}
	var year, month, day, hour, minute, second C.int
	if C.cmdb_query_datetime_value(q.sql, C.uint(i), &year, &month, &day, &hour, &minute, &second) == 0 {
		return time.Time{}, false
	}
	return time.Date(int(year), time.Month(month), int(day), int(hour), int(minute), int(second), 0, time.Local), true
}

func ParseInt(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
}

func ParseFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}
