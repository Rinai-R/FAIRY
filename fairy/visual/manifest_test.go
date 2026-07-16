package visual

import (
	"os"
	"strings"
	"testing"
)

func validManifest() string {
	return "{\n" +
		"  \"schemaVersion\": 2,\n" +
		"  \"packId\": \"fairy.atri\",\n" +
		"  \"displayName\": \"亚托莉\",\n" +
		"  \"renderer\": \"state_images\",\n" +
		"  \"frame\": {\"width\": 128, \"height\": 192},\n" +
		"  \"scale\": 1,\n" +
		"  \"anchor\": {\"x\": 64, \"y\": 190},\n" +
		"  \"states\": [\n" +
		"    {\"id\": \"idle\", \"description\": \"安静站立\", \"imagePath\": \"fairy-character://localhost/fairy.atri/images/idle.png\"},\n" +
		"    {\"id\": \"happy\", \"description\": \"开心回应\", \"imagePath\": \"fairy-character://localhost/fairy.atri/images/happy.png\"}\n" +
		"  ]\n" +
		"}"
}

func TestParseManifestAcceptsStateImagesV2(t *testing.T) {
	manifest, err := ParseManifest([]byte(validManifest()))
	if err != nil {
		t.Fatalf("ParseManifest() error = %v", err)
	}
	if manifest.PackID != "fairy.atri" {
		t.Fatalf("PackID = %q, want fairy.atri", manifest.PackID)
	}
	if _, ok := manifest.State("happy"); !ok {
		t.Fatal("State(happy) missing")
	}
}

func TestParseManifestRejectsInvalidManifests(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(string) string
		wantErr string
	}{
		{
			name: "wrong renderer",
			edit: func(source string) string {
				return strings.Replace(source, "\"renderer\": \"state_images\"", "\"renderer\": \"sprite_sheet\"", 1)
			},
			wantErr: "renderer",
		},
		{
			name: "missing idle",
			edit: func(source string) string {
				return strings.Replace(source, "\"id\": \"idle\"", "\"id\": \"neutral\"", 1)
			},
			wantErr: "idle",
		},
		{
			name: "duplicate state",
			edit: func(source string) string {
				return strings.Replace(source, "\"id\": \"happy\"", "\"id\": \"idle\"", 1)
			},
			wantErr: "duplicated",
		},
		{
			name: "duplicate image path",
			edit: func(source string) string {
				return strings.Replace(source, "images/happy.png", "images/idle.png", 1)
			},
			wantErr: "duplicated",
		},
		{
			name: "remote URL",
			edit: func(source string) string {
				return strings.Replace(source, "fairy-character://localhost/fairy.atri/images/happy.png", "https://example.com/happy.png", 1)
			},
			wantErr: "scheme",
		},
		{
			name: "query string",
			edit: func(source string) string {
				return strings.Replace(source, "images/happy.png", "images/happy.png?cache=1", 1)
			},
			wantErr: "query",
		},
		{
			name: "path traversal",
			edit: func(source string) string {
				return strings.Replace(source, "/fairy.atri/images/happy.png", "/fairy.atri/../secret.png", 1)
			},
			wantErr: "clean",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tt.edit(validManifest())))
			if err == nil {
				t.Fatal("ParseManifest() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParseManifest() error = %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateImageURI(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		wantErr bool
	}{
		{name: "valid", uri: "fairy-character://localhost/fairy.atri/images/sad.png"},
		{name: "wrong host", uri: "fairy-character://remote/fairy.atri/images/sad.png", wantErr: true},
		{name: "wrong pack", uri: "fairy-character://localhost/fairy.other/images/sad.png", wantErr: true},
		{name: "not png", uri: "fairy-character://localhost/fairy.atri/images/sad.webp", wantErr: true},
		{name: "fragment", uri: "fairy-character://localhost/fairy.atri/images/sad.png#x", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateImageURI("fairy.atri", tt.uri)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateImageURI() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadActiveManifestFromEnvironment(t *testing.T) {
	path := os.Getenv("FAIRY_ACTIVE_VISUAL_MANIFEST")
	if path == "" {
		t.Skip("FAIRY_ACTIVE_VISUAL_MANIFEST is not set")
	}

	manifest, err := LoadManifestFromFile(path)
	if err != nil {
		t.Fatalf("LoadManifestFromFile(%q) error = %v", path, err)
	}
	for _, stateID := range []string{"idle", "happy", "sad"} {
		if _, ok := manifest.State(stateID); !ok {
			t.Fatalf("active manifest missing %q state", stateID)
		}
	}
}
