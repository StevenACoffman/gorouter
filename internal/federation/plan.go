package federation

import (
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// Plan is the execution plan for a GraphQL operation.
type Plan struct {
	// Fetches are the initial per-subgraph queries, safe to run in parallel.
	Fetches []*FetchSpec
	// EntityFetches run after Fetches in order; later steps may depend on earlier ones.
	EntityFetches []*EntityFetchSpec
	// Projection holds the user-requested field tree, used to strip planner-added
	// fields (key fields, __typename, @requires pre-fetch fields) from the final response.
	Projection []*FieldProjection
}

// FetchSpec is a query to send to one subgraph.
type FetchSpec struct {
	Subgraph  *Subgraph
	Query     string
	Variables []string // variable names from the original operation used here
}

// EntityFetchSpec describes a cross-subgraph entity resolution step.
type EntityFetchSpec struct {
	Subgraph       *Subgraph
	TypeName       string   // entity type, e.g. "User"
	KeyFields      []string // key field names, e.g. ["id"]
	RequiresFields []string // @requires fields to embed in representations beyond key fields
	Selection      string   // the fields to fetch: "reviews {\n  title\n}\n"
	ParentPath     []string // JSON path to the parent in the merged data, e.g. ["user"]
	IsParentList   bool     // true when ParentPath resolves to a list
}

// FieldProjection is a node in the user-requested selection tree.
// It is used to strip planner-added fields from the final merged response.
type FieldProjection struct {
	Key      string             // response key (alias or field name)
	Children []*FieldProjection // nil for scalar fields
}

// node is an intermediate representation of a field for query building.
type node struct {
	alias    string
	name     string
	args     string   // pre-formatted argument string
	children []*node
	forced   bool // added by the planner for key/requires resolution, not in the original query
}

// BuildPlan analyzes a GraphQL query against the supergraph routing table
// and returns a Plan describing how to execute it.
func BuildPlan(sg *Supergraph, queryStr, operationName string) (*Plan, error) {
	doc, err := parser.ParseQuery(&ast.Source{Input: queryStr, Name: "query"})
	if err != nil {
		return nil, fmt.Errorf("federation: parse query: %w", err)
	}

	op := findOperation(doc, operationName)
	if op == nil {
		if operationName != "" {
			return nil, fmt.Errorf("federation: operation %q not found", operationName)
		}
		return nil, fmt.Errorf("federation: no operation in document")
	}

	plan := &Plan{
		Projection: buildProjection(op.SelectionSet),
	}

	// Group root selections by their owning subgraph.
	type group struct {
		sg       *Subgraph
		nodes    []*node
		usedVars map[string]bool
	}
	groups := make(map[string]*group)
	var groupOrder []string

	for _, sel := range op.SelectionSet {
		field, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		ownerEnum := sg.OwnerOf("Query", field.Name)
		if ownerEnum == "" {
			return nil, fmt.Errorf("federation: field %q has no subgraph owner in routing table", field.Name)
		}
		sub := sg.SubgraphByEnum(ownerEnum)
		if sub == nil {
			return nil, fmt.Errorf("federation: subgraph %q not found", ownerEnum)
		}
		if groups[ownerEnum] == nil {
			groups[ownerEnum] = &group{sg: sub, usedVars: make(map[string]bool)}
			groupOrder = append(groupOrder, ownerEnum)
		}
		g := groups[ownerEnum]

		n := buildNode(field)
		returnType := sg.FieldTypeName("Query", field.Name)
		returnIsList := sg.FieldIsList("Query", field.Name)
		rootPath := []string{effectiveName(field)}

		// Provides from a Query-level field (uncommon, but check anyway).
		rootProvides := parseProvidesSet(sg.FieldProvides("Query", field.Name))
		entityFetches := splitCrossSubgraph(sg, n, returnType, ownerEnum, rootPath, returnIsList, rootProvides)
		plan.EntityFetches = append(plan.EntityFetches, entityFetches...)

		g.nodes = append(g.nodes, n)
		collectVarNames(n, g.usedVars)
	}

	// Build the query string for each group.
	for _, enumName := range groupOrder {
		g := groups[enumName]
		opKind := operationKind(op.Operation)
		varDecls := buildVarDecls(op.VariableDefinitions, g.usedVars)

		var sb strings.Builder
		sb.WriteString(opKind)
		sb.WriteString(varDecls)
		sb.WriteString(" {\n")
		for _, n := range g.nodes {
			sb.WriteString(renderNode(n, "  "))
		}
		sb.WriteString("}")

		usedList := make([]string, 0, len(g.usedVars))
		for v := range g.usedVars {
			usedList = append(usedList, v)
		}

		plan.Fetches = append(plan.Fetches, &FetchSpec{
			Subgraph:  g.sg,
			Query:     sb.String(),
			Variables: usedList,
		})
	}

	return plan, nil
}

// crossGroup accumulates fields bound for one foreign subgraph during planning.
type crossGroup struct {
	sub            *Subgraph
	keyFields      []string
	nodes          []*node
	requiresFields []string // top-level @requires field names to embed in representations
}

// splitCrossSubgraph walks n's children and separates fields belonging to a
// different subgraph into EntityFetchSpecs.
//
//   - Fields in the current subgraph (or in providedLocally) stay in n.children.
//   - Fields in another subgraph become EntityFetchSpecs.
//   - Fields with @requires in the current subgraph are removed from the initial
//     fetch and become EntityFetchSpecs with RequiresFields populated; prerequisite
//     fields from other subgraphs become preliminary EntityFetchSpecs that execute first.
//   - Key fields for entity resolution are added as forced nodes when missing.
//
// providedLocally is the @provides field set of the parent field that returned typeName.
func splitCrossSubgraph(
	sg *Supergraph,
	n *node,
	typeName, currentEnum string,
	path []string,
	pathIsList bool,
	providedLocally map[string]bool,
) []*EntityFetchSpec {
	if typeName == "" || len(n.children) == 0 {
		return nil
	}

	var keep []*node
	var entityFetches []*EntityFetchSpec
	// requiresEntityFetches are collected separately and appended after any
	// preliminary entity fetches (so prerequisites execute first).
	var requiresEntityFetches []*EntityFetchSpec

	crossGroups := make(map[string]*crossGroup)
	var crossOrder []string

	for _, child := range n.children {
		// __typename is available everywhere; keep if user requested it.
		if child.name == "__typename" {
			keep = append(keep, child)
			continue
		}

		ownerEnum := sg.OwnerOf(typeName, child.name)
		locallyProvided := providedLocally[child.name]

		// Check for @requires on a same-subgraph field.
		requiresStr := sg.FieldRequires(typeName, child.name)
		if requiresStr != "" && (ownerEnum == currentEnum) && !child.forced {
			// This field can only be resolved via entity fetch after its @requires
			// prerequisites are available. Do NOT include it in the initial query.
			requiresNodes := parseRequiresNodes(requiresStr)
			topLevelRequiresFields := topLevelFieldNames(requiresNodes)

			// For each @requires field, either keep it locally or schedule a
			// preliminary entity fetch.
			existingForced := existingNodeNames(keep)
			for _, rn := range requiresNodes {
				rfOwner := sg.OwnerOf(typeName, rn.name)
				isLocallyAvailable := rfOwner == "" || rfOwner == currentEnum || providedLocally[rn.name]
				if isLocallyAvailable {
					if !existingForced[rn.name] {
						keep = append(keep, &node{name: rn.name, children: rn.children, forced: true})
						existingForced[rn.name] = true
					}
				} else if rfOwner != "" {
					// Schedule preliminary entity fetch from rfOwner.
					if crossGroups[rfOwner] == nil {
						crossGroups[rfOwner] = &crossGroup{
							sub:       sg.SubgraphByEnum(rfOwner),
							keyFields: sg.KeysFor(typeName, rfOwner),
						}
						crossOrder = append(crossOrder, rfOwner)
					}
					if !crossGroups[rfOwner].hasField(rn.name) {
						crossGroups[rfOwner].nodes = append(crossGroups[rfOwner].nodes,
							&node{name: rn.name, children: rn.children, forced: true})
					}
				}
			}

			// Build entity fetch for this @requires field itself.
			requiresEntityFetches = append(requiresEntityFetches, &EntityFetchSpec{
				Subgraph:       sg.SubgraphByEnum(currentEnum),
				TypeName:       typeName,
				KeyFields:      sg.KeysFor(typeName, currentEnum),
				RequiresFields: topLevelRequiresFields,
				Selection:      renderNode(child, ""),
				ParentPath:     path,
				IsParentList:   pathIsList,
			})
			continue
		}

		if ownerEnum == "" || ownerEnum == currentEnum || locallyProvided {
			// Same subgraph (or locally provided via @provides): keep and recurse.
			childType := sg.FieldTypeName(typeName, child.name)
			childIsList := sg.FieldIsList(typeName, child.name)
			childPath := append(append([]string{}, path...), effectiveNodeName(child))
			// Pass @provides of this child field to the recursive call.
			childProvides := parseProvidesSet(sg.FieldProvides(typeName, child.name))
			subFetches := splitCrossSubgraph(sg, child, childType, currentEnum, childPath, childIsList, childProvides)
			entityFetches = append(entityFetches, subFetches...)
			keep = append(keep, child)
		} else {
			// Different subgraph: schedule entity resolution.
			if crossGroups[ownerEnum] == nil {
				crossGroups[ownerEnum] = &crossGroup{
					sub:       sg.SubgraphByEnum(ownerEnum),
					keyFields: sg.KeysFor(typeName, ownerEnum),
				}
				crossOrder = append(crossOrder, ownerEnum)
			}
			crossGroups[ownerEnum].nodes = append(crossGroups[ownerEnum].nodes, child)

			// If this cross-subgraph field has @requires, pre-fetch the required
			// fields locally so they can be included in the entity representation.
			xReqStr := sg.FieldRequires(typeName, child.name)
			if xReqStr != "" {
				xReqNodes := parseRequiresNodes(xReqStr)
				existingForced := existingNodeNames(keep)
				for _, rn := range xReqNodes {
					rfOwner := sg.OwnerOf(typeName, rn.name)
					isLocal := rfOwner == "" || rfOwner == currentEnum || providedLocally[rn.name]
					if isLocal && !existingForced[rn.name] {
						keep = append(keep, &node{name: rn.name, children: rn.children, forced: true})
						existingForced[rn.name] = true
					}
				}
				cg := crossGroups[ownerEnum]
				for _, rn := range xReqNodes {
					cg.requiresFields = append(cg.requiresFields, rn.name)
				}
			}
		}
	}

	// Build EntityFetchSpecs for normal cross-subgraph groups.
	// These must come BEFORE requiresEntityFetches so prerequisites execute first.
	for _, enumName := range crossOrder {
		cg := crossGroups[enumName]
		var selParts []string
		for _, child := range cg.nodes {
			selParts = append(selParts, renderNode(child, ""))
		}
		entityFetches = append(entityFetches, &EntityFetchSpec{
			Subgraph:       cg.sub,
			TypeName:       typeName,
			KeyFields:      cg.keyFields,
			RequiresFields: cg.requiresFields,
			Selection:      strings.Join(selParts, ""),
			ParentPath:     path,
			IsParentList:   pathIsList,
		})
	}

	// Append @requires entity fetches after their prerequisites.
	entityFetches = append(entityFetches, requiresEntityFetches...)

	// Ensure key fields for entity resolution are present in this subgraph's fetch.
	existingFields := existingNodeNames(keep)
	for _, cg := range crossGroups {
		for _, kf := range cg.keyFields {
			if !existingFields[kf] {
				keep = append(keep, &node{name: kf, forced: true})
				existingFields[kf] = true
			}
		}
	}
	// Also ensure key fields for @requires entity fetches.
	for _, ref := range requiresEntityFetches {
		for _, kf := range ref.KeyFields {
			if !existingFields[kf] {
				keep = append(keep, &node{name: kf, forced: true})
				existingFields[kf] = true
			}
		}
	}

	n.children = keep
	return entityFetches
}

// --- projection ---

// buildProjection converts an AST selection set to a FieldProjection tree.
func buildProjection(sels ast.SelectionSet) []*FieldProjection {
	var proj []*FieldProjection
	for _, sel := range sels {
		switch v := sel.(type) {
		case *ast.Field:
			fp := &FieldProjection{
				Key:      effectiveName(v),
				Children: buildProjection(v.SelectionSet),
			}
			proj = append(proj, fp)
		case *ast.InlineFragment:
			proj = append(proj, buildProjection(v.SelectionSet)...)
		}
	}
	return proj
}

// ApplyProjection trims data to only the fields in proj, discarding planner-added fields.
func ApplyProjection(data map[string]interface{}, proj []*FieldProjection) map[string]interface{} {
	if len(proj) == 0 {
		return data
	}
	result := make(map[string]interface{}, len(proj))
	for _, p := range proj {
		v, ok := data[p.Key]
		if !ok {
			continue
		}
		if len(p.Children) > 0 {
			switch vt := v.(type) {
			case map[string]interface{}:
				result[p.Key] = ApplyProjection(vt, p.Children)
			case []interface{}:
				list := make([]interface{}, len(vt))
				for i, item := range vt {
					if m, ok := item.(map[string]interface{}); ok {
						list[i] = ApplyProjection(m, p.Children)
					} else {
						list[i] = item
					}
				}
				result[p.Key] = list
			default:
				result[p.Key] = v
			}
		} else {
			result[p.Key] = v
		}
	}
	return result
}

// --- @requires field set parsing ---

// parseRequiresNodes converts a @requires field set string (e.g. "id email" or
// "dimensions { size weight }") into a slice of nodes for use in query building.
func parseRequiresNodes(s string) []*node {
	nodes, _ := parseFieldSetTokens(tokenizeFieldSet(s), 0)
	return nodes
}

// topLevelFieldNames returns the names of the top-level fields in a node slice.
func topLevelFieldNames(nodes []*node) []string {
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.name)
	}
	return names
}

