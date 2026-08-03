package gomdb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

func referenceUnicodeToUTF8(src []byte, isJet4 bool) string {
	if len(src) == 0 {
		return ""
	}
	if !isJet4 {
		runes := make([]rune, len(src))
		for i, b := range src {
			runes[i] = rune(b)
		}
		return string(runes)
	}

	decoded := src
	if len(src) >= 2 && src[0] == 0xff && src[1] == 0xfe {
		decoded = make([]byte, 0, (len(src)-2)*2)
		compressed := true
		for i := 2; i < len(src); {
			if src[i] == 0 {
				compressed = !compressed
				i++
			} else if compressed {
				decoded = append(decoded, src[i], 0)
				i++
			} else if i+1 < len(src) {
				decoded = append(decoded, src[i], src[i+1])
				i += 2
			} else {
				break
			}
		}
	}

	units := make([]uint16, len(decoded)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(decoded[i*2:])
	}
	return string(utf16.Decode(units))
}

func TestUnicodeToUTF8MatchesReference(t *testing.T) {
	tests := []struct {
		name string
		src  []byte
		jet4 bool
	}{
		{"empty", nil, true},
		{"jet3 latin1 and NUL", []byte{0, 'A', 0x80, 0xff}, false},
		{"jet4 basic", []byte{'A', 0, 0x2d, 0x4e}, true},
		{"jet4 surrogate pair", []byte{0x3d, 0xd8, 0x03, 0xde}, true},
		{"jet4 lone high", []byte{0x3d, 0xd8}, true},
		{"jet4 lone low", []byte{0x03, 0xde}, true},
		{"jet4 high high low", []byte{0x3d, 0xd8, 0x3d, 0xd8, 0x03, 0xde}, true},
		{"jet4 odd tail", []byte{'A', 0, 'B'}, true},
		{"compressed ASCII", append([]byte{0xff, 0xfe}, []byte("hello")...), true},
		{"compressed toggles", []byte{0xff, 0xfe, 'A', 0, 0x2d, 0x4e, 0, 'B'}, true},
		{"compressed surrogate", []byte{0xff, 0xfe, 0, 0x3d, 0xd8, 0x03, 0xde, 0}, true},
		{"compressed embedded NUL controls", []byte{0xff, 0xfe, 'A', 0, 0, 'B'}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := referenceUnicodeToUTF8(tt.src, tt.jet4)
			if got := UnicodeToUTF8(tt.src, tt.jet4); got != want {
				t.Fatalf("UnicodeToUTF8(% x) = %q, want %q", tt.src, got, want)
			}
		})
	}

	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 1000; i++ {
		src := make([]byte, 2+rng.Intn(64))
		src[0], src[1] = 0xff, 0xfe
		_, _ = rng.Read(src[2:])
		want := referenceUnicodeToUTF8(src, true)
		if got := UnicodeToUTF8(src, true); got != want {
			t.Fatalf("random compressed input % x = %q, want %q", src, got, want)
		}
	}
}

func TestASCIItoUCS2PreservesLegacyUTF8Bytes(t *testing.T) {
	const name = "编号"
	got := ASCIItoUCS2(name)
	want := []byte{
		0xe7, 0, 0xbc, 0, 0x96, 0,
		0xe5, 0, 0x8f, 0, 0xb7, 0,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ASCIItoUCS2(%q) = % x, want % x", name, got, want)
	}
	if decoded := UnicodeToUTF8(got, true); decoded != "\u00e7\u00bc\u0096\u00e5\u008f\u00b7" {
		t.Fatalf("legacy DESCRIBE value = %q", decoded)
	}
}

func TestDescribeTablePreservesLegacyNonASCIIColumnName(t *testing.T) {
	const legacyName = "ç¼å·"
	for _, tableName := range []string{
		"ReTableNameList",
		"TableRelationShip",
		"TableToTableRel",
	} {
		t.Run(tableName, func(t *testing.T) {
			q, err := OpenQuery("../../testdata/lm.mdb", "DESCRIBE TABLE ["+tableName+"]")
			if err != nil {
				t.Fatal(err)
			}
			defer q.Close()

			found := false
			for {
				ok, err := q.Next()
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					break
				}
				if q.Value(0) == legacyName {
					found = true
					if typ, size := q.Value(1), q.Value(2); typ != "Long Integer" || size != "4" {
						t.Fatalf("column metadata = (%q, %q), want (%q, %q)",
							typ, size, "Long Integer", "4")
					}
				}
			}
			if !found {
				t.Fatalf("DESCRIBE TABLE did not return legacy column name %q", legacyName)
			}
		})
	}
}

