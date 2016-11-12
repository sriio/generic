// Package generic generates package with type replacements.
package generic

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"os"
	"path/filepath"

	"github.com/taylorchu/generic/importer"

	"golang.org/x/tools/go/ast/astutil"
)

// RewritePackage applies type replacements on a package in $GOPATH, and saves results as a new package in $PWD.
//
// If there is a dir with the same name as newPkgPath, it will first be removed. It is possible to re-run this
// to update a generic package.
func RewritePackage(ctx *Context) error {
	fset := token.NewFileSet()
	files := make(map[string]*ast.File)

	// Apply AST changes and refresh.
	for _, rewriteFunc := range []func(*Context, map[string]*ast.File, *token.FileSet) error{
		parsePackage,
		rewritePkgName,
		removeTypeDecl,
		rewriteIdent,
		rewriteTopLevelIdent,
		refreshAST,
		typeCheck,
		writePackage,
	} {
		err := rewriteFunc(ctx, files, fset)
		if err != nil {
			return err
		}
	}
	return nil
}

func writePackage(ctx *Context, files map[string]*ast.File, fset *token.FileSet) error {
	writeOutput := func() error {
		for path, f := range files {
			// Print ast to file.
			dest, err := os.Create(outPath(ctx, path))
			if err != nil {
				return err
			}
			defer dest.Close()

			err = format.Node(dest, fset, f)
			if err != nil {
				return err
			}
		}
		return nil
	}

	if ctx.SameDir {
		return writeOutput()
	}

	err := os.RemoveAll(ctx.PkgPath)
	if err != nil {
		return err
	}

	err = os.MkdirAll(ctx.PkgPath, 0777)
	if err != nil {
		return err
	}
	err = writeOutput()
	if err != nil {
		os.RemoveAll(ctx.PkgPath)
		return err
	}

	return nil
}

func outPath(ctx *Context, path string) string {
	if ctx.SameDir {
		return fmt.Sprintf("%s_%s", ctx.PkgPath, filepath.Base(path))
	}
	return filepath.Join(ctx.PkgPath, filepath.Base(path))
}

func typeCheck(ctx *Context, files map[string]*ast.File, fset *token.FileSet) error {
	var tc []*ast.File
	for _, f := range files {
		tc = append(tc, f)
	}

	// Type-check.
	if ctx.SameDir {
		// Also include same-dir files.
		// However, it is silly to add the entire file,
		// because that file might have identifiers from another generic package.
		buildP, err := build.Import(".", ".", 0)
		if err != nil {
			if _, ok := err.(*build.NoGoError); !ok {
				return err
			}
		}
		generated := func(path string) bool {
			for p := range files {
				if outPath(ctx, p) == path {
					return true
				}
			}
			return false
		}
		for _, file := range buildP.GoFiles {
			path := filepath.Join(buildP.Dir, file)
			if generated(path) {
				// Allow updating existing generated files.
				continue
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			decl := findTypeDecl(f)
			if len(decl) > 0 {
				tc = append(tc, &ast.File{
					Decls: decl,
					Name:  f.Name,
				})
			}
		}
	}
	conf := types.Config{Importer: importer.New()}
	_, err := conf.Check("", fset, tc, nil)
	if err != nil {
		for _, f := range tc {
			printer.Fprint(os.Stderr, fset, f)
		}
		return err
	}

	return nil
}

func parsePackage(ctx *Context, files map[string]*ast.File, fset *token.FileSet) error {
	// NOTE: this package that we try to rewrite from should not contain vendor/.
	buildP, err := build.Import(ctx.FromPkgPath, "", 0)
	if err != nil {
		return err
	}
	for _, file := range buildP.GoFiles {
		path := filepath.Join(buildP.Dir, file)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		files[path] = f
	}

	// Gather ast.File to create ast.Package.
	// ast.NewPackage will try to resolve unresolved identifiers.
	ast.NewPackage(fset, files, nil, nil)
	return nil
}

func refreshAST(ctx *Context, files map[string]*ast.File, fset *token.FileSet) error {
	// AST in dirty state; refresh
	buf := new(bytes.Buffer)
	for path, f := range files {
		buf.Reset()
		err := printer.Fprint(buf, fset, f)
		if err != nil {
			return err
		}
		f, err = parser.ParseFile(fset, "", buf, 0)
		if err != nil {
			printer.Fprint(os.Stderr, fset, f)
			return err
		}
		files[path] = f
	}
	return nil
}

// rewritePkgName sets current package name.
func rewritePkgName(ctx *Context, nodes map[string]*ast.File, fset *token.FileSet) error {
	for _, node := range nodes {
		node.Name.Name = ctx.PkgName
	}
	return nil
}

// rewriteIdent converts TypeXXX to its replacement defined in typeMap.
func rewriteIdent(ctx *Context, nodes map[string]*ast.File, fset *token.FileSet) error {
	for _, node := range nodes {
		var used []string
		ast.Inspect(node, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if x.Obj == nil || x.Obj.Kind != ast.Typ {
					return false
				}
				to, ok := ctx.TypeMap[x.Name]
				if !ok {
					return false
				}
				x.Name = to.Ident

				if to.Import == "" {
					return false
				}
				var found bool
				for _, im := range used {
					if im == to.Import {
						found = true
						break
					}
				}
				if !found {
					used = append(used, to.Import)
				}
				return false
			}
			return true
		})
		for _, im := range used {
			astutil.AddImport(fset, node, im)
		}
	}
	return nil
}

