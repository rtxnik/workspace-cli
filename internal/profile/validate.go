package profile

import (
	"fmt"
	"regexp"
)

var nameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

var reservedNames = map[string]bool{
	"proxy":  true,
	"shared": true,
}

// ValidateName checks that a profile name uses only lowercase alphanumeric
// characters and dashes, is between 2 and 64 characters, does not start or
// end with a dash, and is not a reserved name.
func ValidateName(name string) error {
	n := len(name)
	if n < 2 || n > 64 {
		return fmt.Errorf("invalid profile name %q: must be 2-64 characters", name)
	}
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("invalid profile name %q: use lowercase alphanumeric and dashes, no leading/trailing dash", name)
	}
	if reservedNames[name] {
		return fmt.Errorf("invalid profile name %q: reserved name", name)
	}
	return nil
}
