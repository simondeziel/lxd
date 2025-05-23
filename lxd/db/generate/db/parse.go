//go:build linux && cgo && !agent

package db

import (
	"fmt"
	"go/ast"
	"net/url"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/canonical/lxd/lxd/db/generate/lex"
	"github.com/canonical/lxd/shared"
)

// Packages returns the AST packages in which to search for structs.
//
// By default it includes the lxd/db and shared/api packages.
func Packages() (map[string]*packages.Package, error) {
	packages := map[string]*packages.Package{}

	_, filename, _, _ := runtime.Caller(0)

	for _, name := range defaultPackages {
		pkg, err := lex.Parse(filepath.Join(filepath.Dir(filename), "..", "..", "..", "..", name))
		if err != nil {
			return nil, fmt.Errorf("Parse %q: %w", name, err)
		}

		parts := strings.Split(name, "/")
		packages[parts[len(parts)-1]] = pkg
	}

	return packages, nil
}

// ParsePackage returns the AST package in which to search for structs.
func ParsePackage(pkgPath string) (*packages.Package, error) {
	pkg, err := lex.Parse(pkgPath)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse package path %q: %w", pkgPath, err)
	}

	return pkg, nil
}

var defaultPackages = []string{
	"shared/api",
	"lxd/db",
}

// GetVars gets all variable declarations in the given package, by name.
func GetVars(pkg *packages.Package) map[string]*ast.ValueSpec {
	objects := map[string]*ast.ValueSpec{}
	for _, s := range pkg.Syntax {
		for _, d := range s.Decls {
			decl, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}

			for _, s := range decl.Specs {
				spec, ok := s.(*ast.ValueSpec)
				if !ok {
					continue
				}

				if len(spec.Values) != 1 && len(spec.Names) != 1 {
					continue
				}

				objects[spec.Names[0].Name] = spec
			}
		}
	}

	return objects
}

// FiltersFromStmt parses all filtering statement defined for the given entity. It
// returns all supported combinations of filters, sorted by number of criteria, and
// the corresponding set of unused filters from the Filter struct.
func FiltersFromStmt(pkg *packages.Package, kind string, entity string, filters []*Field) (stmtFilters [][]string, ignoredFilters [][]string) {
	objects := GetVars(pkg)
	stmtFilters = [][]string{}
	prefix := lex.Minuscule(lex.Camel(entity)) + lex.Camel(kind) + "By"

	for name := range objects {
		if !strings.HasPrefix(name, prefix) {
			continue
		}

		rest := name[len(prefix):]
		stmtFilters = append(stmtFilters, strings.Split(rest, "And"))
	}

	stmtFilters = sortFilters(stmtFilters)
	ignoredFilters = [][]string{}

	for _, filterGroup := range stmtFilters {
		ignoredFilterGroup := []string{}
		for _, filter := range filters {
			if !shared.ValueInSlice(filter.Name, filterGroup) {
				ignoredFilterGroup = append(ignoredFilterGroup, filter.Name)
			}
		}
		ignoredFilters = append(ignoredFilters, ignoredFilterGroup)
	}

	return stmtFilters, ignoredFilters
}

// RefFiltersFromStmt parses all filtering statement defined for the given entity reference.
func RefFiltersFromStmt(pkg *packages.Package, entity string, ref string, filters []*Field) (stmtFilters [][]string, ignoredFilters [][]string) {
	objects := GetVars(pkg)
	stmtFilters = [][]string{}

	prefix := lex.Minuscule(lex.Camel(entity)) + lex.Capital(ref) + "RefBy"

	for name := range objects {
		if !strings.HasPrefix(name, prefix) {
			continue
		}

		rest := name[len(prefix):]
		stmtFilters = append(stmtFilters, strings.Split(rest, "And"))
	}

	stmtFilters = sortFilters(stmtFilters)
	ignoredFilters = [][]string{}

	for _, filterGroup := range stmtFilters {
		ignoredFilterGroup := []string{}
		for _, filter := range filters {
			if !shared.ValueInSlice(filter.Name, filterGroup) {
				ignoredFilterGroup = append(ignoredFilterGroup, filter.Name)
			}
		}
		ignoredFilters = append(ignoredFilters, ignoredFilterGroup)
	}

	return stmtFilters, ignoredFilters
}

func sortFilters(filters [][]string) [][]string {
	sort.Slice(filters, func(i, j int) bool {
		n1 := len(filters[i])
		n2 := len(filters[j])
		if n1 != n2 {
			return n1 > n2
		}

		f1 := sortFilter(filters[i])
		f2 := sortFilter(filters[j])
		for k := range f1 {
			if f1[k] == f2[k] {
				continue
			}

			return f1[k] > f2[k]
		}

		panic("duplicate filter")
	})
	return filters
}

