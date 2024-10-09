package builder

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"svelte-ssr-to-templ/builder/parser"
	"svelte-ssr-to-templ/builder/types"

	"golang.org/x/sync/errgroup"
)

var findPropertyRegex = regexp.MustCompile(`svelte-[a-zA-Z0-9-\[\]{}, ]+-`)
var regexWithQuotes = regexp.MustCompile(`["']{ props.[a-zA-Z0-9.]+ }["']`)

var typeRegex = regexp.MustCompile(`((\[\])?(string|int|bool))|({(string|int|bool), (\[\])?(string|int|bool)})|(\[\])`)

var newLine = regexp.MustCompile(`\s+`)
var catWhiskers = regexp.MustCompile(`> <`)

type BuildOptions struct {
	QueueDir       string // Relative path
	FullQueueDir   string // Absolute path
	OutputBuildDir string // Relative path
	ExecutablePath string // Absolute path to the executable
	WaitGroup      *errgroup.Group
}

func Build(opts *BuildOptions) {
	// Recursively process all files in the queue directory
	searchPath := opts.ExecutablePath + opts.QueueDir
	err := filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		filename := info.Name()
		if strings.HasSuffix(filename, ".html") {
			path = strings.ReplaceAll(path, "\\", "/")
			index := strings.Index(path, opts.FullQueueDir)
			path = path[index:]
			path = strings.Replace(path, opts.FullQueueDir, "", 1)

			// Remove the name of the file from the path
			path = strings.TrimSuffix(path, filename)

			opts.WaitGroup.Go(func() error {
				processHTMLFile(path, info.Name(), opts)
				return nil
			})
		}
		return nil
	})
	if err != nil {
		log.Fatalf("Error walking queue directory: %s", err)
	}
	err = opts.WaitGroup.Wait()
	if err != nil {
		log.Fatalf("Error processing files: %s", err)
	}
}

func processHTMLFile(path string, filename string, opts *BuildOptions) {
	packageName := strings.TrimSuffix(filename, ".html")
	var props map[string]*parser.Property
	wg := errgroup.Group{}
	wg.Go(func() error {
		dir := opts.ExecutablePath + opts.OutputBuildDir + path + packageName
		err := os.MkdirAll(dir, os.ModePerm)
		return err
	})
	wg.Go(func() error {
		props = parseHTMLFile(path, filename, opts)
		return nil
	})

	err := wg.Wait()
	if err != nil {
		panic(err)
	}

	props = promoteProperty(props)
	opts.WaitGroup.Go(func() error {
		trimmed := strings.TrimSuffix(filename, ".html")
		generateStructs(props, path, &trimmed, &packageName, opts)
		return nil
	})
	opts.WaitGroup.Go(func() error {
		replacePlaceholders(props, path, filename, packageName, opts)
		return nil
	})
}

func promoteProperty(props map[string]*parser.Property) map[string]*parser.Property {
	// If a child property has only one child, promote the child property to be a
	// field of the parent property.
	for _, prop := range props {
		promoteProperty(prop.Children)

		if len(prop.Children) == 1 {
			prop.Children = nil
		}
	}
	return props
}

