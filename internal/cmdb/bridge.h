#ifndef CMDB_BRIDGE_H
#define CMDB_BRIDGE_H

#include "mdbsql.h"

MdbSQL *cmdb_query_open(const char *path, const char *query);
void cmdb_query_close(MdbSQL *sql);
int cmdb_query_has_error(MdbSQL *sql);
const char *cmdb_query_error(MdbSQL *sql);
unsigned int cmdb_query_column_count(MdbSQL *sql);
const char *cmdb_query_column_name(MdbSQL *sql, unsigned int idx);
int cmdb_query_column_type(MdbSQL *sql, unsigned int idx);
int cmdb_query_column_size(MdbSQL *sql, unsigned int idx);
const char *cmdb_query_column_database_type(MdbSQL *sql, unsigned int idx);
int cmdb_query_column_is_null(MdbSQL *sql, unsigned int idx);
const char *cmdb_query_value(MdbSQL *sql, unsigned int idx);
const void *cmdb_query_binary_value(MdbSQL *sql, unsigned int idx, int *size);
int cmdb_query_datetime_value(MdbSQL *sql, unsigned int idx, int *year, int *month, int *day, int *hour, int *minute, int *second);
int cmdb_query_next(MdbSQL *sql);

#endif
