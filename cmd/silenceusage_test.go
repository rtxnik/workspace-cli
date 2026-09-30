package cmd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Where code sets cmd.SilenceUsage.
//
// Whether an error is an argument error or a runtime error is read from the
// failed command's SilenceUsage: a leaf's body sets it as soon as it starts
// work, so a leaf that fails with it still false failed before its body ran —
// cobra rejected its arguments or its flags. That holds only while the
// assignment stands where the two forms below put it, and this test holds it
// there over every non-test file of the package:
//
//   - a RunE function literal starts with <cmd>.SilenceUsage = true, where
//     <cmd> is the literal's first parameter;
//   - in a PreRunE or PersistentPreRunE function literal, an error is
//     returned only as the whole body of an `if err != nil` block: the
//     assignment, then `return err` of the variable that block checked;
//     and SilenceUsage appears nowhere else in the literal, a function
//     literal nested in it included. cobra runs the hooks before it checks
//     required and grouped flags, so a hook that set it on its way to
//     succeeding would turn those rejections into runtime errors — and a hook
//     that returned a call's result, a variable nothing checked, or one it
//     assigned again after the check, could.
//
// A RunE, PreRunE or PersistentPreRunE that is not a function literal is
// reported too: the check cannot read its body. Nor does it follow a call: a
// function that sets SilenceUsage for a hook is beyond its reach.

// silenceUsageViolations checks every non-test Go file in dir. It returns
// the violations, one "file:line: reason" each, sorted, and how many RunE
// literals and hook literals it checked.
func silenceUsageViolations(dir string) (violations []string, runE, hooks int, err error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, 0, 0, err
	}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, 0, 0, err
		}
		report := func(n ast.Node, format string, args ...any) {
			pos := fset.Position(n.Pos())
			violations = append(violations, fmt.Sprintf("%s:%d: %s", filepath.Base(pos.Filename), pos.Line, fmt.Sprintf(format, args...)))
		}
		check := func(field string, value ast.Expr) {
			lit, ok := value.(*ast.FuncLit)
			if !ok {
				report(value, "%s is not a function literal, so its body cannot be checked", field)
				return
			}
			switch field {
			case "RunE":
				runE++
				body := lit.Body.List
				if len(body) == 0 || !assignsSilenceUsage(body[0], firstParam(lit)) {
					report(lit, "RunE does not start with %s.SilenceUsage = true", firstParam(lit))
				}
			case "PreRunE", "PersistentPreRunE":
				hooks++
				checkHook(lit, func(n ast.Node, reason string) { report(n, "%s %s", field, reason) })
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.KeyValueExpr:
				if key, ok := x.Key.(*ast.Ident); ok && isCheckedField(key.Name) {
					check(key.Name, x.Value)
				}
			case *ast.AssignStmt:
				for i, lhs := range x.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && isCheckedField(sel.Sel.Name) && i < len(x.Rhs) {
						check(sel.Sel.Name, x.Rhs[i])
					}
				}
			}
			return true
		})
	}
	sort.Strings(violations)
	return violations, runE, hooks, nil
}

func isCheckedField(name string) bool {
	return name == "RunE" || name == "PreRunE" || name == "PersistentPreRunE"
}

// firstParam is the name of a function literal's first parameter, "" if it
// has none.
func firstParam(lit *ast.FuncLit) string {
	if params := lit.Type.Params.List; len(params) > 0 && len(params[0].Names) > 0 {
		return params[0].Names[0].Name
	}
	return ""
}

