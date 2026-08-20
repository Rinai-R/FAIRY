package seekdb

import "testing"

func TestClassifyEngineErrorMapsOceanBaseTableMissing(t *testing.T) {
	if got := classifyEngineError(0, "Table doesn't exist (ret=-5019)"); got != 1146 {
		t.Fatalf("classifyEngineError() = %d", got)
	}
	if got := classifyEngineError(1062, "duplicate key"); got != 1062 {
		t.Fatalf("preserve duplicate errno = %d", got)
	}
}
