package workspace

import "testing"

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "my-app", false},
		{"valid short", "ab", false},
		{"valid numeric", "app1", false},
		{"valid all digits", "12", false},
		{"too short", "a", true},
		{"too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
		{"uppercase", "MyApp", true},
		{"spaces", "my app", true},
		{"special chars", "my_app!", true},
		{"leading dash", "-app", true},
		{"trailing dash", "app-", true},
		{"underscore", "my_app", true},
		{"64 chars valid", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