// assignsSilenceUsage reports whether s is exactly `<param>.SilenceUsage = true`.
func assignsSilenceUsage(s ast.Stmt, param string) bool {
	as, ok := s.(*ast.AssignStmt)
	if !ok || as.Tok != token.ASSIGN || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
		return false
	}
	sel, ok := as.Lhs[0].(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "SilenceUsage" {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	val, isIdent := as.Rhs[0].(*ast.Ident)
	return ok && param != "" && recv.Name == param && isIdent && val.Name == "true"
}

// isErrorReturn reports whether s returns something other than nil: an
// error, or a value that may be one.
func isErrorReturn(s ast.Stmt) bool {
	ret, ok := s.(*ast.ReturnStmt)
	if !ok || len(ret.Results) == 0 {
		return false
	}
	id, isIdent := ret.Results[0].(*ast.Ident)
	return !isIdent || id.Name != "nil"
}

// returnsChecked reports whether s is `return <checked>`, the variable the
// enclosing `if <checked> != nil` tested; "" is no such if.
func returnsChecked(s ast.Stmt, checked string) bool {
	ret, ok := s.(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 || checked == "" {
		return false
	}
	id, isIdent := ret.Results[0].(*ast.Ident)
	return isIdent && id.Name == checked
}

// nilChecked is the variable an if condition of the form `<v> != nil` tests,
// "" for any other condition.
func nilChecked(cond ast.Expr) string {
	be, ok := cond.(*ast.BinaryExpr)
	if !ok || be.Op != token.NEQ {
		return ""
	}
	v, isVar := be.X.(*ast.Ident)
	n, isNil := be.Y.(*ast.Ident)
	if !isVar || !isNil || n.Name != "nil" || v.Name == "nil" {
		return ""
	}
	return v.Name
}

// unlabel is the statement a label stands before, s itself when none does.
func unlabel(s ast.Stmt) ast.Stmt {
	for {
		l, ok := s.(*ast.LabeledStmt)
		if !ok {
			return s
		}
		s = l.Stmt
	}
}

// checkHook applies the hook form to lit's own statements, in every block,
// case clause and select clause of its body, and not in function literals
// nested inside it: their returns are theirs. A label is read through: the
// form is checked on the statement it stands before. A block is an error
// branch of v when it is the body of `if v != nil`; only there may v be
// returned, and only by a block that holds nothing but the assignment and the
// return: another statement there could assign v again.
//
// A second walk reports SilenceUsage wherever the first did not meet it as
// the form's assignment: in an init statement, under another name, by its
// address, or in a function literal nested in the hook, which can run on its
// success path.
func checkHook(lit *ast.FuncLit, report func(ast.Node, string)) {
	param := firstParam(lit)
	form := "if err != nil { " + param + ".SilenceUsage = true; return err }"
	branchOf := map[*ast.BlockStmt]string{}
	// met holds the field of each <param>.SilenceUsage = true the list walk
	// met.
	met := map[ast.Node]bool{}
	checkList := func(labelled []ast.Stmt, checked string) {
		list := make([]ast.Stmt, len(labelled))
		for i, s := range labelled {
			list[i] = unlabel(s)
		}
		for i, s := range list {
			if isErrorReturn(s) {
				switch {
				case !returnsChecked(s, checked):
					report(s, "returns an error other than as `"+form+"`")
				case i == 0 || !assignsSilenceUsage(list[i-1], param):
					report(s, "returns an error without setting "+param+".SilenceUsage = true on the line before")
				}
			}
			if assignsSilenceUsage(s, param) {
				met[s.(*ast.AssignStmt).Lhs[0]] = true
				switch {
				case i+1 == len(list) || !returnsChecked(list[i+1], checked):
					report(s, "sets "+param+".SilenceUsage = true where no error return follows")
				case len(list) != 2:
					report(s, "sets "+param+".SilenceUsage = true in a block that holds more than `"+form+"`")
				}
			}
		}
	}
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.IfStmt:
			branchOf[x.Body] = nilChecked(x.Cond)
		case *ast.BlockStmt:
			checkList(x.List, branchOf[x])
		case *ast.CaseClause:
			checkList(x.Body, "")
		case *ast.CommClause:
			checkList(x.Body, "")
		}
		return true
	})
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "SilenceUsage" && !met[sel] {
			report(sel, "mentions SilenceUsage outside `"+form+"`")
		}
		return true
	})
}

