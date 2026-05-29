# Third-Party Licenses

This project includes third-party software with the following licenses:

## mdbtools

The `internal/mdbtools/` directory contains the
[mdbtools](https://github.com/mdbtools/mdbtools) library as a git submodule.

- **License:** GNU Library General Public License v2 or later (LGPL-2.0-or-later)
- **Copyright:** Copyright (C) 2000 Brian Bruns and contributors
- **Full license text:** See `internal/mdbtools/COPYING.LIB`

The mdbtools libraries (libmdb, libmdbsql) are used under the terms of the
LGPL-2.0-or-later. This permits linking from software under any license,
including proprietary code, provided the LGPL terms are satisfied.

### What is covered by LGPL

All C source files under `internal/mdbtools/src/` and their headers under
`internal/mdbtools/include/` are part of mdbtools and licensed under LGPL-2.0+.

The stub `.c` files in `internal/cmdb/` (e.g. `libmdb_file.c`,
`libmdbsql.c`) that `#include` mdbtools sources are also subject to LGPL-2.0+.

The pre-generated parser (`internal/cmdb/parser.c`) and lexer
(`internal/cmdb/lexer.c`) are derived from mdbtools source and carry the
original mdbtools copyright headers.
