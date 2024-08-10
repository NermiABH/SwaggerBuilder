package main

import (
	"cmp"
	"flag"
	"fmt"
	"github.com/getkin/kin-openapi/openapi3"
	"go/parser"
	"go/token"
	"gopkg.in/yaml.v2"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

func main() {
	dirPath := flag.String("dir", "", "Path to the directory containing Go source files")
	outPut := flag.String("output", "", "Path output")
	flag.Parse()

	if *dirPath == "" {
		*dirPath = "./"
	}
	if *outPut == "" {
		*outPut = "./swagger.yaml"
	}

	if _, err := os.Stat(*dirPath); os.IsNotExist(err) {
		fmt.Println("Directory does not exist:", *dirPath)
		return
	}

	swaggerParts := make([]string, 0)
	err := filepath.Walk(*dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Обрабатываем только Go файлы
		if !info.IsDir() && strings.HasSuffix(path, ".go") {
			processFile(path, &swaggerParts)
		}
		return nil
	})

	if err != nil {
		fmt.Println("Error walking the path:", err)
	}

	err = buildSwaggerFile(*outPut, swaggerParts)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Done", " output: ", *outPut)
}

func processFile(filePath string, swaggerParts *[]string) {
	fset := token.NewFileSet()

	node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		fmt.Println("Error parsing file:", err)
		return
	}

	for _, commentGroup := range node.Comments {
		var swaggerPart string
		comments := commentGroup.List
		if len(comments) < 0 {
			continue
		}
		s := comments[0].Text
		if strings.HasPrefix(s, "//") {
			s = strings.TrimPrefix(s, "//")
		} else if strings.HasPrefix(s, "/*") {
			s = strings.TrimPrefix(s, "/*")
		} else {
			continue
		}
		s = strings.TrimSpace(s)
		if !strings.HasPrefix(s, "swagger:") {
			continue
		}
		for _, comment := range comments {
			var s string
			s = comment.Text
			if strings.HasPrefix(s, "//") {
				s = strings.TrimPrefix(s, "//")
				s = strings.TrimLeftFunc(s, unicode.IsSpace)
			} else if strings.HasPrefix(s, "/*") {
				s = strings.TrimPrefix(s, "/*")
				s = strings.TrimLeftFunc(s, unicode.IsSpace)
				if strings.HasSuffix(s, "*/") {
					//fmt.Println(s, strings.HasSuffix(s, "*/"), strings.TrimSuffix(s, "*/"))
					s = strings.TrimSuffix(s, "*/")
				}
			}
			s = strings.ReplaceAll(s, "\t", "    ")
			if strings.TrimSpace(s) == "" {
				continue
			}
			//fmt.Println(s, strings.HasSuffix(s, "*/"), strings.TrimSuffix(s, "*/"))
			swaggerPart += s + "\n"
		}
		*swaggerParts = append(*swaggerParts, swaggerPart)
	}
}

func createSwagger(swaggerParts []string) (string, error) {
	swagger := NewSwagger()
	for _, part := range swaggerParts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		var err error
		part, err = validateAndTrimYAML(part)
		if err != nil {
			return "", fmt.Errorf("err: %v", err)
		}
		switch {
		case strings.HasPrefix(part, "swagger:main"):
			part = part[len("swagger:main")+1:]
			if err := swagger.CreateMain(part); err != nil {
				return "", err
			}
		case strings.HasPrefix(part, "swagger:operation"):
			part = part[len("swagger:operation")+1:]
			if err := swagger.CreateOperation(part); err != nil {
				return "", err
			}
		case strings.HasPrefix(part, "swagger:components"):
			part = part[len("swagger:components")+1:]
			if err := swagger.CreateComponents(part); err != nil {
				return "", err
			}
		default:
			log.Fatal("Unknown part: " + part)
		}
	}

	newSwagger := swagger.String()
	err := validateSwagger(newSwagger)
	if err != nil {
		return "", err
	}
	return newSwagger, nil
}

