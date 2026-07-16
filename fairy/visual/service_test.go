package visual

import "testing"

func TestVisualServiceListVisualPacksMissingRoot(t *testing.T) {
	catalog, err := NewVisualService(t.TempDir()).ListVisualPacks()
	if err != nil {
		t.Fatalf("ListVisualPacks() error = %v", err)
	}
	if len(catalog.VisualPacks) != 0 {
		t.Fatalf("catalog = %#v", catalog)
	}
}
