package federation

import "fmt"

// PlanSpec is the serializable form of a Plan. Subgraphs are identified by
// enum name rather than by *Subgraph pointer so the spec can be embedded in
// generated code and resolved against any *Supergraph at runtime.
//
// The URL of each subgraph is not stored here — it is supplied by the
// *Supergraph passed to Resolve, after any WithURLOverrides have been applied.
type PlanSpec struct {
	Fetches       []*FetchSpecData       `json:"fetches"`
	EntityFetches []*EntityFetchSpecData `json:"entityFetches,omitempty"`
	Projection    []*FieldProjection     `json:"projection,omitempty"`
}

// FetchSpecData is the serializable form of a FetchSpec.
type FetchSpecData struct {
	SubgraphEnum string   `json:"subgraphEnum"`
	Query        string   `json:"query"`
	Variables    []string `json:"variables,omitempty"`
}

// EntityFetchSpecData is the serializable form of an EntityFetchSpec.
type EntityFetchSpecData struct {
	SubgraphEnum   string   `json:"subgraphEnum"`
	TypeName       string   `json:"typeName"`
	KeyFields      []string `json:"keyFields"`
	RequiresFields []string `json:"requiresFields,omitempty"`
	Selection      string   `json:"selection"`
	ParentPath     []string `json:"parentPath"`
	IsParentList   bool     `json:"isParentList,omitempty"`
}

// BuildPlanSpec builds a Plan for the given query and converts it to a PlanSpec.
func BuildPlanSpec(sg *Supergraph, queryStr, operationName string) (*PlanSpec, error) {
	plan, err := BuildPlan(sg, queryStr, operationName)
	if err != nil {
		return nil, err
	}
	return PlanToSpec(plan), nil
}

// PlanToSpec converts a resolved Plan to its serializable form.
// The Projection slice is shared with plan; BuildPlan never mutates it after returning.
func PlanToSpec(plan *Plan) *PlanSpec {
	spec := &PlanSpec{
		Fetches:    make([]*FetchSpecData, 0, len(plan.Fetches)),
		Projection: plan.Projection,
	}
	for _, f := range plan.Fetches {
		spec.Fetches = append(spec.Fetches, &FetchSpecData{
			SubgraphEnum: f.Subgraph.EnumName,
			Query:        f.Query,
			Variables:    f.Variables,
		})
	}
	for _, ef := range plan.EntityFetches {
		spec.EntityFetches = append(spec.EntityFetches, &EntityFetchSpecData{
			SubgraphEnum:   ef.Subgraph.EnumName,
			TypeName:       ef.TypeName,
			KeyFields:      ef.KeyFields,
			RequiresFields: ef.RequiresFields,
			Selection:      ef.Selection,
			ParentPath:     ef.ParentPath,
			IsParentList:   ef.IsParentList,
		})
	}
	return spec
}

// Resolve returns a Plan with *Subgraph pointers filled from sg.
// Returns an error if any referenced subgraph enum is not present in sg —
// the supergraph changed incompatibly since the spec was built.
func (s *PlanSpec) Resolve(sg *Supergraph) (*Plan, error) {
	plan := &Plan{
		Fetches:    make([]*FetchSpec, 0, len(s.Fetches)),
		Projection: s.Projection,
	}
	for _, f := range s.Fetches {
		sub := sg.SubgraphByEnum(f.SubgraphEnum)
		if sub == nil {
			return nil, fmt.Errorf("federation: unknown subgraph enum %q", f.SubgraphEnum)
		}
		plan.Fetches = append(plan.Fetches, &FetchSpec{
			Subgraph:  sub,
			Query:     f.Query,
			Variables: f.Variables,
		})
	}
	for _, ef := range s.EntityFetches {
		sub := sg.SubgraphByEnum(ef.SubgraphEnum)
		if sub == nil {
			return nil, fmt.Errorf("federation: unknown subgraph enum %q", ef.SubgraphEnum)
		}
		plan.EntityFetches = append(plan.EntityFetches, &EntityFetchSpec{
			Subgraph:       sub,
			TypeName:       ef.TypeName,
			KeyFields:      ef.KeyFields,
			RequiresFields: ef.RequiresFields,
			Selection:      ef.Selection,
			ParentPath:     ef.ParentPath,
			IsParentList:   ef.IsParentList,
		})
	}
	return plan, nil
}