func recursiveLikeReference(s, pattern string) bool {
	if len(pattern) == 0 {
		return len(s) == 0
	}
	switch pattern[0] {
	case '_':
		return len(s) > 0 && recursiveLikeReference(s[1:], pattern[1:])
	case '%':
		for i := 0; i <= len(s); i++ {
			if recursiveLikeReference(s[i:], pattern[1:]) {
				return true
			}
		}
		return false
	default:
		i := 0
		for i < len(pattern) && pattern[i] != '_' && pattern[i] != '%' {
			i++
		}
		return len(s) >= i && s[:i] == pattern[:i] &&
			recursiveLikeReference(s[i:], pattern[i:])
	}
}

func generateStrings(alphabet string, maxLen int) []string {
	result := []string{""}
	for length := 1; length <= maxLen; length++ {
		var build func(string)
		build = func(prefix string) {
			if len(prefix) == length {
				result = append(result, prefix)
				return
			}
			for i := 0; i < len(alphabet); i++ {
				build(prefix + alphabet[i:i+1])
			}
		}
		build("")
	}
	return result
}

func TestLikeCmpMatchesRecursiveBehavior(t *testing.T) {
	values := generateStrings("ab", 4)
	patterns := generateStrings("ab%_", 5)
	for _, value := range values {
		for _, pattern := range patterns {
			want := recursiveLikeReference(value, pattern)
			if got := LikeCmp(value, pattern); got != want {
				t.Fatalf("LikeCmp(%q, %q) = %v, want %v", value, pattern, got, want)
			}
		}
	}

	if LikeCmp("é", "_") || !LikeCmp("é", "__") {
		t.Fatal("LIKE underscore must preserve the existing byte-oriented behavior")
	}
	if !ILikeCmp("Ärger", "är%") {
		t.Fatal("ILIKE no longer performs Unicode lowercasing")
	}
	if LikeCmp(strings.Repeat("a", 4096)+"b", strings.Repeat("%a", 256)+"c") {
		t.Fatal("adversarial non-match unexpectedly matched")
	}
}

type countingReaderAt struct {
	data  []byte
	reads int
}

func (r *countingReaderAt) ReadAt(dst []byte, off int64) (int, error) {
	r.reads++
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(dst, r.data[off:])
	if n != len(dst) {
		return n, io.EOF
	}
	return n, nil
}

func testHandle(reader *countingReaderAt, dbKey uint32) *MdbHandle {
	mdb := &MdbHandle{
		f:   &MdbFile{stream: reader, size: int64(len(reader.data)), dbKey: dbKey, jetVersion: Jet4},
		fmt: &Jet4FormatConstants,
	}
	mdb.pgBuf = mdb.pgArr[:]
	mdb.altPgBuf = mdb.altPgArr[:]
	return mdb
}

func TestReadAltPageDecryptsCachesAndKeepsMainPage(t *testing.T) {
	plain := make([]byte, Jet4FormatConstants.PgSize)
	plain[0] = PageData
	binary.LittleEndian.PutUint16(plain[Jet4FormatConstants.RowCountOffset:], 1)
	binary.LittleEndian.PutUint16(plain[Jet4FormatConstants.RowCountOffset+2:], 4000)
	copy(plain[4000:], "row payload")

	const dbKey = uint32(0x12345678)
	encrypted := append([]byte(nil), plain...)
	key := dbKey ^ 1
	rc4Key := []byte{byte(key), byte(key >> 8), byte(key >> 16), byte(key >> 24)}
	rc4Decrypt(rc4Key, encrypted)

	fileData := make([]byte, Jet4FormatConstants.PgSize*2)
	copy(fileData[Jet4FormatConstants.PgSize:], encrypted)
	reader := &countingReaderAt{data: fileData}
	mdb := testHandle(reader, dbKey)
	mdb.pgBuf[0] = 0x7f

	if err := mdb.readAltPage(1); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mdb.altPgBuf[:Jet4FormatConstants.PgSize], plain) {
		t.Fatal("alternate page was not decrypted identically to the main-page path")
	}
	if mdb.pgBuf[0] != 0x7f {
		t.Fatal("alternate-page read modified the active page")
	}
	if err := mdb.readAltPage(1); err != nil {
		t.Fatal(err)
	}
	if reader.reads != 1 {
		t.Fatalf("alternate-page cache performed %d reads, want 1", reader.reads)
	}

	buf, start, size, err := mdb.findPgRow(1 << 8)
	if err != nil {
		t.Fatal(err)
	}
	if start != 4000 || size != len(plain)-4000 || string(buf[start:start+11]) != "row payload" {
		t.Fatalf("findPgRow = start %d size %d payload %q", start, size, buf[start:start+11])
	}
}

func setDataPage(data []byte, page, owner int) {
	off := page * Jet4FormatConstants.PgSize
	data[off] = PageData
	binary.LittleEndian.PutUint32(data[off+4:], uint32(owner))
}

