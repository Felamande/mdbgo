package gomdb

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// hasPrefixFold checks if s starts with prefix, case-insensitively (ASCII only).
// Avoids the allocation of strings.ToUpper for simple prefix checks.
func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		cs, cp := s[i], prefix[i]
		if cs >= 'A' && cs <= 'Z' {
			cs += 32
		}
		if cs != cp {
			return false
		}
	}
	return true
}

// SQL holds the state for a parsed SQL query.
type SQL struct {
	Mdb *MdbHandle

	// Parsed columns
	Columns    []*SQLColumn
	NumColumns int
	AllColumns bool
	SelCount   bool

	// Parsed tables
	Tables    []*SQLTable
	NumTables int

	// Sarg tree (WHERE clause)
	SargTree  *SargNode
	SargStack []*SargNode

	// Bound values
	BoundValues  [][]byte
	BoundColumns []*MdbColumn

	// Current table being queried
	CurTable *MdbTableDef

	// Limit
	Limit        int
	LimitPercent bool
	RowCount     int

	// ORDER BY terms and materialized sorted rows (non-nil SortedRows means
	// FetchRow serves rows from the sorted snapshot instead of the table).
	OrderBy    []OrderTerm
	SortedRows []sortedRow

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

	// Fast ASCII prefix check — avoids strings.ToUpper allocation
	if hasPrefixFold(query, "list tables") {
		if err := mdb.ListTables(sql); err != nil {
			return nil, err
		}
		mdb.sqlBindAll(sql)
		return sql, nil
	}

	if hasPrefixFold(query, "describe table ") {
		tableName := strings.TrimSpace(query[len("describe table "):])
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
		return nil, fmt.Errorf("gomdb: no result table for query")
	}

	// Bind columns for value extraction
	if err := mdb.sqlBindAll(sql); err != nil {
		return nil, err
	}

	return sql, nil
}

// sqlBindAll resolves result columns once. Values are decoded lazily by the
// SQL/driver getters; the old byte-buffer binding remains available through
// sqlBindColumn for catalog and compatibility callers.
func (mdb *MdbHandle) sqlBindAll(sql *SQL) error {
	sql.BoundColumns = make([]*MdbColumn, sql.NumColumns)
	for i := 0; i < sql.NumColumns; i++ {
		col := mdb.resultColumn(sql, i)
		if col == nil {
			return fmt.Errorf("gomdb: column %q not found in table", sql.Columns[i].Name)
		}
		sql.BoundColumns[i] = col
	}
	return nil
}

func (mdb *MdbHandle) resultColumn(sql *SQL, idx int) *MdbColumn {
	if sql == nil || sql.CurTable == nil || idx < 0 || idx >= sql.NumColumns {
		return nil
	}
	name := sql.Columns[idx].Name
	for _, col := range sql.CurTable.Columns {
		if equalFold(col.Name, name) {
			return col
		}
	}
	return nil
}

// sqlBindColumn binds a single result column by its ordinal (1-based).
func (mdb *MdbHandle) sqlBindColumn(sql *SQL, colNum int, buf []byte) error {
	if colNum <= 0 || colNum > sql.NumColumns {
		return fmt.Errorf("gomdb: column %d out of range", colNum)
	}

	sqlCol := sql.Columns[colNum-1]

	col := mdb.resultColumn(sql, colNum-1)
	if col == nil {
		return fmt.Errorf("gomdb: column %q not found in table", sqlCol.Name)
	}
	size := colBindSize(col)
	if len(buf) < size {
		return fmt.Errorf("gomdb: bind buffer for column %q is too small", sqlCol.Name)
	}
	col.BindPtr = buf[:size]
	col.BindLen = 0
	if len(sql.BoundColumns) < sql.NumColumns {
		sql.BoundColumns = make([]*MdbColumn, sql.NumColumns)
	}
	sql.BoundColumns[colNum-1] = col
	return nil
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
	input string
	pos   int
}

