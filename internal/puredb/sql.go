package puredb

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SQL holds the state for a parsed SQL query.
type SQL struct {
	Mdb *MdbHandle

	// Parsed columns
	Columns     []*SQLColumn
	NumColumns  int
	AllColumns  bool
	SelCount    bool

	// Parsed tables
	Tables     []*SQLTable
	NumTables  int

	// Sarg tree (WHERE clause)
	SargTree  *SargNode
	SargStack []*SargNode

	// Bound values
	BoundValues [][]byte

	// Current table being queried
	CurTable *MdbTableDef

	// Limit
	Limit        int
	LimitPercent bool
	RowCount     int

	// Error
	ErrorMsg string
	HasError bool
}

// SQLColumn represents a column reference in the SQL query.
type SQLColumn struct {
	Name     string
	DispSize int
}

// SQLTable represents a table reference in the SQL query.
type SQLTable struct {
	Name  string
	Alias string
}

// NewSQL creates a new SQL state for query execution.
func NewSQL(mdb *MdbHandle) *SQL {
	return &SQL{
		Mdb:         mdb,
		Columns:     make([]*SQLColumn, 0),
		Tables:      make([]*SQLTable, 0),
		BoundValues: make([][]byte, 0),
		Limit:       -1,
	}
}

// OpenQuery executes a SQL query against the database.
func (mdb *MdbHandle) OpenQuery(query string) (*SQL, error) {
	sql := NewSQL(mdb)

	query = strings.TrimSpace(query)

	upper := strings.ToUpper(query)

	if strings.HasPrefix(upper, "LIST TABLES") {
		if err := mdb.ListTables(sql); err != nil {
			return nil, err
		}
		// Bind the single column
		mdb.sqlBindAll(sql)
		return sql, nil
	}

	if strings.HasPrefix(upper, "DESCRIBE TABLE ") {
		tableName := strings.TrimSpace(query[len("DESCRIBE TABLE "):])
		// Remove quotes/brackets
		tableName = strings.Trim(tableName, `"'[]`)
		if err := mdb.DescribeTable(sql, tableName); err != nil {
			return nil, err
		}
		mdb.sqlBindAll(sql)
		return sql, nil
	}

	// SELECT query
	if err := parseSelect(sql, query); err != nil {
		return nil, err
	}

	if sql.CurTable == nil {
		return nil, fmt.Errorf("puredb: no result table for query")
	}

	// Bind columns for value extraction
	if err := mdb.sqlBindAll(sql); err != nil {
		return nil, err
	}

	return sql, nil
}

// sqlBindAll binds all result columns so values can be extracted.
func (mdb *MdbHandle) sqlBindAll(sql *SQL) error {
	for i := 0; i < sql.NumColumns; i++ {
		boundValue := make([]byte, mdb.bindSize)
		sql.BoundValues = append(sql.BoundValues, boundValue)
		if err := mdb.sqlBindColumn(sql, i+1, boundValue); err != nil {
			return err
		}
	}
	return nil
}

// sqlBindColumn binds a single result column by its ordinal (1-based).
func (mdb *MdbHandle) sqlBindColumn(sql *SQL, colNum int, buf []byte) error {
	if colNum <= 0 || colNum > sql.NumColumns {
		return fmt.Errorf("puredb: column %d out of range", colNum)
	}

	sqlCol := sql.Columns[colNum-1]

	// Find the matching MdbColumn in the current table
	for _, col := range sql.CurTable.Columns {
		if equalFold(col.Name, sqlCol.Name) {
			col.BindPtr = buf
			return nil
		}
	}

	return fmt.Errorf("puredb: column %q not found in table", sqlCol.Name)
}

// --- Tokenizer ---

type tokenType int

const (
	tokEOF tokenType = iota
	tokIdent
	tokString
	tokNumber
	tokComma
	tokStar
	tokLParen
	tokRParen
	tokDot
	tokSemicolon
)

type token struct {
	typ   tokenType
	value string
}

type lexer struct {
	input []byte
	pos   int
}

func newLexer(query string) *lexer {
	return &lexer{input: []byte(query), pos: 0}
}

func (l *lexer) skipWS() {
	for l.pos < len(l.input) && (l.input[l.pos] == ' ' || l.input[l.pos] == '\t' || l.input[l.pos] == '\n' || l.input[l.pos] == '\r') {
		l.pos++
	}
}

