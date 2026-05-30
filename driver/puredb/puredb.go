// Package puredb provides a pure Go database/sql driver for Microsoft Access (.mdb) files.
// It registers under the name "mdb" and requires no CGo or external C libraries.
package puredb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	backend "github.com/Felamande/mdbgo/internal/puredb"
)

const DriverName = "gomdb"

func init() {
	sql.Register(DriverName, &Driver{})
}

// --- Driver ---

type Driver struct{}

func (d *Driver) Open(name string) (driver.Conn, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("mdb: empty database path")
	}
	return &Conn{path: name}, nil
}

// --- Conn ---

type Conn struct {
	path string
}

func (c *Conn) Prepare(query string) (driver.Stmt, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("mdb: empty query")
	}
	return &Stmt{conn: c, query: query}, nil
}

func (c *Conn) Close() error  { return nil }
func (c *Conn) Begin() (driver.Tx, error) {
	return nil, errors.New("mdb: transactions are not supported")
}

func (c *Conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	expanded, err := interpolateQuery(query, args)
	if err != nil {
		return nil, err
	}
	h, err := backend.OpenQuery(c.path, expanded)
	if err != nil {
		return nil, err
	}
	info := h.ColumnInfo()
	cols := make([]string, len(info))
	for i := range info {
		cols[i] = info[i].Name
	}
	return &Rows{h: h, cols: cols, info: info}, nil
}

func (c *Conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("mdb: exec is not supported; the driver is read-only")
}

func (c *Conn) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h, err := backend.OpenQuery(c.path, "LIST TABLES")
	if err != nil {
		return err
	}
	return h.Close()
}

var _ driver.Driver = (*Driver)(nil)
var _ driver.Conn = (*Conn)(nil)
var _ driver.QueryerContext = (*Conn)(nil)
var _ driver.ExecerContext = (*Conn)(nil)
var _ driver.Pinger = (*Conn)(nil)

// --- Stmt ---

type Stmt struct {
	conn  *Conn
	query string
}

func (s *Stmt) Close() error  { return nil }
func (s *Stmt) NumInput() int { return countPlaceholders(s.query) }

func (s *Stmt) Exec(args []driver.Value) (driver.Result, error) {
	return nil, errors.New("mdb: exec is not supported; the driver is read-only")
}

func (s *Stmt) Query(args []driver.Value) (driver.Rows, error) {
	named := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: arg}
	}
	return s.QueryContext(context.Background(), named)
}

func (s *Stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("mdb: exec is not supported; the driver is read-only")
}

func (s *Stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.conn.QueryContext(ctx, s.query, args)
}

var _ driver.Stmt = (*Stmt)(nil)
var _ driver.StmtQueryContext = (*Stmt)(nil)
var _ driver.StmtExecContext = (*Stmt)(nil)

// --- Rows ---

type Rows struct {
	h      *backend.Query
	cols   []string
	info   []backend.Column
	closed bool
}

func (r *Rows) Columns() []string {
	out := make([]string, len(r.cols))
	copy(out, r.cols)
	return out
}

func (r *Rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.h.Close()
}

func (r *Rows) Next(dest []driver.Value) error {
	if r.closed {
		return io.EOF
	}
	ok, err := r.h.Next()
	if err != nil {
		return err
	}
	if !ok {
		return io.EOF
	}
	if len(dest) < len(r.cols) {
		return fmt.Errorf("mdb: destination has %d values, need %d", len(dest), len(r.cols))
	}
	for i := range r.cols {
		dest[i] = r.value(i)
	}
	return nil
}

func (r *Rows) ColumnTypeDatabaseTypeName(index int) string {
	if index < 0 || index >= len(r.info) {
		return ""
	}
	return r.info[index].DatabaseType
}

func (r *Rows) ColumnTypeLength(index int) (int64, bool) {
	if index < 0 || index >= len(r.info) || r.info[index].Size <= 0 {
		return 0, false
	}
	switch r.info[index].Type {
	case backend.TypeText, backend.TypeMemo, backend.TypeBinary, backend.TypeOLE:
		return r.info[index].Size, true
	}
	return 0, false
}

func (r *Rows) ColumnTypeNullable(index int) (bool, bool) { return true, false }

func (r *Rows) ColumnTypeScanType(index int) reflect.Type {
	if index < 0 || index >= len(r.info) {
		return reflect.TypeOf("")
	}
	switch r.info[index].Type {
	case backend.TypeBool:
		return reflect.TypeOf(false)
	case backend.TypeByte, backend.TypeInt, backend.TypeLongInt, backend.TypeComplex:
		return reflect.TypeOf(int64(0))
	case backend.TypeMoney, backend.TypeFloat, backend.TypeDouble:
		return reflect.TypeOf(float64(0))
	case backend.TypeDateTime:
		return reflect.TypeOf(time.Time{})
	case backend.TypeBinary, backend.TypeOLE:
		return reflect.TypeOf([]byte{})
	}
	return reflect.TypeOf("")
}

