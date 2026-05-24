// Package federation implements Apollo Federation v2 query planning and execution.
package federation

import (
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// Subgraph is a downstream federated GraphQL service.
type Subgraph struct {
	EnumName string // join__Graph enum value, e.g. "ACCOUNTS"
	Name     string // human name from @join__graph, e.g. "accounts"
	URL      string // service URL
}

// typeOwnership holds federation routing information for one GraphQL type.
type typeOwnership struct {
	// keys maps subgraph enum name → key field set, e.g. {"ACCOUNTS": "id"}.
	keys map[string]string
	// declared is the set of subgraph enum names that declare this type via @join__type.
	declared map[string]bool
	// fields maps non-external field name → owning subgraph enum name.
	fields map[string]string
}

// fieldMeta describes the return type and federation directives of a schema field.
type fieldMeta struct {
	typeName string
	isList   bool
	requires string // raw @requires field set from owning subgraph's @join__field
	provides string // raw @provides field set from owning subgraph's @join__field
}

// Supergraph holds parsed routing information extracted from a supergraph SDL.
type Supergraph struct {
	subgraphs  map[string]*Subgraph       // enum name → subgraph
	ownership  map[string]*typeOwnership  // type name → ownership
	fieldMetas map[string]map[string]fieldMeta // type name → field name → meta
}

// SubgraphByEnum returns the subgraph for an enum value name (e.g. "ACCOUNTS").
func (sg *Supergraph) SubgraphByEnum(name string) *Subgraph { return sg.subgraphs[name] }

// WithURLOverrides returns a shallow copy of sg with the specified subgraph URLs
// replaced. Keys are join__Graph enum names (e.g. "PRODUCTS").
func (sg *Supergraph) WithURLOverrides(overrides map[string]string) *Supergraph {
	if len(overrides) == 0 {
		return sg
	}
	copy := &Supergraph{
		ownership:  sg.ownership,
		fieldMetas: sg.fieldMetas,
		subgraphs:  make(map[string]*Subgraph, len(sg.subgraphs)),
	}
	for enum, sub := range sg.subgraphs {
		if url, ok := overrides[enum]; ok {
			patched := *sub
			patched.URL = url
			copy.subgraphs[enum] = &patched
		} else {
			copy.subgraphs[enum] = sub
		}
	}
	return copy
}

// OwnerOf returns the subgraph enum name owning a field on a type, or "" if unknown.
func (sg *Supergraph) OwnerOf(typeName, fieldName string) string {
	if own := sg.ownership[typeName]; own != nil {
		return own.fields[fieldName]
	}
	return ""
}

// KeysFor returns the key field names for a type in a given subgraph,
// parsed from the @join__type key argument (e.g. "id email" → ["id","email"]).
// Falls back to ["id"] when not specified.
func (sg *Supergraph) KeysFor(typeName, subgraphEnum string) []string {
	if own := sg.ownership[typeName]; own != nil {
		if k := own.keys[subgraphEnum]; k != "" {
			return strings.Fields(k)
		}
	}
	return []string{"id"}
}

// FieldTypeName returns the base return type name of a field (no !, no []).
func (sg *Supergraph) FieldTypeName(typeName, fieldName string) string {
	if m := sg.fieldMetas[typeName]; m != nil {
		return m[fieldName].typeName
	}
	return ""
}

// FieldIsList reports whether a field on a type returns a list.
func (sg *Supergraph) FieldIsList(typeName, fieldName string) bool {
	if m := sg.fieldMetas[typeName]; m != nil {
		return m[fieldName].isList
	}
	return false
}

// FieldRequires returns the raw @requires field set for a field (e.g. "id email"),
// or "" if the field has no @requires.
func (sg *Supergraph) FieldRequires(typeName, fieldName string) string {
	if m := sg.fieldMetas[typeName]; m != nil {
		return m[fieldName].requires
	}
	return ""
}

// FieldProvides returns the raw @provides field set for a field (e.g. "totalProductsCreated"),
// or "" if the field has no @provides.
func (sg *Supergraph) FieldProvides(typeName, fieldName string) string {
	if m := sg.fieldMetas[typeName]; m != nil {
		return m[fieldName].provides
	}
	return ""
}

// ParseSchema parses a Federation v2 supergraph SDL and returns a routing table.
// It extracts subgraph URLs from the join__Graph enum and field ownership
// from @join__type / @join__field directives.
// SubgraphURLs parses sdl and returns a map of join__Graph enum name → service URL.
// It is the minimal SDL parse a federation client needs at startup: just the routing table.
// Use the returned map as the subgraphURLs argument to generated NewClient constructors.
func SubgraphURLs(sdl string) (map[string]string, error) {
	doc, err := parser.ParseSchema(&ast.Source{Input: sdl, Name: "supergraph"})
	if err != nil {
		return nil, fmt.Errorf("federation: parse schema: %w", err)
	}
	return extractSubgraphURLs(doc), nil
}

// extractSubgraphURLs is the pure core of both SubgraphURLs and ParseSchema Pass 1.
func extractSubgraphURLs(doc *ast.SchemaDocument) map[string]string {
	urls := make(map[string]string)
	for _, def := range doc.Definitions {
		if def.Kind == ast.Enum && def.Name == "join__Graph" {
			for _, ev := range def.EnumValues {
				for _, d := range ev.Directives {
					if d.Name == "join__graph" {
						urls[ev.Name] = directiveArg(d, "url")
					}
				}
			}
		}
	}
	return urls
}

func ParseSchema(sdl string) (*Supergraph, error) {
	doc, err := parser.ParseSchema(&ast.Source{Input: sdl, Name: "supergraph"})
	if err != nil {
		return nil, fmt.Errorf("federation: parse schema: %w", err)
	}

	sg := &Supergraph{
		subgraphs:  make(map[string]*Subgraph),
		ownership:  make(map[string]*typeOwnership),
		fieldMetas: make(map[string]map[string]fieldMeta),
	}

	// Pass 1: extract subgraph URLs and names from the join__Graph enum.
	for _, def := range doc.Definitions {
		if def.Kind == ast.Enum && def.Name == "join__Graph" {
			for _, ev := range def.EnumValues {
				for _, d := range ev.Directives {
					if d.Name == "join__graph" {
						sg.subgraphs[ev.Name] = &Subgraph{
							EnumName: ev.Name,
							Name:     directiveArg(d, "name"),
							URL:      directiveArg(d, "url"),
						}
					}
				}
			}
		}
	}

	// Pass 2: extract type/field ownership from object and interface types.
	for _, def := range doc.Definitions {
		if def.Kind != ast.Object && def.Kind != ast.Interface {
			continue
		}
		own := &typeOwnership{
			keys:     make(map[string]string),
			declared: make(map[string]bool),
			fields:   make(map[string]string),
		}
		metas := make(map[string]fieldMeta)

		for _, d := range def.Directives {
			if d.Name == "join__type" {
				g := directiveArg(d, "graph")
				if g == "" {
					continue
				}
				own.declared[g] = true
				k := directiveArg(d, "key")
				if k != "" {
					own.keys[g] = k
				}
			}
		}

		for _, f := range def.Fields {
			meta := fieldMeta{
				typeName: namedType(f.Type),
				isList:   isListType(f.Type),
			}
			assigned := false
			for _, d := range f.Directives {
				if d.Name == "join__field" {
					g := directiveArg(d, "graph")
					ext := directiveArg(d, "external") == "true"
					if g != "" && !ext {
						own.fields[f.Name] = g
						assigned = true
						meta.requires = directiveArg(d, "requires")
						meta.provides = directiveArg(d, "provides")
					}
				}
			}
			// Fields without @join__field on a single-subgraph type are
			// implicitly owned by that subgraph.
			if !assigned && len(own.declared) == 1 {
				for g := range own.declared {
					own.fields[f.Name] = g
				}
			}
			metas[f.Name] = meta
		}

		sg.ownership[def.Name] = own
		sg.fieldMetas[def.Name] = metas
	}

	return sg, nil
}

func directiveArg(d *ast.Directive, name string) string {
	for _, a := range d.Arguments {
		if a.Name == name && a.Value != nil {
			return a.Value.Raw
		}
	}
	return ""
}

func namedType(t *ast.Type) string {
	if t == nil {
		return ""
	}
	if t.Elem != nil {
		return namedType(t.Elem)
	}
	return t.NamedType
}

func isListType(t *ast.Type) bool {
	return t != nil && t.Elem != nil
}