func validateAndTrimYAML(yamlContent string) (string, error) {
	parts := strings.SplitN(yamlContent, "\n", 2)
	if len(parts) < 2 {
		return "", fmt.Errorf("error split n")
	}
	if strings.TrimSpace(parts[1]) == "" {
		return parts[0], nil
	}
	var temp interface{}
	err := yaml.Unmarshal([]byte(parts[1]), &temp)
	if err != nil {
		fmt.Println(parts[0], parts[1])
		return "", fmt.Errorf("invalid YAML: %v; text: \n%s", err, parts[1])
	}

	formattedYAML, err := yaml.Marshal(temp)
	if err != nil {
		return "", fmt.Errorf("unable to format YAML: %v; text: \n%s", err, temp)
	}

	return parts[0] + "\n" + string(formattedYAML), nil
}

func buildSwaggerFile(filename string, swaggerParts []string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	s, err := createSwagger(swaggerParts)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(s); err != nil {
		return err
	}
	return err
}

type Swagger struct {
	mainSet    bool
	main       string
	operations map[string]string
	components map[string]string
}

func NewSwagger() *Swagger {
	return &Swagger{
		operations: make(map[string]string),
		components: make(map[string]string),
	}
}

func (s *Swagger) CreateMain(part string) error {
	if s.mainSet {
		return fmt.Errorf("main section already set")
	}
	lines := strings.Split(part, "\n")
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimmedLine, "components:") || strings.HasPrefix(trimmedLine, "paths:") {
			return fmt.Errorf("main section should not contain 'components:' or 'paths:'")
		}
	}
	s.main = part
	s.mainSet = true
	return nil
}

func (s *Swagger) CreateOperation(part string) error {
	lines := strings.Split(part, "\n")
	header := strings.Fields(lines[0])
	if len(header) < 3 {
		return fmt.Errorf("invalid operation part: %s", part)
	}
	method := header[0]
	path := header[1]
	operationID := header[2]

	operationContent := fmt.Sprintf("  %s:\n    operationId: %s\n", method, operationID)
	for _, line := range lines[1:] {
		operationContent += "    " + strings.ReplaceAll(line, "\t", "    ") + "\n"
	}

	if existingContent, exists := s.operations[path]; exists {
		s.operations[path] = existingContent + "\n" + operationContent
	} else {
		s.operations[path] = operationContent
	}

	return nil
}

func (s *Swagger) CreateComponents(part string) error {
	lines := strings.Split(part, "\n")
	header := strings.Fields(lines[0])
	if len(header) < 1 {
		return fmt.Errorf("invalid component part: %s", part)
	}
	componentType := header[0]

	componentContent := ""
	for _, line := range lines[1:] {
		componentContent += "  " + strings.ReplaceAll(line, "\t", "    ") + "\n"
	}

	if existingContent, exists := s.components[componentType]; exists {
		s.components[componentType] = existingContent + "\n" + componentContent
	} else {
		s.components[componentType] = componentContent
	}

	return nil
}

func (s *Swagger) String() string {
	var sb strings.Builder
	sb.WriteString(s.main)

	if len(s.operations) > 0 {
		sb.WriteString("\npaths:\n")
		var operations [][2]string
		for path, op := range s.operations {
			operations = append(operations, [2]string{path, op})
		}
		slices.SortFunc(operations, func(a, b [2]string) int {
			return cmp.Compare(a[0], b[0])
		})
		for _, v := range operations {
			path, op := v[0], v[1]
			sb.WriteString(fmt.Sprintf("  %s:\n", path))
			contentLines := strings.Split(op, "\n")
			for _, line := range contentLines {
				if strings.TrimSpace(line) != "" {
					sb.WriteString(fmt.Sprintf("    %s\n", line))
				}
			}
		}
	}

	if len(s.components) > 0 {
		sb.WriteString("components:\n")
		for compType, compContents := range s.components {
			sb.WriteString(fmt.Sprintf("  %s:\n", compType))
			contentLines := strings.Split(compContents, "\n")
			for _, line := range contentLines {
				if strings.TrimSpace(line) != "" {
					sb.WriteString(fmt.Sprintf("    %s\n", line))
				}
			}
		}
	}

	return sb.String()
}

func validateSwagger(content string) error {
	loader := openapi3.NewLoader()
	v, err := loader.LoadFromData([]byte(content))
	if err != nil {
		return err
	}
	return v.Validate(loader.Context)
}

// swagger:operation [method] [path pattern] [?tag1 tag2 tag3] [operation id]

// swagger:components [component]

// swagger:main
