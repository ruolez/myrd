package gcc

import (
	"errors"
	"reflect"
	"testing"
)

// Minimal stand-ins for the testify assertions pion's tests use, so the
// copied tests stay close to upstream without the dependency.

func assertEqual(t testing.TB, want, got any, msg ...any) bool {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Errorf("not equal:\nwant %#v\ngot  %#v %v", want, got, msg)
		return false
	}
	return true
}

func assertEqualf(t testing.TB, want, got any, format string, args ...any) bool {
	t.Helper()
	return assertEqual(t, want, got, append([]any{format}, args...)...)
}

func toFloat(v any) float64 {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint())
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	}
	panic("not a number")
}

func assertInDelta(t testing.TB, want, got any, delta float64, msg ...any) bool {
	t.Helper()
	w, g := toFloat(want), toFloat(got)
	if d := w - g; d > delta || d < -delta {
		t.Errorf("%v and %v differ by more than %v %v", w, g, delta, msg)
		return false
	}
	return true
}

func assertLen(t testing.TB, obj any, n int, msg ...any) bool {
	t.Helper()
	if l := reflect.ValueOf(obj).Len(); l != n {
		t.Errorf("len %d, want %d %v", l, n, msg)
		return false
	}
	return true
}

func isEmpty(obj any) bool {
	rv := reflect.ValueOf(obj)
	switch rv.Kind() {
	case reflect.Slice, reflect.Map, reflect.Array, reflect.String, reflect.Chan:
		return rv.Len() == 0
	case reflect.Invalid:
		return true
	}
	return rv.IsZero()
}

func assertEmpty(t testing.TB, obj any, msg ...any) bool {
	t.Helper()
	if !isEmpty(obj) {
		t.Errorf("not empty: %#v %v", obj, msg)
		return false
	}
	return true
}

func assertNotEmpty(t testing.TB, obj any, msg ...any) bool {
	t.Helper()
	if isEmpty(obj) {
		t.Errorf("empty %v", msg)
		return false
	}
	return true
}

func assertContains(t testing.TB, list, elem any, msg ...any) bool {
	t.Helper()
	rv := reflect.ValueOf(list)
	for i := range rv.Len() {
		if reflect.DeepEqual(rv.Index(i).Interface(), elem) {
			return true
		}
	}
	t.Errorf("%#v does not contain %#v %v", list, elem, msg)
	return false
}

func assertNotPanics(t testing.TB, f func(), msg ...any) bool {
	t.Helper()
	ok := true
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panicked: %v %v", r, msg)
				ok = false
			}
		}()
		f()
	}()
	return ok
}

func assertNoError(t testing.TB, err error, msg ...any) bool {
	t.Helper()
	if err != nil {
		t.Errorf("unexpected error: %v %v", err, msg)
		return false
	}
	return true
}

func assertError(t testing.TB, err error, msg ...any) bool {
	t.Helper()
	if err == nil {
		t.Errorf("expected an error %v", msg)
		return false
	}
	return true
}

func requireNoError(t testing.TB, err error, msg ...any) {
	t.Helper()
	if !assertNoError(t, err, msg...) {
		t.FailNow()
	}
}

func requireEqual(t testing.TB, want, got any, msg ...any) {
	t.Helper()
	if !assertEqual(t, want, got, msg...) {
		t.FailNow()
	}
}

func requireNotEqual(t testing.TB, notWant, got any) {
	t.Helper()
	if reflect.DeepEqual(notWant, got) {
		t.Fatalf("should not be %#v", got)
	}
}

func requireNotNil(t testing.TB, obj any) {
	t.Helper()
	rv := reflect.ValueOf(obj)
	if obj == nil || (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) && rv.IsNil() {
		t.Fatal("unexpected nil")
	}
}

func requireErrorIs(t testing.TB, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error %v is not %v", err, target)
	}
}

func requireLess(t testing.TB, a, b int) {
	t.Helper()
	if a >= b {
		t.Fatalf("%d is not less than %d", a, b)
	}
}
