#ifndef MDBPRIVATE_H
#define MDBPRIVATE_H

#include "mdbtools.h"

#ifdef __cplusplus
extern "C" {
#endif

void mdbi_rc4(unsigned char *key, guint32 key_len, unsigned char *buf, guint32 buf_len);
MdbBackend *mdbi_register_backend2(MdbHandle *mdb, char *backend_name, guint32 capabilities,
	const MdbBackendType *backend_type,
	const MdbBackendType *type_shortdate,
	const MdbBackendType *type_autonum,
	const char *short_now, const char *long_now,
	const char *date_fmt, const char *shortdate_fmt,
	const char *charset_statement, const char *create_table_statement,
	const char *drop_statement, const char *constaint_not_empty_statement,
	const char *column_comment_statement, const char *per_column_comment_statement,
	const char *table_comment_statement, const char *per_table_comment_statement,
	gchar *(*quote_schema_name)(const gchar *, const gchar *),
	gchar *(*normalise_case)(const gchar *));

#ifdef __cplusplus
}
#endif

#endif

