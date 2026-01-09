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
// Package ansi provides ANSI style configuration for glamour.
package ansi

// StyleConfig is a markdown style configuration.
type StyleConfig struct {
	Document              StyleBlock
	BlockQuote            StyleBlock
	Paragraph             StyleBlock
	List                  StyleList
	Heading               StyleBlock
	H1                    StyleBlock
	H2                    StyleBlock
	H3                    StyleBlock
	H4                    StyleBlock
	H5                    StyleBlock
	H6                    StyleBlock
	Strikethrough         StylePrimitive
	Emph                  StylePrimitive
	Strong                StylePrimitive
	HorizontalRule        StylePrimitive
	Item                  StylePrimitive
	Enumeration           StylePrimitive
	Task                  StyleTask
	Link                  StylePrimitive
	LinkText              StylePrimitive
	Image                 StylePrimitive
	ImageText             StylePrimitive
	Code                  StyleBlock
	CodeBlock             StyleCodeBlock
	Table                 StyleTable
	DefinitionList        StyleBlock
	DefinitionTerm        StylePrimitive
	DefinitionDescription StylePrimitive
	Text                  StylePrimitive
}

// StyleBlock configures a block element.
type StyleBlock struct {
	StylePrimitive
	Indent      *uint
	IndentToken *string
	Margin      *uint
}

// StylePrimitive configures a primitive element.
type StylePrimitive struct {
	Color           *string
	BackgroundColor *string
	Bold            *bool
	Italic          *bool
	Underline       *bool
	Strikethrough   *bool
	CrossedOut      *bool // Alias for strikethrough
	Overlined       *bool
	Inverse         *bool
	Blink           *bool
	Prefix          string
	Suffix          string
	Format          string
	BlockPrefix     string
	BlockSuffix     string
}

// StyleList configures list elements.
type StyleList struct {
	StyleBlock
	LevelIndent uint
}

// StyleTask configures task items.
type StyleTask struct {
	StylePrimitive
	Ticked   string
	Unticked string
}

// StyleCodeBlock configures code blocks.
type StyleCodeBlock struct {
	StyleBlock
	Chroma *Chroma
	Theme  string
	Margin *uint
}

// Chroma configures syntax highlighting.
type Chroma struct {
	Text                StylePrimitive
	Error               StylePrimitive
	Comment             StylePrimitive
	CommentPreproc      StylePrimitive
	Keyword             StylePrimitive
	KeywordReserved     StylePrimitive
	KeywordNamespace    StylePrimitive
	KeywordType         StylePrimitive
	Operator            StylePrimitive
	Punctuation         StylePrimitive
	Name                StylePrimitive
	NameBuiltin         StylePrimitive
	NameTag             StylePrimitive
	NameAttribute       StylePrimitive
	NameClass           StylePrimitive
	NameConstant        StylePrimitive
	NameDecorator       StylePrimitive
	NameException       StylePrimitive
	NameFunction        StylePrimitive
	NameOther           StylePrimitive
	Literal             StylePrimitive
	LiteralNumber       StylePrimitive
	LiteralDate         StylePrimitive
	LiteralString       StylePrimitive
	LiteralStringEscape StylePrimitive
	GenericDeleted      StylePrimitive
	GenericEmph         StylePrimitive
	GenericInserted     StylePrimitive
	GenericStrong       StylePrimitive
	GenericSubheading   StylePrimitive
	Background          StylePrimitive
}

// StyleTable configures tables.
type StyleTable struct {
	StyleBlock
	CenterSeparator *string
	ColumnSeparator *string
	RowSeparator    *string
}

// DefaultStyles returns default style configuration.
func DefaultStyles() StyleConfig {
	return StyleConfig{}
}
