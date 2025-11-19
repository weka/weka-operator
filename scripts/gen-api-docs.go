package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	sourceDir := "./pkg/weka-k8s-api/api/v1alpha1"
	outputDir := "./doc/api_dump"

	// Create output directory
	os.MkdirAll(outputDir, 0755)

	// Parse Go files
	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, sourceDir, nil, parser.ParseComments)
	if err != nil {
		fmt.Printf("Error parsing directory: %v\n", err)
		return
	}

	// Collect all types
	allTypes := make(map[string]*TypeInfo)
	var mainCRDs []string

	for _, pkg := range packages {
		for filename, file := range pkg.Files {
			if strings.HasSuffix(filename, "_test.go") || strings.HasSuffix(filename, "zz_generated.deepcopy.go") {
				continue
			}

			// Create a map of line positions to comments for faster lookup
			commentMap := make(map[int]bool)
			for _, commentGroup := range file.Comments {
				for _, comment := range commentGroup.List {
					if strings.Contains(comment.Text, "+kubebuilder:object:root=true") {
						pos := fset.Position(comment.Pos())
						// Mark lines around the comment as potential CRD locations
						for i := -3; i <= 10; i++ {
							commentMap[pos.Line+i] = true
						}
						// Debug: fmt.Printf("Found kubebuilder annotation at line %d\n", pos.Line)
					}
				}
			}

			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.TypeSpec:
					if structType, ok := node.Type.(*ast.StructType); ok {
						typePos := fset.Position(node.Pos())
						// Check a wider range around the type position
						isCRDType := false
						for i := -15; i <= 5; i++ {
							if commentMap[typePos.Line+i] {
								isCRDType = true
								break
							}
						}
						// Debug cluster/container detection
						// if strings.Contains(node.Name.Name, "WekaCluster") || strings.Contains(node.Name.Name, "WekaContainer") {
						//     fmt.Printf("Checking %s at line %d, isCRD: %v\n", node.Name.Name, typePos.Line, isCRDType)
						// }

						typeInfo := &TypeInfo{
							Name:     node.Name.Name,
							TypeSpec: node,
							Struct:   structType,
							File:     file,
							IsCRD:    isCRDType,
						}
						allTypes[typeInfo.Name] = typeInfo

						// Only consider types ending with base resource name (not List types)
						// and filter out non-main resource types
						if typeInfo.IsCRD && !strings.HasSuffix(typeInfo.Name, "List") && isMainResourceType(typeInfo.Name) {
							mainCRDs = append(mainCRDs, typeInfo.Name)
							fmt.Printf("Found main CRD: %s\n", typeInfo.Name)
						}
					}
				}
				return true
			})
		}
	}

	// Sort main CRDs
	sort.Strings(mainCRDs)

	// Generate one file per main CRD with all related types
	for _, crdName := range mainCRDs {
		generateCRDMarkdown(crdName, allTypes, outputDir, fset)
	}

	fmt.Printf("Generated %d CRD documentation files in %s\n", len(mainCRDs), outputDir)
}

type TypeInfo struct {
	Name     string
	TypeSpec *ast.TypeSpec
	Struct   *ast.StructType
	File     *ast.File
	IsCRD    bool
}

func isCRD(typeSpec *ast.TypeSpec, file *ast.File) bool {
	// Look for kubebuilder annotations in type's comment group
	if typeSpec.Comment != nil {
		for _, comment := range typeSpec.Comment.List {
			if strings.Contains(comment.Text, "+kubebuilder:object:root=true") {
				fmt.Printf("Found CRD in comment: %s\n", typeSpec.Name.Name)
				return true
			}
		}
	}

	// Also check doc comments (comments before the type)
	if typeSpec.Doc != nil {
		for _, comment := range typeSpec.Doc.List {
			if strings.Contains(comment.Text, "+kubebuilder:object:root=true") {
				fmt.Printf("Found CRD in doc: %s\n", typeSpec.Name.Name)
				return true
			}
		}
	}

	return false
}

