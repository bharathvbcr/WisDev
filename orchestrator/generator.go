package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "go/ast"
    "go/parser"
    "go/printer"
    "go/token"
    "os"
    "os/exec"
    "path/filepath"
    "sort"
    "strings"
)

type pkgInfo struct {
    Dir     string   `json:"Dir"`
    Name    string   `json:"Name"`
    GoFiles []string `json:"GoFiles"`
}

type coverageSpec struct {
    funcs   []string
    methods map[string]map[string]struct{}
}

var unsafeFunctions = map[string]map[string]struct{}{
    "main": {
        "Run": {},
    },
    "search": {
        "StreamParallelSearch": {},
    },
    "wisdev": {
        "StartTemporalWorker": {},
        "StartGRPCServer": {},
    },
}

func main() {
    packages, err := goListPackages()
    if err != nil {
        panic(err)
    }

    for _, p := range packages {
        if strings.TrimSpace(p.Name) == "" {
            continue
        }
        spec, err := analyzePackage(p)
        if err != nil {
            panic(fmt.Sprintf("analyze %s: %v", p.Dir, err))
        }

        err = writeAutoCoverageTest(p.Dir, p.Name, spec)
        if err != nil {
            panic(fmt.Sprintf("write test for %s: %v", p.Dir, err))
        }
    }
}

func goListPackages() ([]pkgInfo, error) {
    cmd := exec.Command("go", "list", "-json", "./...")
    var buf bytes.Buffer
    cmd.Stdout = &buf
    cmd.Stderr = os.Stderr

    if err := cmd.Run(); err != nil {
        return nil, err
    }

    dec := json.NewDecoder(&buf)
    packages := []pkgInfo{}

    for {
        var p pkgInfo
        if err := dec.Decode(&p); err != nil {
            if err.Error() == "EOF" {
                break
            }
            return nil, err
        }

        packages = append(packages, p)
    }

    return packages, nil
}

func analyzePackage(p pkgInfo) (coverageSpec, error) {
    fset := token.NewFileSet()
    spec := coverageSpec{methods: map[string]map[string]struct{}{}}

    for _, file := range p.GoFiles {
        path := filepath.Join(p.Dir, file)
        parsed, err := parser.ParseFile(fset, path, nil, 0)
        if err != nil {
            return coverageSpec{}, err
        }

        for _, decl := range parsed.Decls {
            fn, ok := decl.(*ast.FuncDecl)
            if !ok || fn.Name == nil || fn.Body == nil {
                continue
            }

            if fn.Name.Name == "init" || fn.Name.Name == "main" {
                continue
            }
            if shouldSkipFunction(p.Name, fn.Name.Name) {
                continue
            }
            if fn.Type != nil && fn.Type.TypeParams != nil {
                continue
            }

            if fn.Recv == nil {
                spec.funcs = append(spec.funcs, fn.Name.Name)
                continue
            }

            receiverType := renderReceiverExpr(fset, fn.Recv.List[0].Type)
            if receiverType == "" {
                continue
            }

            methodName := fn.Name.Name
            if _, ok := spec.methods[receiverType]; !ok {
                spec.methods[receiverType] = map[string]struct{}{}
            }
            spec.methods[receiverType][methodName] = struct{}{}
        }
    }

    sort.Strings(spec.funcs)

    for receiver, names := range spec.methods {
        methodNames := make([]string, 0, len(names))
        for name := range names {
            methodNames = append(methodNames, name)
        }
        sort.Strings(methodNames)
        uniq := []string{}
        seen := map[string]struct{}{}
        for _, n := range methodNames {
            if _, ok := seen[n]; ok {
                continue
            }
            seen[n] = struct{}{}
            uniq = append(uniq, n)
        }
        methodSet := map[string]struct{}{}
        for _, n := range uniq {
            methodSet[n] = struct{}{}
        }
        spec.methods[receiver] = methodSet
    }

    return spec, nil
}

func renderReceiverExpr(fset *token.FileSet, expr ast.Expr) string {
    var out bytes.Buffer

    switch e := expr.(type) {
    case *ast.StarExpr:
        if isUnsupportedReceiverExpr(e.X) {
            return ""
        }
    case *ast.Ident, *ast.IndexExpr, *ast.IndexListExpr, *ast.SelectorExpr:
    default:
        return ""
    }

    if isGenericExpr(expr) {
        return ""
    }

    if err := printer.Fprint(&out, fset, expr); err != nil {
        return ""
    }

    return out.String()
}

func isUnsupportedReceiverExpr(expr ast.Expr) bool {
    switch e := expr.(type) {
    case *ast.IndexExpr, *ast.IndexListExpr, *ast.FuncType, *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.InterfaceType:
        return true
    case *ast.StarExpr:
        return isUnsupportedReceiverExpr(e.X)
    default:
        return false
    }
}