func newLexer(query string) *lexer {
	return &lexer{input: query, pos: 0}
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
		return token{typ: tokString, value: l.input[start:l.pos]}

	case ch == '[':
		// Quoted identifier
		l.pos++
		start := l.pos
		for l.pos < len(l.input) && l.input[l.pos] != ']' {
			l.pos++
		}
		val := l.input[start:l.pos]
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
		return token{typ: tokNumber, value: l.input[start:l.pos]}

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
		return token{typ: tokIdent, value: l.input[start:l.pos]}

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
		val := l.input[start:l.pos]
		if l.pos < len(l.input) {
			l.pos++
		}
		return token{typ: tokIdent, value: val}

	default:
		// Operators and other single chars
		if l.pos+1 < len(l.input) {
			two := l.input[l.pos : l.pos+2]
			switch two {
			case "<>", "<=", ">=":
				l.pos += 2
				return token{typ: tokIdent, value: two}
			}
		}
		val := l.input[l.pos : l.pos+1]
		l.pos++
		return token{typ: tokIdent, value: val}
	}
}

// --- Recursive Descent Parser ---

type parser struct {
	l      *lexer
	sql    *SQL
	cur    token
	peeked bool
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
		return tok, fmt.Errorf("gomdb: expected token type %d, got %v", typ, tok)
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
		return fmt.Errorf("gomdb: expected SELECT, got %v", tok)
	}

	if !equalFold(tok.value, "SELECT") {
		return fmt.Errorf("gomdb: unexpected keyword %q", tok.value)
	}
	return p.parseSelectStmt()
}