func (l *lexer) next() token {
	l.skipWS()
	if l.pos >= len(l.input) {
		return token{typ: tokEOF}
	}

	ch := l.input[l.pos]

	switch {
	case ch == ',':
		l.pos++
		return token{typ: tokComma, value: ","}
	case ch == '*':
		l.pos++
		return token{typ: tokStar, value: "*"}
	case ch == '(':
		l.pos++
		return token{typ: tokLParen, value: "("}
	case ch == ')':
		l.pos++
		return token{typ: tokRParen, value: ")"}
	case ch == '.':
		l.pos++
		return token{typ: tokDot, value: "."}
	case ch == ';':
		l.pos++
		return token{typ: tokSemicolon, value: ";"}
	case ch == '\'':
		// String literal
		start := l.pos
		l.pos++
		for l.pos < len(l.input) {
			if l.input[l.pos] == '\'' {
				if l.pos+1 < len(l.input) && l.input[l.pos+1] == '\'' {
					l.pos += 2 // escaped quote
				} else {
					l.pos++ // end of string
					break
				}
			} else {
				l.pos++
			}
		}
		return token{typ: tokString, value: string(l.input[start:l.pos])}

	case ch == '[':
		// Quoted identifier
		l.pos++
		start := l.pos
		for l.pos < len(l.input) && l.input[l.pos] != ']' {
			l.pos++
		}
		val := string(l.input[start:l.pos])
		if l.pos < len(l.input) {
			l.pos++ // skip ']'
		}
		return token{typ: tokIdent, value: val}

	case (ch >= '0' && ch <= '9') || (ch == '-' && l.pos+1 < len(l.input) && l.input[l.pos+1] >= '0' && l.input[l.pos+1] <= '9'):
		// Number
		start := l.pos
		if l.input[l.pos] == '-' {
			l.pos++
		}
		hasDot := false
		for l.pos < len(l.input) {
			c := l.input[l.pos]
			if c >= '0' && c <= '9' {
				l.pos++
			} else if c == '.' && !hasDot {
				hasDot = true
				l.pos++
			} else {
				break
			}
		}
		return token{typ: tokNumber, value: string(l.input[start:l.pos])}

	case (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || ch == '#':
		// Identifier or keyword
		start := l.pos
		for l.pos < len(l.input) {
			c := l.input[l.pos]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '#' {
				l.pos++
			} else {
				break
			}
		}
		return token{typ: tokIdent, value: string(l.input[start:l.pos])}

	case ch == '"':
		// Double-quoted identifier
		l.pos++
		start := l.pos
		for l.pos < len(l.input) && l.input[l.pos] != '"' {
			if l.input[l.pos] == '"' && l.pos+1 < len(l.input) && l.input[l.pos+1] == '"' {
				l.pos += 2 // escaped
			} else {
				l.pos++
			}
		}
		val := string(l.input[start:l.pos])
		if l.pos < len(l.input) {
			l.pos++
		}
		return token{typ: tokIdent, value: val}

	default:
		// Operators and other single chars
		if l.pos+1 < len(l.input) {
			two := string([]byte{l.input[l.pos], l.input[l.pos+1]})
			switch two {
			case "<>", "<=", ">=":
				l.pos += 2
				return token{typ: tokIdent, value: two}
			}
		}
		val := string(ch)
		l.pos++
		return token{typ: tokIdent, value: val}
	}
}

// --- Recursive Descent Parser ---

type parser struct {
	l       *lexer
	sql     *SQL
	cur     token
	peeked  bool
}

func newParser(sql *SQL, query string) *parser {
	return &parser{
		l:   newLexer(query),
		sql: sql,
	}
}

func (p *parser) peek() token {
	if !p.peeked {
		p.cur = p.l.next()
		p.peeked = true
	}
	return p.cur
}

func (p *parser) next() token {
	if p.peeked {
		p.peeked = false
		return p.cur
	}
	return p.l.next()
}

func (p *parser) expect(typ tokenType) (token, error) {
	tok := p.next()
	if tok.typ != typ {
		return tok, fmt.Errorf("puredb: expected token type %d, got %v", typ, tok)
	}
	return tok, nil
}

// parseSelect parses a SELECT statement.
func parseSelect(sql *SQL, query string) error {
	p := newParser(sql, query)
	return p.parse()
}

func (p *parser) parse() error {
	tok := p.next()

	if tok.typ != tokIdent {
		return fmt.Errorf("puredb: expected SELECT, got %v", tok)
	}

	upper := strings.ToUpper(tok.value)

	switch upper {
	case "SELECT":
		return p.parseSelectStmt()
	default:
		return fmt.Errorf("puredb: unexpected keyword %q", tok.value)
	}
}

func (p *parser) parseSelectStmt() error {
	// Parse SELECT column list
	if err := p.parseSelectList(); err != nil {
		return err
	}

	// Expect FROM
	fromTok := p.next()
	if strings.ToUpper(fromTok.value) != "FROM" {
		return fmt.Errorf("puredb: expected FROM, got %q", fromTok.value)
	}

	// Parse table name
	if err := p.parseTableList(); err != nil {
		return err
	}

	// Optional WHERE, ORDER BY, LIMIT
	for {
		tok := p.peek()
		if tok.typ == tokEOF || tok.typ == tokSemicolon {
			break
		}

		upper := strings.ToUpper(tok.value)
		switch upper {
		case "WHERE":
			p.next()
			if err := p.parseWhereClause(); err != nil {
				return err
			}
		case "ORDER":
			p.next()
			if err := p.parseOrderBy(); err != nil {
				return err
			}
		case "LIMIT":
			p.next()
			if err := p.parseLimit(); err != nil {
				return err
			}
		default:
			// Unknown clause — stop parsing
			return nil
		}
	}

	// Execute the query
	return p.sql.mdbExecute()
}

func (p *parser) parseSelectList() error {
	for {
		tok := p.peek()
		if tok.typ == tokStar {
			p.next()
			p.sql.AllColumns = true
			// After *, we should see FROM
			return nil
		}

		if tok.typ == tokIdent {
			upper := strings.ToUpper(tok.value)
			if upper == "FROM" || upper == "WHERE" || upper == "ORDER" || upper == "LIMIT" {
				break
			}

			// Handle COUNT(*)
			if upper == "COUNT" {
				p.next()
				p.next() // (
				p.next() // *
				p.next() // )
				p.sql.SelCount = true
				return nil
			}

			p.next()
			p.sql.AddColumn(tok.value)

			// Check for comma
			if p.peek().typ == tokComma {
				p.next()
				continue
			}
			break
		}

		break
	}
	return nil
}

func (p *parser) parseTableList() error {
	tok := p.next()
	if tok.typ != tokIdent {
		return fmt.Errorf("puredb: expected table name, got %v", tok)
	}
	p.sql.AddTable(tok.value)
	return nil
}

func (p *parser) parseWhereClause() error {
	node, err := p.parseExpr()
	if err != nil {
		return err
	}
	p.sql.SargTree = node
	return nil
}

func (p *parser) parseExpr() (*SargNode, error) {
	return p.parseOr()
}

func (p *parser) parseOr() (*SargNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}

	for {
		tok := p.peek()
		if tok.typ == tokIdent && strings.ToUpper(tok.value) == "OR" {
			p.next()
			right, err := p.parseAnd()
			if err != nil {
				return nil, err
			}
			node := &SargNode{
				Op:    OpOr,
				Left:  left,
				Right: right,
			}
			left = node
		} else {
			break
		}
	}

	return left, nil
}

