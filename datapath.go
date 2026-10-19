package jaml

import (
	"slices"
	"strconv"
	"strings"
)

type dataPath struct {
	parent *dataPath
	key    string
	index  int
	keyed  bool
	levels int
}

func (p *dataPath) atKey(key string) *dataPath {
	return &dataPath{parent: p, key: key, keyed: true, levels: p.depth() + 1}
}

func (p *dataPath) atIndex(index int) *dataPath {
	return &dataPath{parent: p, index: index, levels: p.depth() + 1}
}

func selectedPath(path []string) *dataPath {
	var selected *dataPath
	for _, step := range path {
		selected = selected.atStep(step)
	}
	return selected
}

func (p *dataPath) atStep(step string) *dataPath {
	if index, err := strconv.Atoi(step); err == nil && index >= 0 && strconv.Itoa(index) == step {
		return p.atIndex(index)
	}
	return p.atKey(step)
}

func (p *dataPath) depth() int {
	if p == nil {
		return 0
	}
	return p.levels
}

func (p *dataPath) String() string {
	return p.spelledFrom("$")
}

func (p *dataPath) spelledFrom(root string) string {
	var steps []*dataPath
	for step := p; step != nil; step = step.parent {
		steps = append(steps, step)
	}
	var rendered strings.Builder
	rendered.WriteString(root)
	for _, step := range slices.Backward(steps) {
		if step.keyed {
			rendered.WriteString(keyStep(step.key))
		} else {
			rendered.WriteString(indexStep(step.index))
		}
	}
	return rendered.String()
}

func keyStep(key string) string {
	if isPlainKey(key) {
		return "." + clip(key, keyExcerptLength)
	}
	return "[" + strconv.Quote(clip(key, keyExcerptLength)) + "]"
}

func indexStep(index int) string {
	return "[" + strconv.Itoa(index) + "]"
}

func isPlainKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		letter := r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		digit := r >= '0' && r <= '9'
		if !letter && !(digit && i > 0) {
			return false
		}
	}
	return true
}
