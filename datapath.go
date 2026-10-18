package jaml

import (
	"slices"
	"strings"
)

type dataPath struct {
	parent *dataPath
	key    string
	index  int
	keyed  bool
}

func (p *dataPath) atKey(key string) *dataPath {
	return &dataPath{parent: p, key: key, keyed: true}
}

func (p *dataPath) atIndex(index int) *dataPath {
	return &dataPath{parent: p, index: index}
}

func (p *dataPath) String() string {
	var steps []*dataPath
	for step := p; step != nil; step = step.parent {
		steps = append(steps, step)
	}
	var rendered strings.Builder
	rendered.WriteString("$")
	for _, step := range slices.Backward(steps) {
		if step.keyed {
			rendered.WriteString(keyStep(step.key))
		} else {
			rendered.WriteString(indexStep(step.index))
		}
	}
	return rendered.String()
}
