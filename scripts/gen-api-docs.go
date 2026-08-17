package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---- JSON schema structures ----

type Schema struct {
	APIVersion  string                   `json:"apiVersion"`
	Resources   map[string]*ResourceInfo `json:"resources"`
	Definitions map[string]*Definition   `json:"definitions"`
}

type ResourceInfo struct {
	Description string `json:"description"`
	Spec        string `json:"spec,omitempty"`
	Status      string `json:"status,omitempty"`
}

type Definition struct {
	Kind        string     `json:"kind"`
	Description string     `json:"description,omitempty"`
	Fields      []FieldDef `json:"fields,omitempty"`
	EnumValues  []string   `json:"enumValues,omitempty"`
	BaseType    string     `json:"baseType,omitempty"`
	UsedBy      []string   `json:"usedBy,omitempty"`
}

type FieldDef struct {
	Name        string      `json:"name"`
	JSONName    string      `json:"jsonName"`
	Type        interface{} `json:"type"`
	Description string      `json:"description,omitempty"`
	Optional    bool        `json:"optional"`
	Pointer     bool        `json:"pointer,omitempty"`
	Embedded    bool        `json:"embedded,omitempty"`
}

type RefType struct {
	Ref string `json:"$ref"`
}

type ArrayType struct {
	Type  string      `json:"type"`
	Items interface{} `json:"items"`
}

type MapType struct {
	Type  string      `json:"type"`
	Key   string      `json:"key"`
	Value interface{} `json:"value"`
}

// ---- Internal tracking structures ----

type typeInfo struct {
	name     string
	typeSpec *ast.TypeSpec
	file     *ast.File
}

type enumInfo struct {
	baseType string
	values   []string
}

var mainCRDs = []string{"WekaCluster", "WekaContainer", "WekaClient", "WekaPolicy", "WekaManualOperation"}

func main() {
	sourceDir := "./pkg/weka-k8s-api/api/v1alpha1"
	outputDir := "./doc/api_dump"

	os.MkdirAll(outputDir, 0o755) //nolint:errcheck // best-effort dir creation; a real failure surfaces on the next file write

	// Phase 1: Generate api-schema.json
	schema := generateSchema(sourceDir)
	writeSchemaJSON(schema, outputDir)

	// Phase 2: Generate markdown docs from schema
	generateMarkdownDocs(schema, outputDir)
}

// ---- Phase 1: Parse Go source and build schema ----

// resolveConstString resolves a const value expression to its string literal,
// following identifiers and single-argument type conversions (e.g.
// `InstructionType(opSignDrives)`) via the collected constLiterals map.
func resolveConstString(expr ast.Expr, constLiterals map[string]string) (string, bool) {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			return strings.Trim(v.Value, `"`), true
		}
	case *ast.Ident:
		if s, ok := constLiterals[v.Name]; ok {
			return s, true
		}
	case *ast.CallExpr:
		// Type conversion such as InstructionType(opSignDrives).
		if len(v.Args) == 1 {
			return resolveConstString(v.Args[0], constLiterals)
		}
	}
	return "", false
}

