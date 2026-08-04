//go:build windows

// Package odbcbench holds benchmark-only tests that run the large lm.mdb
// workload through the 32-bit Microsoft Access ODBC driver. Build and run
// them with GOARCH=386 so the 32-bit odbc32.dll (and Access driver) load:
//
//	GOARCH=386 go test ./driver/odbc -bench .
package odbcbench
