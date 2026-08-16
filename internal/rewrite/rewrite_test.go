package rewrite

import "testing"

func TestApply(t *testing.T) {
	mappings := []Mapping{
		{From: "/tv/", To: "/media/tv/"},
		{From: "/anime/", To: "/media/anime/"},
	}

	got, err := Apply(mappings, "/tv/Breaking Bad/Season 05/S05E01.mkv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/media/tv/Breaking Bad/Season 05/S05E01.mkv"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestApplyUnmatchedFailsSafely(t *testing.T) {
	mappings := []Mapping{{From: "/tv/", To: "/media/tv/"}}

	_, err := Apply(mappings, "/movies/Inception (2010)/Inception.mkv")
	if err == nil {
		t.Fatal("expected error for unmapped path, got nil")
	}
}

func TestApplyNoMappingsPassesThrough(t *testing.T) {
	// No mappings configured at all means source and target paths are
	// identical for this instance - not an error, not a "no match" case.
	got, err := Apply(nil, "/media/tv/Breaking Bad/Season 05/S05E01.mkv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/media/tv/Breaking Bad/Season 05/S05E01.mkv"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestScanPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"tv episode", "/media/tv/Breaking Bad/Season 05/S05E01.mkv", "/media/tv/Breaking Bad/Season 05"},
		{"movie", "/media/movies/Inception (2010)/Inception.mkv", "/media/movies/Inception (2010)"},
		{"root-level file", "/file.mkv", "/file.mkv"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScanPath(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