func generateSchema(sourceDir string) *Schema {
	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, sourceDir, nil, parser.ParseComments) //nolint:staticcheck // ParseDir is deprecated (doesn't consider build tags) but sufficient for this doc-generation script over a single-build-tag API package
	if err != nil {
		fmt.Printf("Error parsing directory: %v\n", err)
		os.Exit(1)
	}

	allTypes := make(map[string]*typeInfo)
	enums := make(map[string]*enumInfo)
	structTypes := make(map[string]bool)
	aliasTypes := make(map[string]string)

	// Pre-pass: collect every string-literal const value (typed or untyped) by
	// name, so enum consts defined via a shared identifier or a type conversion
	// (e.g. `= opSignDrives` or `= InstructionType(opSignDrives)`) can be
	// resolved back to their literal value rather than silently dropped.
	constLiterals := make(map[string]string)
	for _, pkg := range packages {
		for filename, file := range pkg.Files {
			if strings.HasSuffix(filename, "_test.go") || strings.HasSuffix(filename, "zz_generated.deepcopy.go") {
				continue
			}
			for _, decl := range file.Decls {
				genDecl, ok := decl.(*ast.GenDecl)
				if !ok || genDecl.Tok != token.CONST {
					continue
				}
				for _, spec := range genDecl.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if i >= len(vs.Values) {
							continue
						}
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							constLiterals[name.Name] = strings.Trim(lit.Value, `"`)
						}
					}
				}
			}
		}
	}

	for _, pkg := range packages {
		for filename, file := range pkg.Files {
			if strings.HasSuffix(filename, "_test.go") || strings.HasSuffix(filename, "zz_generated.deepcopy.go") {
				continue
			}

			ast.Inspect(file, func(n ast.Node) bool {
				genDecl, ok := n.(*ast.GenDecl)
				if !ok {
					return true
				}

				if genDecl.Tok == token.TYPE {
					for _, spec := range genDecl.Specs {
						ts, ok := spec.(*ast.TypeSpec)
						if !ok {
							continue
						}
						ti := &typeInfo{
							name:     ts.Name.Name,
							typeSpec: ts,
							file:     file,
						}
						allTypes[ts.Name.Name] = ti

						switch t := ts.Type.(type) {
						case *ast.StructType:
							structTypes[ts.Name.Name] = true
						case *ast.Ident:
							if isBasicType(t.Name) {
								aliasTypes[ts.Name.Name] = t.Name
							}
						case *ast.SelectorExpr:
							pkgName := ""
							if ident, ok := t.X.(*ast.Ident); ok {
								pkgName = ident.Name
							}
							aliasTypes[ts.Name.Name] = pkgName + "." + t.Sel.Name
						}
					}
				}

				if genDecl.Tok == token.CONST {
					for _, spec := range genDecl.Specs {
						vs, ok := spec.(*ast.ValueSpec)
						if !ok || vs.Type == nil {
							continue
						}
						typeName := ""
						switch t := vs.Type.(type) {
						case *ast.Ident:
							typeName = t.Name
						case *ast.SelectorExpr:
							continue
						}
						if typeName == "" {
							continue
						}

						for _, val := range vs.Values {
							v, ok := resolveConstString(val, constLiterals)
							if !ok {
								continue
							}
							if enums[typeName] == nil {
								enums[typeName] = &enumInfo{}
							}
							enums[typeName].values = append(enums[typeName].values, v)
						}
					}
				}

				return true
			})
		}
	}

	// Set base types for enums
	for name, ei := range enums {
		if baseType, ok := aliasTypes[name]; ok {
			ei.baseType = baseType
		}
	}

	// Build definitions for all custom types
	definitions := make(map[string]*Definition)

	for name, ti := range allTypes {
		if isBasicType(name) || isKubernetesType(name) {
			continue
		}

		structType, isStruct := ti.typeSpec.Type.(*ast.StructType)

		if isStruct {
			def := &Definition{
				Kind:        "struct",
				Description: getTypeDescription(ti),
			}

			for _, field := range structType.Fields.List {
				fieldDefs := processField(field, allTypes, structTypes, aliasTypes, enums)
				def.Fields = append(def.Fields, fieldDefs...)
			}

			definitions[name] = def
		} else if ei, isEnum := enums[name]; isEnum {
			def := &Definition{
				Kind:        "enum",
				Description: getTypeDescription(ti),
				BaseType:    ei.baseType,
				EnumValues:  ei.values,
			}
			definitions[name] = def
		} else if baseType, isAlias := aliasTypes[name]; isAlias {
			def := &Definition{
				Kind:        "alias",
				Description: getTypeDescription(ti),
				BaseType:    baseType,
			}
			definitions[name] = def
		}
	}

	// Build resources map
	resources := make(map[string]*ResourceInfo)

	for _, crdName := range mainCRDs {
		ti, exists := allTypes[crdName]
		if !exists {
			fmt.Printf("Warning: CRD type %s not found\n", crdName)
			continue
		}

		ri := &ResourceInfo{
			Description: getTypeDescription(ti),
		}

		specName := crdName + "Spec"
		if _, ok := definitions[specName]; ok {
			ri.Spec = "#/definitions/" + specName
		}
		statusName := crdName + "Status"
		if _, ok := definitions[statusName]; ok {
			ri.Status = "#/definitions/" + statusName
		}

		resources[crdName] = ri
	}

	// Compute usedBy for each definition
	computeUsedBy(definitions, mainCRDs)

	return &Schema{
		APIVersion:  "v1alpha1",
		Resources:   resources,
		Definitions: definitions,
	}
}

