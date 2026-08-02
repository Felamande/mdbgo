package gomdb

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

// MdbFile represents an open MDB database file.
type MdbFile struct {
	stream     io.ReaderAt
	size       int64
	jetVersion int
	dbKey      uint32
	codePage   uint16
	langID     uint16
}

// MdbHandle holds all state for reading an MDB database.
type MdbHandle struct {
	f        *MdbFile
	curPg    uint32
	curPos   int
	pgBuf    [PageSize]byte
	altPgBuf [PageSize]byte
	altPg    uint32
	altValid bool
	fmt      *MdbFormatConstants

	bindSize     int
	dateFmt      string
	shortDateFmt string
	unicodeBuf   []byte
	memoBuf      []byte

	// Catalog
	numCatalog int
	Catalog    []*CatalogEntry

	// Properties
	props map[string]string
}

// CatalogEntry represents an entry in the MSysObjects catalog table.
type CatalogEntry struct {
	Mdb        *MdbHandle
	ObjectName string
	ObjectType int
	TablePg    uint32
	Flags      int
	Props      []*Properties
}

// Properties represents parsed KKD property data.
type Properties struct {
	Name string
	Hash map[string]string
}

// OpenMDB opens an MDB file at the given path and returns a handle.
func OpenMDB(filename string) (*MdbHandle, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("gomdb: unable to open file %s: %w", filename, err)
	}
	return openMDBFromReader(f)
}

// openMDBFromReader creates an MdbHandle from an io.ReaderAt.
func openMDBFromReader(r io.ReaderAt) (*MdbHandle, error) {
	mdb := &MdbHandle{
		bindSize:     BindSize,
		dateFmt:      "%x %X",
		shortDateFmt: "%x",
		Catalog:      make([]*CatalogEntry, 0),
		props:        make(map[string]string),
	}

	// Bootstrap with Jet3 constants; will be corrected after reading page 0
	mdb.fmt = &Jet3FormatConstants

	size, err := readerSize(r)
	if err != nil {
		return nil, fmt.Errorf("gomdb: unable to determine file size: %w", err)
	}

	mdb.f = &MdbFile{
		stream: r,
		size:   size,
	}

	// Read page 0 (database definition page)
	if err := mdb.readPage(0); err != nil {
		return nil, fmt.Errorf("gomdb: unable to read page 0: %w", err)
	}

	// Verify it's an MDB file
	if mdb.pgBuf[0] != 0 {
		return nil, fmt.Errorf("gomdb: not a valid MDB file (page 0 type = %d)", mdb.pgBuf[0])
	}

	// Detect Jet version
	mdb.f.jetVersion = int(mdb.pgBuf[0x14])
	switch mdb.f.jetVersion {
	case Jet3:
		mdb.fmt = &Jet3FormatConstants
	case Jet4, Accdb2007, Accdb2010, Accdb2013, Accdb2016, Accdb2019:
		mdb.fmt = &Jet4FormatConstants
	default:
		return nil, fmt.Errorf("gomdb: unknown Jet version: 0x%02x", mdb.f.jetVersion)
	}

	// Decrypt database definition section
	tmpKey := []byte{0xC7, 0xDA, 0x39, 0x6B}
	decryptLen := 128
	if mdb.f.jetVersion == Jet3 {
		decryptLen = 126
	}
	rc4Decrypt(tmpKey, mdb.pgBuf[0x18:0x18+decryptLen])

	// Extract code page and language ID
	if mdb.f.jetVersion == Jet3 {
		mdb.f.langID = GetUint16(mdb.pgBuf[:], 0x3a)
	} else {
		mdb.f.langID = GetUint16(mdb.pgBuf[:], 0x6e)
	}
	mdb.f.codePage = GetUint16(mdb.pgBuf[:], 0x3c)
	mdb.f.dbKey = GetUint32(mdb.pgBuf[:], 0x3e)

	return mdb, nil
}

// IsJet3 returns true if the database is Jet3 (Access 97) format.
func (mdb *MdbHandle) IsJet3() bool {
	return mdb.f.jetVersion == Jet3
}

// IsJet4 returns true if the database is Jet4 (Access 2000+) format.
func (mdb *MdbHandle) IsJet4() bool {
	return mdb.f.jetVersion != Jet3
}

// Fmt returns the format constants for this database.
func (mdb *MdbHandle) Fmt() *MdbFormatConstants {
	return mdb.fmt
}

// CodePage returns the database code page.
func (mdb *MdbHandle) CodePage() uint16 {
	return mdb.f.codePage
}

// readPage reads a page by number into pgBuf.
func (mdb *MdbHandle) readPage(pg uint32) error {
	if pg != 0 && mdb.curPg == pg {
		return nil // already loaded
	}

	if err := mdb.readPageInto(mdb.pgBuf[:], pg); err != nil {
		return err
	}

	mdb.curPg = pg
	mdb.curPos = 0
	return nil
}

