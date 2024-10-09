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
	doc, err := html.Parse(htmlInput)
	if err != nil {
		panic(err)
	}

	first := doc.FirstChild
	modifyHTML(first, props, &Context{})
	printHtml(first, &printHtmlArgs{-1, buffer})
}

type printHtmlArgs struct {
	depth int
	buf   *bufio.Writer
}

func printHtml(n *html.Node, args *printHtmlArgs) {
	depth := args.depth
	buf := args.buf
	if n.Type == html.TextNode && depth != -1 {
		buf.WriteString(
			fmt.Sprintf(
				"%s%s\n",
				strings.Repeat("  ", depth),
				n.Data,
			),
		)
		return
	}

	if n.Type == html.CommentNode {
		buf.WriteString(fmt.Sprintf("<!--%s-->", n.Data))
		recursiveMap(n, printHtml, &printHtmlArgs{depth + 1, buf})
		return
	}

	if depth != -1 {
		indent := strings.Repeat("  ", depth)
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
	}

	recursiveMap(n, printHtml, &printHtmlArgs{depth + 1, buf})

	// If node starts with `for`
	if depth != -1 {
		indent := strings.Repeat("  ", depth)
		if strings.HasPrefix(n.Data, "for") {
			buf.WriteString(indent + "}\n")
		} else {
			buf.WriteString(indent + "</" + n.Data + ">\n")
		}
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

func modifyHTML(
	node *html.Node,
	props map[string]*Property,
	context *Context,
) *html.Node {
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

				prevContext := setPrevContext(context)
				context = &Context{
					PropName:    propName,
					LoopContext: &LoopContext{IndexName: indexName},
					PrevContext: prevContext,
				}

				currentProp := findProps(props, propName)
				recursiveMap(
					node, replaceNodeWithLoop, &PropertyWithContext{currentProp, context},
				)
				break
			}

			result = mapRegex.FindStringSubmatch(attr.Val)
			if result != nil {
				// Extract the key and value results from the class
				propName := result[3]
				keyName := result[4]
				valName := result[5]

				prevContext := setPrevContext(context)
				context = &Context{
					PropName:    propName,
					MapContext:  &MapContext{KeyName: keyName, ValName: valName},
					PrevContext: prevContext,
				}

				currentProp := findProps(props, propName)
				recursiveMap(
					node, replaceNodeWithMap, &PropertyWithContext{currentProp, context},
				)
				break
			}
		}
	}
	for c := node.FirstChild; c != nil; c = c.NextSibling {
		modifyHTML(c, props, context)
	}

	return node
}

func replaceNodeWithLoop(
	node *html.Node,
	args *PropertyWithContext,
) {
	first := node.FirstChild
	recursiveMap(node, replaceListProp, args)

	loopNode := createNode(args.context)

	node.RemoveChild(first)
	loopNode.AppendChild(first)
	node.AppendChild(loopNode)
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
	first := node.FirstChild
	recursiveMap(node, replaceMapProp, args)

	mapNode := createNode(args.context)

	node.RemoveChild(first)
	mapNode.AppendChild(first)
	node.AppendChild(mapNode)
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
	index := strings.Index(node.Data, "."+propName)
	if index == -1 {
		return -1, fmt.Errorf("could not find the prop %s", propName)
	}
	node.Data = strings.ReplaceAll(node.Data, "props.", ".")
	index -= 5 // Offset for the `props.` -> `.` replacement
	if index < 0 {
		panic("Negative index. This should not happen.")
	}
	return index, nil
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

	var trimmed string = strings.TrimSuffix(currentLine, ") }")
	if typeCast == "" {
		node.Data = "{ " + trimmed + " }"
	} else {
		// Note that `trimmed` still has the ) in this case. Why? I'm not sure but
		// it works, so I'm not going to question it. Well, I am going to question
		// it, but I'm not going to change it. Somewhere I messed up the recursion
		// and I just patched on top of it.
		node.Data = "{ " + typeCast + "(" + trimmed + " }"
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
	if typeCast == "" {
		node.Data = "{ " + strings.TrimSuffix(currentLine, " ) }") + " }"
	} else {
		node.Data = "{ " + typeCast + "(" + currentLine
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
	return prevContext
}