func (p *parser) parseAnd() (*SargNode, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}

	for {
		tok := p.peek()
		if tok.typ == tokIdent && strings.ToUpper(tok.value) == "AND" {
			p.next()
			right, err := p.parseNot()
			if err != nil {
				return nil, err
			}
			node := &SargNode{
				Op:    OpAnd,
				Left:  left,
				Right: right,
			}
			left = node
		} else {
			break
		}
	}

	return left, nil
}

func (p *parser) parseNot() (*SargNode, error) {
	tok := p.peek()
	if tok.typ == tokIdent && strings.ToUpper(tok.value) == "NOT" {
		p.next()
		left, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &SargNode{
			Op:   OpNot,
			Left: left,
		}, nil
	}
	return p.parseComparison()
}

func (p *parser) parseComparison() (*SargNode, error) {
	tok := p.peek()

	// Handle parenthesized expressions
	if tok.typ == tokLParen {
		p.next()
		node, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		p.next() // )
		return node, nil
	}

	// Must be an identifier (column name)
	if tok.typ != tokIdent {
		// This could be a literal comparison or something unexpected
		return nil, fmt.Errorf("puredb: expected column name, got %v", tok.value)
	}

	colName := tok.value
	p.next()

	// Check what follows
	nextTok := p.peek()
	if nextTok.typ == tokEOF || nextTok.typ == tokRParen {
		// Just a column name alone — not a valid comparison
		return &SargNode{
			Op:    OpEqual,
			Col:   nil,
			Value: MdbAny{I: 1},
		}, nil
	}

	upper := strings.ToUpper(nextTok.value)

	// Handle IS NULL / IS NOT NULL
	if upper == "IS" {
		p.next()
		notNull := false
		peek := p.peek()
		if strings.ToUpper(peek.value) == "NOT" {
			p.next()
			notNull = true
		}
		nullTok := p.next()
		if strings.ToUpper(nullTok.value) != "NULL" {
			return nil, fmt.Errorf("puredb: expected NULL after IS, got %q", nullTok.value)
		}

		op := OpIsNull
		if notNull {
			op = OpNotNull
		}

		return &SargNode{
			Op: op,
			Parent: &SargNode{Value: MdbAny{S: colName}},
		}, nil
	}

	// Handle function call: strptime('...','...')
	if upper == "STRPTIME" || nextTok.typ == tokLParen {
		// Skip: treat strptime() calls specially
		// strptime('date','format') → returned as a string value
		var value MdbAny
		var valType int

		if upper == "STRPTIME" {
			p.next() // STRPTIME
			p.next() // (
			dateStr := p.next().value
			p.next() // ,
			formatStr := p.next().value
			p.next() // )

			// Parse the date
			dateStr = strings.Trim(dateStr, "'")
			formatStr = strings.Trim(formatStr, "'")

			// Convert strptime format to Go format
			goFmt := strptimeFormatToGo(formatStr)
			if t, err := time.Parse(goFmt, dateStr); err == nil {
				td := TmToDate(t)
				value = MdbAny{D: td}
				valType = TypeDouble
			} else {
				return nil, fmt.Errorf("puredb: strptime parse error: %w", err)
			}
		} else {
			// Just skip the parenthesized content
			_ = colName
			p.next() // (
			value = MdbAny{D: 0}
			valType = TypeDouble
			return &SargNode{
				Op:      OpEqual,
				Col:     nil,
				Value:   value,
				ValType: valType,
			}, nil
		}

		// Now we need the comparison operator — look ahead
		opTok := p.peek()
		opUpper := strings.ToUpper(opTok.value)
		op := map[string]int{
			"=": OpEqual, "<": OpLT, ">": OpGT,
			"<=": OpLTEQ, ">=": OpGTEQ, "<>": OpNEQ,
		}[opUpper]
		if op == 0 {
			op = OpEqual
		} else {
			p.next()
		}

		return &SargNode{
			Op:      op,
			Col:     nil,
			Value:   value,
			ValType: valType,
		}, nil
	}

	// Binary comparison operator
	opUpper := strings.ToUpper(nextTok.value)
	opMap := map[string]int{
		"=": OpEqual, "<": OpLT, ">": OpGT,
		"<=": OpLTEQ, ">=": OpGTEQ, "<>": OpNEQ,
		"LIKE": OpLike, "ILIKE": OpILike,
	}
	op, known := opMap[opUpper]
	if !known {
		return nil, fmt.Errorf("puredb: unexpected token %q after column", nextTok.value)
	}
	p.next() // Consume operator

	// Parse the value
	valTok := p.next()
	var value MdbAny
	var valType int

	switch valTok.typ {
	case tokString:
		s := valTok.value
		if len(s) >= 2 && s[0] == '\'' {
			s = s[1 : len(s)-1] // strip quotes
		}
		value = MdbAny{S: s}
		valType = TypeText

	case tokNumber:
		if strings.Contains(valTok.value, ".") {
			d, _ := strconv.ParseFloat(valTok.value, 64)
			value = MdbAny{D: d}
			valType = TypeDouble
		} else {
			i, _ := strconv.Atoi(valTok.value)
			value = MdbAny{I: i}
			valType = TypeInt
		}

	case tokIdent:
		upper := strings.ToUpper(valTok.value)
		if upper == "NULL" {
			return &SargNode{
				Op:     OpIsNull,
				Parent: &SargNode{Value: MdbAny{S: colName}},
			}, nil
		}
		// Could be TRUE/FALSE
		if upper == "TRUE" {
			value = MdbAny{I: 1}
			valType = TypeInt
		} else if upper == "FALSE" {
			value = MdbAny{I: 0}
			valType = TypeInt
		} else {
			// Identifier — could be another column
			value = MdbAny{S: valTok.value}
			valType = TypeText
		}

	default:
		return nil, fmt.Errorf("puredb: expected value after operator, got %v", valTok)
	}

	return &SargNode{
		Op:      op,
		Col:     nil,
		Value:   value,
		ValType: valType,
		Parent:  &SargNode{Value: MdbAny{S: colName}},
	}, nil
}

