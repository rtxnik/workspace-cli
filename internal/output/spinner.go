package output

import (
	"context"
	"fmt"

	"github.com/charmbracelet/huh/spinner"
)

// RunWithSpinner runs fn while showing an animated spinner with the given title.
// On success it prints "✓ title" in green; on error "✗ title: err" in red.
func RunWithSpinner(title string, fn func() error) error {
	var fnErr error
	err := spinner.New().
		Title(title).
		ActionWithErr(func(_ context.Context) error {
			fnErr = fn()
			return fnErr
		}).
		Run()

	if fnErr != nil {
		fmt.Println(errorStyle.Render(fmt.Sprintf("✗ %s: %s", title, fnErr)))
		return fnErr
	}
	if err != nil {
		return err
	}
	fmt.Println(successStyle.Render("✓ " + title))
	return nil
}
