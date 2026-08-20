#include "embed_api.h"

#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>

#define BIND(name)                                                 \
	do {                                                       \
		p_##name = (typeof(p_##name))dlsym(lib, #name);    \
		if (p_##name == NULL) {                            \
			return "missing symbol " #name;            \
		}                                                  \
	} while (0)

static void *lib;

static int (*p_seekdb_open)(const char *);
static void (*p_seekdb_close)(void);
static int (*p_seekdb_connect)(void **, const char *, bool);
static void (*p_seekdb_connect_close)(void *);
static int (*p_seekdb_query)(void *, const char *, void **);
static void *(*p_seekdb_store_result)(void *);
static void (*p_seekdb_result_free)(void *);
static unsigned int (*p_seekdb_field_count)(void *);
static unsigned int (*p_seekdb_num_fields)(void *);
static size_t (*p_seekdb_result_column_name_len)(void *, int32_t);
static int (*p_seekdb_result_column_name)(void *, int32_t, char *, size_t);
static void *(*p_seekdb_fetch_row)(void *);
static bool (*p_seekdb_row_is_null)(void *, int32_t);
static size_t (*p_seekdb_row_get_string_len)(void *, int32_t);
static int (*p_seekdb_row_get_string)(void *, int32_t, char *, size_t);
static unsigned long long (*p_seekdb_affected_rows)(void *);
static unsigned long long (*p_seekdb_insert_id)(void *);
static const char *(*p_seekdb_error)(void *);
static unsigned int (*p_seekdb_errno)(void *);
static int (*p_seekdb_begin)(void *);
static int (*p_seekdb_commit)(void *);
static int (*p_seekdb_rollback)(void *);
static int (*p_seekdb_autocommit)(void *, bool);
static int (*p_seekdb_ping)(void *);
static int (*p_seekdb_set_character_set)(void *, const char *);

const char *fairy_seekdb_load(const char *path)
{
	if (lib != NULL) {
		return NULL;
	}
	if (path == NULL || path[0] == '\0') {
		return "SeekDB library path is empty";
	}
	lib = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	if (lib == NULL) {
		return dlerror();
	}
	BIND(seekdb_open);
	BIND(seekdb_close);
	BIND(seekdb_connect);
	BIND(seekdb_connect_close);
	BIND(seekdb_query);
	BIND(seekdb_store_result);
	BIND(seekdb_result_free);
	BIND(seekdb_field_count);
	BIND(seekdb_num_fields);
	BIND(seekdb_result_column_name_len);
	BIND(seekdb_result_column_name);
	BIND(seekdb_fetch_row);
	BIND(seekdb_row_is_null);
	BIND(seekdb_row_get_string_len);
	BIND(seekdb_row_get_string);
	BIND(seekdb_affected_rows);
	BIND(seekdb_insert_id);
	BIND(seekdb_error);
	BIND(seekdb_errno);
	BIND(seekdb_begin);
	BIND(seekdb_commit);
	BIND(seekdb_rollback);
	BIND(seekdb_autocommit);
	BIND(seekdb_ping);
	BIND(seekdb_set_character_set);
	return NULL;
}

int fairy_seekdb_open(const char *db_dir) { return p_seekdb_open(db_dir); }
void fairy_seekdb_close(void) { p_seekdb_close(); }
int fairy_seekdb_connect(FairySeekdbHandle *handle, const char *database, bool autocommit)
{
	return p_seekdb_connect((void **)handle, database, autocommit);
}
void fairy_seekdb_connect_close(FairySeekdbHandle handle) { p_seekdb_connect_close(handle); }
int fairy_seekdb_query(FairySeekdbHandle handle, const char *query, FairySeekdbResult *result)
{
	return p_seekdb_query(handle, query, (void **)result);
}
FairySeekdbResult fairy_seekdb_store_result(FairySeekdbHandle handle) { return p_seekdb_store_result(handle); }
void fairy_seekdb_result_free(FairySeekdbResult result)
{
	if (result != NULL) {
		p_seekdb_result_free(result);
	}
}
unsigned int fairy_seekdb_field_count(FairySeekdbHandle handle) { return p_seekdb_field_count(handle); }
unsigned int fairy_seekdb_num_fields(FairySeekdbResult result) { return p_seekdb_num_fields(result); }
size_t fairy_seekdb_result_column_name_len(FairySeekdbResult result, int32_t column_index)
{
	return p_seekdb_result_column_name_len(result, column_index);
}
int fairy_seekdb_result_column_name(FairySeekdbResult result, int32_t column_index, char *name, size_t name_len)
{
	return p_seekdb_result_column_name(result, column_index, name, name_len);
}
FairySeekdbRow fairy_seekdb_fetch_row(FairySeekdbResult result) { return p_seekdb_fetch_row(result); }
bool fairy_seekdb_row_is_null(FairySeekdbRow row, int32_t column_index) { return p_seekdb_row_is_null(row, column_index); }
size_t fairy_seekdb_row_get_string_len(FairySeekdbRow row, int32_t column_index)
{
	return p_seekdb_row_get_string_len(row, column_index);
}
int fairy_seekdb_row_get_string(FairySeekdbRow row, int32_t column_index, char *value, size_t value_len)
{
	return p_seekdb_row_get_string(row, column_index, value, value_len);
}
unsigned long long fairy_seekdb_affected_rows(FairySeekdbHandle handle) { return p_seekdb_affected_rows(handle); }
unsigned long long fairy_seekdb_insert_id(FairySeekdbHandle handle) { return p_seekdb_insert_id(handle); }
const char *fairy_seekdb_error(FairySeekdbHandle handle) { return p_seekdb_error(handle); }
unsigned int fairy_seekdb_errno(FairySeekdbHandle handle) { return p_seekdb_errno(handle); }
int fairy_seekdb_begin(FairySeekdbHandle handle) { return p_seekdb_begin(handle); }
int fairy_seekdb_commit(FairySeekdbHandle handle) { return p_seekdb_commit(handle); }
int fairy_seekdb_rollback(FairySeekdbHandle handle) { return p_seekdb_rollback(handle); }
int fairy_seekdb_autocommit(FairySeekdbHandle handle, bool mode) { return p_seekdb_autocommit(handle, mode); }
int fairy_seekdb_ping(FairySeekdbHandle handle) { return p_seekdb_ping(handle); }
int fairy_seekdb_set_character_set(FairySeekdbHandle handle, const char *name)
{
	return p_seekdb_set_character_set(handle, name);
}
