package gomdb

import "testing"

func TestPlanReuseMatchesFreshExecution(t *testing.T) {
	mdb, err := OpenMDB("../../testdata/people.mdb")
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	const query = "SELECT id, name, active FROM people WHERE id > 1"
	run := func(q *Query) [][]any {
		t.Helper()
		var rows [][]any
		for {
			ok, err := q.Next()
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			row := make([]any, 3)
			for i := 0; i < 3; i++ {
				row[i] = q.DriverValue(i)
			}
			rows = append(rows, row)
		}
		return rows
	}

	q1, err := OpenQueryOnHandle(mdb, query)
	if err != nil {
		t.Fatal(err)
	}
	first := run(q1)
	p := q1.CapturePlan()
	q1.Close()
	if p == nil {
		t.Fatal("CapturePlan returned nil for a regular SELECT")
	}
	defer p.release()

	for i := 0; i < 3; i++ {
		q2, err := p.Execute(mdb)
		if err != nil {
			t.Fatalf("Execute #%d: %v", i, err)
		}
		rows := run(q2)
		q2.Close()
		if len(rows) != len(first) {
			t.Fatalf("execute %d row count = %d, want %d", i, len(rows), len(first))
		}
		for r := range rows {
			for c := range rows[r] {
				if !fastValueEqual(rows[r][c], first[r][c]) {
					t.Fatalf("execute %d row %d col %d: %#v vs %#v", i, r, c, rows[r][c], first[r][c])
				}
			}
		}
	}
}

func TestPlanRejectsConcurrentUse(t *testing.T) {
	mdb, err := OpenMDB("../../testdata/people.mdb")
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	q, err := OpenQueryOnHandle(mdb, "SELECT id FROM people")
	if err != nil {
		t.Fatal(err)
	}
	p := q.CapturePlan()
	q.Close()
	if p == nil {
		t.Fatal("CapturePlan returned nil")
	}
	defer p.release()

	q1, err := p.Execute(mdb)
	if err != nil {
		t.Fatal(err)
	}
	defer q1.Close()
	if _, err := p.Execute(mdb); err == nil {
		t.Fatal("Execute succeeded while the plan was in use")
	}
}