func sortFilter(filter []string) []string {
	f := make([]string, len(filter))
	copy(f, filter)
	sort.Sort(sort.Reverse(sort.StringSlice(f)))
	return f
}

// Parse the structure declaration with the given name found in the given Go package.
// Any 'Entity' struct should also have an 'EntityFilter' struct defined in the same file.
func Parse(pkg *packages.Package, name string, kind string) (*Mapping, error) {
	// The main entity struct.
	str := findStruct(pkg, name)
	if str == nil {
		return nil, fmt.Errorf("No declaration found for %q", name)
	}

	fields, err := parseStruct(str, kind)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse %q: %w", name, err)
	}

	m := &Mapping{
		Package:    pkg.Name,
		Name:       name,
		Fields:     fields,
		Type:       tableType(pkg, name, fields),
		Filterable: true,
	}

	if m.Filterable {
		// The 'EntityFilter' struct. This is used for filtering on specific fields of the entity.
		filterName := name + "Filter"
		filterStr := findStruct(pkg, filterName)
		if filterStr == nil {
			return nil, fmt.Errorf("No declaration found for %q", filterName)
		}

		filters, err := parseStruct(filterStr, kind)
		if err != nil {
			return nil, fmt.Errorf("Failed to parse %q: %w", name, err)
		}

		for i, filter := range filters {
			// Any field in EntityFilter must be present in the original struct.
			field := m.FieldByName(filter.Name)
			if field == nil {
				return nil, fmt.Errorf("Filter field %q is not in struct %q", filter.Name, name)
			}

			// Assign the config tags from the main entity struct to the Filter struct.
			filters[i].Config = field.Config

			// A Filter field and its indirect references must all be in the Filter struct.
			if field.IsIndirect() {
				indirectField := lex.Camel(field.Config.Get("via"))
				for i, f := range filters {
					if f.Name == indirectField {
						break
					}

					if i == len(filters)-1 {
						return nil, fmt.Errorf("Field %q requires field %q in struct %q", field.Name, indirectField, name+"Filter")
					}
				}
			}
		}

		m.Filters = filters
	}

	return m, nil
}

// ParseStmt returns the SQL string passed as an argument to a variable declaration of a call to RegisterStmt with the given name.
// e.g. the SELECT string from 'var instanceObjects = RegisterStmt(`SELECT * from instances...`)'.
func ParseStmt(pkg *packages.Package, dbPkg *packages.Package, name string) (string, error) {
	pkgs := []*packages.Package{pkg}
	if dbPkg != nil {
		pkgs = append(pkgs, dbPkg)
	}

	for _, defaultPkg := range pkgs {
		for _, syntax := range defaultPkg.Syntax {
			for _, d := range syntax.Decls {
				decl, ok := d.(*ast.GenDecl)
				if !ok {
					continue
				}

				for _, s := range decl.Specs {
					spec, ok := s.(*ast.ValueSpec)
					if !ok {
						continue
					}

					if len(spec.Values) != 1 && len(spec.Names) != 1 {
						continue
					}

					if spec.Names[0].Name != name {
						continue
					}

					expr, ok := spec.Values[0].(*ast.CallExpr)
					if !ok {
						return "", fmt.Errorf("Object %q is not variable defined as a function call to RegisterStmt", name)
					}

					if len(expr.Args) != 1 {
						return "", fmt.Errorf("Object %q's call to RegisterStmt should have only one argument, found %d", name, len(expr.Args))
					}

					lit, ok := expr.Args[0].(*ast.BasicLit)
					if !ok {
						return "", fmt.Errorf("Object %q's call to RegisterStmt must have a SQL string as its argument", name)
					}

					return lit.Value, nil
				}
			}
		}
	}

	return "", fmt.Errorf("Value %q not found", name)
}

// tableType determines the TableType for the given struct fields.
func tableType(pkg *packages.Package, name string, fields []*Field) TableType {
	fieldNames := FieldNames(fields)
	entities := strings.Split(lex.Snake(name), "_")
	if len(entities) == 2 {
		struct1 := findStruct(pkg, lex.Camel(lex.Singular(entities[0])))
		struct2 := findStruct(pkg, lex.Camel(lex.Singular(entities[1])))
		if struct1 != nil && struct2 != nil {
			return AssociationTable
		}
	}

	if shared.ValueInSlice("ReferenceID", fieldNames) {
		if shared.ValueInSlice("Key", fieldNames) && shared.ValueInSlice("Value", fieldNames) {
			return MapTable
		}

		return ReferenceTable
	}

	return EntityTable
}