func isMainResourceType(typeName string) bool {
	// Skip DriveClaim - deprecated and will be removed from API later
	if typeName == "DriveClaim" {
		return false
	}

	// Only include main CRD types, exclude helper/payload types
	mainTypes := map[string]bool{
		"WekaCluster":         true,
		"WekaPolicy":          true,
		"WekaContainer":       true,
		"WekaClient":          true,
		"WekaManualOperation": true,
	}
	return mainTypes[typeName]
}

func generateCRDMarkdown(crdName string, allTypes map[string]*TypeInfo, outputDir string, fset *token.FileSet) {
	filename := filepath.Join(outputDir, strings.ToLower(crdName)+".md")

	f, err := os.Create(filename)
	if err != nil {
		fmt.Printf("Error creating file %s: %v\n", filename, err)
		return
	}
	defer f.Close()

	// Write header
	fmt.Fprintf(f, "# %s\n\n", crdName)

	// Find related types
	relatedTypes := findRelatedTypes(crdName, allTypes)

	// Write table of contents
	fmt.Fprintf(f, "## API Types\n\n")
	for _, typeName := range relatedTypes {
		fmt.Fprintf(f, "- [%s](#%s)\n", typeName, strings.ToLower(typeName))
	}
	fmt.Fprintf(f, "\n---\n\n")

	// Generate documentation for each related type
	for _, typeName := range relatedTypes {
		if typeInfo, exists := allTypes[typeName]; exists {
			generateTypeSection(f, typeInfo)
		}
	}

	fmt.Printf("Generated: %s\n", filename)
}

func findRelatedTypes(crdName string, allTypes map[string]*TypeInfo) []string {
	var related []string
	visited := make(map[string]bool)

	// Add the main CRD first
	related = append(related, crdName)
	visited[crdName] = true

	// Add spec, status, and list types
	candidates := []string{
		crdName + "Spec",
		crdName + "Status",
		crdName + "List",
	}

	for _, candidate := range candidates {
		if _, exists := allTypes[candidate]; exists && !visited[candidate] {
			related = append(related, candidate)
			visited[candidate] = true
		}
	}

	// Find types referenced by spec and status
	queue := []string{crdName + "Spec", crdName + "Status"}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if typeInfo, exists := allTypes[current]; exists {
			referencedTypes := getReferencedTypes(typeInfo.Struct)
			for _, refType := range referencedTypes {
				if _, exists := allTypes[refType]; exists && !visited[refType] && !isKubernetesType(refType) {
					related = append(related, refType)
					visited[refType] = true
					queue = append(queue, refType)
				}
			}
		}
	}

	return related
}

func getReferencedTypes(structType *ast.StructType) []string {
	var types []string

	for _, field := range structType.Fields.List {
		fieldTypes := extractFieldTypes(field.Type)
		types = append(types, fieldTypes...)
	}

	return types
}

func extractFieldTypes(expr ast.Expr) []string {
	var types []string

	switch t := expr.(type) {
	case *ast.Ident:
		if !isBasicType(t.Name) {
			types = append(types, t.Name)
		}
	case *ast.StarExpr:
		types = append(types, extractFieldTypes(t.X)...)
	case *ast.ArrayType:
		types = append(types, extractFieldTypes(t.Elt)...)
	case *ast.MapType:
		types = append(types, extractFieldTypes(t.Key)...)
		types = append(types, extractFieldTypes(t.Value)...)
	case *ast.SelectorExpr:
		// Skip external types like v1.Pod, metav1.Time, etc.
		return types
	}

	return types
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
	// Skip common Kubernetes types that would clutter the docs
	k8sTypes := map[string]bool{
		"ObjectMeta": true, "TypeMeta": true, "ListMeta": true,
		"Time": true, "Duration": true, "Quantity": true,
		"Pod": true, "Service": true, "Deployment": true,
	}
	return k8sTypes[typeName]
}

