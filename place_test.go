package jaml

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"
)

var leadingPlace = regexp.MustCompile(`^\$(\.|\[|: | )`)

type _ErrorSite struct {
	name  string
	place string
	err   func(t *testing.T, sb *Sandbox) error
}

func cancelledContext(t *testing.T) context.Context {
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errors.New("user hit stop"))
	return ctx
}

func resolveError(ctx context.Context, e *Expression, args ...any) error {
	var out any
	return resolveInto(ctx, &out, e, args...)
}

var errorSites = []_ErrorSite{
	{"nil expression option", "$: ", func(t *testing.T, sb *Sandbox) error {
		_, err := sb.MakeExpression(t.Context(), "1", nil)
		return err
	}},
	{"bad sigil", "$: ", func(t *testing.T, sb *Sandbox) error {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: 1", WithSigil("{"))
		return err
	}},
	{"bad coercion", "$: ", func(t *testing.T, sb *Sandbox) error {
		_, err := sb.MakeExpression(t.Context(), "1", WithNullCoercion(9))
		return err
	}},
	{"sigil on an expression", "$: ", func(t *testing.T, sb *Sandbox) error {
		_, err := sb.MakeExpression(t.Context(), "1", WithSigil("@"))
		return err
	}},
	{"bad sandbox option", "$: ", func(t *testing.T, _ *Sandbox) error {
		_, err := MakeTrustedSandbox(t.Context(), WithMaxConcurrency(0))
		return err
	}},
	{"nil sandbox option", "$: ", func(t *testing.T, _ *Sandbox) error {
		_, err := MakeTrustedSandbox(t.Context(), nil)
		return err
	}},
	{"memory limit out of range", "$: ", func(t *testing.T, _ *Sandbox) error {
		_, err := MakeTrustedSandbox(t.Context(), WithMemoryLimit(64<<20))
		return err
	}},
	{"unmade sandbox", "$: ", func(t *testing.T, _ *Sandbox) error {
		_, err := new(Sandbox).MakeExpression(t.Context(), "1")
		return err
	}},
	{"unmade expression", "$: ", func(t *testing.T, _ *Sandbox) error {
		return resolveError(t.Context(), new(Expression))
	}},
	{"expression syntax", "$: ", func(t *testing.T, sb *Sandbox) error {
		_, err := sb.MakeExpression(t.Context(), "1 +")
		return err
	}},
	{"expression not a function", "$: ", func(t *testing.T, sb *Sandbox) error {
		_, err := sb.MakeExpression(t.Context(), "parseInt", AsFunction())
		return err
	}},
	{"expression not UTF-8", "$: ", func(t *testing.T, sb *Sandbox) error {
		_, err := sb.MakeExpression(t.Context(), "'\xff'")
		return err
	}},
	{"jaml not UTF-8", "$: ", func(t *testing.T, sb *Sandbox) error {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: \xff")
		return err
	}},
	{"yaml syntax", "$: ", func(t *testing.T, sb *Sandbox) error {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: [1")
		return err
	}},
	{"second document", "$: ", func(t *testing.T, sb *Sandbox) error {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: 1\n---\nb: 2\n")
		return err
	}},
	{"code syntax in a document", "$.a: ", func(t *testing.T, sb *Sandbox) error {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: ${ 1 + }\n")
		return err
	}},
	{"cancelled build", "$: ", func(t *testing.T, sb *Sandbox) error {
		_, err := sb.MakeExpression(cancelledContext(t), "1")
		return err
	}},
	{"cancelled call", "$.a: ", func(t *testing.T, sb *Sandbox) error {
		return resolveError(cancelledContext(t), mustJaml(t, sb, "a: 1\n").At("a"))
	}},
	{"cancelled call at the root", "$: ", func(t *testing.T, sb *Sandbox) error {
		return resolveError(cancelledContext(t), mustExpression(t, sb, "1"))
	}},
	{"cancelled copy", "$.a: ", func(t *testing.T, sb *Sandbox) error {
		_, err := mustJaml(t, sb, "a: 1\n").At("a").Copy(cancelledContext(t))
		return err
	}},
	{"missing path", "$.a ", func(t *testing.T, sb *Sandbox) error {
		return resolveError(t.Context(), mustJaml(t, sb, "a: 1\n").At("a", "b"))
	}},
	{"missing path not UTF-8", "$ ", func(t *testing.T, sb *Sandbox) error {
		return resolveError(t.Context(), mustJaml(t, sb, "a: 1\n").At("\xff"))
	}},
	{"bad value", "$: ", func(t *testing.T, sb *Sandbox) error {
		return resolveError(WithValues(t.Context(), map[string]any{"bad": make(chan int)}), mustExpression(t, sb, "1"))
	}},
	{"bad argument", "$: ", func(t *testing.T, sb *Sandbox) error {
		return resolveError(t.Context(), mustExpression(t, sb, "(a) => a"), make(chan int))
	}},
	{"bad script", "$: ", func(t *testing.T, sb *Sandbox) error {
		return resolveError(WithScript(t.Context(), "let ="), mustExpression(t, sb, "1"))
	}},
	{"throwing script", "$: ", func(t *testing.T, sb *Sandbox) error {
		return resolveError(WithScript(t.Context(), "throw new Error('no')"), mustExpression(t, sb, "1"))
	}},
	{"bad target", "$: ", func(t *testing.T, sb *Sandbox) error {
		_, err := Resolve[func()](t.Context(), mustExpression(t, sb, "1"))
		return err
	}},
	{"evaluation", "$.a: ", func(t *testing.T, sb *Sandbox) error {
		return resolveError(t.Context(), mustJaml(t, sb, "a: ${ nope }\n"))
	}},
	{"strict undefined", "$.a[0]: ", func(t *testing.T, sb *Sandbox) error {
		return resolveError(t.Context(), mustJaml(t, sb, "a: [\"${ undefined }\"]\n"))
	}},
	{"coercion", "$.a: ", func(t *testing.T, sb *Sandbox) error {
		_, err := Resolve[map[string]int](t.Context(), mustJaml(t, sb, "a: x\n"))
		return err
	}},
	{"closed sandbox", "$.a: ", func(t *testing.T, _ *Sandbox) error {
		ctx, end := context.WithCancel(t.Context())
		t.Cleanup(end)
		sb, err := MakeTrustedSandbox(ctx)
		if err != nil {
			return err
		}
		document := mustJaml(t, sb, "a: 1\n")
		end()
		return resolveError(t.Context(), document.At("a"))
	}},
	{"closed expression", "$.a: ", func(t *testing.T, sb *Sandbox) error {
		ctx, end := context.WithCancel(t.Context())
		t.Cleanup(end)
		document, err := sb.MakeExpressionFromJaml(ctx, "a: 1\n")
		if err != nil {
			return err
		}
		end()
		return resolveError(t.Context(), document.At("a"))
	}},
}

func TestEveryErrorLeadsWithAPath(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, site := range errorSites {
			err := site.err(t, sb)
			if err == nil {
				t.Fatalf("%s: no error", site.name)
			}
			if !errors.Is(err, ExpressionError) || !leadingPlace.MatchString(err.Error()) || err.Error()[:len(site.place)] != site.place {
				t.Fatalf("%s: want a leading %q, got %q", site.name, site.place, err)
			}
		}
	})
}

