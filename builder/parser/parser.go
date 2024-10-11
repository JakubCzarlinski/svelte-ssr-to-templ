package parser

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
	"svelte-ssr-to-templ/builder/types"

	"golang.org/x/net/html"
)

var loopRegex = regexp.MustCompile(`iter-(([a-zA-Z\[\]-]+)-)*([a-zA-Z]+)\[([a-zA-Z]+)\]--`)
var mapRegex = regexp.MustCompile(`iter-(([a-zA-Z\[\]-]+)-)*([a-zA-Z]+)\[([a-zA-Z]+)-([a-zA-Z]+)\]--`)

type Context struct {
	PropName    string
	LoopContext *LoopContext
	MapContext  *MapContext
	PrevContext *Context
}

type LoopContext struct {
	IndexName string
}

type MapContext struct {
	KeyName string
	ValName string
}

type Property struct {
	Name     string
	Type     string
	Children map[string]*Property
}

type PropertyWithContext struct {
	currentProp *Property
	context     *Context
}

func Parse(
	props map[string]*Property,
	htmlInput *strings.Reader,
	buffer *bufio.Writer,
) {
	scaffold, err := html.Parse(&strings.Reader{})
	if err != nil {
		panic(err)
	}
	body := scaffold.FirstChild.FirstChild.NextSibling

	doc, err := html.ParseFragment(htmlInput, body)
	if err != nil {
		panic(err)
	}

	context := &Context{}
	for i, node := range doc {
		if i == len(doc)-1 {
			break
		}
		body.AppendChild(node)
	}

	modifyHTML(body, &modifyHTMLArgs{props, context})
	recursiveMap(body, printHtml, &printHtmlArgs{2, buffer})
}

type printHtmlArgs struct {
	depth int
	buf   *bufio.Writer
}

func printHtml(n *html.Node, args *printHtmlArgs) {
	depth := args.depth
	buf := args.buf

	indent := strings.Repeat("\t", depth)

	if n.Type == html.TextNode {
		buf.WriteString(fmt.Sprintf("%s%s\n", indent, n.Data))
		return
	} else if n.Type == html.CommentNode {
		buf.WriteString(fmt.Sprintf("%s<!--%s-->\n", indent, n.Data))
		return
	}

	if strings.HasPrefix(n.Data, "for") {
		buf.WriteString(indent + n.Data + "\n")
	} else {
		buf.WriteString(indent + "<" + n.Data)
		if n.Attr != nil {
			for _, attr := range n.Attr {
				buf.WriteString(" " + attr.Key + `="` + attr.Val + `"`)

				// If the attribute is an id, then we need to replace the prop name
				// with the loop index
				if attr.Key == "id" {
					buf.WriteString(`{ "` + attr.Val + `" }`)
				}
			}
		}
		buf.WriteString(">\n")
	}
	recursiveMap(n, printHtml, &printHtmlArgs{depth + 1, buf})

	// If node starts with `for`
	if strings.HasPrefix(n.Data, "for") {
		buf.WriteString(indent + "}\n")
	} else {
		buf.WriteString(indent + "</" + n.Data + ">\n")
	}
}

// Recursively search for the property
func findProps(props map[string]*Property, name string) *Property {
	for _, prop := range props {
		if prop.Name == name {
			return prop
		}
		if prop.Children != nil {
			return findProps(prop.Children, name)
		}
	}
	panic("Could not find prop")
}

type modifyHTMLArgs struct {
	props   map[string]*Property
	context *Context
}

func modifyHTML(
	node *html.Node,
	args *modifyHTMLArgs,
) {
	// If the node has a class called `iter-[propName]--` then we need to
	// replace the single child node with a loop.
	if node.Type == html.ElementNode {
		for _, attr := range node.Attr {
			if attr.Key != "class" {
				continue
			}

			result := loopRegex.FindStringSubmatch(attr.Val)
			if result != nil {
				// Extract the index name from the class
				propName := result[3]
				indexName := result[4]

				prevContext := setPrevContext(args.context)
				args.context = &Context{
					PropName:    propName,
					LoopContext: &LoopContext{IndexName: indexName},
					PrevContext: prevContext,
				}

				currentProp := findProps(args.props, propName)
				recursiveMap(
					node, replaceNodeWithLoop, &PropertyWithContext{
						currentProp, args.context,
					},
				)
				break
			}

			result = mapRegex.FindStringSubmatch(attr.Val)
			if result != nil {
				// Extract the key and value results from the class
				propName := result[3]
				keyName := result[4]
				valName := result[5]

				prevContext := setPrevContext(args.context)
				args.context = &Context{
					PropName:    propName,
					MapContext:  &MapContext{KeyName: keyName, ValName: valName},
					PrevContext: prevContext,
				}

				currentProp := findProps(args.props, propName)
				recursiveMap(
					node, replaceNodeWithMap, &PropertyWithContext{
						currentProp, args.context,
					},
				)
				break
			}
		}
	}

	if args.context.PrevContext != nil {
		args.context = args.context.PrevContext
	}

	recursiveMap(node, modifyHTML, args)
}

func replaceNodeWithLoop(
	node *html.Node,
	args *PropertyWithContext,
) {
	if node.Type == html.CommentNode {
		return
	}
	parent := node.Parent
	recursiveMap(node, replaceListProp, args)
	swapNodeChildren(parent, createNode(args.context))
}

func swapNodeChildren(parent *html.Node, node *html.Node) {
	for c := parent.FirstChild; c != nil; c = parent.FirstChild {
		parent.RemoveChild(c)
		node.AppendChild(c)
	}
	parent.AppendChild(node)
}

