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
// Package lspprotocol provides LSP protocol types (stub for github.com/charmbracelet/x/powernap/pkg/lsp/protocol).
package lspprotocol

// DiagnosticSeverity represents the severity of a diagnostic.
type DiagnosticSeverity int

const (
	SeverityError       DiagnosticSeverity = 1
	SeverityWarning     DiagnosticSeverity = 2
	SeverityInformation DiagnosticSeverity = 3
	SeverityHint        DiagnosticSeverity = 4
)

// Diagnostic represents an LSP diagnostic.
type Diagnostic struct {
	Range    Range
	Severity DiagnosticSeverity
	Code     interface{}
	Source   string
	Message  string
}

// Range represents a range in a document.
type Range struct {
	Start Position
	End   Position
}

// Position represents a position in a document.
type Position struct {
	Line      int
	Character int
}

// Location represents a location in a document.
type Location struct {
	URI   string
	Range Range
}

// TextEdit represents a text edit.
type TextEdit struct {
	Range   Range
	NewText string
}

// CompletionItem represents a completion item.
type CompletionItem struct {
	Label            string
	Kind             CompletionItemKind
	Detail           string
	Documentation    interface{}
	InsertText       string
	InsertTextFormat InsertTextFormat
}

// CompletionItemKind represents the kind of completion item.
type CompletionItemKind int

const (
	TextCompletion        CompletionItemKind = 1
	MethodCompletion      CompletionItemKind = 2
	FunctionCompletion    CompletionItemKind = 3
	ConstructorCompletion CompletionItemKind = 4
	FieldCompletion       CompletionItemKind = 5
	VariableCompletion    CompletionItemKind = 6
	ClassCompletion       CompletionItemKind = 7
	InterfaceCompletion   CompletionItemKind = 8
	ModuleCompletion      CompletionItemKind = 9
	PropertyCompletion    CompletionItemKind = 10
	KeywordCompletion     CompletionItemKind = 14
	SnippetCompletion     CompletionItemKind = 15
)

// InsertTextFormat represents the format of insert text.
type InsertTextFormat int

const (
	PlainTextFormat InsertTextFormat = 1
	SnippetFormat   InsertTextFormat = 2
)

// Hover represents hover information.
type Hover struct {
	Contents interface{}
	Range    *Range
}

// SymbolKind represents the kind of symbol.
type SymbolKind int

const (
	FileSymbol        SymbolKind = 1
	ModuleSymbol      SymbolKind = 2
	NamespaceSymbol   SymbolKind = 3
	PackageSymbol     SymbolKind = 4
	ClassSymbol       SymbolKind = 5
	MethodSymbol      SymbolKind = 6
	PropertySymbol    SymbolKind = 7
	FieldSymbol       SymbolKind = 8
	ConstructorSymbol SymbolKind = 9
	EnumSymbol        SymbolKind = 10
	InterfaceSymbol   SymbolKind = 11
	FunctionSymbol    SymbolKind = 12
	VariableSymbol    SymbolKind = 13
	ConstantSymbol    SymbolKind = 14
	StringSymbol      SymbolKind = 15
)

// DocumentSymbol represents a document symbol.
type DocumentSymbol struct {
	Name           string
	Detail         string
	Kind           SymbolKind
	Range          Range
	SelectionRange Range
	Children       []DocumentSymbol
}
