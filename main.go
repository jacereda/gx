package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"io/ioutil"
	"path/filepath"
	"strings"
)

const debug = true

//
// Compiler
//

type Compiler struct {
	directoryPath string
	filePaths     []string

	fileSet *token.FileSet
	files   []*ast.File
	types   *types.Info

	genTypeNames map[types.Type]string // Should we use `typeutil.Map` (equivalence-based keys)?
	genFuncDecls map[*ast.FuncDecl]string

	indent int
	errors *strings.Builder
	output *strings.Builder
}

func (c *Compiler) errorf(pos token.Pos, format string, args ...interface{}) {
	fmt.Fprintf(c.errors, "%s: ", c.fileSet.PositionFor(pos, true))
	fmt.Fprintf(c.errors, format, args...)
	fmt.Fprintln(c.errors)
}

func (c *Compiler) errored() bool {
	return c.errors.Len() != 0
}

func (c *Compiler) write(s string) {
	if peek := c.output.String(); len(peek) > 0 && peek[len(peek)-1] == '\n' {
		for i := 0; i < 2*c.indent; i++ {
			c.output.WriteByte(' ')
		}
	}
	c.output.WriteString(s)
}

func (c *Compiler) genTypeName(typ types.Type) string {
	if result, ok := c.genTypeNames[typ]; ok {
		return result
	} else {
		result = typ.String()
		c.genTypeNames[typ] = result
		return result
	}
}

func (c *Compiler) genFuncDecl(decl *ast.FuncDecl) string {
	if result, ok := c.genFuncDecls[decl]; ok {
		return result
	} else {
		signature := c.types.Defs[decl.Name].Type().(*types.Signature)
		results := signature.Results()
		if results.Len() > 1 {
			c.errorf(decl.Type.Results.Pos(), "multiple return values not supported")
		}

		builder := &strings.Builder{}

		// Return type
		if results.Len() == 0 {
			if decl.Name.String() == "main" && decl.Recv == nil {
				builder.WriteString("int ")
			} else {
				builder.WriteString("void ")
			}
		} else {
			builder.WriteString(c.genTypeName(results.At(0).Type()))
			builder.WriteByte(' ')
		}

		// Name
		builder.WriteString(decl.Name.String())

		// Parameters
		builder.WriteByte('(')
		for i, nParams := 0, signature.Params().Len(); i < nParams; i++ {
			if i > 0 {
				builder.WriteString(", ")
			}
			param := signature.Params().At(i)
			builder.WriteString(c.genTypeName(param.Type())) // TODO: Factor out into `c.genVarDecl`
			builder.WriteByte(' ')
			builder.WriteString(param.Name())
		}
		builder.WriteByte(')')

		result = builder.String()
		c.genFuncDecls[decl] = result
		return result
	}
}

func (c *Compiler) writeIdent(ident *ast.Ident) {
	c.write(ident.Name)
}

func (c *Compiler) writeBasicLit(lit *ast.BasicLit) {
	switch lit.Kind {
	case token.INT, token.STRING:
		c.write(lit.Value)
	default:
		c.errorf(lit.Pos(), "unsupported literal kind")
	}
}

func (c *Compiler) writeBinaryExpr(bin *ast.BinaryExpr) {
	c.writeExpr(bin.X)
	c.write(" ")
	switch op := bin.Op; op {
	case token.EQL, token.LSS, token.LEQ, token.GTR, token.GEQ,
		token.ADD, token.SUB, token.MUL, token.QUO, token.REM,
		token.AND, token.OR, token.XOR, token.SHL, token.SHR,
		token.ADD_ASSIGN, token.SUB_ASSIGN, token.MUL_ASSIGN, token.QUO_ASSIGN, token.REM_ASSIGN:
		c.write(op.String())
	default:
		c.errorf(bin.OpPos, "unsupported operator")
	}
	c.write(" ")
	c.writeExpr(bin.Y)
}

func (c *Compiler) writeCallExpr(call *ast.CallExpr) {
	c.writeExpr(call.Fun)
	c.write("(")
	for i, arg := range call.Args {
		if i > 0 {
			c.write(", ")
		}
		c.writeExpr(arg)
	}
	c.write(")")
}

func (c *Compiler) writeExpr(expr ast.Expr) {
	switch expr := expr.(type) {
	case *ast.Ident:
		c.writeIdent(expr)
	case *ast.BasicLit:
		c.writeBasicLit(expr)
	case *ast.BinaryExpr:
		c.writeBinaryExpr(expr)
	case *ast.CallExpr:
		c.writeCallExpr(expr)
	default:
		c.errorf(expr.Pos(), "unsupported expression type")
	}
}

