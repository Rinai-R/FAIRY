package statemachine

import "fmt"

type Edge[S comparable] struct {
	From S
	To   S
}

// Table is an immutable typed transition allowlist.
type Table[S comparable] struct {
	edges map[S]map[S]struct{}
}

func NewTable[S comparable](edges ...Edge[S]) (Table[S], error) {
	if len(edges) == 0 {
		return Table[S]{}, fmt.Errorf("state machine requires at least one transition")
	}
	table := Table[S]{edges: make(map[S]map[S]struct{})}
	for _, edge := range edges {
		if table.edges[edge.From] == nil {
			table.edges[edge.From] = make(map[S]struct{})
		}
		if _, exists := table.edges[edge.From][edge.To]; exists {
			return Table[S]{}, fmt.Errorf("duplicate state transition")
		}
		table.edges[edge.From][edge.To] = struct{}{}
	}
	return table, nil
}

func MustTable[S comparable](edges ...Edge[S]) Table[S] {
	table, err := NewTable(edges...)
	if err != nil {
		panic(err)
	}
	return table
}

func (t Table[S]) Allows(from, to S) bool {
	_, ok := t.edges[from][to]
	return ok
}
