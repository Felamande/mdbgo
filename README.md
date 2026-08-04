# mdbgo

A read-only `database/sql` driver for Microsoft Access databases (`.mdb` and `.accdb`) written in Go. **No Microsoft software, ODBC drivers, or OLE DB providers required.**

mdbgo provides **two drivers** — a pure Go implementation and a CGo-based implementation backed by [mdbtools](https://github.com/mdbtools/mdbtools).

| Driver | Name | Backend | C compiler | Speed | Memory |
|--------|------|---------|------------|-------|--------|
| Pure Go | `"gomdb"` | `driver/gomdb` | No | Fastest | ~25 KB/op |
| CGo | `"cmdb"` | `driver/cmdb` | `zig cc` | Fast | ~2 KB/op |

Both drivers are feature-equivalent and produce identical results — verified across 32,549 rows over 5 databases with 0 value differences.

Large scans use a parallel fast path: unencrypted databases are read once
into memory (with a bounded cross-connection file cache), page views are
zero-copy, and row cracking, WHERE evaluation, and value formatting run
across a small worker pool with ordered, backpressure-bounded batches. On
the 27k-row `lm.mdb` workload the pure-Go driver scans all 44 columns of
`MibTree` in ~30 ms (vs ~135 ms for the synchronous path), and a 2-column
projection in ~10 ms.

The decode kernels (compressed-ASCII validation, UTF-16 ASCII packing, and
compressed-byte expansion) can additionally be built with Go 1.26's
experimental `simd/archsimd` package: build with `GOEXPERIMENT=simd` and an
AVX2-capable CPU to run them as vectorized kernels (roughly 2.5-8x faster
than the scalar loops; the exact figures depend on the workload). Without
the experiment flag, or on older CPUs, the scalar kernels are used
automatically. Note that the end-to-end scan time is dominated by value
allocation and interface boxing, so the SIMD gain is visible in the decode
step rather than in the full `database/sql` scan.

> **Note:** Both drivers are read-only — querying existing Access databases only, no writes.

## Features

- Standard `database/sql` interface
- Full [sqlx](https://github.com/jmoiron/sqlx) compatibility (`StructScan`, `Select`, `Get`, `Named` queries)
- Reads MDB (Jet 3/4) and ACCDB files
- No dependency on Microsoft Access, ACE/Jet OLE DB, or ODBC
- SQL queries: `SELECT` (with `TOP N [PERCENT]`), `WHERE` (including `IN` and `LIKE`/`ILIKE`), `ORDER BY` (column or `Len(column)`, `ASC`/`DESC`), `LIMIT`, `LIST TABLES`, `DESCRIBE TABLE`
- Parameterized queries with `?` placeholders
- All 15 Access column types with `sql.Null*` support
- Full column metadata (`ColumnTypeDatabaseTypeName`, `ColumnTypeLength`, `ColumnTypeScanType`)
- Unicode support (CJK, Arabic, etc.)
- Binary data and OLE object reading
- DateTime handling with `time.Time`
- LIKE pattern matching with Chinese/Unicode text

## Installation

```bash
go get github.com/Felamande/mdbgo
```

### Pure Go driver — `"gomdb"`

No C toolchain needed:

```bash
CGO_ENABLED=0 go build
```

### CGo driver — `"cmdb"`

Requires a C compiler. [zig cc](https://ziglang.org) recommended:

```bash
CC="zig cc" CGO_ENABLED=1 go build
```

No external C libraries needed — mdbtools is compiled in-tree.

## Usage

```go
import (
    "database/sql"
    _ "github.com/Felamande/mdbgo/driver/gomdb"  // pure Go
    // _ "github.com/Felamande/mdbgo/driver/cmdb" // or CGo
)

db, _ := sql.Open("gomdb", "path/to/database.mdb") // or "cmdb"

// Query with parameters
rows, _ := db.Query("SELECT ID, Name, Birthday FROM Users WHERE Age > ?", 18)

// sqlx struct scanning
type User struct {
    ID       int64     `db:"ID"`
    Name     string    `db:"Name"`
    Birthday time.Time `db:"Birthday"`
}
var users []User
sqlxDB.Select(&users, "SELECT * FROM Users")
```

`WHERE ... IN` and `ORDER BY` are pure-Go (gomdb) features; the CGo cmdb
driver's SQL grammar does not support them. Dotted values such as OIDs must
be quoted in `IN` lists:

```go
rows, _ := db.Query(
    "SELECT TOP 1 * FROM MibTree WHERE OID IN ('1.2.1.1.1.1','1.2.1.1.1','1.2.1.1','1.2.1') ORDER BY Len(OID) DESC",
)
```

### Listing tables
```go
rows, _ := db.Query("LIST TABLES")
```

### Describing a table
```go
rows, _ := db.Query("DESCRIBE TABLE MyTable")
```

## Type Mapping

| Access Type | Go Type | `DatabaseTypeName()` |
|---|---|---|
| Boolean | `bool` | `"Boolean"` |
| Byte | `int64` | `"Byte"` |
| Integer | `int64` | `"Integer"` |
| Long Integer | `int64` | `"Long Integer"` |
| Currency | `float64` | `"Currency"` |
| Single | `float64` | `"Single"` |
| Double | `float64` | `"Double"` |
| DateTime | `time.Time` | `"DateTime"` |
| Text | `string` | `"Text"` |
| Memo | `string` | `"Memo/Hyperlink"` |
| Binary | `[]byte` | `"Binary"` |
| OLE Object | `[]byte` | `"OLE"` |
| Replication ID | `string` | `"Replication ID"` |
| Numeric | `string` | `"Numeric"` |
| Complex | `int64` | — |

## Limitations

- **Read-only** — no INSERT, UPDATE, DELETE, or DDL operations
- **No transactions** — `Begin()` not supported
- **Client-side parameter interpolation** — `?` placeholders are escaped and interpolated into the SQL string
- **BIT/Boolean NULL** — Access BIT fields cannot store NULL; NULL coerces to FALSE
- **TEXT length** — Unicode TEXT(n) columns report byte length (2×n)
- **No `IN (...)` syntax** — use multiple `OR` conditions instead
- **Jet SQL only** — advanced ACCDB SQL features may not be supported

## Architecture

```
┌──────────────────────────────────────────────┐
│  Go application / sqlx                       │
├──────────────────────────────────────────────┤
│  driver/gomdb/gomdb.go    driver/cmdb/cmdb.go
│  (pure Go, zero CGo)        (CGo + mdbtools) │
├──────────────────────────────────────────────┤
│  internal/gomdb/           internal/cmdb/   │
│  19 .go files               C source + cgo   │
└──────────────────────────────────────────────┘
```

Both backends parse Access database files directly — the pure Go driver is a ground-up port of mdbtools with no C dependencies, while the CGo driver wraps the original C library.

## Project layout

```
mdbgo/
├── driver/
│   ├── cmdb/                 # CGo driver — registers "cmdb"
│   │   └── cmdb.go
│   └── gomdb/               # Pure Go driver — registers "gomdb"
│       └── gomdb.go
├── internal/
│   ├── cmdb/                 # CGo backend (bridge + mdbtools C source)
│   └── gomdb/               # Pure Go backend (MDB parser, SQL engine)
├── testdata/                 # .mdb test databases
└── temp/                     # Comparison harnesses
```

## Requirements

- Go 1.26+
- CGo driver: C compiler (gcc, clang, or `zig cc`)
- Pure Go driver: no additional requirements

## License

Go code: [MIT License](LICENSE)

This project embeds [mdbtools](https://github.com/mdbtools/mdbtools) under the [LGPL-2.0-or-later](THIRDPARTY-LICENSES.md). See `internal/cmdb/mdbtools/COPYING.LIB`.
