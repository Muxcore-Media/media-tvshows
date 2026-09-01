package internal

import "testing"

func TestClientReachableHTTPHost(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{":9450", "127.0.0.1:9450"},
		{"0.0.0.0:9450", "127.0.0.1:9450"},
		{"127.0.0.1:9450", "127.0.0.1:9450"},
		{"", "127.0.0.1"},
	}
	for _, tc := range tests {
		if got := clientReachableHTTPHost(tc.in); got != tc.want {
			t.Errorf("clientReachableHTTPHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestArtworkURLUsesReachableHost(t *testing.T) {
	got := artworkURL(":9450", "tv_1/poster.jpg")
	want := "http://127.0.0.1:9450/images/tv_1/poster.jpg"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
