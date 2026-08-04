package gomdb

import (
	"bytes"
	"fmt"
	"sort"
	"unicode/utf8"
)

// OrderTerm is one ORDER BY key parsed from the query.
type OrderTerm struct {
	Col   string
	IsLen bool
	Desc  bool
}

// sort key kinds.
const (
	skNull uint8 = iota
	skInt
	skFloat
	skText
)

// sortKey is a precomputed, typed ORDER BY key. Text keys reference the
// materialization key arena, so no per-row string allocations occur.
type sortKey struct {
	kind uint8
	s    []byte
	i    int64
	f    float64
}

// sortedRow is one materialized row: the copied raw row bytes plus its
// sort keys. keys is a slice into the flat key array shared by all rows.
type sortedRow struct {
	row  []byte
	keys []sortKey
}

type orderCol struct {
	col   *MdbColumn
	isLen bool
	desc  bool
}

func isIntFamily(t int) bool {
	switch t {
	case TypeBool, TypeByte, TypeInt, TypeLongInt, TypeComplex:
		return true
	}
	return false
}

func isFloatFamily(t int) bool {
	switch t {
	case TypeFloat, TypeDouble, TypeMoney:
		return true
	}
	return false
}

func isTextFamily(t int) bool {
	switch t {
	case TypeText, TypeMemo, TypeRepID:
		return true
	}
	return false
}

func isSortableType(t int) bool {
	return isIntFamily(t) || isFloatFamily(t) || isTextFamily(t) || t == TypeDateTime
}

// materializeOrderBy scans the source table once (WHERE already applied by
// FetchRow), copies each matching row's raw bytes and typed sort keys, and
// stably sorts the snapshot. Row bytes and text keys live in growing arenas,
// so the scan performs no per-row heap allocations beyond amortized growth.
func (mdb *MdbHandle) materializeOrderBy(sql *SQL, table *MdbTableDef) error {
	cols := make([]orderCol, len(sql.OrderBy))
	for i, term := range sql.OrderBy {
		var col *MdbColumn
		for _, c := range table.Columns {
			if equalFold(c.Name, term.Col) {
				col = c
				break
			}
		}
		if col == nil {
			return fmt.Errorf("gomdb: ORDER BY column %q not found", term.Col)
		}
		if term.IsLen && !isTextFamily(col.ColType) {
			return fmt.Errorf("gomdb: Len() is only supported on text columns, got %q", term.Col)
		}
		if !isSortableType(col.ColType) {
			return fmt.Errorf("gomdb: ORDER BY on column type %s not supported", ColTypeName(col.ColType))
		}
		cols[i] = orderCol{col: col, isLen: term.IsLen, desc: term.Desc}
	}

	mdb.RewindTable(table)

	var rowArena []byte
	var keyArena []byte
	var allKeys []sortKey
	var rows []sortedRow
	nKeys := len(cols)

	for {
		ok, err := mdb.FetchRow(table)
		if err != nil {
			return err
		}
		if !ok {
			break
		}

		// Copy the raw row segment into the arena. Slices created from an
		// earlier arena generation stay valid while the arena grows, so no
		// per-row allocation is needed.
		start, length, err := mdb.findRow(table.CurRow - 1)
		if err != nil {
			return err
		}
		segStart := start & OffsetMask
		rowOff := len(rowArena)
		rowArena = append(rowArena, mdb.pgBuf[segStart:segStart+length]...)

		keyOff := len(allKeys)
		for i := 0; i < nKeys; i++ {
			allKeys = append(allKeys, sortKey{})
		}
		for i := range cols {
			allKeys[keyOff+i] = mdb.buildSortKey(cols[i], &keyArena)
		}

		rows = append(rows, sortedRow{
			row:  rowArena[rowOff:],
			keys: allKeys[keyOff : keyOff+nKeys],
		})
	}

	if rows == nil {
		rows = make([]sortedRow, 0)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		ki := rows[i].keys
		kj := rows[j].keys
		for t := range cols {
			c := compareSortKeys(ki[t], kj[t])
			if c == 0 {
				continue
			}
			if cols[t].desc {
				return c > 0
			}
			return c < 0
		}
		return false
	})

	sql.SortedRows = rows
	return nil
}

// buildSortKey decodes one row's key for an ORDER BY term without allocating:
// text uses the handle's reusable unicode buffer (then copies into the key
// arena), numbers use the existing typed getters.
func (mdb *MdbHandle) buildSortKey(oc orderCol, keyArena *[]byte) sortKey {
	col := oc.col
	if col.CurValueIsNull {
		return sortKey{kind: skNull}
	}

	switch {
	case isIntFamily(col.ColType):
		if v, ok := mdb.Int64Value(col); ok {
			return sortKey{kind: skInt, i: v}
		}
	case isFloatFamily(col.ColType):
		if v, ok := mdb.Float64Value(col); ok {
			return sortKey{kind: skFloat, f: v}
		}
	case col.ColType == TypeDateTime:
		return sortKey{kind: skFloat, f: GetDouble(mdb.pgBuf, col.CurValueStart)}
	case isTextFamily(col.ColType):
		b := mdb.sortTextBytes(col)
		if oc.isLen {
			return sortKey{kind: skInt, i: int64(utf8.RuneCount(b))}
		}
		off := len(*keyArena)
		*keyArena = append(*keyArena, b...)
		return sortKey{kind: skText, s: (*keyArena)[off:]}
	}
	return sortKey{kind: skNull}
}

// sortTextBytes decodes a text-family column value into a byte slice without
// allocating for the common TypeText case.
func (mdb *MdbHandle) sortTextBytes(col *MdbColumn) []byte {
	field := MdbField{
		Start:  col.CurValueStart,
		Siz:    col.CurValueLen,
		IsNull: col.CurValueIsNull,
	}
	if col.ColType == TypeText {
		src := mdb.pgBuf[field.Start : field.Start+field.Siz]
		if body, ok := unicodeFastPath(src, mdb.IsJet4()); ok {
			return body
		}
		buf := appendUnicodeUTF8(mdb.unicodeBuf[:0], src, mdb.IsJet4())
		mdb.unicodeBuf = buf
		return buf
	}
	// Memo/Replication ID: decoded through the normal path (rare).
	return []byte(mdb.colToString(col, &field))
}

// compareSortKeys compares two keys, placing NULLs first in ascending order.
func compareSortKeys(a, b sortKey) int {
	if a.kind == skNull || b.kind == skNull {
		switch {
		case a.kind == skNull && b.kind == skNull:
			return 0
		case a.kind == skNull:
			return -1
		default:
			return 1
		}
	}
	switch a.kind {
	case skInt:
		switch {
		case a.i < b.i:
			return -1
		case a.i > b.i:
			return 1
		}
	case skFloat:
		switch {
		case a.f < b.f:
			return -1
		case a.f > b.f:
			return 1
		}
	case skText:
		return bytes.Compare(a.s, b.s)
	}
	return 0
}

// serveSortedRow re-cracks a materialized row from its private buffer and
// binds column values exactly like the synchronous ReadRow path.
func (mdb *MdbHandle) serveSortedRow(table *MdbTableDef, sr sortedRow) (bool, error) {
	mdb.pgBuf = sr.row
	mdb.curPg = 0
	fields, err := mdb.CrackRow(table, 0, len(sr.row))
	if err != nil {
		return false, err
	}
	for i := 0; i < len(fields); i++ {
		field := &fields[i]
		if field.ColNum >= len(table.Columns) {
			continue
		}
		col := table.Columns[field.ColNum]
		mdb.attemptBind(col, field)
	}
	return true, nil
}
