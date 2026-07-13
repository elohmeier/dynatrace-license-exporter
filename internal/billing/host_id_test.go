package billing

import "testing"

func TestCanonicalHostID(t *testing.T) {
	tests := []struct {
		name    string
		input   Identifier
		want    string
		wantErr bool
	}{
		{name: "zero", input: "0", want: "HOST-0000000000000000"},
		{name: "positive", input: "42", want: "HOST-000000000000002A"},
		{name: "negative", input: "-42", want: "HOST-FFFFFFFFFFFFFFD6"},
		{name: "maximum signed", input: "9223372036854775807", want: "HOST-7FFFFFFFFFFFFFFF"},
		{name: "minimum signed", input: "-9223372036854775808", want: "HOST-8000000000000000"},
		{name: "canonical uppercase", input: "HOST-000000000000002A", want: "HOST-000000000000002A"},
		{name: "canonical lowercase", input: "host-abcdef0123456789", want: "HOST-ABCDEF0123456789"},
		{name: "empty", input: "", wantErr: true},
		{name: "malformed", input: "HOST-SYNTHETIC", wantErr: true},
		{name: "unsigned overflow", input: "18446744073709551615", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalHostID(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("CanonicalHostID(%q) unexpectedly succeeded with %q", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CanonicalHostID(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("CanonicalHostID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
