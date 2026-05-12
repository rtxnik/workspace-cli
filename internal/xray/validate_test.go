package xray

import (
	"strings"
	"testing"
)

func TestProfileNameValidate(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		// 4 valid names — must return nil error.
		{name: "valid_primary", input: "primary", wantErr: ""},
		{name: "valid_backup", input: "backup", wantErr: ""},
		{name: "valid_backup_2", input: "backup_2", wantErr: ""},
		{name: "valid_work-test", input: "work-test", wantErr: ""},

		// 3 reserved names — must contain "reserved".
		{name: "reserved_empty", input: "", wantErr: "reserved"},
		{name: "reserved_config", input: "config", wantErr: "reserved"},
		{name: "reserved_tmp", input: "tmp", wantErr: "reserved"},

		// 6 regex-fail names — must contain "must match".
		{name: "regex_capital", input: "Primary", wantErr: "must match"},
		{name: "regex_33_chars", input: strings.Repeat("a", 33), wantErr: "must match"},
		{name: "regex_slash", input: "a/b", wantErr: "must match"},
		{name: "regex_traversal", input: "../etc", wantErr: "must match"},
		{name: "regex_space", input: "foo bar", wantErr: "must match"},
		{name: "regex_dot", input: "foo.bar", wantErr: "must match"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProfileName(tc.input)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateProfileName(%q) returned %v; want nil", tc.input, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateProfileName(%q) returned nil; want error containing %q", tc.input, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateProfileName(%q) returned %q; want substring %q", tc.input, err.Error(), tc.wantErr)
			}
		})
	}
}