// parseProvidesSet splits a @provides field set string into a lookup map.
// Only top-level field names are captured (nested fields are not yet used).
func parseProvidesSet(s string) map[string]bool {
	if s == "" {
		return nil
	}
	nodes := parseRequiresNodes(s)
	m := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		m[n.name] = true
	}
	return m
}

// tokenizeFieldSet splits a field set string into tokens (names, '{', '}').
func tokenizeFieldSet(s string) []string {
	var tokens []string
	var buf strings.Builder
	for _, r := range s {
		switch r {
		case '{', '}':
			if buf.Len() > 0 {
				tokens = append(tokens, strings.TrimSpace(buf.String()))
				buf.Reset()
			}
			tokens = append(tokens, string(r))
		case ' ', '\t', '\n', '\r':
			if buf.Len() > 0 {
				tokens = append(tokens, strings.TrimSpace(buf.String()))
				buf.Reset()
			}
		default:
			buf.WriteRune(r)
		}
	}
	if buf.Len() > 0 {
		tokens = append(tokens, strings.TrimSpace(buf.String()))
	}
	// Filter empty strings from TrimSpace artifacts
	var out []string
	for _, t := range tokens {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// parseFieldSetTokens parses a flat token slice into a node slice.
// pos is the current index; returns nodes and the index after the last token consumed.
func parseFieldSetTokens(tokens []string, pos int) ([]*node, int) {
	var nodes []*node
	for pos < len(tokens) {
		tok := tokens[pos]
		if tok == "}" {
			return nodes, pos // caller will consume the "}"
		}
		if tok == "{" {
			pos++
			continue
		}
		n := &node{name: tok, forced: true}
		pos++
		if pos < len(tokens) && tokens[pos] == "{" {
			pos++ // consume "{"
			children, newPos := parseFieldSetTokens(tokens, pos)
			n.children = children
			pos = newPos
			if pos < len(tokens) && tokens[pos] == "}" {
				pos++ // consume "}"
			}
		}
		nodes = append(nodes, n)
	}
	return nodes, pos
}

// --- helpers ---

// existingNodeNames builds a lookup map from a node slice's field names.
func existingNodeNames(nodes []*node) map[string]bool {
	m := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		m[n.name] = true
	}
	return m
}

func (cg *crossGroup) hasField(name string) bool {
	for _, n := range cg.nodes {
		if n.name == name {
			return true
		}
	}
	return false
}

// buildNode converts an *ast.Field to a *node for sub-query reconstruction.
func buildNode(f *ast.Field) *node {
	n := &node{
		alias: f.Alias,
		name:  f.Name,
		args:  renderArgs(f.Arguments),
	}
	for _, sel := range f.SelectionSet {
		switch v := sel.(type) {
		case *ast.Field:
			n.children = append(n.children, buildNode(v))
		case *ast.InlineFragment:
			if v.TypeCondition == "" {
				for _, isel := range v.SelectionSet {
					if ff, ok := isel.(*ast.Field); ok {
						n.children = append(n.children, buildNode(ff))
					}
				}
			}
		}
	}
	return n
}

// renderNode prints a node tree as a GraphQL selection fragment.
func renderNode(n *node, indent string) string {
	var sb strings.Builder
	sb.WriteString(indent)
	if n.alias != "" && n.alias != n.name {
		sb.WriteString(n.alias + ": ")
	}
	sb.WriteString(n.name)
	if n.args != "" {
		sb.WriteString(n.args)
	}
	if len(n.children) > 0 {
		sb.WriteString(" {\n")
		for _, child := range n.children {
			sb.WriteString(renderNode(child, indent+"  "))
		}
		sb.WriteString(indent + "}\n")
	} else {
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderArgs prints an argument list as "(key: value, ...)".
func renderArgs(args ast.ArgumentList) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, a.Name+": "+renderValue(a.Value))
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func renderValue(v *ast.Value) string {
	if v == nil {
		return "null"
	}
	switch v.Kind {
	case ast.Variable:
		return "$" + v.Raw
	case ast.StringValue:
		return `"` + strings.ReplaceAll(v.Raw, `"`, `\"`) + `"`
	case ast.ListValue:
		parts := make([]string, 0, len(v.Children))
		for _, c := range v.Children {
			parts = append(parts, renderValue(c.Value))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case ast.ObjectValue:
		parts := make([]string, 0, len(v.Children))
		for _, c := range v.Children {
			parts = append(parts, c.Name+": "+renderValue(c.Value))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return v.Raw
	}
}

// buildVarDecls returns "(${var}: ${type}, ...)" for variables in usedVars.
func buildVarDecls(defs ast.VariableDefinitionList, used map[string]bool) string {
	var parts []string
	for _, vd := range defs {
		if used[vd.Variable] {
			parts = append(parts, "$"+vd.Variable+": "+vd.Type.String())
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// collectVarNames adds variable names referenced in n's argument tree to used.
func collectVarNames(n *node, used map[string]bool) {
	s := n.args
	for i := 0; i < len(s); i++ {
		if s[i] == '$' {
			end := i + 1
			for end < len(s) && isIdentChar(s[end]) {
				end++
			}
			if end > i+1 {
				used[s[i+1:end]] = true
			}
			i = end - 1
		}
	}
	for _, child := range n.children {
		collectVarNames(child, used)
	}
}

func isIdentChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func effectiveName(f *ast.Field) string {
	if f.Alias != "" {
		return f.Alias
	}
	return f.Name
}

func effectiveNodeName(n *node) string {
	if n.alias != "" {
		return n.alias
	}
	return n.name
}

func findOperation(doc *ast.QueryDocument, name string) *ast.OperationDefinition {
	for _, op := range doc.Operations {
		if name == "" || op.Name == name {
			return op
		}
	}
	return nil
}

func operationKind(op ast.Operation) string {
	switch op {
	case ast.Mutation:
		return "mutation"
	case ast.Subscription:
		return "subscription"
	default:
		return "query"
	}
}