// readAltPage reads a page by number into altPgBuf.
func (mdb *MdbHandle) readAltPage(pg uint32) error {
	if mdb.altValid && mdb.altPg == pg {
		return nil
	}

	if err := mdb.readPageInto(mdb.altPgBuf[:], pg); err != nil {
		return err
	}
	mdb.altPg = pg
	mdb.altValid = true
	return nil
}

// readPageInto reads and decrypts a page into dst. Keeping the disk I/O and
// decryption path shared is important: rows referenced through pg_row values
// are read through the alternate buffer and may live in encrypted databases.
func (mdb *MdbHandle) readPageInto(dst []byte, pg uint32) error {
	pgSize := mdb.fmt.PgSize
	offset := int64(pg) * int64(pgSize)
	if offset >= mdb.f.size {
		return fmt.Errorf("gomdb: page %d is beyond EOF (offset %d, size %d)", pg, offset, mdb.f.size)
	}

	buf := dst[:pgSize]
	n, err := mdb.f.stream.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return fmt.Errorf("gomdb: error reading page %d: %w", pg, err)
	}
	clear(buf[n:])

	if pg != 0 && mdb.f.dbKey != 0 {
		key := mdb.f.dbKey ^ pg
		tmpKey := [4]byte{byte(key), byte(key >> 8), byte(key >> 16), byte(key >> 24)}
		rc4Decrypt(tmpKey[:], buf)
	}
	return nil
}

// Close closes the MDB handle and releases resources.
func (mdb *MdbHandle) Close() error {
	if mdb.f != nil {
		if closer, ok := mdb.f.stream.(io.Closer); ok {
			return closer.Close()
		}
	}
	return nil
}

// readerSize attempts to determine the size of an io.ReaderAt.
func readerSize(r io.ReaderAt) (int64, error) {
	if f, ok := r.(*os.File); ok {
		fi, err := f.Stat()
		if err != nil {
			return 0, err
		}
		return fi.Size(), nil
	}
	// For other ReaderAt implementations, try common interfaces
	if s, ok := r.(interface{ Size() int64 }); ok {
		return s.Size(), nil
	}
	return 0, fmt.Errorf("gomdb: cannot determine size of reader")
}

// --- Byte/Integer/Float extraction from buffers ---

// GetByte reads a single byte at offset from a buffer.
func GetByte(buf []byte, offset int) byte {
	return buf[offset]
}

// GetInt16 reads a little-endian signed 16-bit integer at offset from a buffer.
func GetInt16(buf []byte, offset int) int {
	return int(int16(binary.LittleEndian.Uint16(buf[offset:])))
}

// GetInt32 reads a little-endian signed 32-bit integer at offset from a buffer.
func GetInt32(buf []byte, offset int) int {
	return int(int32(binary.LittleEndian.Uint32(buf[offset:])))
}

// GetUint32 reads a little-endian unsigned 32-bit integer at offset from a buffer.
func GetUint32(buf []byte, offset int) uint32 {
	return binary.LittleEndian.Uint32(buf[offset:])
}

// GetInt32MSB reads a big-endian (MSB) 32-bit integer at offset from a buffer.
func GetInt32MSB(buf []byte, offset int) int {
	return int(binary.BigEndian.Uint32(buf[offset:]))
}

// GetUint16 reads a little-endian unsigned 16-bit integer at offset from a buffer.
func GetUint16(buf []byte, offset int) uint16 {
	return binary.LittleEndian.Uint16(buf[offset:])
}

// GetSingle reads a 32-bit IEEE 754 float (little-endian) at offset from a buffer.
func GetSingle(buf []byte, offset int) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(buf[offset:]))
}

// GetDouble reads a 64-bit IEEE 754 double (little-endian) at offset from a buffer.
func GetDouble(buf []byte, offset int) float64 {
	return math.Float64frombits(binary.LittleEndian.Uint64(buf[offset:]))
}

// PgGetByte reads a single byte at the current position, advancing it by 1.
func (mdb *MdbHandle) PgGetByte() byte {
	b := mdb.pgBuf[mdb.curPos]
	mdb.curPos++
	return b
}

// PgGetInt16 reads a little-endian 16-bit integer at the current position, advancing by 2.
func (mdb *MdbHandle) PgGetInt16() int {
	v := GetInt16(mdb.pgBuf[:], mdb.curPos)
	mdb.curPos += 2
	return v
}

// PgGetInt32 reads a little-endian 32-bit integer at the current position, advancing by 4.
func (mdb *MdbHandle) PgGetInt32() int {
	v := GetInt32(mdb.pgBuf[:], mdb.curPos)
	mdb.curPos += 4
	return v
}

// PgGetInt32MSB reads a big-endian 32-bit integer at the current position, advancing by 4.
func (mdb *MdbHandle) PgGetInt32MSB() int {
	v := GetInt32MSB(mdb.pgBuf[:], mdb.curPos)
	mdb.curPos += 4
	return v
}