func (p *parser) parseOrderBy() error {
	// Skip ORDER BY parsing — just consume tokens until LIMIT or EOF
	for {
		tok := p.peek()
		if tok.typ == tokEOF || tok.typ == tokSemicolon {
			break
		}
		upper := strings.ToUpper(tok.value)
		if upper == "LIMIT" || upper == "WHERE" {
			break
		}
		p.next()
	}
	return nil
}

func (p *parser) parseLimit() error {
	tok := p.next()
	limit, err := strconv.Atoi(tok.value)
	if err != nil {
		return fmt.Errorf("puredb: invalid LIMIT value: %q", tok.value)
	}
	p.sql.Limit = limit
	return nil
}

// --- Query Execution ---

// mdbExecute executes the parsed SQL query.
func (sql *SQL) mdbExecute() error {
	mdb := sql.Mdb

	if !mdb.IsJet4() && !mdb.IsJet3() {
		// Need to check — Jet3/Jet4 should have been set in OpenMDB
	}

	if sql.NumTables == 0 {
		return nil
	}

	tableName := sql.Tables[0].Name

	// Read catalog and find the table
	if err := mdb.ReadCatalog(ObjTable); err != nil {
		return fmt.Errorf("puredb: %w", err)
	}

	table, err := mdb.ReadTableByName(tableName, ObjTable)
	if err != nil {
		return fmt.Errorf("puredb: %w", err)
	}

	if err := mdb.ReadColumns(table); err != nil {
		return fmt.Errorf("puredb: %w", err)
	}

	// Handle COUNT(*) without WHERE
	if sql.SelCount && sql.SargTree == nil {
		ttable := mdb.CreateTempTable("#count")
		col := &MdbColumn{}
		FillTempCol(col, "count", 30, TypeText, false)
		col.RowColNum = 1
		mdb.TempTableAddCol(ttable, col)

		var fields [1]MdbField
		rowBuf := make([]byte, mdb.fmt.PgSize)
		countStr := fmt.Sprintf("%d", table.NumRows)
		ucs2 := ASCIItoUCS2(countStr)
		FillTempField(&fields[0], ucs2, len(ucs2), false, false, 0, 0)
		rowSize := mdb.PackRow(ttable, rowBuf, 1, fields[:])
		mdb.AddRowToTempTable(ttable, rowBuf, rowSize)
		ttable.NumRows++

		// Add SQL column for "count"
		sql.AddColumn("count")
		sql.CurTable = ttable
		return nil
	}

	// Read indices
	mdb.ReadIndices(table)

	// Rewind for reading
	mdb.RewindTable(table)

	// Handle SELECT * — add all columns
	if sql.AllColumns {
		for _, col := range table.Columns {
			sql.AddColumn(col.Name)
		}
	}

	// Resolve column names in sarg tree to MdbColumn pointers
	if sql.SargTree != nil {
		resolveSargColumns(sql.SargTree, table)
	}

	// Move sarg tree to table
	table.SargTree = sql.SargTree
	sql.SargTree = nil

	// Convert LIMIT PERCENT to absolute limit
	if sql.Limit >= 0 && sql.LimitPercent && table.NumRows > 0 {
		sql.Limit = int(float64(table.NumRows) / 100 * float64(sql.Limit))
		sql.LimitPercent = false
	}

	sql.CurTable = table

	return nil
}