func (r *Rows) value(index int) driver.Value {
	if r.h.IsNull(index) {
		return nil
	}
	raw := r.h.Value(index)
	switch r.info[index].Type {
	case backend.TypeBool:
		return raw == "1" || strings.EqualFold(raw, "true")
	case backend.TypeByte, backend.TypeInt, backend.TypeLongInt, backend.TypeComplex:
		if v, ok := backend.ParseInt(raw); ok {
			return v
		}
	case backend.TypeMoney, backend.TypeFloat, backend.TypeDouble:
		if v, ok := backend.ParseFloat(raw); ok {
			return v
		}
	case backend.TypeDateTime:
		if v, ok := r.h.DateTimeValue(index); ok {
			return v
		}
	case backend.TypeBinary:
		return r.h.BinaryValue(index)
	}
	return raw
}

var _ driver.Rows = (*Rows)(nil)
var _ driver.RowsColumnTypeDatabaseTypeName = (*Rows)(nil)
var _ driver.RowsColumnTypeLength = (*Rows)(nil)
var _ driver.RowsColumnTypeNullable = (*Rows)(nil)
var _ driver.RowsColumnTypeScanType = (*Rows)(nil)

// --- Query utilities ---

func interpolateQuery(query string, args []driver.NamedValue) (string, error) {
	var b strings.Builder
	argIndex := 0
	inSingle, inDouble, inBracket := false, false, false

	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch ch {
		case '\'':
			b.WriteByte(ch)
			if inSingle && i+1 < len(query) && query[i+1] == '\'' {
				i++
				b.WriteByte(query[i])
				continue
			}
			if !inDouble && !inBracket {
				inSingle = !inSingle
			}
		case '"':
			b.WriteByte(ch)
			if inDouble && i+1 < len(query) && query[i+1] == '"' {
				i++
				b.WriteByte(query[i])
				continue
			}
			if !inSingle && !inBracket {
				inDouble = !inDouble
			}
		case '[':
			b.WriteByte(ch)
			if !inSingle && !inDouble {
				inBracket = true
			}
		case ']':
			b.WriteByte(ch)
			if !inSingle && !inDouble {
				inBracket = false
			}
		case '?':
			if inSingle || inDouble || inBracket {
				b.WriteByte(ch)
				continue
			}
			if argIndex >= len(args) {
				return "", errors.New("mdb: not enough query arguments")
			}
			lit, err := sqlLiteral(args[argIndex].Value)
			if err != nil {
				return "", err
			}
			b.WriteString(lit)
			argIndex++
		default:
			b.WriteByte(ch)
		}
	}
	if argIndex != len(args) {
		return "", errors.New("mdb: too many query arguments")
	}
	return b.String(), nil
}

func countPlaceholders(query string) int {
	count := 0
	inSingle, inDouble, inBracket := false, false, false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch ch {
		case '\'':
			if inSingle && i+1 < len(query) && query[i+1] == '\'' {
				i++
				continue
			}
			if !inDouble && !inBracket {
				inSingle = !inSingle
			}
		case '"':
			if inDouble && i+1 < len(query) && query[i+1] == '"' {
				i++
				continue
			}
			if !inSingle && !inBracket {
				inDouble = !inDouble
			}
		case '[':
			if !inSingle && !inDouble {
				inBracket = true
			}
		case ']':
			if !inSingle && !inDouble {
				inBracket = false
			}
		case '?':
			if !inSingle && !inDouble && !inBracket {
				count++
			}
		}
	}
	return count
}

func sqlLiteral(v driver.Value) (string, error) {
	switch x := v.(type) {
	case nil:
		return "NULL", nil
	case bool:
		if x {
			return "1", nil
		}
		return "0", nil
	case int64:
		return fmt.Sprintf("%d", x), nil
	case float64:
		return fmt.Sprintf("%.16g", x), nil
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'", nil
	case []byte:
		return "'" + strings.ReplaceAll(string(x), "'", "''") + "'", nil
	case time.Time:
		return fmt.Sprintf("strptime('%s','%%Y-%%m-%%d %%H:%%M:%%S')", x.Format("2006-01-02 15:04:05")), nil
	default:
		return "", fmt.Errorf("mdb: unsupported query argument type %T", v)
	}
}
