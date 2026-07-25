// Package nodegraph provides FAIRY's small typed graph runtime. It is designed
// for Core rulebook composition, not as a general AI component framework.
package nodegraph

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrCompiled = errors.New("node graph has been compiled")

type Node[S any] struct {
	Key  string
	Kind string
	Run  func(context.Context, S) (S, error)
}

func Step[S any](key, kind string, run func(context.Context, S) (S, error)) Node[S] {
	return Node[S]{Key: key, Kind: kind, Run: run}
}

type branch[S any] struct {
	choose  func(context.Context, S) (string, error)
	allowed map[string]struct{}
}

type edge struct{ from, to string }

type Builder[S any] struct {
	maxNodes int
	nodes    map[string]Node[S]
	edges    []edge
	branches map[string]branch[S]
	err      error
	compiled bool
}

func New[S any](maxNodes int) *Builder[S] {
	if maxNodes <= 0 {
		maxNodes = 1
	}
	return &Builder[S]{maxNodes: maxNodes, nodes: make(map[string]Node[S]), branches: make(map[string]branch[S])}
}

func (b *Builder[S]) Nodes(nodes ...Node[S]) *Builder[S] {
	if !b.mutable() {
		return b
	}
	for _, node := range nodes {
		if strings.TrimSpace(node.Key) == "" || strings.TrimSpace(node.Kind) == "" || node.Run == nil {
			b.err = errors.New("node graph step requires key, kind and runner")
			return b
		}
		if _, exists := b.nodes[node.Key]; exists {
			b.err = fmt.Errorf("node graph step %q already exists", node.Key)
			return b
		}
		if len(b.nodes) >= b.maxNodes {
			b.err = fmt.Errorf("node graph exceeds node limit %d", b.maxNodes)
			return b
		}
		b.nodes[node.Key] = node
	}
	return b
}

func (b *Builder[S]) Path(keys ...string) *Builder[S] {
	if !b.mutable() {
		return b
	}
	if len(keys) < 2 {
		b.err = errors.New("node graph path requires at least two nodes")
		return b
	}
	for i := 0; i < len(keys)-1; i++ {
		b.connect(keys[i], keys[i+1])
		if b.err != nil {
			return b
		}
	}
	return b
}

func (b *Builder[S]) Branch(from string, choose func(context.Context, S) (string, error), endpoints ...string) *Builder[S] {
	if !b.mutable() {
		return b
	}
	if choose == nil || len(endpoints) == 0 {
		b.err = errors.New("node graph branch requires selector and endpoints")
		return b
	}
	if _, ok := b.nodes[from]; !ok {
		b.err = fmt.Errorf("node graph branch source %q is missing", from)
		return b
	}
	allowed := make(map[string]struct{}, len(endpoints))
	for _, endpoint := range endpoints {
		if _, ok := b.nodes[endpoint]; !ok {
			b.err = fmt.Errorf("node graph branch endpoint %q is missing", endpoint)
			return b
		}
		allowed[endpoint] = struct{}{}
		b.connect(from, endpoint)
		if b.err != nil {
			return b
		}
	}
	b.branches[from] = branch[S]{choose: choose, allowed: allowed}
	return b
}

func (b *Builder[S]) If(enabled bool, build func(*Builder[S])) *Builder[S] {
	if enabled && build != nil && b.mutable() {
		build(b)
	}
	return b
}

type Graph[S any] struct {
	nodes    map[string]Node[S]
	outgoing map[string][]string
	branches map[string]branch[S]
}

type EventStatus string

const (
	EventStarted   EventStatus = "started"
	EventCompleted EventStatus = "completed"
	EventFailed    EventStatus = "failed"
)

// Event deliberately contains graph structure only. Request state and node
// inputs are never exposed to diagnostics observers.
type Event struct {
	Node   string
	Kind   string
	Status EventStatus
}

type Observer func(Event)

