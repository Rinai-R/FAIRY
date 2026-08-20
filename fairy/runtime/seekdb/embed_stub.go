//go:build !cgo || windows

package seekdb

import "errors"

var errEmbeddedEngineUnavailable = errors.New("SeekDB embedded engine requires cgo on darwin or linux")

type resultHandle *byte
type rowHandle *byte

func loadSeekDBLibrary(string) error { return errEmbeddedEngineUnavailable }
func engineOpen(string) error        { return errEmbeddedEngineUnavailable }
func engineClose()                   {}

type engineHandle struct{}

func engineConnect(string, bool) (*engineHandle, error) {
	return nil, errEmbeddedEngineUnavailable
}

func (h *engineHandle) Close() {}
func (h *engineHandle) Query(string) (resultHandle, error) {
	return nil, errEmbeddedEngineUnavailable
}
func (h *engineHandle) Affected() int64 { return 0 }
func (h *engineHandle) InsertID() int64 { return 0 }
func (h *engineHandle) Begin() error    { return errEmbeddedEngineUnavailable }
func (h *engineHandle) Commit() error   { return errEmbeddedEngineUnavailable }
func (h *engineHandle) Rollback() error { return errEmbeddedEngineUnavailable }
func (h *engineHandle) Ping() error     { return errEmbeddedEngineUnavailable }

func resultColumnCount(resultHandle) int { return 0 }
func resultColumnNames(resultHandle) []string {
	return nil
}
func resultFree(resultHandle)            {}
func resultFetch(resultHandle) (rowHandle, bool) {
	return nil, false
}
func rowValue(rowHandle, int) ([]byte, bool, error) {
	return nil, false, errEmbeddedEngineUnavailable
}
