package compaction

import (
	"errors"
	"math"
	"testing"
)

func TestNewStoreRejectsMissingPool(t *testing.T) {
	store, err := NewStoreFromPool(nil)
	if store != nil || !errors.Is(err, ErrDatabasePoolEmpty) {
		t.Fatalf("NewStoreFromPool(nil) = (%v, %v), want nil, %v", store, err, ErrDatabasePoolEmpty)
	}
}

func TestNextProjectionRevisionsRejectsDatabaseIntegerOverflow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		expectedWindow     int64
		expectedProjection int64
		wantWindow         int64
		wantProjection     int64
		wantError          bool
	}{
		{
			name:               "increments both revisions",
			expectedWindow:     3,
			expectedProjection: 5,
			wantWindow:         4,
			wantProjection:     6,
		},
		{name: "missing window revision", expectedProjection: 1, wantError: true},
		{name: "missing projection revision", expectedWindow: 1, wantError: true},
		{
			name:               "window revision overflow",
			expectedWindow:     math.MaxInt64,
			expectedProjection: 1,
			wantError:          true,
		},
		{
			name:               "projection revision overflow",
			expectedWindow:     1,
			expectedProjection: math.MaxInt64,
			wantError:          true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			window, projection, err := nextProjectionRevisions(test.expectedWindow, test.expectedProjection)
			if test.wantError {
				if err == nil {
					t.Fatalf("nextProjectionRevisions(%d, %d) error = nil", test.expectedWindow, test.expectedProjection)
				}
				return
			}
			if err != nil || window != test.wantWindow || projection != test.wantProjection {
				t.Fatalf(
					"nextProjectionRevisions(%d, %d) = (%d, %d, %v), want (%d, %d, nil)",
					test.expectedWindow, test.expectedProjection, window, projection, err,
					test.wantWindow, test.wantProjection,
				)
			}
		})
	}
}