func inlineUsageMap(base int, pages ...int) []byte {
	usage := make([]byte, 6)
	binary.LittleEndian.PutUint32(usage[1:], uint32(base))
	for _, page := range pages {
		bit := page - base
		if bit >= 0 && bit < 8 {
			usage[5] |= 1 << bit
		}
	}
	return usage
}

func TestReadNextDpgStopsAtMapEOFAndFallsBackForBrokenMaps(t *testing.T) {
	const owner = 42
	data := make([]byte, Jet4FormatConstants.PgSize*3)
	setDataPage(data, 1, owner)
	reader := &countingReaderAt{data: data}
	mdb := testHandle(reader, 0)
	table := &MdbTableDef{
		Entry:    &CatalogEntry{TablePg: owner},
		UsageMap: inlineUsageMap(0, 1),
		MapSz:    6,
	}

	if err := mdb.ReadNextDpg(table); err != nil {
		t.Fatal(err)
	}
	if reader.reads != 1 {
		t.Fatalf("first map lookup read %d pages, want 1", reader.reads)
	}
	if err := mdb.ReadNextDpg(table); !errors.Is(err, errNoMorePages) {
		t.Fatalf("second map lookup error = %v, want errNoMorePages", err)
	}
	if reader.reads != 1 {
		t.Fatalf("normal map EOF caused %d total reads, want 1", reader.reads)
	}

	incompleteData := make([]byte, Jet4FormatConstants.PgSize*2)
	setDataPage(incompleteData, 1, owner)
	incompleteReader := &countingReaderAt{data: incompleteData}
	incompleteMDB := testHandle(incompleteReader, 0)
	incompleteTable := &MdbTableDef{
		Entry:    &CatalogEntry{TablePg: owner},
		NumRows:  1,
		UsageMap: inlineUsageMap(0),
		MapSz:    6,
	}
	if err := incompleteMDB.ReadNextDpg(incompleteTable); err != nil {
		t.Fatal(err)
	}
	if incompleteTable.CurPhysPg != 1 || incompleteReader.reads != 1 {
		t.Fatalf("incomplete-map recovery ended at page %d after %d reads", incompleteTable.CurPhysPg, incompleteReader.reads)
	}

	brokenData := make([]byte, Jet4FormatConstants.PgSize*3)
	setDataPage(brokenData, 2, owner)
	brokenReader := &countingReaderAt{data: brokenData}
	brokenMDB := testHandle(brokenReader, 0)
	brokenTable := &MdbTableDef{
		Entry:    &CatalogEntry{TablePg: owner},
		UsageMap: []byte{0xff},
		MapSz:    1,
	}
	if err := brokenMDB.ReadNextDpg(brokenTable); err != nil {
		t.Fatal(err)
	}
	if brokenTable.CurPhysPg != 2 || brokenReader.reads != 2 {
		t.Fatalf("broken-map fallback ended at page %d after %d reads", brokenTable.CurPhysPg, brokenReader.reads)
	}

	mismatchData := make([]byte, Jet4FormatConstants.PgSize*3)
	setDataPage(mismatchData, 1, owner+1)
	setDataPage(mismatchData, 2, owner)
	mismatchReader := &countingReaderAt{data: mismatchData}
	mismatchMDB := testHandle(mismatchReader, 0)
	mismatchTable := &MdbTableDef{
		Entry:    &CatalogEntry{TablePg: owner},
		UsageMap: inlineUsageMap(0, 1),
		MapSz:    6,
	}
	if err := mismatchMDB.ReadNextDpg(mismatchTable); err != nil {
		t.Fatal(err)
	}
	if mismatchTable.CurPhysPg != 2 || mismatchReader.reads != 2 {
		t.Fatalf("mismatched-map fallback ended at page %d after %d reads", mismatchTable.CurPhysPg, mismatchReader.reads)
	}
}

func TestCrackRowReusesVariableOffsetScratch(t *testing.T) {
	mdb := &MdbHandle{
		f:   &MdbFile{jetVersion: Jet4},
		fmt: &Jet4FormatConstants,
	}
	mdb.pgBuf = mdb.pgArr[:]
	table := mdb.CreateTempTable("#scratch")
	col := &MdbColumn{}
	FillTempCol(col, "value", 32, TypeText, false)
	mdb.TempTableAddCol(table, col)

	value := ASCIItoUCS2("scratch")
	field := MdbField{}
	FillTempField(&field, value, len(value), false, false, 0, 0)
	row := make([]byte, Jet4FormatConstants.PgSize)
	rowSize := mdb.PackRow(table, row, 1, []MdbField{field})
	const rowStart = 128
	copy(mdb.pgBuf[rowStart:], row[:rowSize])

	fields, err := mdb.CrackRow(table, rowStart, rowSize)
	if err != nil {
		t.Fatal(err)
	}
	if got := UnicodeToUTF8(fields[0].Value, true); got != "scratch" {
		t.Fatalf("cracked value = %q", got)
	}
	if len(table.varOffsetsBuf) < 2 {
		t.Fatal("variable offset scratch was not retained")
	}
	first := &table.varOffsetsBuf[0]
	if _, err := mdb.CrackRow(table, rowStart, rowSize); err != nil {
		t.Fatal(err)
	}
	if &table.varOffsetsBuf[0] != first {
		t.Fatal("variable offset scratch was reallocated")
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		_, _ = mdb.CrackRow(table, rowStart, rowSize)
	}); allocs != 0 {
		t.Fatalf("CrackRow allocs = %v, want 0 after warmup", allocs)
	}
}

