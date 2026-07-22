package runtime

import "testing"

func TestParseProfile(t *testing.T) {
	cases := []struct {
		raw  string
		want Profile
		err  bool
	}{
		{raw: "", want: ProfileFull},
		{raw: "full", want: ProfileFull},
		{raw: "FULL", want: ProfileFull},
		{raw: "desktop-lite", want: ProfileDesktopLite},
		{raw: "bogus", err: true},
	}
	for _, tc := range cases {
		got, err := ParseProfile(tc.raw)
		if tc.err {
			if err == nil {
				t.Fatalf("ParseProfile(%q) error = nil", tc.raw)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("ParseProfile(%q) = (%q, %v), want %q", tc.raw, got, err, tc.want)
		}
	}
	if ProfileFull.RequiresVectorIndex() != true {
		t.Fatal("full must require vector index")
	}
	if ProfileDesktopLite.RequiresVectorIndex() != false {
		t.Fatal("desktop-lite must not require vector index")
	}
}

func TestProfileFromEnvDefaultFull(t *testing.T) {
	got, err := ProfileFromEnv(func(string) string { return "" })
	if err != nil || got != ProfileFull {
		t.Fatalf("ProfileFromEnv empty = (%q, %v)", got, err)
	}
}