// TestSilenceUsageForms holds every RunE and every pre-run hook of the
// package to its form.
func TestSilenceUsageForms(t *testing.T) {
	violations, runE, hooks, err := silenceUsageViolations(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
	// A positive precondition: a scan that found nothing would pass.
	if runE < 40 || hooks < 1 {
		t.Fatalf("checked %d RunE literals and %d hooks; the package has more than 40 and at least 1", runE, hooks)
	}
	t.Logf("checked %d RunE literals and %d hooks", runE, hooks)
}

// TestSilenceUsageFormsCanFail is the check's own control: over a directory
// of planted sources, it reports each planted defect at its line, and
// nothing in the conforming file or in a test file.
func TestSilenceUsageFormsCanFail(t *testing.T) {
	const good = `package x

var leaf = &cobra.Command{RunE: func(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	return run()
}}

var group = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	check := func() error { return errNotReady }
	if err := check(); err != nil {
		cmd.SilenceUsage = true
		return err
	}
	return nil
}}
`
	for _, c := range []struct {
		name, src, want string
	}{
		{"the assignment is not first", `package x

var a = &cobra.Command{RunE: func(cmd *cobra.Command, _ []string) error {
	cfg := load()
	cmd.SilenceUsage = true
	return run(cfg)
}}
`, "bad.go:3: RunE does not start with cmd.SilenceUsage = true"},
		{"another command is silenced", `package x

var a = &cobra.Command{RunE: func(cmd *cobra.Command, _ []string) error {
	rootCmd.SilenceUsage = true
	return nil
}}
`, "bad.go:3: RunE does not start with cmd.SilenceUsage = true"},
		{"the assignment sets false", `package x

var a = &cobra.Command{RunE: func(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = false
	return nil
}}
`, "bad.go:3: RunE does not start with cmd.SilenceUsage = true"},
		{"RunE is assigned, not composed", `package x

func init() {
	a.RunE = func(cmd *cobra.Command, _ []string) error { return nil }
}
`, "bad.go:4: RunE does not start with cmd.SilenceUsage = true"},
		{"RunE is a named function", `package x

var a = &cobra.Command{RunE: runA}
`, "bad.go:3: RunE is not a function literal, so its body cannot be checked"},
		{"a hook returns an error bare", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	return check()
}}
`, "bad.go:4: PersistentPreRunE returns an error other than as `if err != nil { cmd.SilenceUsage = true; return err }`"},
		{"a hook sets it before a return that may succeed", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	return check()
}}
`, "bad.go:4: PersistentPreRunE sets cmd.SilenceUsage = true where no error return follows\n" +
			"bad.go:5: PersistentPreRunE returns an error other than as `if err != nil { cmd.SilenceUsage = true; return err }`"},
		{"a hook returns a variable nothing checked", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	err := check()
	cmd.SilenceUsage = true
	return err
}}
`, "bad.go:5: PersistentPreRunE sets cmd.SilenceUsage = true where no error return follows\n" +
			"bad.go:6: PersistentPreRunE returns an error other than as `if err != nil { cmd.SilenceUsage = true; return err }`"},
		{"a hook returns another variable than the one it checked", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	if err := check(); err != nil {
		cmd.SilenceUsage = true
		return errOther
	}
	return nil
}}
`, "bad.go:5: PersistentPreRunE sets cmd.SilenceUsage = true where no error return follows\n" +
			"bad.go:6: PersistentPreRunE returns an error other than as `if err != nil { cmd.SilenceUsage = true; return err }`"},
		{"a hook shadows the checked error", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	if err := check(); err != nil {
		err := retry()
		cmd.SilenceUsage = true
		return err
	}
	return nil
}}
`, "bad.go:6: PersistentPreRunE sets cmd.SilenceUsage = true in a block that holds more than `if err != nil { cmd.SilenceUsage = true; return err }`"},
		{"a hook overwrites the checked error", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	if err := check(); err != nil {
		err = retry()
		cmd.SilenceUsage = true
		return err
	}
	return nil
}}
`, "bad.go:6: PersistentPreRunE sets cmd.SilenceUsage = true in a block that holds more than `if err != nil { cmd.SilenceUsage = true; return err }`"},
		{"a hook hides the form behind labels", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	if err := check(); err != nil {
		err = retry()
		goto silence
	silence:
		cmd.SilenceUsage = true
		goto done
	done:
		return err
	}
	return nil
}}
`, "bad.go:11: PersistentPreRunE returns an error without setting cmd.SilenceUsage = true on the line before\n" +
			"bad.go:8: PersistentPreRunE sets cmd.SilenceUsage = true where no error return follows"},
		{"a hook sets it in a deferred function", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	defer func() { cmd.SilenceUsage = true }()
	return nil
}}
`, "bad.go:4: PersistentPreRunE mentions SilenceUsage outside `if err != nil { cmd.SilenceUsage = true; return err }`"},
		{"a hook sets it in an if statement's init", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	if cmd.SilenceUsage = true; ready() {
		return nil
	}
	return nil
}}
`, "bad.go:4: PersistentPreRunE mentions SilenceUsage outside `if err != nil { cmd.SilenceUsage = true; return err }`"},
		{"a hook sets it through another name", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	c := cmd
	c.SilenceUsage = true
	return nil
}}
`, "bad.go:5: PersistentPreRunE mentions SilenceUsage outside `if err != nil { cmd.SilenceUsage = true; return err }`"},
		{"a hook takes its address", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	p := &cmd.SilenceUsage
	*p = true
	return nil
}}
`, "bad.go:4: PersistentPreRunE mentions SilenceUsage outside `if err != nil { cmd.SilenceUsage = true; return err }`"},
		{"a hook's if checks err == nil", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	if err := check(); err == nil {
		cmd.SilenceUsage = true
		return err
	}
	return nil
}}
`, "bad.go:5: PersistentPreRunE sets cmd.SilenceUsage = true where no error return follows\n" +
			"bad.go:6: PersistentPreRunE returns an error other than as `if err != nil { cmd.SilenceUsage = true; return err }`"},
		{"a hook sets it on its success path", `package x

var g = &cobra.Command{PreRunE: func(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	if err := check(); err != nil {
		return err
	}
	return nil
}}
`, "bad.go:4: PreRunE sets cmd.SilenceUsage = true where no error return follows\n" +
			"bad.go:6: PreRunE returns an error without setting cmd.SilenceUsage = true on the line before"},
		{"a hook returns bare inside a switch", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	switch mode() {
	case 1:
		return errOne
	}
	return nil
}}
`, "bad.go:6: PersistentPreRunE returns an error other than as `if err != nil { cmd.SilenceUsage = true; return err }`"},
		{"a hook returns bare inside a select", `package x

var g = &cobra.Command{PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
	select {
	case <-done:
		return errDone
	}
	return nil
}}
`, "bad.go:6: PersistentPreRunE returns an error other than as `if err != nil { cmd.SilenceUsage = true; return err }`"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, src := range map[string]string{
				"good.go": good,
				"bad.go":  c.src,
				// A test file is never checked, even when it breaks both forms.
				"bad_test.go": "package x\n\nvar b = &cobra.Command{RunE: func(cmd *cobra.Command, _ []string) error { return nil }}\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, _, _, err := silenceUsageViolations(dir)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "\n") != c.want {
				t.Errorf("reported\n%s\nwant\n%s", strings.Join(got, "\n"), c.want)
			}
		})
	}
}