func writeSchemaJSON(schema *Schema, outputDir string) {
	outputPath := filepath.Join(outputDir, "api-schema.json")
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		fmt.Printf("Error marshaling JSON: %v\n", err)
		os.Exit(1)
	}

	err = os.WriteFile(outputPath, data, 0o644)
	if err != nil {
		fmt.Printf("Error writing file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Generated %s (%d resources, %d definitions)\n", outputPath, len(schema.Resources), len(schema.Definitions))
}

// ---- Phase 2: Generate markdown docs from schema ----

func generateMarkdownDocs(schema *Schema, outputDir string) {
	var crdNames []string
	for name := range schema.Resources {
		crdNames = append(crdNames, name)
	}
	sort.Strings(crdNames)

	for _, crdName := range crdNames {
		generateCRDMarkdown(crdName, schema, outputDir)
	}

	fmt.Printf("Generated %d CRD documentation files in %s\n", len(crdNames), outputDir)
}

func generateCRDMarkdown(crdName string, schema *Schema, outputDir string) {
	filename := filepath.Join(outputDir, strings.ToLower(crdName)+".md")

	f, err := os.Create(filename)
	if err != nil {
		fmt.Printf("Error creating file %s: %v\n", filename, err)
		return
	}
	defer f.Close() //nolint:errcheck // best-effort close on a file opened for writing; a real failure already surfaced at Write/Sync time

	fmt.Fprintf(f, "# %s\n\n", crdName) //nolint:errcheck // best-effort doc generation; a write failure would already be surfaced by a later os.WriteFile/f.Close error, or is simply not actionable for this internal tool

	// Find related types via BFS through $ref links (structs only)
	relatedTypes := findRelatedTypes(crdName, schema)

	// Write table of contents
	fmt.Fprintf(f, "## API Types\n\n") //nolint:errcheck // best-effort doc generation; a write failure would already be surfaced by a later os.WriteFile/f.Close error, or is simply not actionable for this internal tool
	for _, typeName := range relatedTypes {
		fmt.Fprintf(f, "- [%s](#%s)\n", typeName, strings.ToLower(typeName)) //nolint:errcheck // best-effort doc generation; a write failure would already be surfaced by a later os.WriteFile/f.Close error, or is simply not actionable for this internal tool
	}
	fmt.Fprintf(f, "\n---\n\n") //nolint:errcheck // best-effort doc generation; a write failure would already be surfaced by a later os.WriteFile/f.Close error, or is simply not actionable for this internal tool

	// Generate documentation for each related type
	for _, typeName := range relatedTypes {
		def, exists := schema.Definitions[typeName]
		if !exists {
			continue
		}
		generateTypeSection(f, typeName, def)
	}

	fmt.Printf("Generated: %s\n", filename)
}

