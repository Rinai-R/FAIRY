#ifndef FAIRY_SEEKDB_EMBED_API_H
#define FAIRY_SEEKDB_EMBED_API_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef void *FairySeekdbHandle;
typedef void *FairySeekdbResult;
typedef void *FairySeekdbRow;

const char *fairy_seekdb_load(const char *path);
int fairy_seekdb_open(const char *db_dir);
void fairy_seekdb_close(void);
int fairy_seekdb_connect(FairySeekdbHandle *handle, const char *database, bool autocommit);
void fairy_seekdb_connect_close(FairySeekdbHandle handle);
int fairy_seekdb_query(FairySeekdbHandle handle, const char *query, FairySeekdbResult *result);
FairySeekdbResult fairy_seekdb_store_result(FairySeekdbHandle handle);
void fairy_seekdb_result_free(FairySeekdbResult result);
unsigned int fairy_seekdb_field_count(FairySeekdbHandle handle);
unsigned int fairy_seekdb_num_fields(FairySeekdbResult result);
size_t fairy_seekdb_result_column_name_len(FairySeekdbResult result, int32_t column_index);
int fairy_seekdb_result_column_name(FairySeekdbResult result, int32_t column_index, char *name, size_t name_len);
FairySeekdbRow fairy_seekdb_fetch_row(FairySeekdbResult result);
bool fairy_seekdb_row_is_null(FairySeekdbRow row, int32_t column_index);
size_t fairy_seekdb_row_get_string_len(FairySeekdbRow row, int32_t column_index);
int fairy_seekdb_row_get_string(FairySeekdbRow row, int32_t column_index, char *value, size_t value_len);
unsigned long long fairy_seekdb_affected_rows(FairySeekdbHandle handle);
unsigned long long fairy_seekdb_insert_id(FairySeekdbHandle handle);
const char *fairy_seekdb_error(FairySeekdbHandle handle);
unsigned int fairy_seekdb_errno(FairySeekdbHandle handle);
int fairy_seekdb_begin(FairySeekdbHandle handle);
int fairy_seekdb_commit(FairySeekdbHandle handle);
int fairy_seekdb_rollback(FairySeekdbHandle handle);
int fairy_seekdb_autocommit(FairySeekdbHandle handle, bool mode);
int fairy_seekdb_ping(FairySeekdbHandle handle);
int fairy_seekdb_set_character_set(FairySeekdbHandle handle, const char *name);

#ifdef __cplusplus
}
#endif

#endif
