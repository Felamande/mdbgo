package gomdb

import (
	"context"
	"encoding/binary"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Fast-scan pipeline.
//
// For large tables in unencrypted, fully resident databases, row cracking,
// sarg evaluation, and value formatting are embarrassingly parallel: rows
// only reference their own page, and page views alias stable file data. A
// producer walks the table's data pages and dispatches row ranges to a small
// worker pool; each worker cracks rows with its own scratch, formats the
// selected columns into preallocated row slots, and the consumer hands the
// finished batches back in order. Everything else (encrypted, streamed,
// temp-table, or small queries) keeps the original synchronous path.

var fastScanEnabled = true // test hook to force the synchronous path

const (
	fastScanMinRows  = 2048
	fastScanMinCells = 20000
	maxFastWorkers   = 10
	fastBatchRows    = 256
	fastTaskRows     = 128
	fastInFlight     = 8
)

func canFastScan(mdb *MdbHandle, sql *SQL) bool {
	if !fastScanEnabled || mdb == nil || mdb.f == nil || sql == nil || sql.CurTable == nil {
		return false
	}
	if sql.CurTable.IsTempTable || sql.NumColumns == 0 || len(sql.BoundColumns) != sql.NumColumns {
		return false
	}
	if sql.CurTable.Strategy != TableScan {
		return false
	}
	// Pure page views and direct memo access require resident, unencrypted data.
	if mdb.f.data == nil || mdb.f.dbKey != 0 {
		return false
	}
	rows := int64(sql.CurTable.NumRows)
	cells := rows * int64(sql.NumColumns)
	return rows >= fastScanMinRows && cells >= fastScanMinCells
}

type fastRow struct {
	page   []byte
	fields []rowField
	values []any
	valid  bool
}

// rowField is the compact per-cell descriptor retained for legacy getters
// (Value, Int64Value, ...). The full MdbField is only kept in worker scratch.
type rowField struct {
	start  int32
	siz    int32
	isNull bool
}

type fastBatch struct {
	rows        []fastRow
	n           int
	eof         bool
	err         error
	remaining   atomic.Int64
	doneSending atomic.Bool
	readyClosed atomic.Bool
	ready       chan struct{}
}

func newFastBatch(nRows, nBound int) *fastBatch {
	b := &fastBatch{
		rows:  make([]fastRow, nRows),
		ready: make(chan struct{}),
	}
	values := make([]any, nRows*nBound)
	fields := make([]rowField, nRows*nBound)
	for i := range b.rows {
		b.rows[i].values = values[i*nBound : (i+1)*nBound]
		b.rows[i].fields = fields[i*nBound : (i+1)*nBound]
	}
	return b
}

func (b *fastBatch) markReady() {
	if b.readyClosed.CompareAndSwap(false, true) {
		close(b.ready)
	}
}

type fastTask struct {
	batch    *fastBatch
	slot     int
	page     []byte
	firstRow int
	n        int
}

type fastScan struct {
	mdb       *MdbHandle
	sql       *SQL
	table     *MdbTableDef
	bound     []*MdbColumn
	crackCols []int
	sargMask  []bool

	ctx    context.Context
	cancel context.CancelFunc

	batches chan *fastBatch
	tasks   chan fastTask
	pool    chan *fastBatch
	slots   chan struct{}

	producerWG sync.WaitGroup
	workerWG   sync.WaitGroup

	// Consumer-side state.
	cur          *fastBatch
	curIdx       int
	curRow       *fastRow
	valueScratch *decodeScratch
	done         bool
	err          error
}

func newFastScan(mdb *MdbHandle, sql *SQL) *fastScan {
	ctx, cancel := context.WithCancel(context.Background())
	workers := runtime.GOMAXPROCS(0)
	if workers > maxFastWorkers {
		workers = maxFastWorkers
	}
	if workers < 2 {
		workers = 2
	}
	fs := &fastScan{
		mdb:          mdb,
		sql:          sql,
		table:        sql.CurTable,
		bound:        sql.BoundColumns,
		ctx:          ctx,
		cancel:       cancel,
		batches:      make(chan *fastBatch, 4),
		tasks:        make(chan fastTask, workers*4),
		pool:         make(chan *fastBatch, fastPoolBatches),
		slots:        make(chan struct{}, fastInFlight),
		valueScratch: &decodeScratch{fields: make([]MdbField, len(sql.CurTable.Columns))},
	}
	fs.sargMask = buildSargMask(sql)
	fs.crackCols = buildCrackCols(sql, fs.sargMask)
	for i := 0; i < fastInFlight; i++ {
		fs.slots <- struct{}{}
	}
	fs.producerWG.Add(1)
	go fs.produce()
	for i := 0; i < workers; i++ {
		fs.workerWG.Add(1)
		go func() {
			defer fs.workerWG.Done()
			fs.worker()
		}()
	}
	return fs
}

// buildSargMask returns a per-column mask of columns referenced by the sarg
// tree (nil when there is no WHERE clause). Those columns need per-field
// Value slices during cracking.
func buildSargMask(sql *SQL) []bool {
	tree := sql.CurTable.SargTree
	if tree == nil {
		return nil
	}
	mask := make([]bool, len(sql.CurTable.Columns))
	var walk func(n *SargNode)
	walk = func(n *SargNode) {
		if n == nil {
			return
		}
		if n.Col != nil && n.Col.ColNum >= 0 && n.Col.ColNum < len(mask) {
			mask[n.Col.ColNum] = true
		}
		walk(n.Left)
		walk(n.Right)
	}
	walk(tree)
	return mask
}

// buildCrackCols returns the set of column indices that row cracking must
// populate: every bound result column plus every column referenced by the
// sarg tree. Projection queries then avoid cracking unselected columns.
func buildCrackCols(sql *SQL, sargMask []bool) []int {
	needed := make([]bool, len(sql.CurTable.Columns))
	mark := func(col *MdbColumn) {
		if col != nil && col.ColNum >= 0 && col.ColNum < len(needed) {
			needed[col.ColNum] = true
		}
	}
	for _, col := range sql.BoundColumns {
		mark(col)
	}
	for i, ok := range sargMask {
		if ok {
			needed[i] = true
		}
	}
	cols := make([]int, 0, len(needed))
	for i, ok := range needed {
		if ok {
			cols = append(cols, i)
		}
	}
	return cols
}

func (fs *fastScan) close() {
	fs.cancel()
	fs.producerWG.Wait()
	fs.workerWG.Wait()
}

// produce walks the table's data pages and dispatches row ranges to workers.
// It mirrors MdbHandle.FetchRow's iteration (including the map-based
// ReadNextDpg traversal) but never binds shared column state.
func (fs *fastScan) produce() {
	defer fs.producerWG.Done()
	defer close(fs.tasks)
	mdb := fs.mdb
	table := fs.table
	mfmt := mdb.fmt

	var prev <-chan struct{}
	if !fs.acquireSlot() {
		return
	}
	batch := fs.acquireBatch()
	slot := 0

	// First data page; empty tables end here.
	if err := mdb.ReadNextDpg(table); err != nil {
		if err != errNoMorePages {
			fs.enqueueErr(err, prev)
			return
		}
		fs.enqueueEOF(prev)
		return
	}

	for {
		page := mdb.pgBuf
		rows := GetInt16(page, mfmt.RowCountOffset)
		row := 0
		for row < rows {
			n := rows - row
			if n > fastTaskRows {
				n = fastTaskRows
			}
			if slot+n > fastBatchRows {
				prev = fs.enqueueWhen(batch, prev, slot)
				if !fs.acquireSlot() {
					return
				}
				batch = fs.acquireBatch()
				slot = 0
			}
			if !fs.sendTask(batch, slot, page, row, n) {
				return
			}
			slot += n
			row += n
		}

		if err := mdb.ReadNextDpg(table); err != nil {
			if err == errNoMorePages {
				break
			}
			if slot > 0 {
				prev = fs.enqueueWhen(batch, prev, slot)
				slot = 0
			}
			fs.enqueueErr(err, prev)
			return
		}
	}

	if slot > 0 {
		prev = fs.enqueueWhen(batch, prev, slot)
	}
	fs.enqueueEOF(prev)
}

const fastPoolBatches = 8

func (fs *fastScan) acquireBatch() *fastBatch {
	select {
	case b := <-fs.pool:
		b.remaining.Store(0)
		b.doneSending.Store(false)
		b.readyClosed.Store(false)
		b.ready = make(chan struct{})
		return b
	default:
		return newFastBatch(fastBatchRows, len(fs.bound))
	}
}

func (fs *fastScan) releaseBatch(b *fastBatch) {
	select {
	case fs.pool <- b:
	default:
	}
}

// acquireSlot bounds how many batches the producer may have in flight; the
// consumer returns a slot when it finishes a batch, giving the pipeline
// backpressure so the producer cannot allocate unbounded arenas ahead of it.
func (fs *fastScan) acquireSlot() bool {
	select {
	case <-fs.slots:
		return true
	case <-fs.ctx.Done():
		return false
	}
}

func (fs *fastScan) releaseSlot() {
	fs.slots <- struct{}{}
}

func (fs *fastScan) sendTask(batch *fastBatch, slot int, page []byte, firstRow, n int) bool {
	batch.remaining.Add(1)
	select {
	case fs.tasks <- fastTask{batch: batch, slot: slot, page: page, firstRow: firstRow, n: n}:
		return true
	case <-fs.ctx.Done():
		batch.remaining.Add(-1)
		return false
	}
}

// enqueueWhen hands a finished batch to the consumer once every task has been
// processed. Batches are enqueued strictly in order via a prev chain.
func (fs *fastScan) enqueueWhen(b *fastBatch, prev <-chan struct{}, n int) <-chan struct{} {
	b.n = n
	// No more tasks will be added to this batch from here on, so the ready
	// signal can only fire once the outstanding task count is permanently 0.
	b.doneSending.Store(true)
	if b.remaining.Load() == 0 {
		b.markReady()
	}
	next := make(chan struct{})
	fs.producerWG.Add(1)
	go func() {
		defer fs.producerWG.Done()
		if prev != nil {
			select {
			case <-prev:
			case <-fs.ctx.Done():
				return
			}
		}
		select {
		case <-b.ready:
		case <-fs.ctx.Done():
			return
		}
		select {
		case fs.batches <- b:
		case <-fs.ctx.Done():
			return
		}
		close(next)
	}()
	return next
}

func (fs *fastScan) enqueueEOF(prev <-chan struct{}) {
	b := &fastBatch{ready: make(chan struct{})}
	b.eof = true
	b.markReady()
	fs.enqueueWhen(b, prev, 0)
}

func (fs *fastScan) enqueueErr(err error, prev <-chan struct{}) {
	b := &fastBatch{ready: make(chan struct{})}
	b.err = err
	b.markReady()
	fs.enqueueWhen(b, prev, 0)
}

func (fs *fastScan) worker() {
	s := &decodeScratch{
		fields:    make([]MdbField, len(fs.table.Columns)),
		layouts:   buildCrackLayouts(fs.table),
		valueMask: fs.sargMask,
	}
	for t := range fs.tasks {
		fs.processTask(t, s)
		t.batch.remaining.Add(-1)
		if t.batch.doneSending.Load() && t.batch.remaining.Load() == 0 {
			t.batch.markReady()
		}
	}
}

func (fs *fastScan) processTask(t fastTask, s *decodeScratch) {
	page := t.page
	table := fs.table
	bound := fs.bound
	for k := 0; k < t.n; k++ {
		r := &t.batch.rows[t.slot+k]
		r.valid = false
		r.page = page

		rowStart, rowSize, err := fs.mdb.findRowIn(page, t.firstRow+k)
		if err != nil || rowSize == 0 {
			continue
		}
		if rowStart&0x4000 != 0 && table.NoSkipDel == 0 {
			continue
		}
		rowStart &= OffsetMask

		fields, err := crackRowInto(fs.mdb, table, page, rowStart, rowSize, s, s.valueMask, fs.crackCols)
		if err != nil {
			continue
		}
		if table.SargTree != nil && !testSargsIn(fs.mdb, table.SargTree, fields, page, s) {
			continue
		}
		for i, col := range bound {
			f := &fields[col.ColNum]
			r.fields[i] = rowField{start: int32(f.Start), siz: int32(f.Siz), isNull: f.IsNull}
			r.values[i] = formatDriverValue(fs.mdb, col, page, f, s)
		}
		r.valid = true
	}
}

func testSargsIn(mdb *MdbHandle, node *SargNode, fields []MdbField, page []byte, s *decodeScratch) bool {
	return testSargNodeScratch(mdb, node, fields, page, s) != 0
}

// formatDriverValue produces the native driver.Value for one column of one
// row, mirroring the synchronous Rows.value/DriverValue semantics.
func formatDriverValue(mdb *MdbHandle, col *MdbColumn, page []byte, f *MdbField, s *decodeScratch) any {
	if col.ColType != TypeBool && f.IsNull {
		return nil
	}
	switch col.ColType {
	case TypeBool:
		v := 0
		if !f.IsNull {
			v = 1
		}
		return v != 0
	case TypeByte:
		if f.Siz >= 1 {
			return int64(page[f.Start])
		}
	case TypeInt:
		if f.Siz >= 2 {
			return int64(GetInt16(page, f.Start))
		}
	case TypeLongInt, TypeComplex:
		if f.Siz >= 4 {
			return int64(GetInt32(page, f.Start))
		}
	case TypeFloat:
		if f.Siz >= 4 {
			return compatibilityFloat64(float64(GetSingle(page, f.Start)), 8, 32)
		}
	case TypeDouble:
		if f.Siz >= 8 {
			return compatibilityFloat64(GetDouble(page, f.Start), 16, 64)
		}
	case TypeMoney:
		if f.Siz >= 8 {
			return float64(int64(binary.LittleEndian.Uint64(page[f.Start:]))) / 10000
		}
	case TypeDateTime:
		return DateToTime(GetDouble(page, f.Start))
	case TypeBinary:
		if f.Siz > 0 {
			data := make([]byte, f.Siz)
			copy(data, page[f.Start:f.Start+f.Siz])
			return data
		}
	case TypeText:
		// Fast-path text is pure ASCII and therefore NUL-free; skip the
		// trimNUL pass that the generic string path performs.
		src := page[f.Start : f.Start+f.Siz]
		if _, ok := unicodeFastPath(src, mdb.IsJet4()); ok {
			return string(src[2:])
		}
		return trimNUL(unicodeScratch(src, mdb.IsJet4(), s))
	default:
		return trimNUL(colToStringIn(mdb, col, f, page, s))
	}
	return trimNUL(colToStringIn(mdb, col, f, page, s))
}

// nextRow returns the next sarg-matching row, enforcing the query LIMIT like
// SQL.FetchRow does.
func (fs *fastScan) nextRow() (bool, error) {
	if fs.done {
		return false, fs.err
	}
	for {
		if fs.cur == nil || fs.curIdx >= fs.cur.n {
			if fs.cur != nil && fs.cur.rows != nil {
				fs.releaseBatch(fs.cur)
				fs.releaseSlot()
				fs.cur = nil
			}
			select {
			case b := <-fs.batches:
				fs.cur = b
				fs.curIdx = 0
				if b.err != nil {
					fs.curRow = nil
					fs.done = true
					fs.err = b.err
					return false, b.err
				}
				if b.eof {
					fs.curRow = nil
					fs.done = true
					return false, nil
				}
			case <-fs.ctx.Done():
				fs.curRow = nil
				fs.done = true
				return false, nil
			}
		}
		r := &fs.cur.rows[fs.curIdx]
		fs.curIdx++
		if !r.valid {
			continue
		}
		if fs.sql.Limit >= 0 && fs.sql.RowCount+1 > fs.sql.Limit {
			fs.curRow = nil
			return false, nil
		}
		fs.sql.RowCount++
		fs.curRow = r
		return true, nil
	}
}

// --- Fast-mode legacy getters (descriptor-based, no shared column state) ---

func (fs *fastScan) driverValue(i int) any {
	if fs.curRow == nil || i < 0 || i >= len(fs.bound) {
		return nil
	}
	return fs.curRow.values[i]
}

// driverRow returns the preformatted values for the current row, or nil when
// no row is current. Fast scans already box every value, so the consumer can
// copy the slice directly instead of calling per-column getters.
func (fs *fastScan) driverRow() []any {
	if fs.curRow == nil {
		return nil
	}
	return fs.curRow.values
}

func (fs *fastScan) value(i int) string {
	if fs.curRow == nil || i < 0 || i >= len(fs.bound) {
		return ""
	}
	col := fs.bound[i]
	if col == nil {
		return ""
	}
	// Boolean compatibility strings mirror columnValueToString: a NULL bool
	// renders as "0", not as the empty string.
	if col.ColType == TypeBool {
		if fs.curRow.fields[i].isNull {
			return "0"
		}
		return "1"
	}
	rf := &fs.curRow.fields[i]
	field := MdbField{Start: int(rf.start), Siz: int(rf.siz), IsNull: rf.isNull}
	return trimNUL(colToStringIn(fs.mdb, col, &field, fs.curRow.page, fs.valueScratch))
}

func (fs *fastScan) isNull(i int) bool {
	if fs.curRow == nil || i < 0 || i >= len(fs.bound) {
		return true
	}
	if fs.bound[i].ColType == TypeBool {
		return false
	}
	return fs.curRow.fields[i].isNull
}

func (fs *fastScan) boolValue(i int) (bool, bool) {
	if fs.curRow == nil || i < 0 || i >= len(fs.bound) {
		return false, false
	}
	col := fs.bound[i]
	if col.ColType != TypeBool {
		return false, false
	}
	return !fs.curRow.fields[i].isNull, true
}

func (fs *fastScan) int64Value(i int) (int64, bool) {
	if fs.curRow == nil || i < 0 || i >= len(fs.bound) {
		return 0, false
	}
	col := fs.bound[i]
	f := &fs.curRow.fields[i]
	if col.ColType != TypeBool && f.isNull {
		return 0, false
	}
	page := fs.curRow.page
	switch col.ColType {
	case TypeBool:
		v := 0
		if !f.isNull {
			v = 1
		}
		return int64(v), true
	case TypeByte:
		if f.siz < 1 {
			return 0, false
		}
		return int64(page[f.start]), true
	case TypeInt:
		if f.siz < 2 {
			return 0, false
		}
		return int64(GetInt16(page, int(f.start))), true
	case TypeLongInt, TypeComplex:
		if f.siz < 4 {
			return 0, false
		}
		return int64(GetInt32(page, int(f.start))), true
	}
	return 0, false
}

func (fs *fastScan) float64Value(i int) (float64, bool) {
	if fs.curRow == nil || i < 0 || i >= len(fs.bound) {
		return 0, false
	}
	col := fs.bound[i]
	f := &fs.curRow.fields[i]
	if f.isNull {
		return 0, false
	}
	page := fs.curRow.page
	switch col.ColType {
	case TypeFloat:
		if f.siz < 4 {
			return 0, false
		}
		return compatibilityFloat64(float64(GetSingle(page, int(f.start))), 8, 32), true
	case TypeDouble:
		if f.siz < 8 {
			return 0, false
		}
		return compatibilityFloat64(GetDouble(page, int(f.start)), 16, 64), true
	case TypeMoney:
		if f.siz < 8 {
			return 0, false
		}
		return float64(int64(binary.LittleEndian.Uint64(page[int(f.start):]))) / 10000, true
	}
	return 0, false
}

func (fs *fastScan) dateTimeValue(i int) (time.Time, bool) {
	if fs.curRow == nil || i < 0 || i >= len(fs.bound) {
		return time.Time{}, false
	}
	col := fs.bound[i]
	f := &fs.curRow.fields[i]
	if col.ColType != TypeDateTime || f.isNull {
		return time.Time{}, false
	}
	return DateToTime(GetDouble(fs.curRow.page, int(f.start))), true
}

func (fs *fastScan) binaryValue(i int) []byte {
	if fs.curRow == nil || i < 0 || i >= len(fs.bound) {
		return nil
	}
	col := fs.bound[i]
	f := &fs.curRow.fields[i]
	if col.ColType != TypeBinary || f.isNull || f.siz <= 0 {
		return nil
	}
	data := make([]byte, f.siz)
	copy(data, fs.curRow.page[int(f.start):int(f.start)+int(f.siz)])
	return data
}
