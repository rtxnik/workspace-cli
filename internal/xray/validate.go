package xray

import (
	"fmt"
	"regexp"
)

var profileNameRegex = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

var reservedProfileNames = map[string]bool{
	"config": true,
	"tmp":    true,
	"":       true,
}

// ValidateProfileName enforces D-12: name matches ^[a-z0-9_-]{1,32}$ and is not
// in {"config","tmp",""}. Different from internal/profile.ValidateName — do not
// import or share.
func ValidateProfileName(name string) error {
	if reservedProfileNames[name] {
		return fmt.Errorf("invalid profile name %q: reserved", name)
	}
	if !profileNameRegex.MatchString(name) {
		return fmt.Errorf("invalid profile name %q: must match [a-z0-9_-]{1,32}", name)
	}
	return nil
}
