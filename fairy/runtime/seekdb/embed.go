//go:build cgo && (darwin || linux)

package seekdb

/*
#cgo LDFLAGS: -ldl
#include "embed_api.h"
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"unsafe"
)

var embedLive struct {
	mu      sync.Mutex
	dataDir string
}

func loadSeekDBLibrary(path string) error {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	if msg := C.fairy_seekdb_load(cPath); msg != nil {
		return fmt.Errorf("load SeekDB library: %s", C.GoString(msg))
	}
	return nil
}

func engineOpen(dataDir string) error {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolve SeekDB data directory: %w", err)
	}
	embedLive.mu.Lock()
	defer embedLive.mu.Unlock()
	if embedLive.dataDir != "" {
		if filepath.Clean(embedLive.dataDir) == filepath.Clean(abs) {
			return nil
		}
		return errors.New("SeekDB embed engine is already open")
	}
	cDir := C.CString(abs)
	defer C.free(unsafe.Pointer(cDir))
	if rc := C.fairy_seekdb_open(cDir); rc != 0 {
		return fmt.Errorf("seekdb_open: %s", lastEngineError(nil))
	}
	embedLive.dataDir = abs
	return nil
}

func engineClose() {
	// seekdb_close installs SIGABRT/SIGSEGV handlers that _exit(0) on OceanBase
	// abort during observer teardown. Keep the observer until process exit.
}

type engineHandle struct {
	ptr C.FairySeekdbHandle
}

func engineConnect(database string, autocommit bool) (*engineHandle, error) {
	cDatabase := C.CString(database)
	defer C.free(unsafe.Pointer(cDatabase))
	var handle C.FairySeekdbHandle
	if rc := C.fairy_seekdb_connect(&handle, cDatabase, C.bool(autocommit)); rc != 0 {
		return nil, fmt.Errorf("seekdb_connect: %s", lastEngineError(nil))
	}
	connected := &engineHandle{ptr: handle}
	cCharset := C.CString("utf8mb4")
	defer C.free(unsafe.Pointer(cCharset))
	if rc := C.fairy_seekdb_set_character_set(handle, cCharset); rc != 0 {
		err := fmt.Errorf("seekdb_set_character_set: %s", lastEngineError(connected))
		connected.Close()
		return nil, err
	}
	return connected, nil
}

func (h *engineHandle) Close() {
	if h == nil || h.ptr == nil {
		return
	}
	C.fairy_seekdb_connect_close(h.ptr)
	h.ptr = nil
}

type resultHandle = C.FairySeekdbResult
type rowHandle = C.FairySeekdbRow

func (h *engineHandle) Query(sqlText string) (resultHandle, error) {
	cSQL := C.CString(sqlText)
	defer C.free(unsafe.Pointer(cSQL))
	var directResult C.FairySeekdbResult
	if rc := C.fairy_seekdb_query(h.ptr, cSQL, &directResult); rc != 0 {
		return nil, h.sqlError()
	}
	fieldCount := C.fairy_seekdb_field_count(h.ptr)
	// seekdb_query exposes the result twice: through its out parameter and
	// through seekdb_store_result. The latter transfers ownership away from
	// the connection and is the compatibility path used by the upstream C
	// bindings. Always consume it before another query can replace the
	// connection-owned result. Both handles normally alias the same object.
	result := C.fairy_seekdb_store_result(h.ptr)
	if result == nil {
		result = directResult
	}
	if fieldCount == 0 {
		if result != nil {
			C.fairy_seekdb_result_free(result)
		}
		return nil, nil
	}
	if result == nil {
		return nil, h.sqlError()
	}
	return result, nil
}

func (h *engineHandle) Affected() int64 {
	return int64(C.fairy_seekdb_affected_rows(h.ptr))
}

func (h *engineHandle) InsertID() int64 {
	return int64(C.fairy_seekdb_insert_id(h.ptr))
}

func (h *engineHandle) Begin() error {
	if C.fairy_seekdb_begin(h.ptr) != 0 {
		return h.sqlError()
	}
	return nil
}

func (h *engineHandle) Commit() error {
	if C.fairy_seekdb_commit(h.ptr) != 0 {
		return h.sqlError()
	}
	return nil
}

func (h *engineHandle) Rollback() error {
	if C.fairy_seekdb_rollback(h.ptr) != 0 {
		return h.sqlError()
	}
	return nil
}

func (h *engineHandle) Ping() error {
	if C.fairy_seekdb_ping(h.ptr) != 0 {
		return h.sqlError()
	}
	return nil
}

func (h *engineHandle) sqlError() error {
	return engineSQLError(h)
}

func lastEngineError(h *engineHandle) string {
	if h != nil && h.ptr != nil {
		if msg := C.fairy_seekdb_error(h.ptr); msg != nil {
			if text := C.GoString(msg); text != "" {
				return text
			}
		}
	}
	return "SeekDB engine error"
}

func engineSQLError(h *engineHandle) error {
	message := lastEngineError(h)
	var number uint16
	if h != nil && h.ptr != nil {
		number = uint16(C.fairy_seekdb_errno(h.ptr))
	}
	return &Error{Number: classifyEngineError(number, message), Message: message}
}

func resultColumnCount(result resultHandle) int {
	return int(C.fairy_seekdb_num_fields(result))
}

func resultColumnNames(result resultHandle) []string {
	count := resultColumnCount(result)
	names := make([]string, count)
	for i := range names {
		name, err := resultColumnName(result, i)
		if err != nil {
			names[i] = ""
			continue
		}
		names[i] = name
	}
	return names
}

func resultColumnName(result resultHandle, index int) (string, error) {
	length := C.fairy_seekdb_result_column_name_len(result, C.int32_t(index))
	if length == C.size_t(^C.size_t(0)) {
		return "", errors.New("SeekDB column name length is invalid")
	}
	buf := make([]byte, int(length)+1)
	if rc := C.fairy_seekdb_result_column_name(result, C.int32_t(index), (*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf))); rc != 0 {
		return "", errors.New("SeekDB column name read failed")
	}
	return string(buf[:length]), nil
}

func resultFree(result resultHandle) {
	C.fairy_seekdb_result_free(result)
}

func resultFetch(result resultHandle) (rowHandle, bool) {
	row := C.fairy_seekdb_fetch_row(result)
	if row == nil {
		return nil, false
	}
	return row, true
}

func rowValue(row rowHandle, index int) ([]byte, bool, error) {
	if bool(C.fairy_seekdb_row_is_null(row, C.int32_t(index))) {
		return nil, true, nil
	}
	length := C.fairy_seekdb_row_get_string_len(row, C.int32_t(index))
	if length == C.size_t(^C.size_t(0)) {
		return nil, false, errors.New("SeekDB row length is invalid")
	}
	buf := make([]byte, int(length)+1)
	if rc := C.fairy_seekdb_row_get_string(row, C.int32_t(index), (*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf))); rc != 0 {
		return nil, false, errors.New("SeekDB row read failed")
	}
	return buf[:length], false, nil
}