func (b *Builder[S]) Compile() (*Graph[S], error) {
	if b == nil {
		return nil, errors.New("nil node graph builder")
	}
	if b.err != nil {
		return nil, b.err
	}
	if len(b.nodes) == 0 {
		return nil, errors.New("node graph has no nodes")
	}
	if err := validateAcyclic(b.nodes, b.edges); err != nil {
		return nil, err
	}
	b.compiled = true
	nodes := make(map[string]Node[S], len(b.nodes))
	for key, node := range b.nodes {
		nodes[key] = node
	}
	outgoing := make(map[string][]string, len(nodes))
	for _, edge := range b.edges {
		outgoing[edge.from] = append(outgoing[edge.from], edge.to)
	}
	branches := make(map[string]branch[S], len(b.branches))
	for key, value := range b.branches {
		allowed := make(map[string]struct{}, len(value.allowed))
		for endpoint := range value.allowed {
			allowed[endpoint] = struct{}{}
		}
		branches[key] = branch[S]{choose: value.choose, allowed: allowed}
	}
	return &Graph[S]{nodes: nodes, outgoing: outgoing, branches: branches}, nil
}

func (g *Graph[S]) Invoke(ctx context.Context, start, end string, state S) (S, error) {
	return g.InvokeObserved(ctx, start, end, state, nil)
}

func (g *Graph[S]) InvokeObserved(ctx context.Context, start, end string, state S, observer Observer) (S, error) {
	if g == nil {
		return state, errors.New("nil node graph")
	}
	if _, ok := g.nodes[start]; !ok {
		return state, fmt.Errorf("node graph start %q is missing", start)
	}
	if _, ok := g.nodes[end]; !ok {
		return state, fmt.Errorf("node graph end %q is missing", end)
	}
	current := start
	visited := make(map[string]struct{}, len(g.nodes))
	for {
		if err := ctx.Err(); err != nil {
			return state, err
		}
		if _, seen := visited[current]; seen {
			return state, fmt.Errorf("node graph revisited %q", current)
		}
		visited[current] = struct{}{}
		node := g.nodes[current]
		emit(observer, Event{Node: node.Key, Kind: node.Kind, Status: EventStarted})
		var err error
		state, err = node.Run(ctx, state)
		if err != nil {
			emit(observer, Event{Node: node.Key, Kind: node.Kind, Status: EventFailed})
			return state, fmt.Errorf("node graph step %q: %w", current, err)
		}
		emit(observer, Event{Node: node.Key, Kind: node.Kind, Status: EventCompleted})
		if current == end {
			return state, nil
		}
		if branch, ok := g.branches[current]; ok {
			next, err := branch.choose(ctx, state)
			if err != nil {
				return state, fmt.Errorf("node graph branch %q: %w", current, err)
			}
			if _, allowed := branch.allowed[next]; !allowed {
				return state, fmt.Errorf("node graph branch %q selected undeclared endpoint %q", current, next)
			}
			current = next
			continue
		}
		next := g.outgoing[current]
		if len(next) != 1 {
			return state, fmt.Errorf("node graph step %q has %d next steps", current, len(next))
		}
		current = next[0]
	}
}

func emit(observer Observer, event Event) {
	if observer != nil {
		observer(event)
	}
}

func (g *Graph[S]) NodeCount() int {
	if g == nil {
		return 0
	}
	return len(g.nodes)
}
func (g *Graph[S]) HasNode(key string) bool {
	if g == nil {
		return false
	}
	_, ok := g.nodes[key]
	return ok
}

func (b *Builder[S]) mutable() bool {
	if b == nil {
		return false
	}
	if b.compiled && b.err == nil {
		b.err = ErrCompiled
	}
	return b.err == nil
}

func (b *Builder[S]) connect(from, to string) {
	if _, ok := b.nodes[from]; !ok {
		b.err = fmt.Errorf("node graph edge source %q is missing", from)
		return
	}
	if _, ok := b.nodes[to]; !ok {
		b.err = fmt.Errorf("node graph edge target %q is missing", to)
		return
	}
	for _, edge := range b.edges {
		if edge.from == from && edge.to == to {
			b.err = fmt.Errorf("node graph edge %q -> %q already exists", from, to)
			return
		}
	}
	b.edges = append(b.edges, edge{from: from, to: to})
}

func validateAcyclic[S any](nodes map[string]Node[S], edges []edge) error {
	adj := make(map[string][]string, len(nodes))
	for _, edge := range edges {
		adj[edge.from] = append(adj[edge.from], edge.to)
	}
	state := make(map[string]uint8, len(nodes))
	var visit func(string) error
	visit = func(key string) error {
		if state[key] == 1 {
			return fmt.Errorf("node graph contains cycle at %q", key)
		}
		if state[key] == 2 {
			return nil
		}
		state[key] = 1
		for _, next := range adj[key] {
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