func (c *Compiler) writeExprStmt(exprStmt *ast.ExprStmt) {
	c.writeExpr(exprStmt.X)
	c.write(";")
}

func (c *Compiler) writeReturnStmt(retStmt *ast.ReturnStmt) {
	if len(retStmt.Results) > 0 {
		c.write("return ")
		c.writeExpr(retStmt.Results[0])
		c.write(";")
	} else {
		c.write("return;")
	}
}

func (c *Compiler) writeBlockStmt(block *ast.BlockStmt) {
	c.write("{\n")
	c.indent++
	c.writeStmtList(block.List)
	c.indent--
	c.write("}")
}

func (c *Compiler) writeIfStmt(ifStmt *ast.IfStmt) {
	c.write("if (")
	if ifStmt.Init != nil {
		c.writeStmt(ifStmt.Init)
		c.write(" ")
	}
	c.writeExpr(ifStmt.Cond)
	c.write(") ")
	c.writeStmt(ifStmt.Body)
	if ifStmt.Else != nil {
		c.write(" else ")
		c.writeStmt(ifStmt.Else)
	}
}

func (c *Compiler) writeStmt(stmt ast.Stmt) {
	switch stmt := stmt.(type) {
	case *ast.ExprStmt:
		c.writeExprStmt(stmt)
	case *ast.ReturnStmt:
		c.writeReturnStmt(stmt)
	case *ast.BlockStmt:
		c.writeBlockStmt(stmt)
	case *ast.IfStmt:
		c.writeIfStmt(stmt)
	default:
		c.errorf(stmt.Pos(), "unsupported statement type")
	}
}

func (c *Compiler) writeStmtList(list []ast.Stmt) {
	for _, stmt := range list {
		c.writeStmt(stmt)
		c.write("\n")
	}
}

func (c *Compiler) compile() {
	// Collect file paths from directory
	if len(c.filePaths) == 0 && c.directoryPath != "" {
		fileInfos, err := ioutil.ReadDir(c.directoryPath)
		if err != nil {
			fmt.Println(err)
			return
		}
		for _, fileInfo := range fileInfos {
			c.filePaths = append(c.filePaths, filepath.Join(c.directoryPath, fileInfo.Name()))
		}
	}

	// Initialize maps
	c.genTypeNames = make(map[types.Type]string)
	c.genFuncDecls = make(map[*ast.FuncDecl]string)

	// Initialize builders
	c.errors = &strings.Builder{}
	c.output = &strings.Builder{}

	// Parse
	c.fileSet = token.NewFileSet()
	for _, filePath := range c.filePaths {
		file, err := parser.ParseFile(c.fileSet, filePath, nil, 0)
		if err != nil {
			fmt.Println(err)
			return
		}
		c.files = append(c.files, file)
	}

	// Type-check
	c.types = &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Implicits:  make(map[ast.Node]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:     make(map[ast.Node]*types.Scope),
	}
	if _, err := (&types.Config{}).Check("", c.fileSet, c.files, c.types); err != nil {
		fmt.Println(err)
		return
	}

	// `#include`s
	c.write("#include \"prelude.hh\"\n")

	// Function declarations
	c.write("\n\n")
	for _, file := range c.files {
		for _, decl := range file.Decls {
			if decl, ok := decl.(*ast.FuncDecl); ok {
				c.write(c.genFuncDecl(decl))
				c.write(";\n")
			}
		}
	}

	// Function definitions
	c.write("\n")
	for _, file := range c.files {
		for _, decl := range file.Decls {
			if decl, ok := decl.(*ast.FuncDecl); ok {
				if decl.Body != nil {
					c.write("\n")
					c.write(c.genFuncDecl(decl))
					c.write(" ")
					c.writeBlockStmt(decl.Body)
					c.write("\n")
				}
			}
		}
	}

	// Debug
	if debug {
		fmt.Print("// types:")
		for typ := range c.genTypeNames {
			fmt.Printf(" %s", typ.String())
		}
		fmt.Print("\n\n")
	}
}

//
// Main
//

func main() {
	// Compile
	c := Compiler{directoryPath: "example"}
	c.compile()

	// Print output
	if c.errored() {
		fmt.Println(c.errors)
	} else {
		fmt.Println(c.output)
		ioutil.WriteFile("output.cc", []byte(c.output.String()), fs.ModePerm)
	}
}
