package character

import (
	"fmt"
	"testing"
)

func BenchmarkStoreCharacterRead(b *testing.B) {
	for _, characterCount := range []int{1, 128} {
		b.Run(fmt.Sprintf("List/characters=%d", characterCount), func(b *testing.B) {
			root := b.TempDir()
			writeCharacterLibrary(b, root, characterCount)
			store := NewStore(root)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				catalog, err := store.List()
				if err != nil {
					b.Fatal(err)
				}
				if len(catalog.Characters) != characterCount {
					b.Fatalf("List returned %d characters, want %d", len(catalog.Characters), characterCount)
				}
			}
		})
		b.Run(fmt.Sprintf("Lookup/characters=%d", characterCount), func(b *testing.B) {
			root := b.TempDir()
			targetID := writeCharacterLibrary(b, root, characterCount)
			store := NewStore(root)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				record, found, err := store.Lookup(targetID)
				if err != nil {
					b.Fatal(err)
				}
				if !found || record.CharacterID != targetID {
					b.Fatalf("Lookup returned (%q, %v), want (%q, true)", record.CharacterID, found, targetID)
				}
			}
		})
	}
}