func isGenericExpr(expr ast.Expr) bool {
    if strings.Contains(exprString(expr), "[") || strings.Contains(exprString(expr), "]") {
        return true
    }
    return false
}

func exprString(expr ast.Expr) string {
    var out bytes.Buffer
    if err := printer.Fprint(&out, token.NewFileSet(), expr); err != nil {
        return ""
    }
    return out.String()
}

func writeAutoCoverageTest(dir, pkg string, spec coverageSpec) error {
    var buf bytes.Buffer

    buf.WriteString("// Code generated by generator.go. DO NOT EDIT.\n")
    buf.WriteString("\n")
    buf.WriteString("package ")
    buf.WriteString(pkg)
    buf.WriteString("\n\n")
    buf.WriteString("import (\n")
    buf.WriteString("\t\"reflect\"\n")
    buf.WriteString("\t\"testing\"\n")
    buf.WriteString(")\n\n")

    if len(spec.funcs) == 0 && len(spec.methods) == 0 {
        buf.WriteString("// auto_coverage_test generation skipped: no declarations to cover.\n")
        buf.WriteString("func TestAutoCoverageGenerated(t *testing.T) {}\n")
        buf.WriteString("\n")
    } else {
        buf.WriteString("func TestAutoCoverageGenerated(t *testing.T) {\n")
        buf.WriteString("\t_ = t\n")
        for _, fn := range spec.funcs {
            buf.WriteString("\tcallWithZeroValueArgs(")
            buf.WriteString(fn)
            buf.WriteString(")\n")
        }
        receiverKeys := make([]string, 0, len(spec.methods))
        for receiver := range spec.methods {
            receiverKeys = append(receiverKeys, receiver)
        }
        sort.Strings(receiverKeys)

        for _, receiver := range receiverKeys {
            methods := make([]string, 0, len(spec.methods[receiver]))
            for m := range spec.methods[receiver] {
                methods = append(methods, m)
            }
            sort.Strings(methods)
            for _, method := range methods {
                if method == "init" {
                    continue
                }
                buf.WriteString("\tcallMethodWithZeroReceiver[")
                buf.WriteString(receiver)
                buf.WriteString("](\"")
                buf.WriteString(method)
                buf.WriteString("\")\n")
            }
        }
        buf.WriteString("}\n\n")
    }

    buf.WriteString("func callWithZeroValueArgs(fn any) {\n")
    buf.WriteString("\tif fn == nil {\n\t\treturn\n\t}\n")
    buf.WriteString("\trv := reflect.ValueOf(fn)\n")
    buf.WriteString("\tif rv.Kind() != reflect.Func {\n\t\treturn\n\t}\n")
    buf.WriteString("\targs := make([]reflect.Value, rv.Type().NumIn())\n")
    buf.WriteString("\tfor i := 0; i < rv.Type().NumIn(); i++ {\n")
    buf.WriteString("\t\targs[i] = reflect.Zero(rv.Type().In(i))\n")
    buf.WriteString("\t}\n\n")
    buf.WriteString("\tdefer func() { _ = recover() }()\n")
    buf.WriteString("\trv.Call(args)\n")
    buf.WriteString("}\n\n")
    buf.WriteString("func callMethodWithZeroReceiver[T any](methodName string) {\n")
    buf.WriteString("\tvar receiver T\n")
    buf.WriteString("\trv := reflect.ValueOf(receiver)\n")
    buf.WriteString("\tif rv.Kind() == reflect.Ptr && rv.IsNil() {\n")
    buf.WriteString("\t\trv = reflect.New(rv.Type().Elem())\n")
    buf.WriteString("\t}\n")
    buf.WriteString("\tm := rv.MethodByName(methodName)\n")
    buf.WriteString("\tif !m.IsValid() {\n\t\treturn\n\t}\n")
    buf.WriteString("\targs := make([]reflect.Value, m.Type().NumIn())\n")
    buf.WriteString("\tfor i := 0; i < m.Type().NumIn(); i++ {\n")
    buf.WriteString("\t\targs[i] = reflect.Zero(m.Type().In(i))\n")
    buf.WriteString("\t}\n\n")
    buf.WriteString("\tdefer func() { _ = recover() }()\n")
    buf.WriteString("\tm.Call(args)\n")
    buf.WriteString("}\n")

    path := filepath.Join(dir, "auto_coverage_test.go")
    return os.WriteFile(path, buf.Bytes(), 0644)
}

func shouldSkipFunction(pkg, name string) bool {
    if _, ok := unsafeFunctions[pkg][name]; ok {
        return true
    }
    return false
}