func findRelatedTypes(crdName string, schema *Schema) []string {
	var related []string
	visited := make(map[string]bool)

	if _, exists := schema.Definitions[crdName]; exists {
		related = append(related, crdName)
		visited[crdName] = true
	}

	for _, suffix := range []string{"Spec", "Status", "List"} {
		candidate := crdName + suffix
		if _, exists := schema.Definitions[candidate]; exists && !visited[candidate] {
			related = append(related, candidate)
			visited[candidate] = true
		}
	}

	// BFS from spec and status to find all referenced struct types
	queue := []string{crdName + "Spec", crdName + "Status"}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		def, exists := schema.Definitions[current]
		if !exists || def.Kind != "struct" {
			continue
		}

		for _, field := range def.Fields {
			refs := extractTypeRefs(field.Type)
			for _, ref := range refs {
				refDef, exists := schema.Definitions[ref]
				if !exists || visited[ref] {
					continue
				}
				if refDef.Kind != "struct" {
					continue
				}
				related = append(related, ref)
				visited[ref] = true
				queue = append(queue, ref)
			}
		}
	}

	return related
}

func generateTypeSection(f *os.File, typeName string, def *Definition) {
	fmt.Fprintf(f, "## %s\n\n", typeName) //nolint:errcheck // best-effort doc generation; a write failure would already be surfaced by a later os.WriteFile/f.Close error, or is simply not actionable for this internal tool

	// Write fields table for struct types (skip embedded/metadata fields)
	hasFields := false
	for _, field := range def.Fields {
		if !field.Embedded {
			hasFields = true
			break
		}
	}

	if hasFields {
		fmt.Fprintf(f, "| JSON Field | Type | Description |\n") //nolint:errcheck // best-effort doc generation; a write failure would already be surfaced by a later os.WriteFile/f.Close error, or is simply not actionable for this internal tool
		fmt.Fprintf(f, "|------------|------|-------------|\n") //nolint:errcheck // best-effort doc generation; a write failure would already be surfaced by a later os.WriteFile/f.Close error, or is simply not actionable for this internal tool

		for _, field := range def.Fields {
			if field.Embedded {
				continue
			}
			goType := typeToGoString(field.Type, field.Pointer)
			desc := strings.ReplaceAll(field.Description, "\n", "<br>")
			desc = strings.ReplaceAll(desc, "|", "\\|")
			fmt.Fprintf(f, "| %s | %s | %s |\n", field.JSONName, goType, desc) //nolint:errcheck // best-effort doc generation; a write failure would already be surfaced by a later os.WriteFile/f.Close error, or is simply not actionable for this internal tool
		}
		fmt.Fprintf(f, "\n") //nolint:errcheck // best-effort doc generation; a write failure would already be surfaced by a later os.WriteFile/f.Close error, or is simply not actionable for this internal tool
	}

	fmt.Fprintf(f, "---\n\n") //nolint:errcheck // best-effort doc generation; a write failure would already be surfaced by a later os.WriteFile/f.Close error, or is simply not actionable for this internal tool
}

// typeToGoString converts a JSON type representation back to a Go-style type string.
func typeToGoString(t interface{}, pointer bool) string {
	prefix := ""
	if pointer {
		prefix = "*"
	}

	switch v := t.(type) {
	case string:
		if strings.HasPrefix(v, "k8s:") {
			return prefix + strings.TrimPrefix(v, "k8s:")
		}
		return prefix + v

	case RefType:
		name := strings.TrimPrefix(v.Ref, "#/definitions/")
		return prefix + name

	case ArrayType:
		items := typeToGoString(v.Items, false)
		return prefix + "[]" + items

	case MapType:
		value := typeToGoString(v.Value, false)
		return prefix + fmt.Sprintf("map[%s]%s", v.Key, value)
	}

	return "unknown"
}

// ---- Shared helpers ----