func TestAnErrorWithoutAPlaceIsAtTheRoot(t *testing.T) {
	expectEqual(t, expressionError{kind: EvaluationFailed, cause: errors.New("boom")}.Error(), "$: boom: evaluation failed: expression error")
	expectEqual(t, failure{Kind: "EvaluationFailed", Message: "boom"}.rebuild().Error(), "$: boom: evaluation failed: expression error")
	expectEqual(t, describeFailure(fail(CoercionFailed, "boom").within("$.a")).rebuild().Error(), "$.a: boom: coercion failed: expression error")
}

func TestAClosedErrorNamesThePathAndTheContextThatEnded(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		ctx, end := context.WithCancel(t.Context())
		document, err := sb.MakeExpressionFromJaml(ctx, "a:\n  b: ${ (() => { while (true) {} })() }\n")
		if err != nil {
			t.Fatal(err)
		}
		end()
		expectError(t, resolveError(t.Context(), document.At("a", "b")), Closed, "$.a.b: its own context, the one it was made or copied with, has ended")
		_, err = document.At("a").Copy(t.Context())
		expectError(t, err, Closed, "$.a: its own context, the one it was made or copied with, has ended")

		running, stop := context.WithCancel(t.Context())
		spinning, err := sb.MakeExpressionFromJaml(running, "a:\n  b: ${ (() => { while (true) {} })() }\n")
		if err != nil {
			t.Fatal(err)
		}
		time.AfterFunc(50*time.Millisecond, stop)
		expectError(t, resolveError(t.Context(), spinning.At("a", "b")), Closed, "$.a.b: its own context, the one it was made or copied with, ended while it ran")
	})
	for _, kind := range sandboxKinds {
		t.Run("sandbox "+kind.name, func(t *testing.T) {
			lifetime, end := context.WithCancel(t.Context())
			sb, err := kind.make(lifetime)
			if err != nil {
				t.Skip(err)
			}
			document := mustJaml(t, sb, "a:\n  b: 1\n")
			kept, err := document.Copy(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			end()
			expectError(t, resolveError(t.Context(), kept.At("a", "b")), Closed, "$.a.b: the sandbox's context has ended")
			_, err = sb.MakeExpression(t.Context(), "1")
			expectError(t, err, Closed, "$: the sandbox's context has ended")
		})
	}
}