// PgGetFirstByte returns the first byte of the current page.
func (mdb *MdbHandle) PgGetFirstByte() byte {
	return mdb.pgBuf[0]
}

// --- Page-read-if functions (advance across pages as needed) ---

// ReadPgIf8 reads a single byte, advancing pages if needed.
func (mdb *MdbHandle) ReadPgIf8() byte {
	var b [1]byte
	mdb.ReadPgIfN(b[:], 1)
	return b[0]
}

// ReadPgIf16 reads a little-endian uint16, advancing pages if needed.
func (mdb *MdbHandle) ReadPgIf16() uint16 {
	var b [2]byte
	mdb.ReadPgIfN(b[:], 2)
	return GetUint16(b[:], 0)
}

// ReadPgIf32 reads a little-endian uint32, advancing pages if needed.
func (mdb *MdbHandle) ReadPgIf32() uint32 {
	var b [4]byte
	mdb.ReadPgIfN(b[:], 4)
	return GetUint32(b[:], 0)
}

// ReadPgIfN reads n bytes from the current position, advancing across pages as needed.
// Returns the actual number of bytes read.
func (mdb *MdbHandle) ReadPgIfN(dst []byte, n int) int {
	if mdb.curPos < 0 {
		return 0
	}

	origN := n
	read := 0

	for read < origN {
		// If current position is past end of page, advance to next page
		for mdb.curPos >= mdb.fmt.PgSize {
			nextPg := GetInt32(mdb.pgBuf[:], 4)
			if err := mdb.readPage(uint32(nextPg)); err != nil {
				return read
			}
			mdb.curPos -= (mdb.fmt.PgSize - 8)
		}

		// Read available data from current page
		remaining := origN - read
		avail := mdb.fmt.PgSize - mdb.curPos
		if avail > remaining {
			avail = remaining
		}
		copy(dst[read:], mdb.pgBuf[mdb.curPos:mdb.curPos+avail])
		read += avail
		mdb.curPos += avail
	}

	return read
}

// findPgRow locates a row given a pg_row reference (upper 3 bytes = page, lower byte = row).
// Returns a pointer to the buffer, the row offset, and the row length.
func (mdb *MdbHandle) findPgRow(pgRow int) (buf []byte, offset int, length int, err error) {
	pg := uint32(pgRow >> 8)
	row := pgRow & 0xff

	if err := mdb.readAltPage(pg); err != nil {
		return nil, 0, 0, err
	}
	off, sz, err := mdb.findRowIn(mdb.altPgBuf[:], row)
	if err != nil {
		return nil, 0, 0, err
	}
	off &= OffsetMask
	return mdb.altPgBuf[:], off, sz, nil
}

// findRow locates a row within the current page and returns its offset and length.
func (mdb *MdbHandle) findRow(row int) (start int, length int, err error) {
	return mdb.findRowIn(mdb.pgBuf[:], row)
}

// findRowIn locates a row in buf without changing the handle's active page.
func (mdb *MdbHandle) findRowIn(buf []byte, row int) (start int, length int, err error) {
	rco := mdb.fmt.RowCountOffset

	if row > 1000 {
		return 0, 0, fmt.Errorf("gomdb: row %d exceeds maximum", row)
	}

	start = GetInt16(buf, rco+2+row*2)
	nextStart := mdb.fmt.PgSize
	if row > 0 {
		nextStart = GetInt16(buf, rco+row*2) & OffsetMask
	}
	length = nextStart - (start & OffsetMask)

	startOff := start & OffsetMask
	if startOff >= mdb.fmt.PgSize || startOff > nextStart || nextStart > mdb.fmt.PgSize {
		return 0, 0, fmt.Errorf("gomdb: invalid row position")
	}

	return start, length, nil
}

// findEndOfRow returns the end offset for a given row.
func (mdb *MdbHandle) findEndOfRow(row int) int {
	rco := mdb.fmt.RowCountOffset

	if row > 1000 {
		return -1
	}

	if row == 0 {
		return mdb.fmt.PgSize - 1
	}
	return (GetInt16(mdb.pgBuf[:], rco+row*2) & OffsetMask) - 1
}

// isNull checks if a column is null in the null mask.
// colNum is 1-based. Returns true if the column is NULL.
// If colNum is 0, the column has no null mask entry — treat as non-null.
func isNull(nullMask []byte, colNum int) bool {
	if colNum <= 0 {
		return false // no null mask entry → assume non-null
	}
	if colNum > len(nullMask)*8 {
		return false
	}
	byteNum := (colNum - 1) / 8
	bitNum := (colNum - 1) % 8

	if byteNum >= len(nullMask) {
		return false
	}
	if (1<<bitNum)&nullMask[byteNum] != 0 {
		return false
	}
	return true
}

// setPos sets the current page position.
func (mdb *MdbHandle) setPos(pos int) int {
	if pos < 0 || pos >= mdb.fmt.PgSize {
		return 0
	}
	mdb.curPos = pos
	return pos
}
