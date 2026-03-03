package workspace

import (
	"fmt"
	"regexp"
)

var nameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// ValidateName checks that a workspace name uses only lowercase alphanumeric
// characters and dashes, is between 2 and 64 characters, and does not start
// or end with a dash.
func ValidateName(name string) error {
	n := len(name)
	if n < 2 || n > 64 {
		return fmt.Errorf("invalid workspace name %q: must be 2-64 characters", name)
	}
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("invalid workspace name %q: use lowercase alphanumeric and dashes, no leading/trailing dash", name)
	}
	return nil
}
