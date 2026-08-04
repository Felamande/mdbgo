package gomdb

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"time"
)

// Date/time calendar tables (days in each month, non-leap and leap)
var (
	noleapCal = []int{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334, 365}
	leapCal   = []int{0, 31, 60, 91, 121, 152, 182, 213, 244, 274, 305, 335, 366}
)

// FreeTableDef frees a table definition and associated resources.
func (mdb *MdbHandle) FreeTableDef(table *MdbTableDef) {
	if table == nil {
		return
	}
	if table.IsTempTable {
		for _, page := range table.TempTablePages {
			// nothing to free for byte slice pages
			_ = page
		}
		table.TempTablePages = nil
	}
	table.Columns = nil
	table.Indices = nil
	table.UsageMap = nil
	table.FreeUsageMap = nil
}

// RewindTable resets the iteration state for a table.
func (mdb *MdbHandle) RewindTable(table *MdbTableDef) {
	table.CurPgNum = 0
	table.CurPhysPg = 0
	table.CurRow = 0
}

// FetchRow reads the next matching row from a table.
// Returns true if a row was read, false if no more rows.
func (mdb *MdbHandle) FetchRow(table *MdbTableDef) (bool, error) {
	mfmt := mdb.fmt

	// Initialize
	if table.CurPgNum == 0 {
		table.CurPgNum = 1
		table.CurRow = 0
		if !table.IsTempTable && table.Strategy != IndexScan {
			if err := mdb.ReadNextDpg(table); err != nil {
				if err == errNoMorePages {
					return false, nil // empty table — no data pages
				}
				return false, err
			}
		}
	}

	for {
		if table.IsTempTable {
			pages := table.TempTablePages
			if len(pages) == 0 {
				return false, nil
			}
			rows := GetInt16(pages[table.CurPgNum-1], mfmt.RowCountOffset)
			if table.CurRow >= rows {
				table.CurRow = 0
				table.CurPgNum++
				if int(table.CurPgNum) > len(pages) {
					return false, nil
				}
			}
			// Point directly at the temp page: rows live in per-page slices,
			// so no copy is needed and CrackRow can view the page in place.
			mdb.pgBuf = pages[table.CurPgNum-1]
			mdb.curPg = 0 // invalidate: pgBuf no longer contains a real disk page
		} else {
			rows := GetInt16(mdb.pgBuf[:], mfmt.RowCountOffset)

			if table.CurRow >= rows {
				table.CurRow = 0
				if err := mdb.ReadNextDpg(table); err != nil {
					return false, nil
				}
			}
		}

		rc, err := mdb.ReadRow(table, table.CurRow)
		table.CurRow++
		if err != nil {
			return false, err
		}
		if rc {
			return true, nil
		}
	}
}