func replacePlaceholders(
	props map[string]*parser.Property,
	path string,
	filename string,
	packageName string,
	opts *BuildOptions,
) {
	exPath := opts.ExecutablePath
	queueDir := opts.QueueDir
	outputBuildDir := opts.OutputBuildDir

	inputFile, err := os.Open(exPath + queueDir + path + filename)
	if err != nil {
		panic(err)
	}
	defer inputFile.Close()

	outputFilename := exPath + outputBuildDir + path + packageName + "/" + strings.TrimSuffix(filename, ".html") + ".templ"
	outputFile, err := os.Create(outputFilename)
	if err != nil {
		panic(err)
	}
	defer outputFile.Close()
	filestat, err := inputFile.Stat()
	if err != nil {
		panic(err)
	}
	fileLength := filestat.Size()

	scanner := bufio.NewScanner(inputFile)
	writer := bufio.NewWriter(outputFile)
	defer writer.Flush()

	writer.WriteString(`// Code generated by svelte-ssr-to-templ. DO NOT EDIT.
package ` + packageName + `

import ( json "github.com/bytedance/sonic" )

func marshalProps(props *` + packageName + `Props) string {
	jsonProps, err := json.Marshal(*props)
	if err != nil {
		panic(err)
	}
	return string(jsonProps)
}

func addHeadContent(headContents map[string]struct{}) {
	for _, content := range ` + packageName + `Head {
		headContents[content] = struct{}{}
	}
}
`)

	writer.WriteString("templ Home(props *" + strings.TrimSuffix(filename, ".html") + `Props, headContents map[string]struct{}) {
	{{ addHeadContent(headContents) }}
`)
	writer.WriteString("<div class=\"" + packageName + "\" svelte={ marshalProps(props) }>\n")

	htmlContent := &strings.Builder{}
	htmlContent.Grow(int(fileLength))
	for scanner.Scan() {
		line := scanner.Text()
		modifiedLine := findPropertyRegex.ReplaceAllStringFunc(line, func(match string) string {
			parts := strings.Split(strings.TrimPrefix(strings.TrimSuffix(match, "--"), "svelte-"), "-")

			// Remove type information from the property path
			for i, part := range parts {
				indexStart := strings.Index(part, "{")
				if indexStart != -1 && strings.HasSuffix(part, "}") {
					parts[i] = part[:indexStart]
				}
			}
			return "{ props." + strings.Join(parts, ".") + " }"
		})
		modifiedLine = regexWithQuotes.ReplaceAllStringFunc(modifiedLine, func(match string) string {
			return strings.ReplaceAll(strings.ReplaceAll(match, "\"", ""), "'", "")
		})

		_, err := htmlContent.WriteString(modifiedLine + "\n")
		if err != nil {
			panic(err)
		}
	}
	htmlString := htmlContent.String()
	if strings.Contains(htmlString, "iter-") {
		htmlString = newLine.ReplaceAllString(htmlString, " ")
		htmlString = catWhiskers.ReplaceAllString(htmlString, "><")
		parser.Parse(props, strings.NewReader(htmlString), writer)
	} else {
		writer.WriteString(htmlString)
	}
	writer.WriteString("</div>\n}\n")
}

func parseHTMLFile(
	path string,
	filename string,
	opts *BuildOptions,
) map[string]*parser.Property {
	file, err := os.Open(opts.ExecutablePath + opts.QueueDir + path + filename)
	if err != nil {
		panic(err)
	}
	defer file.Close()

	props := make(map[string]*parser.Property)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		// Find all occurrences of "svelte-" followed by the prop name
		matches := findPropertyRegex.FindAllString(line, -1)
		for _, match := range matches {
			// Remove "svelte-" prefix and trailing "-"
			propPath := strings.TrimPrefix(strings.TrimSuffix(match, "-"), "svelte-")
			// Split the property path, but keep nested levels intact
			parts := strings.Split(propPath, "-")
			addProperty(props, &parts)
		}
	}
	return props
}

func addProperty(props map[string]*parser.Property, parts *[]string) {
	current := props
	for i, part := range *parts {
		indexStart := strings.Index(part, "{")
		var currentType string

		if indexStart != -1 && strings.HasSuffix(part, "}") {
			indexEnd := len(part) - 1
			typeString := part[indexStart+1 : indexEnd]
			if !typeRegex.MatchString(typeString) {
				log.Fatalf("Invalid type: %s", typeString)
			}

			part = part[:indexStart]
			currentType = typeString
		} else {
			currentType = types.DefaultType
		}

		if i == len(*parts)-1 {
			if _, exists := current[part]; !exists {
				current[part] = &parser.Property{Name: part, Type: currentType}
			}
		} else {
			if _, exists := current[part]; !exists {
				current[part] = &parser.Property{
					Name:     part,
					Type:     currentType,
					Children: make(map[string]*parser.Property),
				}
			}
			current = current[part].Children
		}
	}
}

