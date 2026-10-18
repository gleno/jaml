package jaml

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const keyExcerptLength = 60

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
		return "." + excerpt(key)
	}
	return "[" + strconv.Quote(excerpt(key)) + "]"
}

func indexStep(index int) string {
	return "[" + strconv.Itoa(index) + "]"
}

func excerpt(key string) string {
	cut := 0
	for range keyExcerptLength {
		_, size := utf8.DecodeRuneInString(key[cut:])
		cut += size
	}
	if cut >= len(key) {
		return key
	}
	return key[:cut] + "…"
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