// resolveSargColumns walks the sarg tree and resolves column names to MdbColumn pointers.
func resolveSargColumns(node *SargNode, table *MdbTableDef) {
	if node == nil {
		return
	}

	if IsRelationalOp(node.Op) && node.Parent != nil {
		colName := node.Parent.Value.S
		for _, col := range table.Columns {
			if equalFold(col.Name, colName) {
				node.Col = col

				// Handle date column with integer value (UNIX timestamp → date)
				if col.ColType == TypeDateTime && node.ValType == TypeInt {
					t := time.Unix(int64(node.Value.I), 0).UTC()
					node.Value = MdbAny{D: TmToDate(t)}
					node.ValType = TypeDouble
				}
				break
			}
		}
	}

	resolveSargColumns(node.Left, table)
	resolveSargColumns(node.Right, table)
}

// --- SQL Helper Methods ---

// AddColumn adds a column to the SQL result set.
func (sql *SQL) AddColumn(name string) {
	sql.Columns = append(sql.Columns, &SQLColumn{Name: name})
	sql.NumColumns++
}

// AddTable adds a table to the SQL query.
func (sql *SQL) AddTable(name string) {
	sql.Tables = append(sql.Tables, &SQLTable{Name: name})
	sql.NumTables++
}

// FetchRow fetches the next row from the current table.
func (sql *SQL) FetchRow() (bool, error) {
	if sql.Mdb == nil {
		return false, fmt.Errorf("puredb: no database connection")
	}
	if sql.CurTable == nil {
		return false, fmt.Errorf("puredb: no current table")
	}

	hasRow, err := sql.Mdb.FetchRow(sql.CurTable)
	if err != nil {
		return false, err
	}

	if !hasRow {
		return false, nil
	}

	// Check limit
	if sql.Limit >= 0 && sql.RowCount+1 > sql.Limit {
		return false, nil
	}
	sql.RowCount++

	return true, nil
}