func generateStructs(
	props map[string]*parser.Property,
	path string,
	filename *string,
	packageName *string,
	opts *BuildOptions,
) {
	exPath := opts.ExecutablePath
	queueDir := opts.QueueDir
	outputBuildDir := opts.OutputBuildDir

	// Create the directory recursively if it doesn't exist
	outputDir := exPath + outputBuildDir + path + *packageName
	err := os.MkdirAll(outputDir, os.ModePerm)
	if err != nil {
		log.Fatalf("Error creating directory: %s", err)
	}

	outputFile, err := os.Create(outputDir + "/" + *filename + ".go")
	if err != nil {
		log.Fatalf("Error creating file: %s", err)
	}
	defer outputFile.Close()

	fmt.Fprintf(outputFile, `// Code generated by svelte-ssr-to-templ. DO NOT EDIT.

package %s

type %sProps struct {
`, *packageName, *filename)

	// Sort the properties by name
	var propNames []string
	for name := range props {
		propNames = append(propNames, name)
	}
	sort.Strings(propNames)

	var parentName string = ""

	for _, name := range propNames {
		prop := props[name]
		generateFields(outputFile, prop, filename, &parentName)
	}
	fmt.Fprint(outputFile, "}\n\n")

	for _, prop := range props {
		if len(prop.Children) > 0 {
			generateNestedStructs(outputFile, prop, filename, &parentName)
		}
	}

	// Read the .head file in the same directory
	headFile, err := os.Open(exPath + queueDir + path + *filename + ".head")
	if err != nil {
		log.Fatalf("Error opening head file: %s", err)
	}
	defer headFile.Close()

	// Create a go array of strings from the head file
	scanner := bufio.NewScanner(headFile)
	fmt.Fprintf(outputFile, "var %sHead = [...]string{\n", *filename)
	for scanner.Scan() {
		fmt.Fprintf(outputFile, "\t`%s`,\n", scanner.Text())
	}
	fmt.Fprintln(outputFile, "}")
}

func generateFields(outputFile *os.File, prop *parser.Property, prefix *string, parentName *string) {
	// TODO(czarlinski): maybe make this omit empty.
	nameWithLower := strings.ToLower(prop.Name[:1]) + prop.Name[1:]
	jsonTag := fmt.Sprintf("`json:\"%s\"`", nameWithLower)
	if len(prop.Children) == 0 {
		fmt.Fprintf(
			outputFile,
			"\t%s %s %s\n",
			prop.Name, types.FieldTypeMap[prop.Type], jsonTag,
		)
	} else if prop.Type == "[]" {
		fmt.Fprintf(
			outputFile,
			"\t%s []%s%s%s %s\n",
			prop.Name, *prefix, *parentName, prop.Name, jsonTag,
		)
	} else {
		fmt.Fprintf(
			outputFile,
			"\t%s %s%s%s %s\n",
			prop.Name, *prefix, *parentName, prop.Name, jsonTag,
		)
	}
}

func generateNestedStructs(outputFile *os.File, prop *parser.Property, prefix *string, parentName *string) {
	if len(prop.Children) > 0 {
		fmt.Fprintf(outputFile, "type %s%s%s struct {\n", *prefix, *parentName, prop.Name)
		// Sort the properties by name
		var propNames []string
		for name := range prop.Children {
			propNames = append(propNames, name)
		}
		sort.Strings(propNames)

		for _, name := range propNames {
			child := prop.Children[name]
			var newParentName string = *parentName + prop.Name
			generateFields(outputFile, child, prefix, &newParentName)
		}
		fmt.Fprint(outputFile, "}\n\n")

		for _, child := range prop.Children {
			if len(child.Children) > 0 {
				var newParentName string = *parentName + prop.Name
				generateNestedStructs(outputFile, child, prefix, &newParentName)
			}
		}
	}
}