// processField converts an AST field into one or more FieldDef entries.
func processField(field *ast.Field, allTypes map[string]*typeInfo, structTypes map[string]bool, aliasTypes map[string]string, enums map[string]*enumInfo) []FieldDef {
	jsonTag := getJSONTag(field)

	if jsonTag == "-" {
		return nil
	}

	if strings.Contains(getRawJSONTag(field), ",inline") {
		return nil
	}

	_, isPointer := field.Type.(*ast.StarExpr)
	optional := isOptionalField(field)

	// Handle anonymous/embedded fields
	if len(field.Names) == 0 {
		typeName := resolveTypeName(field.Type)
		if typeName == "" {
			return nil
		}
		jsonName := jsonTag
		if jsonName == "" {
			jsonName = toCamelCase(typeName)
		}
		fieldType := resolveFieldType(field.Type, allTypes, structTypes, aliasTypes, enums)
		return []FieldDef{{
			Name:        typeName,
			JSONName:    jsonName,
			Type:        fieldType,
			Description: getFieldDescription(field),
			Optional:    optional,
			Pointer:     isPointer,
			Embedded:    true,
		}}
	}

	var results []FieldDef
	for _, name := range field.Names {
		if !name.IsExported() {
			continue
		}

		jsonName := jsonTag
		if jsonName == "" {
			jsonName = toCamelCase(name.Name)
		}

		fieldType := resolveFieldType(field.Type, allTypes, structTypes, aliasTypes, enums)

		results = append(results, FieldDef{
			Name:        name.Name,
			JSONName:    jsonName,
			Type:        fieldType,
			Description: getFieldDescription(field),
			Optional:    optional,
			Pointer:     isPointer,
		})
	}
	return results
}

// resolveFieldType converts an ast.Expr to a JSON-compatible type representation.
func resolveFieldType(expr ast.Expr, allTypes map[string]*typeInfo, structTypes map[string]bool, aliasTypes map[string]string, enums map[string]*enumInfo) interface{} {
	switch t := expr.(type) {
	case *ast.Ident:
		name := t.Name
		if isBasicType(name) {
			return name
		}
		if structTypes[name] || enums[name] != nil || aliasTypes[name] != "" {
			return RefType{Ref: "#/definitions/" + name}
		}
		return name

	case *ast.StarExpr:
		return resolveFieldType(t.X, allTypes, structTypes, aliasTypes, enums)

	case *ast.ArrayType:
		itemType := resolveFieldType(t.Elt, allTypes, structTypes, aliasTypes, enums)
		return ArrayType{Type: "array", Items: itemType}

	case *ast.MapType:
		keyType := resolveFieldType(t.Key, allTypes, structTypes, aliasTypes, enums)
		valType := resolveFieldType(t.Value, allTypes, structTypes, aliasTypes, enums)

		keyStr := "string"
		if s, ok := keyType.(string); ok {
			keyStr = s
		}

		return MapType{Type: "map", Key: keyStr, Value: valType}

	case *ast.SelectorExpr:
		pkgName := ""
		if ident, ok := t.X.(*ast.Ident); ok {
			pkgName = ident.Name
		}
		return "k8s:" + pkgName + "." + t.Sel.Name

	default:
		return "unknown"
	}
}

func resolveTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return resolveTypeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	default:
		return ""
	}
}

func isOptionalField(field *ast.Field) bool {
	if _, ok := field.Type.(*ast.StarExpr); ok {
		return true
	}
	rawTag := getRawJSONTag(field)
	return strings.Contains(rawTag, "omitempty")
}

func getJSONTag(field *ast.Field) string {
	if field.Tag == nil {
		return ""
	}
	tag := strings.Trim(field.Tag.Value, "`")
	jsonStart := strings.Index(tag, `json:"`)
	if jsonStart == -1 {
		return ""
	}
	jsonStart += 6
	jsonEnd := strings.Index(tag[jsonStart:], `"`)
	if jsonEnd == -1 {
		return ""
	}
	jsonTag := tag[jsonStart : jsonStart+jsonEnd]
	parts := strings.Split(jsonTag, ",")
	name := parts[0]
	if name == "-" {
		return "-"
	}
	return name
}

