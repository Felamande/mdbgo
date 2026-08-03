package gomdb

import "errors"

// Plan is a reusable prepared query: a fully executed SQL statement with its
// resolved table definition and sarg tree. Plans are shared within one
// MdbHandle across executions; scan state is reset on every Execute, so
// concurrent use of the same plan is not allowed (Execute returns an error
// while the plan is in use).
type Plan struct {
	sql      *SQL
	sargTree *SargNode
	inUse    bool
	size     int64
	mtime    int64
}

var errPlanUnavailable = errors.New("gomdb: query plan unavailable")

func (p *Plan) release() {
	p.inUse = false
}

// Execute returns a fresh Query borrowing the plan's SQL. The query does not
// own the MdbHandle; call Close (or let the driver Rows close it) to release
// the plan for the next execution.
func (p *Plan) Execute(mdb *MdbHandle) (*Query, error) {
	if p == nil || p.inUse || mdb == nil || mdb.f == nil {
		return nil, errPlanUnavailable
	}
	if p.size != mdb.f.size || p.mtime != mdb.f.mtime {
		return nil, errPlanUnavailable
	}
	p.inUse = true
	sql := p.sql
	sql.RowCount = 0
	table := sql.CurTable
	if table != nil {
		mdb.RewindTable(table)
		table.SargTree = p.sargTree
	}
	q := &Query{sql: sql, plan: p}
	if canFastScan(mdb, sql) {
		q.fast = newFastScan(mdb, sql)
	}
	return q, nil
}
