package initiative

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type desktopGraphStep struct {
	key  string
	kind string
	run  func(context.Context, DesktopGraphState) (DesktopGraphState, error)
}

type desktopGraphBranch struct {
	choose  func(context.Context, DesktopGraphState) (string, error)
	allowed map[string]struct{}
}

type desktopGraphEdge struct {
	from string
	to   string
}

type desktopGraphBuilder struct {
	maxNodes int
	nodes    map[string]desktopGraphStep
	edges    []desktopGraphEdge
	branches map[string]desktopGraphBranch
	err      error
}

func newDesktopGraphBuilder(maxNodes int) *desktopGraphBuilder {
	if maxNodes <= 0 {
		maxNodes = 1
	}
	return &desktopGraphBuilder{
		maxNodes: maxNodes,
		nodes:    make(map[string]desktopGraphStep),
		branches: make(map[string]desktopGraphBranch),
	}
}

func (b *desktopGraphBuilder) add(steps ...desktopGraphStep) *desktopGraphBuilder {
	if b == nil || b.err != nil {
		return b
	}
	for _, step := range steps {
		if strings.TrimSpace(step.key) == "" || strings.TrimSpace(step.kind) == "" || step.run == nil {
			b.err = errors.New("desktop typed flow step requires key, kind and runner")
			return b
		}
		if _, exists := b.nodes[step.key]; exists {
			b.err = fmt.Errorf("desktop typed flow step %q already exists", step.key)
			return b
		}
		if len(b.nodes) >= b.maxNodes {
			b.err = fmt.Errorf("desktop typed flow exceeds node limit %d", b.maxNodes)
			return b
		}
		b.nodes[step.key] = step
	}
	return b
}

func (b *desktopGraphBuilder) path(keys ...string) *desktopGraphBuilder {
	if b == nil || b.err != nil {
		return b
	}
	if len(keys) < 2 {
		b.err = errors.New("desktop typed flow path requires at least two nodes")
		return b
	}
	for index := 0; index < len(keys)-1; index++ {
		b.connect(keys[index], keys[index+1])
		if b.err != nil {
			return b
		}
	}
	return b
}

func (b *desktopGraphBuilder) branch(
	from string,
	choose func(context.Context, DesktopGraphState) (string, error),
	endpoints ...string,
) *desktopGraphBuilder {
	if b == nil || b.err != nil {
		return b
	}
	if choose == nil || len(endpoints) == 0 {
		b.err = errors.New("desktop typed flow branch requires selector and endpoints")
		return b
	}
	if _, exists := b.nodes[from]; !exists {
		b.err = fmt.Errorf("desktop typed flow branch source %q is missing", from)
		return b
	}
	allowed := make(map[string]struct{}, len(endpoints))
	for _, endpoint := range endpoints {
		if _, exists := b.nodes[endpoint]; !exists {
			b.err = fmt.Errorf("desktop typed flow branch endpoint %q is missing", endpoint)
			return b
		}
		allowed[endpoint] = struct{}{}
		b.connect(from, endpoint)
		if b.err != nil {
			return b
		}
	}
	b.branches[from] = desktopGraphBranch{choose: choose, allowed: allowed}
	return b
}

func (b *desktopGraphBuilder) connect(from, to string) {
	if _, exists := b.nodes[from]; !exists {
		b.err = fmt.Errorf("desktop typed flow edge source %q is missing", from)
		return
	}
	if _, exists := b.nodes[to]; !exists {
		b.err = fmt.Errorf("desktop typed flow edge target %q is missing", to)
		return
	}
	for _, edge := range b.edges {
		if edge.from == from && edge.to == to {
			b.err = fmt.Errorf("desktop typed flow edge %q -> %q already exists", from, to)
			return
		}
	}
	b.edges = append(b.edges, desktopGraphEdge{from: from, to: to})
}

// DesktopTypedGraph is Initiative's compiled desktop decision flow. It exposes
// only the operations needed by the desktop orchestration boundary.
type DesktopTypedGraph struct {
	nodes    map[string]desktopGraphStep
	outgoing map[string][]string
	branches map[string]desktopGraphBranch
}

type DesktopGraphExecutionStatus string

const (
	DesktopGraphExecutionStarted   DesktopGraphExecutionStatus = "started"
	DesktopGraphExecutionCompleted DesktopGraphExecutionStatus = "completed"
	DesktopGraphExecutionFailed    DesktopGraphExecutionStatus = "failed"
)

// DesktopGraphExecutionEvent deliberately contains graph structure only.
// Request state and node inputs are never exposed to diagnostics observers.
type DesktopGraphExecutionEvent struct {
	Node   string
	Kind   string
	Status DesktopGraphExecutionStatus
}