func getRawJSONTag(field *ast.Field) string {
	if field.Tag == nil {
		return ""
	}
	tag := strings.Trim(field.Tag.Value, "`")
	jsonStart := strings.Index(tag, `json:"`)
	if jsonStart == -1 {
		return ""
	}
	jsonStart += 6
	jsonEnd := strings.Index(tag[jsonStart:], `"`)
	if jsonEnd == -1 {
		return ""
	}
	return tag[jsonStart : jsonStart+jsonEnd]
}

func getTypeDescription(ti *typeInfo) string {
	if ti.typeSpec.Doc != nil {
		return extractCommentText(ti.typeSpec.Doc)
	}

	for _, decl := range ti.file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != ti.name {
				continue
			}
			if len(genDecl.Specs) == 1 && genDecl.Doc != nil {
				return extractCommentText(genDecl.Doc)
			}
		}
	}

	return ""
}

func extractCommentText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	var lines []string
	for _, c := range cg.List {
		text := cleanComment(c.Text)
		if text != "" {
			lines = append(lines, text)
		}
	}
	return strings.Join(lines, "\n")
}

func getFieldDescription(field *ast.Field) string {
	var lines []string

	if field.Doc != nil {
		for _, c := range field.Doc.List {
			text := cleanComment(c.Text)
			if text != "" {
				lines = append(lines, text)
			}
		}
	}

	if field.Comment != nil {
		for _, c := range field.Comment.List {
			text := cleanComment(c.Text)
			if text != "" {
				lines = append(lines, text)
			}
		}
	}

	return strings.Join(lines, "\n")
}

func cleanComment(text string) string {
	text = strings.TrimPrefix(text, "//")
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "+") {
		return ""
	}
	return text
}

func toCamelCase(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func isBasicType(typeName string) bool {
	basicTypes := map[string]bool{
		"string": true, "int": true, "int32": true, "int64": true,
		"bool": true, "float32": true, "float64": true, "byte": true,
		"uint": true, "uint32": true, "uint64": true, "rune": true,
	}
	return basicTypes[typeName]
}

func isKubernetesType(typeName string) bool {
	k8sTypes := map[string]bool{
		"ObjectMeta": true, "TypeMeta": true, "ListMeta": true,
		"Time": true, "Duration": true, "Quantity": true,
		"Pod": true, "Service": true, "Deployment": true,
	}
	return k8sTypes[typeName]
}

// extractTypeRefs extracts definition names referenced in a type value.
func extractTypeRefs(t interface{}) []string {
	switch v := t.(type) {
	case RefType:
		name := strings.TrimPrefix(v.Ref, "#/definitions/")
		return []string{name}
	case ArrayType:
		return extractTypeRefs(v.Items)
	case MapType:
		return extractTypeRefs(v.Value)
	}
	return nil
}

// computeUsedBy populates the UsedBy field on each definition by tracing
// which main CRD resources reference each type (directly or transitively).
func computeUsedBy(definitions map[string]*Definition, crdNames []string) {
	for _, crdName := range crdNames {
		visited := make(map[string]bool)
		queue := []string{crdName + "Spec", crdName + "Status"}

		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]

			if visited[current] {
				continue
			}
			visited[current] = true

			def, ok := definitions[current]
			if !ok {
				continue
			}

			for _, field := range def.Fields {
				refs := extractTypeRefs(field.Type)
				for _, ref := range refs {
					if !visited[ref] {
						queue = append(queue, ref)
					}
				}
			}
		}

		for typeName := range visited {
			if def, ok := definitions[typeName]; ok {
				def.UsedBy = append(def.UsedBy, crdName)
			}
		}
	}

	for _, def := range definitions {
		if len(def.UsedBy) > 0 {
			sort.Strings(def.UsedBy)
		}
	}
}
