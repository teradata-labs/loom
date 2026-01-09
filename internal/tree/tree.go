// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Package tree provides tree rendering (stub replacement for charm.land/lipgloss/v2/tree).
package tree

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Tree represents a tree structure.
type Tree struct {
	root     *Node
	renderer *Renderer
}

// Node represents a tree node.
type Node struct {
	value    string
	children []*Node
}

// Renderer configures tree rendering.
type Renderer struct {
	itemStyle  lipgloss.Style
	enumStyle  lipgloss.Style
	rootStyle  lipgloss.Style
	indenter   Indenter
	enumerator Enumerator
}

// Indenter defines how to indent tree levels.
type Indenter func(children Children, index int) string

// Enumerator defines how to enumerate tree items.
type Enumerator func(children Children, index int) string

// Children represents a list of child nodes.
type Children interface {
	Length() int
	At(i int) *Node
}

// New creates a new tree.
func New() *Tree {
	return &Tree{
		root:     &Node{},
		renderer: &Renderer{},
	}
}

// Root sets the root value.
func Root(value string) *Tree {
	return &Tree{
		root:     &Node{value: value},
		renderer: &Renderer{},
	}
}

// Items adds items to the tree.
func (t *Tree) Items(items ...interface{}) *Tree {
	for _, item := range items {
		switch v := item.(type) {
		case string:
			t.root.children = append(t.root.children, &Node{value: v})
		case *Node:
			t.root.children = append(t.root.children, v)
		case *Tree:
			if v.root != nil {
				t.root.children = append(t.root.children, v.root)
			}
		}
	}
	return t
}

// Item adds an item.
func (t *Tree) Item(item interface{}) *Tree {
	return t.Items(item)
}

// Child adds a child node.
func (t *Tree) Child(items ...interface{}) *Tree {
	return t.Items(items...)
}

// ItemStyle sets item style.
func (t *Tree) ItemStyle(s lipgloss.Style) *Tree {
	t.renderer.itemStyle = s
	return t
}

// ItemStyleFunc sets item style function.
func (t *Tree) ItemStyleFunc(fn func(children Children, i int) lipgloss.Style) *Tree {
	return t
}

// EnumeratorStyle sets enumerator style.
func (t *Tree) EnumeratorStyle(s lipgloss.Style) *Tree {
	t.renderer.enumStyle = s
	return t
}

// EnumeratorStyleFunc sets enumerator style function.
func (t *Tree) EnumeratorStyleFunc(fn func(children Children, i int) lipgloss.Style) *Tree {
	return t
}

// RootStyle sets root style.
func (t *Tree) RootStyle(s lipgloss.Style) *Tree {
	t.renderer.rootStyle = s
	return t
}

// Indenter sets the indenter.
func (t *Tree) Indenter(i Indenter) *Tree {
	t.renderer.indenter = i
	return t
}

// Enumerator sets the enumerator.
func (t *Tree) Enumerator(e Enumerator) *Tree {
	t.renderer.enumerator = e
	return t
}

// String renders the tree.
func (t *Tree) String() string {
	if t.root == nil {
		return ""
	}
	return t.render(t.root, 0)
}

func (t *Tree) render(n *Node, depth int) string {
	var sb strings.Builder

	if n.value != "" {
		if depth == 0 {
			sb.WriteString(t.renderer.rootStyle.Render(n.value))
		} else {
			indent := strings.Repeat("  ", depth)
			sb.WriteString(indent)
			sb.WriteString("├─ ")
			sb.WriteString(t.renderer.itemStyle.Render(n.value))
		}
		sb.WriteString("\n")
	}

	for _, child := range n.children {
		sb.WriteString(t.render(child, depth+1))
	}

	return sb.String()
}

// DefaultIndenter is the default indenter.
func DefaultIndenter(children Children, index int) string {
	return "  "
}

// DefaultEnumerator is the default enumerator.
func DefaultEnumerator(children Children, index int) string {
	if index == children.Length()-1 {
		return "└─ "
	}
	return "├─ "
}

// RoundedEnumerator uses rounded corners.
func RoundedEnumerator(children Children, index int) string {
	if index == children.Length()-1 {
		return "╰─ "
	}
	return "├─ "
}
