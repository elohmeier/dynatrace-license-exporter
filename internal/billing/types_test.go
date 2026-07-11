package billing

import (
	"encoding/json"
	"testing"
)

func TestIdentifierUnmarshalJSON(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  Identifier
	}{
		{`"HOST-EXAMPLE"`, "HOST-EXAMPLE"},
		{`12345`, "12345"},
		{`null`, ""},
	} {
		var got Identifier
		if err := json.Unmarshal([]byte(tt.input), &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", tt.input, err)
		}
		if got != tt.want {
			t.Fatalf("Unmarshal(%s) = %q, want %q", tt.input, got, tt.want)
		}
	}
	var got Identifier
	if err := json.Unmarshal([]byte(`{}`), &got); err == nil {
		t.Fatal("object identifier unexpectedly succeeded")
	}
}
