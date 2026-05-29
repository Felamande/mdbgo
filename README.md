# mdbgo

A read-only `database/sql` driver for Microsoft Access databases (`.mdb` and `.accdb`) written in Go. **No Microsoft software, ODBC drivers, or OLE DB providers required.**

mdbgo parses Access database files directly by embedding the open-source [mdbtools](https://github.com/mdbtools/mdbtools) C library via cgo. It compiles mdbtools in-tree with zero external Go dependencies.

> **Note:** This driver is read-only and feature-limited. It is designed for querying existing Access databases, not modifying them.

## Features

- Standard `database/sql` interface (driver name: `"mdb"`)
- Reads both MDB (Jet) and ACCDB files
- No dependency on Microsoft Access, ACE/Jet OLE DB, or ODBC
- SQL queries via mdbtools' built-in Jet SQL engine (`SELECT`, `WHERE`, `ORDER BY`, `LIMIT`, `LIST TABLES`, `DESCRIBE`)
- Parameterized queries with `?` placeholders
- Full type coverage for all 15 Access column types
- Rich column metadata (`ColumnTypeDatabaseTypeName`, `ColumnTypeLength`, `ColumnTypeNullable`, `ColumnTypeScanType`)
- Unicode support (CJK, Arabic, etc.)
- Binary data and OLE object reading
- DateTime handling (Julian day conversion to `time.Time`)

## Installation

```bash
go get github.com/Felamande/mdbgo
```

A C compiler (gcc/clang/MSVC) is required since the project uses cgo. No additional C libraries need to be installed — mdbtools is compiled from the embedded submodule.

### Clone with submodule

```bash
git clone --recurse-submodules https://github.com/Felamande/mdbgo.git
```

If already cloned without submodules:

```bash
git submodule update --init --recursive
```

## Usage

```go
package main

import (
    "database/sql"
    "fmt"
    "log"

    _ "github.com/Felamande/mdbgo"
)

func main() {
    db, err := sql.Open("mdb", "path/to/database.mdb")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    rows, err := db.QueryContext(context.Background(),
        "SELECT ID, Name, Birthday FROM Users WHERE Age > ?", 18)
    if err != nil {
        log.Fatal(err)
    }
    defer rows.Close()

    for rows.Next() {
        var id int64
        var name string
        var birthday time.Time
        if err := rows.Scan(&id, &name, &birthday); err != nil {
            log.Fatal(err)
        }
        fmt.Printf("%d: %s (%s)\n", id, name, birthday)
    }
}
```

### Listing tables

```go
rows, err := db.Query("LIST TABLES")
```

### Describing a table

```go
rows, err := db.Query("DESCRIBE MyTable")
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
- **No transactions** — `Begin()` is not supported
- **No connection pooling** — each query opens a fresh mdbtools handle
- **Client-side parameter interpolation** — `?` placeholders are escaped and interpolated into the SQL string before execution (safe, but not true server-side prepared statements)
- **BIT/Boolean NULL** — Access BIT fields cannot store NULL; NULL coerces to FALSE
- **TEXT length** — Unicode TEXT(n) columns report byte length (2*n), not character count
- **cgo required** — a C toolchain is needed to build; cross-compilation is more involved
- **Jet SQL only** — advanced ACCDB-specific SQL features may not be supported

## Architecture

```
┌─────────────────────────────────────┐
│  Go application (database/sql)      │
├─────────────────────────────────────┤
│  driver.go  (mdbtool package)       │  ← sql/driver interfaces
├─────────────────────────────────────┤
│  internal/cmdb/cmdb.go              │  ← cgo bridge (Go side)
├─────────────────────────────────────┤
│  internal/cmdb/bridge.c             │  ← cgo bridge (C side, 148 lines)
├─────────────────────────────────────┤
│  mdbtools (embedded C library)      │  ← libmdb + libmdbsql + parser/lexer
│  + fakeglib (GLib replacement)      │
└─────────────────────────────────────┘
```

mdbtools source is included as a git submodule and compiled in-tree via stub `.c` files that `#include` the real sources. The Bison parser and Flex lexer output are pre-generated, so no code generation tools are needed at build time.

## Requirements

- Go 1.26+
- C compiler (gcc, clang, or MSVC)
- Git (for submodule checkout)

## License

This project wraps [mdbtools](https://github.com/mdbtools/mdbtools), which is licensed under LGPL. See the mdbtools submodule for details.
