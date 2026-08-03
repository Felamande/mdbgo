package gomdb

import "testing"

func TestProbeStringDuplication(t *testing.T) {
	q, err := OpenQuery("../../testdata/lm.mdb", "SELECT * FROM [MibTree]")
	if err != nil { t.Fatal(err) }
	defer q.Close()
	info := q.ColumnInfo()
	seen := map[string]bool{}
	total := 0
	byCol := map[int]map[string]bool{}
	byColTotal := map[int]int{}
	for {
		ok, err := q.Next()
		if err != nil { t.Fatal(err) }
		if !ok { break }
		for c := 0; c < len(info); c++ {
			v := q.DriverValue(c)
			s, isStr := v.(string)
			if !isStr { continue }
			total++
			seen[s] = true
			if byCol[c] == nil { byCol[c] = map[string]bool{} }
			byCol[c][s] = true
			byColTotal[c]++
		}
	}
	t.Logf("total strings=%d distinct=%d", total, len(seen))
	for c := 0; c < len(info); c++ {
		if byColTotal[c] > 0 {
			t.Logf("col %d (%s): %d strings, %d distinct", c, info[c].Name, byColTotal[c], len(byCol[c]))
		}
	}
}
