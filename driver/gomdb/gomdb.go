// Package gomdb provides a pure Go database/sql driver for Microsoft Access (.mdb) files.
// It registers under the name "mdb" and requires no CGo or external C libraries.
package gomdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	backend "github.com/Felamande/mdbgo/internal/gomdb"
)

const DriverName = "gomdb"

const maxCachedPlans = 16

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
	path  string
	mdb   *backend.MdbHandle
	plans map[string]*backend.Plan
}

func (c *Conn) Prepare(query string) (driver.Stmt, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("mdb: empty query")
	}
	return &Stmt{conn: c, query: query}, nil
}

func (c *Conn) Close() error {
	c.plans = nil
	if c.mdb != nil {
		c.mdb.Close()
		c.mdb = nil
	}
	return nil
}

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
	h, plan, err := c.openQuery(expanded)
	if err != nil {
		return nil, err
	}
	info := h.ColumnInfo()
	cols := make([]string, len(info))
	for i := range info {
		cols[i] = info[i].Name
	}
	return &Rows{h: h, cols: cols, info: info, conn: c, planKey: expanded, plan: plan}, nil
}

func (c *Conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("mdb: exec is not supported; the driver is read-only")
}

func (c *Conn) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h, _, err := c.openQuery("LIST TABLES")
	if err != nil {
		return err
	}
	return h.Close()
}

// openQuery lazily opens the MDB file (if not already open) and executes a
// query, reusing a cached plan when one is available. The returned plan is
// non-nil only for cache hits; fresh queries return nil and the Rows close
// path captures a new plan for future executions.
func (c *Conn) openQuery(query string) (*backend.Query, *backend.Plan, error) {
	if c.mdb == nil {
		mdb, err := backend.OpenMDB(c.path)
		if err != nil {
			return nil, nil, err
		}
		c.mdb = mdb
	}
	if c.plans == nil {
		c.plans = make(map[string]*backend.Plan)
	}
	if p, ok := c.plans[query]; ok {
		if q, err := p.Execute(c.mdb); err == nil {
			return q, p, nil
		}
		delete(c.plans, query)
	}
	q, err := backend.OpenQueryOnHandle(c.mdb, query)
	if err != nil {
		return nil, nil, err
	}
	return q, nil, nil
}

func (c *Conn) cachePlan(key string, p *backend.Plan) {
	if c.plans == nil {
		c.plans = make(map[string]*backend.Plan)
	}
	if len(c.plans) >= maxCachedPlans {
		clear(c.plans)
	}
	c.plans[key] = p
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
	h       *backend.Query
	cols    []string
	info    []backend.Column
	conn    *Conn
	planKey string
	plan    *backend.Plan
	closed  bool
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
	if r.h != nil {
		// Fresh queries become reusable plans; cache hits just release the
		// plan through the normal Query close path.
		if r.plan == nil && r.conn != nil {
			if p := r.h.CapturePlan(); p != nil {
				r.conn.cachePlan(r.planKey, p)
			}
		}
		return r.h.Close()
	}
	return nil
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
	if vals := r.h.DriverRow(); vals != nil && len(vals) >= len(r.cols) {
		for i := range r.cols {
			dest[i] = vals[i]
		}
		return nil
	}
	for i := range r.cols {
		dest[i] = r.h.DriverValue(i)
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

var _ driver.Rows = (*Rows)(nil)
var _ driver.RowsColumnTypeDatabaseTypeName = (*Rows)(nil)
var _ driver.RowsColumnTypeLength = (*Rows)(nil)
var _ driver.RowsColumnTypeNullable = (*Rows)(nil)
var _ driver.RowsColumnTypeScanType = (*Rows)(nil)

// --- Query utilities ---

func interpolateQuery(query string, args []driver.NamedValue) (string, error) {
	var b strings.Builder
	argIndex := 0
	lastCopy := 0
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
			if inSingle || inDouble || inBracket {
				continue
			}
			if argIndex >= len(args) {
				return "", errors.New("mdb: not enough query arguments")
			}
			lit, err := sqlLiteral(args[argIndex].Value)
			if err != nil {
				return "", err
			}
			if argIndex == 0 {
				b.Grow(len(query) + len(lit))
			}
			b.WriteString(query[lastCopy:i])
			b.WriteString(lit)
			lastCopy = i + 1
			argIndex++
		}
	}
	if argIndex != len(args) {
		return "", errors.New("mdb: too many query arguments")
	}
	if argIndex == 0 {
		return query, nil
	}
	b.WriteString(query[lastCopy:])
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
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'g', 16, 64), nil
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'", nil
	case []byte:
		return "'" + strings.ReplaceAll(string(x), "'", "''") + "'", nil
	case time.Time:
		return "strptime('" + x.Format("2006-01-02 15:04:05") + "','%Y-%m-%d %H:%M:%S')", nil
	default:
		return "", fmt.Errorf("mdb: unsupported query argument type %T", v)
	}
}