func replaceListProp(
	node *html.Node,
	args *PropertyWithContext,
) {
	recursiveMap(node, replaceListProp, args)
	currentProp := args.currentProp
	context := args.context

	propName := context.PropName

	index, err := getPropIndex(node, propName)
	if err != nil {
		return
	}

	currentLine := context.LoopContext.IndexName + node.Data[index+len(propName)+1:]
	finalDotIndex := strings.LastIndex(currentLine, ".")
	if finalDotIndex == -1 {
		modifyNodeDataOther(currentLine, currentProp, types.ListTypeToStringFunc, node)

	} else {
		field := currentLine[finalDotIndex+1:]
		field = field[:findFinal(field)]

		prop := findProps(currentProp.Children, field)

		modifyNodeData(currentLine, prop, types.ListTypeToStringFunc, node)
	}
}

func replaceNodeWithMap(
	node *html.Node,
	args *PropertyWithContext,
) {
	if node.Type == html.CommentNode {
		return
	}
	parent := node.Parent
	recursiveMap(node, replaceMapProp, args)
	swapNodeChildren(parent, createNode(args.context))
}

func createNode(context *Context) *html.Node {
	propName := context.PropName
	var valueName string

	if context.LoopContext != nil {
		valueName = context.LoopContext.IndexName
	} else if context.MapContext != nil {
		valueName = context.MapContext.ValName
	} else {
		panic("Could not find the value name")
	}

	if context.PrevContext == nil {
		return &html.Node{
			Type: html.ElementNode,
			Data: fmt.Sprintf("for _, %s := range props.%s {", valueName, propName),
		}
	}
	iterName := getIterName(context)
	return &html.Node{
		Type: html.ElementNode,
		Data: fmt.Sprintf(
			"for _, %s := range %s.%s {",
			valueName, iterName, propName,
		),
	}
}

func replaceMapProp(node *html.Node, args *PropertyWithContext) {
	recursiveMap(node, replaceMapProp, args)
	currentProp := args.currentProp
	context := args.context
	propName := context.PropName

	index, err := getPropIndex(node, propName)
	if err != nil {
		return
	}
	currentLine := context.MapContext.ValName + node.Data[index+len(propName)+1:]

	// Given the current line and current prop, find the field that is being accessed
	finalDotIndex := strings.LastIndex(currentLine, ".")
	if finalDotIndex == -1 {
		modifyNodeData(currentLine, currentProp, types.MapTypeToStringFunc, node)
	} else {
		field := currentLine[finalDotIndex+1:]
		field = field[:findFinal(field)]

		prop := findProps(currentProp.Children, field)
		if prop == nil {
			panic("Could not find the prop in the current prop")
		}

		modifyNodeDataOther(currentLine, prop, types.MapTypeToStringFunc, node)
	}
}

func getPropIndex(node *html.Node, propName string) (int, error) {
	if strings.Contains(node.Data, "."+propName) {
		node.Data = strings.ReplaceAll(node.Data, "props.", ".")
		index := strings.Index(node.Data, "."+propName)
		if index == -1 {
			return -1, fmt.Errorf("could not find the prop %s", propName)
		}
		return index, nil
	}
	return -1, fmt.Errorf("could not find the prop %s", propName)
}

func modifyNodeData(
	currentLine string,
	currentProp *Property,
	typeCasts map[string]string,
	node *html.Node,
) {
	typeCast, ok := typeCasts[currentProp.Type]
	if !ok {
		typeCast = types.DefaultType
	}

	trimmed := strings.TrimSuffix(currentLine, " }")
	trimmed = strings.TrimSuffix(trimmed, ")")

	if typeCast == "" {
		node.Data = "{ " + trimmed + " }"
	} else {
		node.Data = "{ " + typeCast + "(" + strings.TrimSuffix(trimmed, " }") + ")" + " }"
	}
}

// TODO(czarlinski): rewrite this.
func modifyNodeDataOther(
	currentLine string,
	currentProp *Property,
	typeCasts map[string]string,
	node *html.Node,
) {
	typeCast, ok := typeCasts[currentProp.Type]
	if !ok {
		typeCast = types.DefaultType
	}
	trimmed := strings.Trim(strings.TrimSuffix(currentLine, " }"), ")")
	if typeCast == "" {
		node.Data = "{ " + trimmed + " }"
	} else {
		node.Data = "{ " + typeCast + "(" + trimmed + ")" + " }"
	}
}

// Find the next space, right bracket, or right curly brace
func findFinal(currentLine string) int {
	final := strings.LastIndex(currentLine, " ")
	if final != -1 {
		return final
	}

	final = strings.LastIndex(currentLine, ")")
	if final != -1 {
		return final
	}

	final = strings.LastIndex(currentLine, "}")
	if final != -1 {
		return final
	}
	panic("Could not find the end of the field")
}

func recursiveMap[Args any](
	node *html.Node,
	function func(*html.Node, Args),
	args Args,
) {
	for c := node.FirstChild; c != nil; c = c.NextSibling {
		function(c, args)
	}
}

func getIterName(context *Context) string {
	if context.PrevContext.LoopContext != nil {
		return context.PrevContext.LoopContext.IndexName
	} else if context.PrevContext.MapContext != nil {
		return context.PrevContext.MapContext.ValName
	} else {
		return "props"
	}
}

func setPrevContext(context *Context) *Context {
	prevContext := &Context{PropName: context.PropName}
	if context.MapContext != nil {
		prevContext.MapContext = &MapContext{
			KeyName: context.MapContext.KeyName,
			ValName: context.MapContext.ValName,
		}
	} else if context.LoopContext != nil {
		prevContext.LoopContext = &LoopContext{
			IndexName: context.LoopContext.IndexName,
		}
	} else {
		prevContext = nil
	}
	if prevContext != nil && context.PrevContext != nil {
		prevContext.PrevContext = context.PrevContext
	}
	return prevContext
}
