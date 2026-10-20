package jaml

import (
	"context"
	"fmt"
	"sync"
	"unicode/utf8"
)

const (
	consoleLineCap  = 1000
	consoleByteCap  = 1 << 20
	consoleLineSize = 8 << 10
	consoleEllipsis = "…"
)

type Level int

const (
	Log Level = iota
	Info
	Warn
	Error
	Debug
)

type Line struct {
	Path  []string
	Level Level
	Text  string
}

type _ConsoleKey struct{}

func WithConsole(ctx context.Context, sink func(Line)) context.Context {
	return context.WithValue(ctx, _ConsoleKey{}, sink)
}

func sinkOf(ctx context.Context) func(Line) {
	var sink, _ = ctx.Value(_ConsoleKey{}).(func(Line))
	return sink
}

func (l Level) known() bool {
	return l >= Log && l <= Debug
}

func clipLine(text string) string {
	if len(text) <= consoleLineSize {
		return text
	}
	var cut = consoleLineSize - len(consoleEllipsis)
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + consoleEllipsis
}

type _Console struct {
	sink      func(Line)
	selection []string
	mu        sync.Mutex
	changed   *sync.Cond
	queue     []Line
	tally     _Tally
	replay    _Replay
	live      int
	closed    bool
	pumped    chan struct{}
}

type _Tally struct {
	kept    int
	bytes   int
	dropped int
}

type _Replay struct {
	heard  int
	echoed int
}

func (t *_Tally) admits(text string) bool {
	if t.kept == consoleLineCap || t.bytes+len(text) > consoleByteCap {
		t.dropped++
		return false
	}
	t.kept++
	t.bytes += len(text)
	return true
}

func (t _Tally) warning() string {
	return fmt.Sprintf("jaml: dropped %d console lines past the limit of %d lines or %d bytes per Resolve", t.dropped, consoleLineCap, consoleByteCap)
}

func (r *_Replay) fresh() bool {
	r.echoed++
	if r.echoed <= r.heard {
		return false
	}
	r.heard++
	return true
}

func (r *_Replay) restart() {
	r.echoed = 0
}

func consoleFor(ctx context.Context, selection []string) *_Console {
	var sink = sinkOf(ctx)
	if sink == nil {
		return nil
	}
	var c = &_Console{sink: sink, selection: selection, pumped: make(chan struct{})}
	c.changed = sync.NewCond(&c.mu)
	go c.pump()
	return c
}

func (c *_Console) say(line Line) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.admit(line)
}

func (c *_Console) echo(line Line) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.replay.fresh() {
		c.admit(line)
	}
}

func (c *_Console) admit(line Line) {
	line.Text = clipLine(line.Text)
	if c.closed || !c.tally.admits(line.Text) {
		return
	}
	c.queue = append(c.queue, line)
	c.changed.Broadcast()
}

func (c *_Console) attend() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.live++
	c.replay.restart()
	return true
}

func (c *_Console) leave() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.live--
	c.changed.Broadcast()
}

func (c *_Console) finish() {
	if c == nil {
		return
	}
	c.mu.Lock()
	for c.live > 0 {
		c.changed.Wait()
	}
	if c.tally.dropped > 0 {
		c.queue = append(c.queue, Line{Path: c.selection, Level: Warn, Text: c.tally.warning()})
	}
	c.closed = true
	c.changed.Broadcast()
	c.mu.Unlock()
	<-c.pumped
}

func (c *_Console) pump() {
	defer close(c.pumped)
	for {
		c.mu.Lock()
		for len(c.queue) == 0 && !c.closed {
			c.changed.Wait()
		}
		var batch = c.queue
		c.queue = nil
		c.mu.Unlock()
		if len(batch) == 0 {
			return
		}
		for _, line := range batch {
			c.sink(line)
		}
	}
}
