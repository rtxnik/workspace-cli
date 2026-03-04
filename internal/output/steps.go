package output

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/huh/spinner"
)

// Step represents a named operation in a multi-step sequence.
type Step struct {
	Name string
	Fn   func() error
}

// StepRunner executes a sequence of steps with progress display.
type StepRunner struct {
	steps []Step
}

// NewStepRunner creates a runner for the given steps.
func NewStepRunner(steps ...Step) *StepRunner {
	return &StepRunner{steps: steps}
}

// Run executes each step sequentially, showing a spinner for the active step
// and a summary line for completed/remaining steps.
func (r *StepRunner) Run() error {
	for i, step := range r.steps {
		start := time.Now()

		var fnErr error
		err := spinner.New().
			Title(step.Name).
			Action(func() { fnErr = step.Fn() }).
			Run()

		duration := time.Since(start)
		durStr := fmt.Sprintf("%.1fs", duration.Seconds())

		if fnErr != nil {
			fmt.Fprintf(os.Stderr, "%s %s\n",
				StyleError.Render(fmt.Sprintf("✗ %s:", step.Name)),
				StyleDim.Render(fnErr.Error()))
			// Print remaining steps as dim.
			for _, rem := range r.steps[i+1:] {
				fmt.Fprintf(os.Stderr, "%s\n", StyleDim.Render("  ○ "+rem.Name))
			}
			return fnErr
		}
		if err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "%s %s\n",
			StyleSuccess.Render("✓ "+step.Name),
			StyleDim.Render(durStr))
	}
	return nil
}
