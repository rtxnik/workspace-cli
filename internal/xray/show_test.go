package xray

import "testing"

func TestMaskUUID(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", "****"},
		{"too short", "abc", "****"},
		{"not a uuid", "not-a-uuid", "****"},
		{"35 chars", "12345678-1234-1234-1234-12345678901", "****"},
		{"37 chars", "12345678-1234-1234-1234-1234567890123", "****"},
		{"valid lowercase", "12345678-1234-1234-1234-123456789012", "12345678-****-****-****-************"},
		{"valid hex first group", "abcdef01-1234-1234-1234-123456789012", "abcdef01-****-****-****-************"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MaskUUID(tc.in); got != tc.want {
				t.Errorf("MaskUUID(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMaskShort(t *testing.T) {
	if got := MaskShort(""); got != "" {
		t.Errorf("MaskShort(\"\") = %q; want \"\"", got)
	}
	if got := MaskShort("secret"); got != "****" {
		t.Errorf("MaskShort(\"secret\") = %q; want \"****\"", got)
	}
	if got := MaskShort("x"); got != "****" {
		t.Errorf("MaskShort(\"x\") = %q; want \"****\"", got)
	}
}
