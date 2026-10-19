package jaml

import (
	"slices"
	"strconv"
	"strings"
)

type _DataPath struct {
	parent *_DataPath
	key    string
	index  int
	keyed  bool
	levels int
}

func (p *_DataPath) atKey(key string) *_DataPath {
	return &_DataPath{parent: p, key: key, keyed: true, levels: p.depth() + 1}
}

func (p *_DataPath) atIndex(index int) *_DataPath {
	return &_DataPath{parent: p, index: index, levels: p.depth() + 1}
}

func selectedPath(path []string) *_DataPath {
	var selected *_DataPath
	for _, step := range path {
		selected = selected.atStep(step)
	}
	return selected
}

func (p *_DataPath) atStep(step string) *_DataPath {
	if index, err := strconv.Atoi(step); err == nil && index >= 0 && strconv.Itoa(index) == step {
		return p.atIndex(index)
	}
	return p.atKey(step)
}

func (p *_DataPath) depth() int {
	if p == nil {
		return 0
	}
	return p.levels
}

func (p *_DataPath) String() string {
	return p.spelledFrom("$")
}

func (p *_DataPath) spelledFrom(root string) string {
	var steps []*_DataPath
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
		var letter = r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		var digit = r >= '0' && r <= '9'
		if !letter && !(digit && i > 0) {
			return false
		}
	}
	return true
}
