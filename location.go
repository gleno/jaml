package jaml

import (
	"fmt"
	"strconv"
	"strings"
)

type position struct {
	line   int
	column int
}

type location struct {
	parent *location
	step   string
	at     position
}

func rootLocation(at position) location {
	return location{at: at}
}

func (l location) key(key string, at position) location {
	return l.descend(renderKey(key), at)
}

func (l location) index(index int, at position) location {
	return l.descend("["+strconv.Itoa(index)+"]", at)
}

func (l location) descend(step string, at position) location {
	parent := l
	if at == (position{}) {
		at = l.at
	}
	return location{parent: &parent, step: step, at: at}
}

func (l location) path() string {
	var steps []string
	for current := &l; current != nil; current = current.parent {
		steps = append(steps, current.step)
	}
	var path strings.Builder
	path.WriteString("$")
	for i := len(steps) - 1; i >= 0; i-- {
		path.WriteString(steps[i])
	}
	return path.String()
}

func (l location) String() string {
	if l.at == (position{}) {
		return l.path()
	}
	return fmt.Sprintf("%s (line %d, column %d)", l.path(), l.at.line, l.at.column)
}

func renderKey(key string) string {
	if isPlainKey(key) {
		return "." + key
	}
	return "[" + strconv.Quote(key) + "]"
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
