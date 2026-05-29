#include "bridge.h"
#include <time.h>

static MdbColumn *cmdb_query_column(MdbSQL *sql, unsigned int idx) {
	if (!sql || !sql->cur_table || idx >= sql->num_columns) {
		return NULL;
	}
	MdbSQLColumn *sqlcol = g_ptr_array_index(sql->columns, idx);
	if (!sqlcol || !sqlcol->name || !sql->cur_table->columns) {
		return NULL;
	}
	for (unsigned int i = 0; i < sql->cur_table->num_cols; i++) {
		MdbColumn *col = g_ptr_array_index(sql->cur_table->columns, i);
		if (col && !g_ascii_strcasecmp(col->name, sqlcol->name)) {
			return col;
		}
	}
	return NULL;
}

MdbSQL *cmdb_query_open(const char *path, const char *query) {
	MdbSQL *sql = mdb_sql_init();
	if (!sql) {
		return NULL;
	}
	if (!mdb_sql_open(sql, (char *)path)) {
		return sql;
	}
	mdb_sql_run_query(sql, query);
	return sql;
}

void cmdb_query_close(MdbSQL *sql) {
	if (sql) {
		mdb_sql_exit(sql);
	}
}

int cmdb_query_has_error(MdbSQL *sql) {
	return sql && mdb_sql_has_error(sql);
}

const char *cmdb_query_error(MdbSQL *sql) {
	if (!sql) {
		return "null query handle";
	}
	return mdb_sql_last_error(sql);
}

unsigned int cmdb_query_column_count(MdbSQL *sql) {
	return sql ? sql->num_columns : 0;
}

const char *cmdb_query_column_name(MdbSQL *sql, unsigned int idx) {
	if (!sql || idx >= sql->num_columns) {
		return "";
	}
	MdbSQLColumn *col = g_ptr_array_index(sql->columns, idx);
	return col && col->name ? col->name : "";
}

int cmdb_query_column_type(MdbSQL *sql, unsigned int idx) {
	MdbColumn *col = cmdb_query_column(sql, idx);
	return col ? col->col_type : 0;
}

int cmdb_query_column_size(MdbSQL *sql, unsigned int idx) {
	MdbColumn *col = cmdb_query_column(sql, idx);
	return col ? col->col_size : 0;
}

const char *cmdb_query_column_database_type(MdbSQL *sql, unsigned int idx) {
	MdbColumn *col = cmdb_query_column(sql, idx);
	if (!col) {
		return "";
	}
	const char *name = mdb_get_colbacktype_string(col);
	return name ? name : "";
}

int cmdb_query_column_is_null(MdbSQL *sql, unsigned int idx) {
	MdbColumn *col = cmdb_query_column(sql, idx);
	return col ? col->cur_value_is_null : 0;
}

const char *cmdb_query_value(MdbSQL *sql, unsigned int idx) {
	if (!sql || idx >= sql->bound_values->len) {
		return "";
	}
	const char *value = g_ptr_array_index(sql->bound_values, idx);
	return value ? value : "";
}

const void *cmdb_query_binary_value(MdbSQL *sql, unsigned int idx, int *size) {
	MdbColumn *col = cmdb_query_column(sql, idx);
	if (!col || col->cur_value_is_null || !sql->cur_table || !sql->cur_table->entry) {
		if (size) {
			*size = 0;
		}
		return NULL;
	}
	if (col->col_type != MDB_BINARY || col->cur_value_len <= 0) {
		if (size) {
			*size = 0;
		}
		return NULL;
	}
	if (size) {
		*size = col->cur_value_len;
	}
	return sql->cur_table->entry->mdb->pg_buf + col->cur_value_start;
}

int cmdb_query_datetime_value(MdbSQL *sql, unsigned int idx, int *year, int *month, int *day, int *hour, int *minute, int *second) {
	MdbColumn *col = cmdb_query_column(sql, idx);
	if (!col || col->cur_value_is_null || col->col_type != MDB_DATETIME || !sql->cur_table || !sql->cur_table->entry) {
		return 0;
	}
	struct tm tm = {0};
	double value = mdb_get_double(sql->cur_table->entry->mdb->pg_buf, col->cur_value_start);
	mdb_date_to_tm(value, &tm);
	if (year) {
		*year = tm.tm_year + 1900;
	}
	if (month) {
		*month = tm.tm_mon + 1;
	}
	if (day) {
		*day = tm.tm_mday;
	}
	if (hour) {
		*hour = tm.tm_hour;
	}
	if (minute) {
		*minute = tm.tm_min;
	}
	if (second) {
		*second = tm.tm_sec;
	}
	return 1;
}

int cmdb_query_next(MdbSQL *sql) {
	if (!sql || !sql->cur_table) {
		return -1;
	}
	return mdb_sql_fetch_row(sql, sql->cur_table);
}
