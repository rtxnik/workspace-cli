package profile

import "testing"

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "my-profile", false},
		{"valid short", "ab", false},
		{"too short", "a", true},
		{"uppercase", "MyProfile", true},
		{"leading dash", "-bad", true},
		{"trailing dash", "bad-", true},
		{"reserved proxy", "proxy", true},
		{"reserved shared", "shared", true},
		{"not reserved", "proxyserver", false},
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