// ReadRow reads a single row and binds its column values.
func (mdb *MdbHandle) ReadRow(table *MdbTableDef, row int) (bool, error) {
	if table.NumCols == 0 || len(table.Columns) == 0 {
		return false, nil
	}

	rowStart, rowSize, err := mdb.findRow(row)
	if err != nil || rowSize == 0 {
		return false, nil
	}

	// Check delete flag
	delflag := false
	if rowStart&0x4000 != 0 {
		delflag = true
	}
	rowStart &= OffsetMask

	if table.NoSkipDel == 0 && delflag {
		return false, nil
	}

	fields, err := mdb.CrackRow(table, rowStart, rowSize)
	if err != nil {
		return false, err
	}

	// Test sargs
	if table.SargTree != nil {
		if !TestSargs(mdb, table, fields) {
			return false, nil
		}
	}

	// Bind column values
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

// attemptBind binds a field value to a column.
func (mdb *MdbHandle) attemptBind(col *MdbColumn, field *MdbField) {
	col.CurValueIsNull = field.IsNull
	col.CurValueText = ""
	col.CurValueTextValid = false
	if col.ColType == TypeBool {
		col.CurValueIsNull = false
	}

	if col.ColType == TypeBool {
		val := 0
		if !field.IsNull {
			val = 1
		}
		col.CurValueLen = val
		if col.BindPtr != nil {
			if val != 0 {
				copy(col.BindPtr[:], "1")
			} else {
				copy(col.BindPtr[:], "0")
			}
			col.BindLen = 1
		}
	} else if field.IsNull {
		col.CurValueStart = 0
		col.CurValueLen = 0
		if col.BindPtr != nil {
			col.BindPtr[0] = 0
		}
		col.BindLen = 0
	} else if col.ColType == TypeOLE {
		col.CurValueStart = field.Start
		col.CurValueLen = field.Siz
		if col.BindPtr != nil && field.Siz >= MemoOverhead {
			copy(col.BindPtr[:MemoOverhead], mdb.pgBuf[field.Start:field.Start+MemoOverhead])
			col.BindLen = MemoOverhead
		}
	} else {
		col.CurValueStart = field.Start
		col.CurValueLen = field.Siz
		if col.BindPtr != nil {
			str := mdb.colToString(col, field)
			if len(str) >= len(col.BindPtr) {
				col.BindPtr = append(col.BindPtr, make([]byte, len(str)-len(col.BindPtr)+1)...)
			}
			copy(col.BindPtr[:], str)
			if len(str) < len(col.BindPtr) {
				col.BindPtr[len(str)] = 0
			}
			col.BindLen = len(str)
		}
	}
}

// columnValueToString lazily produces the compatibility string for a bound
// column. Native consumers can therefore avoid formatting values they do not
// request.
func (mdb *MdbHandle) columnValueToString(col *MdbColumn) string {
	if col == nil {
		return ""
	}
	// Boolean columns are represented as non-null 0/1 strings by the legacy
	// binder even when the row's physical field has zero payload bytes.
	if col.ColType == TypeBool {
		if col.CurValueLen != 0 {
			col.CurValueText = "1"
		} else {
			col.CurValueText = "0"
		}
		col.CurValueTextValid = true
		return col.CurValueText
	}
	if col.CurValueIsNull {
		return ""
	}
	if col.CurValueTextValid {
		return col.CurValueText
	}
	field := MdbField{
		Start:  col.CurValueStart,
		Siz:    col.CurValueLen,
		IsNull: col.CurValueIsNull,
	}
	col.CurValueText = mdb.colToString(col, &field)
	col.CurValueTextValid = true
	return col.CurValueText
}

// colToString converts a column value to its string representation.
func (mdb *MdbHandle) colToString(col *MdbColumn, field *MdbField) string {
	return colToStringIn(mdb, col, field, mdb.pgBuf, nil)
}

// colToStringIn converts a column value to its string representation using
// an explicit row page. With scratch == nil it uses the handle's shared
// buffers (synchronous path, including encrypted/streamed memo pages); with
// scratch != nil it uses caller-owned buffers and pure resident-file memo
// access, which is safe for fast-scan workers.
func colToStringIn(mdb *MdbHandle, col *MdbColumn, field *MdbField, page []byte, s *decodeScratch) string {
	if field.Siz == 0 || field.IsNull {
		return ""
	}

	switch col.ColType {
	case TypeBool:
		if !field.IsNull {
			return "1"
		}
		return "0"

	case TypeByte:
		return strconv.FormatInt(int64(page[field.Start]), 10)

	case TypeInt:
		return strconv.FormatInt(int64(GetInt16(page, field.Start)), 10)

	case TypeLongInt, TypeComplex:
		return strconv.FormatInt(int64(GetInt32(page, field.Start)), 10)

	case TypeFloat:
		f := GetSingle(page, field.Start)
		return strconv.FormatFloat(float64(f), 'g', 8, 32)

	case TypeDouble:
		d := GetDouble(page, field.Start)
		return strconv.FormatFloat(d, 'g', 16, 64)

	case TypeText:
		if s != nil {
			return unicodeBorrow(page[field.Start:field.Start+field.Siz], mdb.IsJet4(), s)
		}
		return mdb.unicodeToUTF8(page[field.Start : field.Start+field.Siz])

	case TypeDateTime:
		return dateTimeToStringIn(page, field.Start)

	case TypeMemo:
		if s != nil {
			return memoToStringIn(mdb, page, field.Start, field.Siz, s)
		}
		return mdb.memoToString(field.Start, field.Siz)

	case TypeMoney:
		return MoneyToString(page, field.Start)

	case TypeRepID:
		return uuidToString(page, field.Start)

	case TypeNumeric:
		return NumericToString(page, field.Start, col.ColScale, col.ColPrec)

	case TypeBinary:
		if field.Siz < 0 {
			return ""
		}
		return string(page[field.Start : field.Start+field.Siz])

	case TypeOLE:
		// The historical binder exposed the twelve-byte OLE header through
		// Value(); retain that representation for lazy callers.
		if field.Siz < MemoOverhead {
			return ""
		}
		return string(page[field.Start : field.Start+MemoOverhead])

	default:
		return ""
	}
}

func dateTimeToStringIn(page []byte, start int) string {
	td := GetDouble(page, start)
	t := DateToTime(td)
	return t.Format("2006-01-02 15:04:05")
}

// memoPageView returns the row bytes for a pg_row reference directly from the
// resident file data. Unlike findPgRow it does not touch the handle's shared
// alternate-page buffer, so fast-scan workers can decode memos in parallel.
func (mdb *MdbHandle) memoPageView(pgRow int) (page []byte, start, length int, ok bool) {
	pg := uint32(pgRow >> 8)
	row := pgRow & 0xff
	if row > 1000 {
		return nil, 0, 0, false
	}
	pgSize := mdb.fmt.PgSize
	offset := int64(pg) * int64(pgSize)
	if mdb.f.data == nil || mdb.f.dbKey != 0 || offset < 0 || offset+int64(pgSize) > mdb.f.size {
		return nil, 0, 0, false
	}
	page = mdb.f.data[offset : offset+int64(pgSize)]
	rco := mdb.fmt.RowCountOffset
	start = GetInt16(page, rco+2+row*2)
	next := pgSize
	if row > 0 {
		next = GetInt16(page, rco+row*2) & OffsetMask
	}
	start &= OffsetMask
	if start >= pgSize || start > next || next > pgSize {
		return nil, 0, 0, false
	}
	return page, start, next - start, true
}

// memoToStringIn decodes a memo field whose row lives in page. Content pages
// are read directly from the resident file data (the fast-scan path), and the
// UTF-8 output is produced with caller-owned scratch.
func memoToStringIn(mdb *MdbHandle, page []byte, start, size int, s *decodeScratch) string {
	if size < MemoOverhead {
		return ""
	}

	memoLen := GetInt32(page, start)

	if memoLen&0x80000000 != 0 {
		// Inline memo
		return unicodeBorrow(page[start+MemoOverhead:start+size], mdb.IsJet4(), s)
	} else if memoLen&0x40000000 != 0 {
		// Single-page memo
		pgRow := GetInt32(page, start+4)
		buf, rowStart, length, ok := mdb.memoPageView(pgRow)
		if !ok {
			return ""
		}
		return unicodeBorrow(buf[rowStart:rowStart+length], mdb.IsJet4(), s)
	} else if (memoLen & 0xFF000000) == 0 {
		// Multi-page memo: decode the page chain as one continuing stream
		// directly into the UTF-8 scratch, skipping a raw concatenation pass.
		out := s.unicode[:0]
		st := unicodeChunkState{}
		first := true
		pgRow := GetInt32(page, start+4)
		for {
			buf, rowStart, length, ok := mdb.memoPageView(pgRow)
			if !ok || length < 4 {
				break
			}
			out = appendUnicodeChunk(out, buf[rowStart+4:rowStart+length], first, mdb.IsJet4(), &st)
			first = false
			pgRow = GetInt32(buf, rowStart)
			if (pgRow >> 8) == 0 {
				break
			}
		}
		s.unicode = out
		return string(out)
	}

	return ""
}

// dateTimeToString converts a DateTime column value to string.
func (mdb *MdbHandle) dateTimeToString(col *MdbColumn) string {
	td := GetDouble(mdb.pgBuf[:], col.CurValueStart)
	t := DateToTime(td)
	return t.Format("2006-01-02 15:04:05")
}

// DateTimeValue returns the time.Time for a DateTime column.
func (mdb *MdbHandle) DateTimeValue(col *MdbColumn) (time.Time, bool) {
	if col == nil || col.CurValueIsNull || col.ColType != TypeDateTime {
		return time.Time{}, false
	}
	td := GetDouble(mdb.pgBuf[:], col.CurValueStart)
	return DateToTime(td), true
}

// BoolValue returns a native boolean for a Boolean column.
func (mdb *MdbHandle) BoolValue(col *MdbColumn) (bool, bool) {
	if col == nil || col.CurValueIsNull || col.ColType != TypeBool {
		return false, false
	}
	return col.CurValueLen != 0, true
}

// Int64Value returns a native integer for the integral Access column types.
func (mdb *MdbHandle) Int64Value(col *MdbColumn) (int64, bool) {
	if col == nil || col.CurValueIsNull {
		return 0, false
	}
	buf := mdb.pgBuf[:]
	start := col.CurValueStart
	switch col.ColType {
	case TypeBool:
		return int64(col.CurValueLen), true
	case TypeByte:
		if col.CurValueLen < 1 {
			return 0, false
		}
		return int64(buf[start]), true
	case TypeInt:
		if col.CurValueLen < 2 {
			return 0, false
		}
		return int64(GetInt16(buf, start)), true
	case TypeLongInt, TypeComplex:
		if col.CurValueLen < 4 {
			return 0, false
		}
		return int64(GetInt32(buf, start)), true
	default:
		return 0, false
	}
}

// Float64Value returns a native floating-point value for floating and Currency
// columns. Currency is stored as a signed 64-bit integer scaled by 10,000.
func (mdb *MdbHandle) Float64Value(col *MdbColumn) (float64, bool) {
	if col == nil || col.CurValueIsNull {
		return 0, false
	}
	start := col.CurValueStart
	switch col.ColType {
	case TypeFloat:
		if col.CurValueLen < 4 {
			return 0, false
		}
		return compatibilityFloat64(float64(GetSingle(mdb.pgBuf[:], start)), 8, 32), true
	case TypeDouble:
		if col.CurValueLen < 8 {
			return 0, false
		}
		return compatibilityFloat64(GetDouble(mdb.pgBuf[:], start), 16, 64), true
	case TypeMoney:
		if col.CurValueLen < 8 {
			return 0, false
		}
		return float64(int64(binary.LittleEndian.Uint64(mdb.pgBuf[start:]))) / 10000, true
	default:
		return 0, false
	}
}

// compatibilityFloat64 reproduces the value historically exposed by the SQL
// drivers after mdbtools' %.8g/%.16g formatting and ParseFloat conversion.
// AppendFloat uses stack scratch, avoiding the old per-cell string pipeline.
func compatibilityFloat64(value float64, precision, bitSize int) float64 {
	var scratch [32]byte
	formatted := strconv.AppendFloat(scratch[:0], value, 'g', precision, bitSize)
	parsed, err := strconv.ParseFloat(string(formatted), 64)
	if err != nil {
		return value
	}
	return parsed
}

// DateToTime converts an Access date double to a time.Time.
// The integer part is days since 12/30/1899.
// The fractional part is the fraction of a day.
func DateToTime(td float64) time.Time {
	if td < 0.0 || td > 1e6 {
		return time.Time{}
	}

	day := int64(td)
	dayFrac := td - float64(day)
	secs := int64(dayFrac*86400.0 + 0.5)

	hour := secs / 3600
	min := (secs / 60) % 60
	sec := secs % 60

	// Days from 1/1/1 to 12/31/1899
	day += 693593

	// Convert days since 1/1/1 to year/month/day
	yr := int64(1)
	q := day / 146097 // 400 years
	yr += 400 * q
	day -= q * 146097

	q = day / 36524 // 100 years
	if q > 3 {
		q = 3
	}
	yr += 100 * q
	day -= q * 36524

	q = day / 1461 // 4 years
	yr += 4 * q
	day -= q * 1461

	q = day / 365 // 1 year
	if q > 3 {
		q = 3
	}
	yr += q
	day -= q * 365

	cal := noleapCal
	if yr%4 == 0 && (yr%100 != 0 || yr%400 == 0) {
		cal = leapCal
	}

	mon := 0
	for mon < 12 {
		if day < int64(cal[mon+1]) {
			break
		}
		mon++
	}

	mday := int(day - int64(cal[mon]) + 1)

	return time.Date(int(yr), time.Month(mon+1), mday, int(hour), int(min), int(sec), 0, time.Local)
}

// memoToString converts a memo field to a string.
func (mdb *MdbHandle) memoToString(start, size int) string {
	if size < MemoOverhead {
		return ""
	}

	memoLen := GetInt32(mdb.pgBuf[:], start)

	if memoLen&0x80000000 != 0 {
		// Inline memo
		return mdb.unicodeToUTF8(mdb.pgBuf[start+MemoOverhead : start+size])
	} else if memoLen&0x40000000 != 0 {
		// Single-page memo
		pgRow := GetInt32(mdb.pgBuf[:], start+4)
		buf, rowStart, length, err := mdb.findPgRow(pgRow)
		if err != nil {
			return ""
		}
		return mdb.unicodeToUTF8(buf[rowStart : rowStart+length])
	} else if (memoLen & 0xFF000000) == 0 {
		// Multi-page memo
		result := mdb.memoBuf[:0]
		pgRow := GetInt32(mdb.pgBuf[:], start+4)
		for {
			buf, rowStart, length, err := mdb.findPgRow(pgRow)
			if err != nil || length < 4 {
				break
			}
			result = append(result, buf[rowStart+4:rowStart+length]...)
			pgRow = GetInt32(buf, rowStart)
			if (pgRow >> 8) == 0 {
				break
			}
		}
		mdb.memoBuf = result
		return mdb.unicodeToUTF8(result)
	}

	return ""
}

// uuidToString formats a Replication ID (GUID) field as a string.
func uuidToString(buf []byte, start int) string {
	return fmt.Sprintf(
		"{%02X%02X%02X%02X-%02X%02X-%02X%02X-%02X%02X-%02X%02X%02X%02X%02X%02X}",
		buf[start+3], buf[start+2], buf[start+1], buf[start],
		buf[start+5], buf[start+4],
		buf[start+7], buf[start+6],
		buf[start+8], buf[start+9],
		buf[start+10], buf[start+11],
		buf[start+12], buf[start+13],
		buf[start+14], buf[start+15],
	)
}

// OleReadFull reads the complete OLE field data.
func (mdb *MdbHandle) OleReadFull(col *MdbColumn, bindBuf []byte) ([]byte, int, error) {
	if col.CurValueIsNull || col.CurValueLen < MemoOverhead {
		return nil, 0, nil
	}

	start := col.CurValueStart
	size := col.CurValueLen

	oleLen := GetInt32(mdb.pgBuf[:], start)

	if oleLen&0x80000000 != 0 {
		// Inline
		length := size - MemoOverhead
		data := make([]byte, length)
		copy(data, mdb.pgBuf[start+MemoOverhead:start+size])
		return data, length, nil
	} else if oleLen&0x40000000 != 0 {
		// Single page
		pgRow := GetInt32(mdb.pgBuf[:], start+4)
		buf, rowStart, length, _ := mdb.findPgRow(pgRow)
		data := make([]byte, length)
		copy(data, buf[rowStart:rowStart+length])
		return data, length, nil
	} else if (oleLen & 0xFF000000) == 0 {
		// Multi-page
		var result []byte
		pgRow := GetInt32(mdb.pgBuf[:], start+4)
		for {
			buf, rowStart, length, err := mdb.findPgRow(pgRow)
			if err != nil || length < 4 {
				break
			}
			result = append(result, buf[rowStart+4:rowStart+length]...)
			pgRow = GetInt32(buf, rowStart)
			if (pgRow >> 8) == 0 {
				break
			}
		}
		return result, len(result), nil
	}

	return nil, 0, nil
}

// BinaryValue returns the raw bytes for a binary column.
func (mdb *MdbHandle) BinaryValue(col *MdbColumn) []byte {
	if col.CurValueIsNull || col.ColType != TypeBinary || col.CurValueLen <= 0 {
		return nil
	}
	data := make([]byte, col.CurValueLen)
	copy(data, mdb.pgBuf[col.CurValueStart:col.CurValueStart+col.CurValueLen])
	return data
}

// TmToDate converts a time.Time to an Access date double.
func TmToDate(t time.Time) float64 {
	yr := t.Year()
	mon := int(t.Month()) - 1
	leap := (yr%4 == 0) && (yr%100 != 0 || yr%400 == 0)

	cal := noleapCal
	if leap {
		cal = leapCal
	}

	days := int64(yr*365+(yr/4)-(yr/100)+(yr/400)+cal[mon]+t.Day()) - 693959

	td := float64(int64(t.Hour())*3600+int64(t.Minute())*60+int64(t.Second())) / 86400.0
	if days >= 0 {
		td += float64(days)
	} else {
		td = float64(days) - td
	}
	return td
}

// FormatFloat helper for sargs — poor man's truncation
func poorMansTrunc(x float64) float64 {
	return math.Trunc(x*1e6) / 1e6
}