// ColumnCount returns the number of columns.
func (sql *SQL) ColumnCount() int {
	return sql.NumColumns
}

// ColumnName returns the name of a column by index.
func (sql *SQL) ColumnName(idx int) string {
	if idx < 0 || idx >= sql.NumColumns {
		return ""
	}
	return sql.Columns[idx].Name
}

// ColumnInfo returns metadata about the result columns.
func (sql *SQL) ColumnInfo() []Column {
	if sql.CurTable == nil {
		return nil
	}

	info := make([]Column, sql.NumColumns)
	for i := 0; i < sql.NumColumns; i++ {
		sqlCol := sql.Columns[i]
		info[i] = Column{
			Name: sqlCol.Name,
		}

		// Find the matching MdbColumn
		for _, col := range sql.CurTable.Columns {
			if equalFold(col.Name, sqlCol.Name) {
				info[i].Type = col.ColType
				info[i].DatabaseType = ColTypeName(col.ColType)
				info[i].Size = int64(col.ColSize)
				break
			}
		}
	}
	return info
}

// Value returns the bound string value for a column.
func (sql *SQL) Value(idx int) string {
	if idx < 0 || idx >= len(sql.BoundValues) {
		return ""
	}
	val := sql.BoundValues[idx]
	// Strip trailing null bytes
	for i, b := range val {
		if b == 0 {
			return string(val[:i])
		}
	}
	return string(val)
}

// IsNull checks if a column value is NULL.
func (sql *SQL) IsNull(idx int) bool {
	if sql.CurTable == nil || idx < 0 || idx >= sql.NumColumns {
		return true
	}
	sqlCol := sql.Columns[idx]
	for _, col := range sql.CurTable.Columns {
		if equalFold(col.Name, sqlCol.Name) {
			return col.CurValueIsNull
		}
	}
	return true
}

// BinaryValue returns the raw bytes for a binary column.
func (sql *SQL) BinaryValue(idx int) []byte {
	if sql.CurTable == nil || idx < 0 || idx >= sql.NumColumns {
		return nil
	}
	sqlCol := sql.Columns[idx]
	for _, col := range sql.CurTable.Columns {
		if equalFold(col.Name, sqlCol.Name) {
			return sql.Mdb.BinaryValue(col)
		}
	}
	return nil
}

// DateTimeValue returns the time.Time for a DateTime column.
func (sql *SQL) DateTimeValue(idx int) (time.Time, bool) {
	if sql.CurTable == nil || idx < 0 || idx >= sql.NumColumns {
		return time.Time{}, false
	}
	sqlCol := sql.Columns[idx]
	for _, col := range sql.CurTable.Columns {
		if equalFold(col.Name, sqlCol.Name) {
			return sql.Mdb.DateTimeValue(col)
		}
	}
	return time.Time{}, false
}

// strptimeFormatToGo converts a C/Python strptime format to Go time layout.
func strptimeFormatToGo(format string) string {
	// Simple conversion of common format specifiers
	replacer := strings.NewReplacer(
		"%Y", "2006",
		"%m", "01",
		"%d", "02",
		"%H", "15",
		"%M", "04",
		"%S", "05",
		"%y", "06",
	)
	return replacer.Replace(format)
}
