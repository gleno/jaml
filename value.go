package jaml

type value interface {
	kind() string
}

type nullValue struct{}

type undefinedValue struct{}

type boolValue struct {
	truth bool
	raw   string
}

type numberValue struct {
	text string
	raw  string
}

type stringValue string

type listValue []element

type objectValue []field

type element struct {
	at    position
	value value
}

type field struct {
	key   string
	at    position
	value value
}

type unrepresentable struct {
	raw    string
	reason string
}

type expressionValue struct {
	expression expression
}

type interpolationValue []segment

type segment struct {
	text       string
	expression *expression
}

func (nullValue) kind() string          { return "null" }
func (undefinedValue) kind() string     { return "undefined" }
func (boolValue) kind() string          { return "bool" }
func (numberValue) kind() string        { return "number" }
func (stringValue) kind() string        { return "string" }
func (listValue) kind() string          { return "list" }
func (objectValue) kind() string        { return "object" }
func (unrepresentable) kind() string    { return "number" }
func (expressionValue) kind() string    { return "expression" }
func (interpolationValue) kind() string { return "string" }

func (b boolValue) text() string {
	if b.raw != "" {
		return b.raw
	}
	if b.truth {
		return "true"
	}
	return "false"
}

func (n numberValue) asWritten() string {
	if n.raw != "" {
		return n.raw
	}
	return n.text
}

func (o objectValue) lookup(key string) (field, bool) {
	for _, candidate := range o {
		if candidate.key == key {
			return candidate, true
		}
	}
	return field{}, false
}
