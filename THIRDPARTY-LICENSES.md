# Third-Party Licenses

This project includes third-party software with the following licenses:

## mdbtools

The `internal/cmdb/mdbtools/` directory contains an in-tree copy of the
[mdbtools](https://github.com/mdbtools/mdbtools) library source.

- **License:** GNU Library General Public License v2 or later (LGPL-2.0-or-later)
- **Copyright:** Copyright (C) 2000 Brian Bruns and contributors
- **Full license text:** See `internal/cmdb/mdbtools/COPYING.LIB`

The mdbtools libraries (libmdb, libmdbsql) are used under the terms of the
LGPL-2.0-or-later. This permits linking from software under any license,
including proprietary code, provided the LGPL terms are satisfied.

### What is covered by LGPL

The original mdbtools C source files under `internal/cmdb/mdbtools/src/` and
their headers under `internal/cmdb/mdbtools/include/` are part of mdbtools
and licensed under LGPL-2.0-or-later.

The stub `.c` files in `internal/cmdb/` (e.g. `libmdb_file.c`,
`libmdbsql.c`) that `#include` the mdbtools sources are also subject to
LGPL-2.0-or-later.

The pre-generated parser (`internal/cmdb/parser.c`) and lexer
(`internal/cmdb/lexer.c`) are derived from mdbtools source (`parser.y`,
`lexer.l`) and carry the original mdbtools copyright headers.

### Files included from mdbtools

Only the C library and SQL engine sources are included:

- `src/libmdb/` — libmdb (file I/O, catalog, data, table, column, index, sargs, encoding, etc.)
- `src/sql/mdbsql.c` — SQL engine
- `include/` — public and private API headers

Unrelated files not used by this project (ODBC driver, autotools build system,
parser generator inputs, template headers) have been removed.