// Find the StructType node for the structure with the given name.
func findStruct(pkg *packages.Package, name string) *ast.StructType {
	for _, syntax := range pkg.Syntax {
		for _, d := range syntax.Decls {
			decl, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}

			for _, s := range decl.Specs {
				typ, ok := s.(*ast.TypeSpec)
				if !ok {
					continue
				}

				if typ.Name.Name != name {
					continue
				}

				str, ok := typ.Type.(*ast.StructType)
				if !ok {
					return nil
				}

				return str
			}
		}
	}

	return nil
}

// Extract field information from the given structure.
func parseStruct(str *ast.StructType, kind string) ([]*Field, error) {
	fields := make([]*Field, 0)

	for _, f := range str.Fields.List {
		if len(f.Names) == 0 {
			// Check if this is a parent struct.
			ident, ok := f.Type.(*ast.Ident)
			if !ok {
				continue
			}

			typ, ok := ident.Obj.Decl.(*ast.TypeSpec)
			if !ok {
				continue
			}

			parentStr, ok := typ.Type.(*ast.StructType)
			if !ok {
				continue
			}

			parentFields, err := parseStruct(parentStr, kind)
			if err != nil {
				return nil, fmt.Errorf("Failed to parse parent struct: %w", err)
			}

			fields = append(fields, parentFields...)

			continue
		}

		if len(f.Names) != 1 {
			return nil, fmt.Errorf("Expected a single field name, got %q", f.Names)
		}

		field, err := parseField(f, kind)
		if err != nil {
			return nil, err
		}

		// Don't add field if it has been ignored.
		if field != nil {
			fields = append(fields, field)
		}
	}

	return fields, nil
}

func parseField(f *ast.Field, kind string) (*Field, error) {
	name := f.Names[0]

	if !name.IsExported() {
		return nil, fmt.Errorf("Unexported field name %q", name.Name)
	}

	// Ignore fields that are marked with a tag of `db:"ingore"`
	if f.Tag != nil {
		tag := f.Tag.Value
		tagValue := reflect.StructTag(tag[1 : len(tag)-1]).Get("db")
		if tagValue == "ignore" {
			return nil, nil
		}
	}

	typeName := parseType(f.Type)
	if typeName == "" {
		return nil, fmt.Errorf("Unsupported type for field %q", name.Name)
	}

	typeObj := Type{
		Name: typeName,
	}

	typeObj.Code = TypeColumn
	if strings.HasPrefix(typeName, "[]") {
		typeObj.Code = TypeSlice
	} else if strings.HasPrefix(typeName, "map[") {
		typeObj.Code = TypeMap
	}

	var config url.Values
	if f.Tag != nil {
		tag := f.Tag.Value
		var err error
		config, err = url.ParseQuery(reflect.StructTag(tag[1 : len(tag)-1]).Get("db"))
		if err != nil {
			return nil, fmt.Errorf("Parse 'db' structure tag: %w", err)
		}
	}

	// Ignore fields that are marked with `db:"omit"`.
	omit := config.Get("omit")
	if omit != "" {
		omitFields := strings.Split(omit, ",")
		stmtKind := strings.ReplaceAll(lex.Snake(kind), "_", "-")
		switch kind {
		case "URIs":
			stmtKind = "names"
		case "GetMany":
			stmtKind = "objects"
		case "GetOne":
			stmtKind = "objects"
		case "DeleteMany":
			stmtKind = "delete"
		case "DeleteOne":
			stmtKind = "delete"
		}

		if shared.ValueInSlice(kind, omitFields) || shared.ValueInSlice(stmtKind, omitFields) {
			return nil, nil
		} else if kind == "exists" && shared.ValueInSlice("id", omitFields) {
			// Exists checks ID, so if we are omitting the field from ID, also omit it from Exists.
			return nil, nil
		}
	}

	field := Field{
		Name:   name.Name,
		Type:   typeObj,
		Config: config,
	}

	return &field, nil
}

func parseType(x ast.Expr) string {
	switch t := x.(type) {
	case *ast.StarExpr:
		return parseType(t.X)
	case *ast.SelectorExpr:
		return parseType(t.X) + "." + t.Sel.String()
	case *ast.Ident:
		s := t.String()
		if s == "byte" {
			return "uint8"
		}

		return s
	case *ast.ArrayType:
		return "[" + parseType(t.Len) + "]" + parseType(t.Elt)
	case *ast.MapType:
		return "map[" + parseType(t.Key) + "]" + parseType(t.Value)
	case *ast.BasicLit:
		return t.Value
	case nil:
		return ""
	default:
		return ""
	}
}