// removeTypeDecl removes type declarations defined in typeMap.
func removeTypeDecl(ctx *Context, nodes map[string]*ast.File, fset *token.FileSet) error {
	for _, node := range nodes {
		for i := len(node.Decls) - 1; i >= 0; i-- {
			genDecl, ok := node.Decls[i].(*ast.GenDecl)
			if !ok {
				continue
			}
			if genDecl.Tok != token.TYPE {
				continue
			}
			var remove bool
			for _, spec := range genDecl.Specs {
				typeSpec := spec.(*ast.TypeSpec)

				_, ok = ctx.TypeMap[typeSpec.Name.Name]
				if !ok {
					continue
				}

				_, ok = typeSpec.Type.(*ast.Ident)
				if !ok {
					continue
				}
				remove = true
				break
			}
			if remove {
				node.Decls = append(node.Decls[:i], node.Decls[i+1:]...)
			}
		}
	}
	return nil
}

// rewriteTopLevelIdent adds a prefix to top-level identifiers and their uses.
//
// This prevents name conflicts when a package is rewritten to $PWD.
func rewriteTopLevelIdent(ctx *Context, nodes map[string]*ast.File, fset *token.FileSet) error {
	if !ctx.SameDir {
		return nil
	}

	prefixIdent := func(name string) string {
		if name == "_" {
			// skip unnamed
			return "_"
		}
		return lintName(fmt.Sprintf("%s_%s", ctx.PkgPath, name))
	}

	declMap := make(map[interface{}]string)

	for _, node := range nodes {
		for _, decl := range node.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Recv != nil {
					continue
				}
				decl.Name.Name = prefixIdent(decl.Name.Name)
				declMap[decl] = decl.Name.Name
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						obj := spec.Name.Obj
						if obj != nil && obj.Kind == ast.Typ {
							if to, ok := ctx.TypeMap[obj.Name]; ok && spec.Name.Name == to.Ident {
								// If this identifier is already rewritten before, we don't need to prefix it.
								continue
							}
						}
						spec.Name.Name = prefixIdent(spec.Name.Name)
						declMap[spec] = spec.Name.Name
					case *ast.ValueSpec:
						for _, ident := range spec.Names {
							ident.Name = prefixIdent(ident.Name)
							declMap[spec] = ident.Name
						}
					}
				}
			}
		}
	}

	// After top-level identifiers are renamed, find where they are used, and rewrite those.
	for _, node := range nodes {
		ast.Inspect(node, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if x.Obj == nil || x.Obj.Decl == nil {
					return false
				}
				name, ok := declMap[x.Obj.Decl]
				if !ok {
					return false
				}
				x.Name = name
				return false
			}
			return true
		})
	}
	return nil
}

// findTypeDecl finds type and related declarations.
func findTypeDecl(node *ast.File) (ret []ast.Decl) {
	for _, decl := range node.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		if genDecl.Tok != token.TYPE {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec := spec.(*ast.TypeSpec)

			// Replace a complex declaration with a dummy idenifier.
			//
			// It seems simpler to check whether a type is defined.
			typeSpec.Type = &ast.Ident{
				Name: "uint32",
			}
		}

		ret = append(ret, decl)
	}
	return
}