func (b *desktopGraphBuilder) compile() (*DesktopTypedGraph, error) {
	if b == nil {
		return nil, errors.New("nil desktop typed flow builder")
	}
	if b.err != nil {
		return nil, b.err
	}
	if len(b.nodes) == 0 {
		return nil, errors.New("desktop typed flow has no nodes")
	}
	if err := validateDesktopGraphAcyclic(b.nodes, b.edges); err != nil {
		return nil, err
	}
	nodes := make(map[string]desktopGraphStep, len(b.nodes))
	for key, step := range b.nodes {
		nodes[key] = step
	}
	outgoing := make(map[string][]string, len(nodes))
	for _, edge := range b.edges {
		outgoing[edge.from] = append(outgoing[edge.from], edge.to)
	}
	branches := make(map[string]desktopGraphBranch, len(b.branches))
	for key, branch := range b.branches {
		allowed := make(map[string]struct{}, len(branch.allowed))
		for endpoint := range branch.allowed {
			allowed[endpoint] = struct{}{}
		}
		branches[key] = desktopGraphBranch{choose: branch.choose, allowed: allowed}
	}
	return &DesktopTypedGraph{nodes: nodes, outgoing: outgoing, branches: branches}, nil
}

func (g *DesktopTypedGraph) Invoke(
	ctx context.Context,
	start string,
	end string,
	state DesktopGraphState,
) (DesktopGraphState, error) {
	return g.InvokeObserved(ctx, start, end, state, nil)
}

func (g *DesktopTypedGraph) InvokeObserved(
	ctx context.Context,
	start string,
	end string,
	state DesktopGraphState,
	observe func(DesktopGraphExecutionEvent),
) (DesktopGraphState, error) {
	if g == nil {
		return state, errors.New("nil desktop typed flow")
	}
	if _, exists := g.nodes[start]; !exists {
		return state, fmt.Errorf("desktop typed flow start %q is missing", start)
	}
	if _, exists := g.nodes[end]; !exists {
		return state, fmt.Errorf("desktop typed flow end %q is missing", end)
	}
	current := start
	visited := make(map[string]struct{}, len(g.nodes))
	for {
		if err := ctx.Err(); err != nil {
			return state, err
		}
		if _, seen := visited[current]; seen {
			return state, fmt.Errorf("desktop typed flow revisited %q", current)
		}
		visited[current] = struct{}{}
		step := g.nodes[current]
		emitDesktopGraphEvent(observe, DesktopGraphExecutionEvent{
			Node: step.key, Kind: step.kind, Status: DesktopGraphExecutionStarted,
		})
		var err error
		state, err = step.run(ctx, state)
		if err != nil {
			emitDesktopGraphEvent(observe, DesktopGraphExecutionEvent{
				Node: step.key, Kind: step.kind, Status: DesktopGraphExecutionFailed,
			})
			return state, fmt.Errorf("desktop typed flow step %q: %w", current, err)
		}
		emitDesktopGraphEvent(observe, DesktopGraphExecutionEvent{
			Node: step.key, Kind: step.kind, Status: DesktopGraphExecutionCompleted,
		})
		if current == end {
			return state, nil
		}
		if branch, exists := g.branches[current]; exists {
			next, err := branch.choose(ctx, state)
			if err != nil {
				return state, fmt.Errorf("desktop typed flow branch %q: %w", current, err)
			}
			if _, allowed := branch.allowed[next]; !allowed {
				return state, fmt.Errorf("desktop typed flow branch %q selected undeclared endpoint %q", current, next)
			}
			current = next
			continue
		}
		next := g.outgoing[current]
		if len(next) != 1 {
			return state, fmt.Errorf("desktop typed flow step %q has %d next steps", current, len(next))
		}
		current = next[0]
	}
}

func (g *DesktopTypedGraph) NodeCount() int {
	if g == nil {
		return 0
	}
	return len(g.nodes)
}

func (g *DesktopTypedGraph) HasNode(key string) bool {
	if g == nil {
		return false
	}
	_, exists := g.nodes[key]
	return exists
}

func emitDesktopGraphEvent(observe func(DesktopGraphExecutionEvent), event DesktopGraphExecutionEvent) {
	if observe != nil {
		observe(event)
	}
}

func validateDesktopGraphAcyclic(nodes map[string]desktopGraphStep, edges []desktopGraphEdge) error {
	adjacent := make(map[string][]string, len(nodes))
	for _, edge := range edges {
		adjacent[edge.from] = append(adjacent[edge.from], edge.to)
	}
	state := make(map[string]uint8, len(nodes))
	var visit func(string) error
	visit = func(key string) error {
		if state[key] == 1 {
			return fmt.Errorf("desktop typed flow contains cycle at %q", key)
		}
		if state[key] == 2 {
			return nil
		}
		state[key] = 1
		for _, next := range adjacent[key] {
			if err := visit(next); err != nil {
				return err
			}
		}
		state[key] = 2
		return nil
	}
	for key := range nodes {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}
