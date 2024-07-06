package snowygo

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

func NewAnalyzerWithConfig(cfg *Config) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "snowygo",
		Doc:  "snowmerak's custom linter for Go.",
		Run:  runAnalyzer(cfg),
	}
}

type Reporter struct {
	report func(pos token.Pos, format string, args ...interface{})
	pos    token.Pos
}

type PairElement struct {
	Name     string
	Reporter Reporter
}

type Pair struct {
	First  *PairElement
	Second *PairElement
}

const (
	LibraryGroup   = "lib"
	LibraryClient  = "client"
	LibraryServer  = "server"
	LibraryUtility = "util"

	InternalGroup = "internal"
	CommandGroup  = "cmd"
	ModelGroup    = "model"
	GenGroup      = "gen"
)

func splitPackagePath(path string) (string, []string) {
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", parts
	}
	if len(parts) == 2 {
		return parts[0], parts[1:]
	}
	return parts[1], parts[2:]
}

func getGroupFromPackagePath(path string) string {
	group, _ := splitPackagePath(path)
	return group
}

func runAnalyzer(cfg *Config) func(pass *analysis.Pass) (interface{}, error) {
	bannedPackageNames := map[string]string{
		"util": "use a more descriptive package name",
	}
	bannedImportPaths := map[string]string{
		"github.com/pkg/errors": "use fmt.Errorf and errors instead",
		"io/ioutil":             "use os or package io instead",
	}

	return func(pass *analysis.Pass) (interface{}, error) {
		a2b := make(map[string]*Pair)
		errorStructMap := make(map[string]Reporter)
		errorCheckerMap := make(map[string]Reporter)

		for _, f := range pass.Files {
			// Get the package path
			packagePath := pass.Pkg.Path()

			ast.Inspect(f, func(node ast.Node) bool {
				// check package path
				group, parts := splitPackagePath(packagePath)
				if len(parts) > 0 {
					if desc, ok := bannedPackageNames[parts[len(parts)-1]]; ok {
						pass.Reportf(node.Pos(), "should not use %s to package name, %s", parts[len(parts)-1], desc)
					}
				}

				for _, imp := range f.Imports {
					impGroup, impParts := splitPackagePath(imp.Path.Value)

					if group == impGroup && len(parts) == len(impParts) {
						pass.Reportf(imp.Pos(), "must not import from the same group, same depth")
					}

					switch group {
					case LibraryGroup:
						switch impGroup {
						case InternalGroup:
							pass.Reportf(imp.Pos(), "must not import from internal package")
						case CommandGroup:
							pass.Reportf(imp.Pos(), "must not import from command package")
						}
					case InternalGroup:
						switch impGroup {
						case CommandGroup:
							pass.Reportf(imp.Pos(), "must not import from command package")
						}
					case ModelGroup:
						fallthrough
					case GenGroup:
						switch impGroup {
						case CommandGroup:
							pass.Reportf(imp.Pos(), "must not import from command package")
						case InternalGroup:
							pass.Reportf(imp.Pos(), "must not import from internal package")
						case LibraryGroup:
							pass.Reportf(imp.Pos(), "must not import from library package")
						}
					}
				}

				switch node := node.(type) {
				// when make goroutine is found
				case *ast.GoStmt:
					pass.Reportf(node.Pos(), "should not use raw goroutine, use goroutine pool instead")
				case *ast.GenDecl: // when variable declaration is found
					// check if global variable is exported
					switch node.Tok {
					case token.VAR: // when variable declaration is found
						for _, spec := range node.Specs {
							if valueSpec, ok := spec.(*ast.ValueSpec); ok {
								if len(valueSpec.Names) > 0 {
									if valueSpec.Names[0].IsExported() {
										pass.Reportf(valueSpec.Pos(), "global variable or local temparary value %s should not be exported or pascal case", valueSpec.Names[0].Name)
									}
								}
							}
						}
					case token.TYPE: // when type declaration is found
						for _, spec := range node.Specs {
							if typeSpec, ok := spec.(*ast.TypeSpec); ok {
								if _, ok := typeSpec.Type.(*ast.StructType); ok {
									if typeSpec.Name.IsExported() && strings.HasSuffix(typeSpec.Name.Name, "Error") {
										errorStructMap[typeSpec.Name.Name] = Reporter{
											report: pass.Reportf,
											pos:    typeSpec.Pos(),
										}
									}
								}
							}
						}
					}
				case *ast.ImportSpec: // when import declaration is found
					// check if import path is valid
					if path, err := strconv.Unquote(node.Path.Value); err == nil {
						if desc, ok := bannedImportPaths[path]; ok {
							pass.Reportf(node.Pos(), "should not use %s, %s", path, desc)
						}
					}
				case *ast.FuncDecl: // when function declaration is found
					switch {
					case strings.HasPrefix(node.Name.Name, "Is") && strings.HasSuffix(node.Name.Name, "Error"): // when function name has prefix, 'Check'
						if _, ok := errorCheckerMap[node.Name.Name]; !ok {
							errorCheckerMap[node.Name.Name] = Reporter{
								report: pass.Reportf,
								pos:    node.Pos(),
							}
						}
					case strings.HasPrefix(node.Name.Name, "New"): // when function name has prefix, 'New'
						// check has context.Context parameter
						hasCtx := false
						for _, param := range node.Type.Params.List {
							if len(param.Names) >= 1 && param.Names[0].Name == "ctx" {
								hasCtx = true
								break
							}
						}

						// report if the function does not have a context.Context parameter
						if !hasCtx {
							pass.Reportf(node.Pos(), "missing context.Context parameter")
						}
					default:
						// check if function name has prefix, 'Request', 'Reply', 'Send', 'Receive', 'Publish', 'Subscribe', 'Set', 'Get'
						trimmedName := strings.TrimPrefix(node.Name.Name, "Request")
						trimmedName = strings.TrimPrefix(trimmedName, "Reply")
						trimmedName = strings.TrimPrefix(trimmedName, "Send")
						trimmedName = strings.TrimPrefix(trimmedName, "Receive")
						trimmedName = strings.TrimPrefix(trimmedName, "Publish")
						trimmedName = strings.TrimPrefix(trimmedName, "Subscribe")
						p := a2b[f.Name.Name+"."+trimmedName]
						if p == nil {
							p = &Pair{}
							a2b[f.Name.Name+"."+trimmedName] = p
						}
						reporter := Reporter{
							report: pass.Reportf,
							pos:    node.Pos(),
						}

						switch {
						case strings.HasPrefix(node.Name.Name, "Request"):
							p.First = &PairElement{
								Name:     "Request",
								Reporter: reporter,
							}
						case strings.HasPrefix(node.Name.Name, "Reply"):
							p.Second = &PairElement{
								Name:     "Reply",
								Reporter: reporter,
							}
						case strings.HasPrefix(node.Name.Name, "Send"):
							p.First = &PairElement{
								Name:     "Send",
								Reporter: reporter,
							}
						case strings.HasPrefix(node.Name.Name, "Receive"):
							p.Second = &PairElement{
								Name:     "Receive",
								Reporter: reporter,
							}
						case strings.HasPrefix(node.Name.Name, "Publish"):
							p.First = &PairElement{
								Name:     "Publish",
								Reporter: reporter,
							}
						case strings.HasPrefix(node.Name.Name, "Subscribe"):
							p.Second = &PairElement{
								Name:     "Subscribe",
								Reporter: reporter,
							}
						}
					}

					// when function has parameters
					if node.Type.Params != nil {
						// check if the function has a context.Context parameter
						hasCtx := false
						isCtxFirst := false
						for i, param := range node.Type.Params.List {
							if len(param.Names) >= 1 && param.Names[0].Name == "ctx" {
								hasCtx = true
								if i == 0 {
									isCtxFirst = true
								}
								break
							}
						}

						// report if the function has a context.Context parameter but it's not the first parameter
						if hasCtx && !isCtxFirst {
							pass.Reportf(node.Pos(), "context.Context should be the first parameter")
						}
					}

					// when function has return values
					if node.Type.Results != nil {
						hasErr := false
						isErrLast := false
						for i, param := range node.Type.Results.List {
							// check if the function has an error return value
							if ident, ok := param.Type.(*ast.Ident); ok && ident.Name == "error" {
								hasErr = true
								if i == len(node.Type.Results.List)-1 {
									isErrLast = true
								}
							}
						}

						// report if the function has an error return value but it's not the last return value
						if hasErr && !isErrLast {
							pass.Reportf(node.Pos(), "error should be the last return value")
						}
					}

					// when function has a body
					if len(node.Body.List) != 0 {
						for _, stmt := range node.Body.List {
							// check if statement has else branch
							if stmt, ok := stmt.(*ast.IfStmt); ok {
								if stmt.Else != nil {
									pass.Reportf(stmt.Pos(), "if statement should not have an else branch, use early return or switch statement instead")
								}
							}
						}
					}
				case *ast.ReturnStmt: // when return statement is found
					// check if return statement has error
					if len(node.Results) > 0 {
						for _, result := range node.Results {
							// check result type is error or fmt.Errorf
							if ident, ok := result.(*ast.Ident); ok && ident.Name == "err" {
								pass.Reportf(node.Pos(), "should not return error, use fmt.Errorf instead")
							}
						}
					}
				case *ast.CallExpr: // when function call is found
					// check if you make function is called
					if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "make" {
						// check if you make function is called with 2 arguments
						if len(node.Args) < 2 {
							pass.Reportf(node.Pos(), "make function should be called with 2 arguments")
						}
					}
				}

				return true
			})
		}

		for e := range errorStructMap {
			c := "Is" + e
			if _, ok := errorCheckerMap[c]; !ok {
				errorStructMap[e].report(errorStructMap[e].pos, "missing %s function", c)
			}
			delete(errorCheckerMap, c)
		}
		for c := range errorCheckerMap {
			e := strings.TrimPrefix(c, "Is")
			if _, ok := errorStructMap[e]; !ok {
				errorCheckerMap[c].report(errorCheckerMap[c].pos, "missing %s struct", e)
			}
		}

		for _, pair := range a2b {
			if pair.First != nil && pair.Second == nil {
				opposite := ""
				switch pair.First.Name {
				case "Request":
					opposite = "Reply"
				case "Send":
					opposite = "Receive"
				case "Publish":
					opposite = "Subscribe"
				case "Set":
					opposite = "Get"
				default:
					opposite = "unknown"
				}
				pair.First.Reporter.report(pair.First.Reporter.pos, "missing %s function", opposite)
			} else if pair.First == nil && pair.Second != nil {
				opposite := ""
				switch pair.Second.Name {
				case "Reply":
					opposite = "Request"
				case "Receive":
					opposite = "Send"
				case "Subscribe":
					opposite = "Publish"
				case "Get":
					opposite = "Set"
				default:
					opposite = "unknown"
				}
				pair.Second.Reporter.report(pair.Second.Reporter.pos, "missing %s function", opposite)
			}
		}

		return nil, nil
	}
}