func generateTypeSection(f *os.File, typeInfo *TypeInfo) {
	typeName := typeInfo.Name

	// Write type header
	fmt.Fprintf(f, "## %s\n\n", typeName)

	// Write type description from comments
	if typeInfo.TypeSpec.Comment != nil {
		hasDescription := false
		for _, comment := range typeInfo.TypeSpec.Comment.List {
			text := strings.TrimPrefix(comment.Text, "//")
			text = strings.TrimSpace(text)
			if !strings.HasPrefix(text, "+") && text != "" { // Skip kubebuilder annotations and empty lines
				fmt.Fprintf(f, "%s\n", text)
				hasDescription = true
			}
		}
		if hasDescription {
			fmt.Fprintf(f, "\n")
		}
	}

	// Write fields table
	if len(typeInfo.Struct.Fields.List) > 0 {
		fmt.Fprintf(f, "| JSON Field | Type | Description |\n")
		fmt.Fprintf(f, "|------------|------|-------------|\n")

		for _, field := range typeInfo.Struct.Fields.List {
			if field.Names != nil {
				for _, name := range field.Names {
					// Skip embedded fields (usually metadata)
					if name.IsExported() {
						jsonName := getJSONFieldName(field)
						fieldType := getTypeName(field.Type)
						description := getFieldDescription(field, typeInfo.File)
						fmt.Fprintf(f, "| %s | %s | %s |\n", jsonName, fieldType, description)
					}
				}
			}
		}
		fmt.Fprintf(f, "\n")
	}

	fmt.Fprintf(f, "---\n\n")
}

func getTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return fmt.Sprintf("%s.%s", getTypeName(t.X), t.Sel.Name)
	case *ast.StarExpr:
		return "*" + getTypeName(t.X)
	case *ast.ArrayType:
		return "[]" + getTypeName(t.Elt)
	case *ast.MapType:
		return fmt.Sprintf("map[%s]%s", getTypeName(t.Key), getTypeName(t.Value))
	default:
		return "unknown"
	}
}

func getJSONFieldName(field *ast.Field) string {
	if field.Tag != nil {
		tag := strings.Trim(field.Tag.Value, "`")
		// Extract json tag
		if jsonStart := strings.Index(tag, `json:"`); jsonStart != -1 {
			jsonStart += 6 // len(`json:"`)
			jsonEnd := strings.Index(tag[jsonStart:], `"`)
			if jsonEnd != -1 {
				jsonTag := tag[jsonStart : jsonStart+jsonEnd]
				// Handle json:",omitempty" or json:"fieldName,omitempty"
				jsonName := strings.Split(jsonTag, ",")[0]
				if jsonName != "" && jsonName != "-" {
					return jsonName
				}
			}
		}
	}

	// If no json tag, return field name in camelCase
	if len(field.Names) > 0 {
		return toCamelCase(field.Names[0].Name)
	}
	return ""
}

func toCamelCase(s string) string {
	if s == "" {
		return ""
	}
	// Convert first letter to lowercase
	return strings.ToLower(s[:1]) + s[1:]
}

func getFieldDescription(field *ast.Field, file *ast.File) string {
	var lines []string

	// Check doc comments (comments above the field)
	if field.Doc != nil {
		for _, comment := range field.Doc.List {
			text := cleanComment(comment.Text)
			if text != "" {
				lines = append(lines, text)
			}
		}
	}

	// Check line comments (comments on the same line)
	if field.Comment != nil {
		for _, comment := range field.Comment.List {
			text := cleanComment(comment.Text)
			if text != "" {
				lines = append(lines, text)
			}
		}
	}

	return strings.Join(lines, "<br>")
}

func cleanComment(text string) string {
	text = strings.TrimPrefix(text, "//")
	text = strings.TrimSpace(text)
	// Skip kubebuilder annotations
	if strings.HasPrefix(text, "+") {
		return ""
	}
	// Escape pipes to avoid breaking markdown tables
	text = strings.ReplaceAll(text, "|", "\\|")
	return text
}