func TestSQLValuesAreFormattedLazily(t *testing.T) {
	q, err := OpenQuery("../../testdata/typed.mdb",
		"SELECT id, flag, val_single, val_memo FROM typed WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if len(q.sql.BoundValues) != 0 {
		t.Fatalf("query allocated %d legacy bound buffers", len(q.sql.BoundValues))
	}
	for i, col := range q.sql.BoundColumns {
		if col.BindPtr != nil {
			t.Fatalf("column %d has an unexpected legacy bind buffer", i)
		}
	}
	if ok, err := q.Next(); err != nil || !ok {
		t.Fatalf("Next() = %v, %v", ok, err)
	}
	if _, ok := q.Int64Value(0); !ok {
		t.Fatal("native integer getter failed")
	}
	if _, ok := q.Float64Value(2); !ok {
		t.Fatal("native float getter failed")
	}
	for i, col := range q.sql.BoundColumns {
		if col.CurValueTextValid {
			t.Fatalf("native getter formatted column %d", i)
		}
	}
	if got := q.Value(0); got != "1" {
		t.Fatalf("Value(id) = %q, want 1", got)
	}
	if got := q.Value(1); got != "1" {
		t.Fatalf("Value(flag) = %q, want 1", got)
	}
	if got := q.Value(3); got != "memo content here" {
		t.Fatalf("Value(val_memo) = %q", got)
	}
	if !q.sql.BoundColumns[0].CurValueTextValid || !q.sql.BoundColumns[1].CurValueTextValid ||
		!q.sql.BoundColumns[3].CurValueTextValid {
		t.Fatal("Value did not populate the lazy compatibility string")
	}
	if got := q.sql.Mdb.columnValueToString(&MdbColumn{ColType: TypeBool}); got != "0" {
		t.Fatalf("false Boolean compatibility value = %q, want 0", got)
	}
}

func TestLazyValuePreservesBoundBufferRepresentations(t *testing.T) {
	mdb := &MdbHandle{}
	mdb.pgBuf = mdb.pgArr[:]
	copy(mdb.pgBuf[64:], []byte{'a', 0, 'b'})
	copy(mdb.pgBuf[128:], "abcdefghijkl")
	tests := []struct {
		name string
		col  *MdbColumn
		want string
	}{
		{"false Boolean", &MdbColumn{ColType: TypeBool}, "0"},
		{"binary NUL truncation", &MdbColumn{ColType: TypeBinary, CurValueStart: 64, CurValueLen: 3}, "a"},
		{"OLE header", &MdbColumn{ColType: TypeOLE, CurValueStart: 128, CurValueLen: MemoOverhead}, "abcdefghijkl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql := &SQL{Mdb: mdb, NumColumns: 1, BoundColumns: []*MdbColumn{tt.col}}
			if got := sql.Value(0); got != tt.want {
				t.Fatalf("Value() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCompatibilityFloat64MatchesLegacyFormatting(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 10000; i++ {
		f32 := math.Float32frombits(rng.Uint32())
		want, err := strconv.ParseFloat(strconv.FormatFloat(float64(f32), 'g', 8, 32), 64)
		if err != nil {
			t.Fatal(err)
		}
		if got := compatibilityFloat64(float64(f32), 8, 32); math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("float32 %v became %v, want %v", f32, got, want)
		}

		f64 := math.Float64frombits(rng.Uint64())
		want, err = strconv.ParseFloat(strconv.FormatFloat(f64, 'g', 16, 64), 64)
		if err != nil {
			t.Fatal(err)
		}
		if got := compatibilityFloat64(f64, 16, 64); math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("float64 %v became %v, want %v", f64, got, want)
		}
	}

	if allocs := testing.AllocsPerRun(1000, func() {
		_ = compatibilityFloat64(1.2345678901234567, 16, 64)
	}); allocs != 0 {
		t.Fatalf("compatibilityFloat64 allocs = %v, want 0", allocs)
	}
}