func (p *parser) parseSelectStmt() error {
	// Optional TOP N [PERCENT] — maps to the LIMIT machinery.
	if tok := p.peek(); tok.typ == tokIdent && equalFold(tok.value, "TOP") {
		p.next()
		num := p.next()
		if num.typ != tokNumber {
			return fmt.Errorf("gomdb: expected number after TOP, got %v", num)
		}
		n, err := strconv.Atoi(num.value)
		if err != nil {
			return fmt.Errorf("gomdb: invalid TOP value: %q", num.value)
		}
		p.sql.Limit = n
		if next := p.peek(); next.typ == tokIdent && equalFold(next.value, "PERCENT") {
			p.next()
			p.sql.LimitPercent = true
		}
	}

	// Parse SELECT column list
	if err := p.parseSelectList(); err != nil {
		return err
	}

	// Expect FROM
	fromTok := p.next()
	if !equalFold(fromTok.value, "FROM") {
		return fmt.Errorf("gomdb: expected FROM, got %q", fromTok.value)
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

		switch {
		case equalFold(tok.value, "WHERE"):
			p.next()
			if err := p.parseWhereClause(); err != nil {
				return err
			}
		case equalFold(tok.value, "ORDER"):
			p.next()
			if err := p.parseOrderBy(); err != nil {
				return err
			}
		case equalFold(tok.value, "LIMIT"):
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
			if equalFold(tok.value, "FROM") || equalFold(tok.value, "WHERE") ||
				equalFold(tok.value, "ORDER") || equalFold(tok.value, "LIMIT") {
				break
			}

			// Handle COUNT(*)
			if equalFold(tok.value, "COUNT") {
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
		return fmt.Errorf("gomdb: expected table name, got %v", tok)
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
		if tok.typ == tokIdent && equalFold(tok.value, "OR") {
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
		if tok.typ == tokIdent && equalFold(tok.value, "AND") {
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
	if tok.typ == tokIdent && equalFold(tok.value, "NOT") {
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

func comparisonOp(value string) (int, bool) {
	switch value {
	case "=":
		return OpEqual, true
	case "<":
		return OpLT, true
	case ">":
		return OpGT, true
	case "<=":
		return OpLTEQ, true
	case ">=":
		return OpGTEQ, true
	case "<>":
		return OpNEQ, true
	}
	if equalFold(value, "LIKE") {
		return OpLike, true
	}
	if equalFold(value, "ILIKE") {
		return OpILike, true
	}
	return 0, false
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
		return nil, fmt.Errorf("gomdb: expected column name, got %v", tok.value)
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

	// Handle IN (value, value, ...)
	if equalFold(nextTok.value, "IN") {
		p.next() // consume IN
		return p.parseInList(colName)
	}

	// Handle IS NULL / IS NOT NULL
	if equalFold(nextTok.value, "IS") {
		p.next()
		notNull := false
		peek := p.peek()
		if equalFold(peek.value, "NOT") {
			p.next()
			notNull = true
		}
		nullTok := p.next()
		if !equalFold(nullTok.value, "NULL") {
			return nil, fmt.Errorf("gomdb: expected NULL after IS, got %q", nullTok.value)
		}

		op := OpIsNull
		if notNull {
			op = OpNotNull
		}

		return &SargNode{
			Op:     op,
			Parent: &SargNode{Value: MdbAny{S: colName}},
		}, nil
	}

	// Handle function call: strptime('...','...')
	if equalFold(nextTok.value, "STRPTIME") || nextTok.typ == tokLParen {
		// Skip: treat strptime() calls specially
		// strptime('date','format') → returned as a string value
		var value MdbAny
		var valType int

		if equalFold(nextTok.value, "STRPTIME") {
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
				return nil, fmt.Errorf("gomdb: strptime parse error: %w", err)
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
		op, known := comparisonOp(opTok.value)
		if !known {
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
	op, known := comparisonOp(nextTok.value)
	if !known {
		return nil, fmt.Errorf("gomdb: unexpected token %q after column", nextTok.value)
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
		if equalFold(valTok.value, "NULL") {
			return &SargNode{
				Op:     OpIsNull,
				Parent: &SargNode{Value: MdbAny{S: colName}},
			}, nil
		}
		// Could be TRUE/FALSE
		if equalFold(valTok.value, "TRUE") {
			value = MdbAny{I: 1}
			valType = TypeInt
		} else if equalFold(valTok.value, "FALSE") {
			value = MdbAny{I: 0}
			valType = TypeInt
		} else {
			// Identifier — could be another column
			value = MdbAny{S: valTok.value}
			valType = TypeText
		}

	default:
		return nil, fmt.Errorf("gomdb: expected value after operator, got %v", valTok)
	}

	return &SargNode{
		Op:      op,
		Col:     nil,
		Value:   value,
		ValType: valType,
		Parent:  &SargNode{Value: MdbAny{S: colName}},
	}, nil
}

// parseInList parses the parenthesized literal list of an IN predicate.
// Elements are kept as raw text (plus a TypeInt hint for TRUE/FALSE) and
// converted to typed values later, once the target column is known.
func (p *parser) parseInList(colName string) (*SargNode, error) {
	if _, err := p.expect(tokLParen); err != nil {
		return nil, err
	}

	node := &SargNode{
		Op:     OpIn,
		Parent: &SargNode{Value: MdbAny{S: colName}},
	}

	for {
		tok := p.next()
		switch tok.typ {
		case tokString:
			s := tok.value
			if len(s) >= 2 && s[0] == '\'' {
				s = s[1 : len(s)-1] // strip quotes
			}
			node.InValues = append(node.InValues, MdbAny{S: s})
			node.InElemTypes = append(node.InElemTypes, TypeText)
		case tokNumber:
			// Kept as raw text; conversion to the column type happens at
			// resolve time (this also keeps 1.2.1.1-style dotted values
			// textual — callers must quote dotted OIDs).
			node.InValues = append(node.InValues, MdbAny{S: tok.value})
			node.InElemTypes = append(node.InElemTypes, TypeDouble)
		case tokIdent:
			switch {
			case equalFold(tok.value, "NULL"):
				// NULL never equals anything, so it is dropped.
				continue
			case equalFold(tok.value, "TRUE"):
				node.InValues = append(node.InValues, MdbAny{I: 1, S: "1"})
				node.InElemTypes = append(node.InElemTypes, TypeInt)
			case equalFold(tok.value, "FALSE"):
				node.InValues = append(node.InValues, MdbAny{I: 0, S: "0"})
				node.InElemTypes = append(node.InElemTypes, TypeInt)
			default:
				return nil, fmt.Errorf("gomdb: unsupported IN element %q", tok.value)
			}
		default:
			return nil, fmt.Errorf("gomdb: expected value in IN list, got %v", tok)
		}

		sep := p.next()
		if sep.typ == tokRParen {
			break
		}
		if sep.typ != tokComma {
			return nil, fmt.Errorf("gomdb: expected , or ) in IN list, got %v", sep)
		}
	}

	if len(node.InValues) == 0 {
		return nil, fmt.Errorf("gomdb: IN list cannot be empty")
	}
	return node, nil
}

func (p *parser) parseOrderBy() error {
	tok := p.next()
	if !equalFold(tok.value, "BY") {
		return fmt.Errorf("gomdb: expected BY after ORDER, got %q", tok.value)
	}

	for {
		term, err := p.parseOrderTerm()
		if err != nil {
			return err
		}
		p.sql.OrderBy = append(p.sql.OrderBy, *term)

		if p.peek().typ == tokComma {
			p.next()
			continue
		}
		break
	}
	return nil
}

// parseOrderTerm parses one ORDER BY key: a column name or Len(column),
// optionally followed by ASC/DESC.
func (p *parser) parseOrderTerm() (*OrderTerm, error) {
	tok := p.next()
	if tok.typ != tokIdent {
		return nil, fmt.Errorf("gomdb: expected ORDER BY column, got %v", tok)
	}

	term := &OrderTerm{Col: tok.value}
	if equalFold(tok.value, "LEN") {
		if p.peek().typ != tokLParen {
			return nil, fmt.Errorf("gomdb: expected ( after Len in ORDER BY")
		}
		p.next() // (
		col := p.next()
		if col.typ != tokIdent {
			return nil, fmt.Errorf("gomdb: expected column name inside Len(), got %v", col)
		}
		if close := p.next(); close.typ != tokRParen {
			return nil, fmt.Errorf("gomdb: expected ) after Len(%s), got %v", col.value, close)
		}
		term.Col = col.value
		term.IsLen = true
	}

	if next := p.peek(); next.typ == tokIdent {
		switch {
		case equalFold(next.value, "DESC"):
			p.next()
			term.Desc = true
		case equalFold(next.value, "ASC"):
			p.next()
		}
	}
	return term, nil
}

func (p *parser) parseLimit() error {
	tok := p.next()
	limit, err := strconv.Atoi(tok.value)
	if err != nil {
		return fmt.Errorf("gomdb: invalid LIMIT value: %q", tok.value)
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
		return fmt.Errorf("gomdb: %w", err)
	}

	table, err := mdb.ReadTableByName(tableName, ObjTable)
	if err != nil {
		return fmt.Errorf("gomdb: %w", err)
	}

	if len(table.Columns) == 0 {
		if err := mdb.ReadColumns(table); err != nil {
			return fmt.Errorf("gomdb: %w", err)
		}
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

	// ORDER BY materializes the matching rows, sorts them, and switches
	// FetchRow to serve from the sorted snapshot.
	if len(sql.OrderBy) > 0 {
		if err := mdb.materializeOrderBy(sql, table); err != nil {
			return err
		}
	}

	sql.CurTable = table

	return nil
}

// resolveSargColumns walks the sarg tree and resolves column names to MdbColumn pointers.
func resolveSargColumns(node *SargNode, table *MdbTableDef) {
	if node == nil {
		return
	}

	if node.Op == OpILike {
		node.ilikePattern = strings.ToLower(node.Value.S)
		node.ilikePatternSet = true
		node.patternBytes = []byte(node.ilikePattern)
	} else if IsRelationalOp(node.Op) && node.ValType == TypeText && node.Op != OpIn {
		// Precompute the byte form of string comparison patterns once per
		// query so per-row sarg evaluation never allocates.
		node.patternBytes = []byte(node.Value.S)
	}

	if IsRelationalOp(node.Op) && node.Parent != nil {
		colName := node.Parent.Value.S
		for _, col := range table.Columns {
			if equalFold(col.Name, colName) {
				node.Col = col

				if node.Op == OpIn {
					resolveInValues(node, col)
				} else if col.ColType == TypeDateTime && node.ValType == TypeInt {
					// Handle date column with integer value (UNIX timestamp → date)
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
		return false, fmt.Errorf("gomdb: no database connection")
	}
	if sql.CurTable == nil {
		return false, fmt.Errorf("gomdb: no current table")
	}

	// Sorted result sets are served from the materialized snapshot. The
	// cursor is RowCount, so plan reuse only has to reset RowCount.
	if sql.SortedRows != nil {
		if sql.Limit >= 0 && sql.RowCount+1 > sql.Limit {
			return false, nil
		}
		if sql.RowCount >= len(sql.SortedRows) {
			return false, nil
		}
		sr := sql.SortedRows[sql.RowCount]
		sql.RowCount++
		return sql.Mdb.serveSortedRow(sql.CurTable, sr)
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

		col := sql.boundColumn(i)
		if col != nil {
			info[i].Type = col.ColType
			info[i].DatabaseType = ColTypeName(col.ColType)
			info[i].Size = int64(col.ColSize)
		}
	}
	return info
}

func (sql *SQL) boundColumn(idx int) *MdbColumn {
	if idx < 0 || idx >= sql.NumColumns {
		return nil
	}
	if idx < len(sql.BoundColumns) && sql.BoundColumns[idx] != nil {
		return sql.BoundColumns[idx]
	}
	if sql.Mdb != nil {
		return sql.Mdb.resultColumn(sql, idx)
	}
	return nil
}

// Value returns the compatibility string value for a column. Formatting is
// deferred until this method is called.
func (sql *SQL) Value(idx int) string {
	if idx < 0 || idx >= sql.NumColumns {
		return ""
	}
	if col := sql.boundColumn(idx); col != nil {
		if col.BindPtr != nil {
			boundLen := col.BindLen
			if boundLen <= 0 || boundLen > len(col.BindPtr) {
				boundLen = clen(col.BindPtr)
			}
			return trimNUL(string(col.BindPtr[:boundLen]))
		}
		if sql.Mdb == nil {
			return ""
		}
		return trimNUL(sql.Mdb.columnValueToString(col))
	}
	if idx < len(sql.BoundValues) {
		val := sql.BoundValues[idx]
		return trimNUL(string(val))
	}
	return ""
}

// IsNull checks if a column value is NULL.
func (sql *SQL) IsNull(idx int) bool {
	if sql.CurTable == nil || idx < 0 || idx >= sql.NumColumns {
		return true
	}
	if col := sql.boundColumn(idx); col != nil {
		return col.CurValueIsNull
	}
	return true
}

// BinaryValue returns the raw bytes for a binary column.
func (sql *SQL) BinaryValue(idx int) []byte {
	if sql.CurTable == nil || idx < 0 || idx >= sql.NumColumns {
		return nil
	}
	if col := sql.boundColumn(idx); col != nil {
		return sql.Mdb.BinaryValue(col)
	}
	return nil
}

// DateTimeValue returns the time.Time for a DateTime column.
func (sql *SQL) DateTimeValue(idx int) (time.Time, bool) {
	if sql.CurTable == nil || idx < 0 || idx >= sql.NumColumns {
		return time.Time{}, false
	}
	if col := sql.boundColumn(idx); col != nil {
		return sql.Mdb.DateTimeValue(col)
	}
	return time.Time{}, false
}

// BoolValue returns a native Boolean result value.
func (sql *SQL) BoolValue(idx int) (bool, bool) {
	if sql.Mdb == nil {
		return false, false
	}
	return sql.Mdb.BoolValue(sql.boundColumn(idx))
}

// Int64Value returns a native integral result value.
func (sql *SQL) Int64Value(idx int) (int64, bool) {
	if sql.Mdb == nil {
		return 0, false
	}
	return sql.Mdb.Int64Value(sql.boundColumn(idx))
}

// Float64Value returns a native floating-point result value.
func (sql *SQL) Float64Value(idx int) (float64, bool) {
	if sql.Mdb == nil {
		return 0, false
	}
	return sql.Mdb.Float64Value(sql.boundColumn(idx))
}

func trimNUL(s string) string {
	if i := strings.IndexByte(s, 0); i >= 0 {
		return s[:i]
	}
	return s
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
