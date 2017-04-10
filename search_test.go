package main

import (
	"runtime"
	"strconv"
	"testing"
)

func checkSearch(t *testing.T, req *ProxyRequest, expected bool, args ...interface{}) {
	checker, err := NewRequestChecker(args...)
	if err != nil { t.Error(err.Error()) }
	result := checker(req)
	if result != expected {
		_, f, ln, _ := runtime.Caller(1)
		t.Errorf("Failed search test at %s:%d. Expected %s, got %s", f, ln, strconv.FormatBool(expected), strconv.FormatBool(result))
	}
}

func TestAllSearch(t *testing.T) {
	checker, err := NewRequestChecker(FieldAll, StrContains, "foo")
	if err != nil { t.Error(err.Error()) }
	req := testReq()
	if !checker(req) { t.Error("Failed to match FieldAll, StrContains") }
}

func TestBodySearch(t *testing.T) {
	req := testReq()

	checkSearch(t, req, true, FieldAllBody, StrContains, "foo")
	checkSearch(t, req, true, FieldAllBody, StrContains, "oo=b")
	checkSearch(t, req, true, FieldAllBody, StrContains, "BBBB")
	checkSearch(t, req, false, FieldAllBody, StrContains, "FOO")

	checkSearch(t, req, true, FieldResponseBody, StrContains, "BBBB")
	checkSearch(t, req, false, FieldResponseBody, StrContains, "foo")

	checkSearch(t, req, false, FieldRequestBody, StrContains, "BBBB")
	checkSearch(t, req, true, FieldRequestBody, StrContains, "foo")
}

func TestHeaderSearch(t *testing.T) {
	req := testReq()

	checkSearch(t, req, true, FieldBothHeaders, StrContains, "Foo")
	checkSearch(t, req, true, FieldBothHeaders, StrContains, "Bar")
	checkSearch(t, req, true, FieldBothHeaders, StrContains, "Foo", StrContains, "Bar")
	checkSearch(t, req, false, FieldBothHeaders, StrContains, "Bar", StrContains, "Bar")
	checkSearch(t, req, false, FieldBothHeaders, StrContains, "Foo", StrContains, "Foo")
}

func TestRegexpSearch(t *testing.T) {
	req := testReq()

	checkSearch(t, req, true, FieldRequestBody, StrContainsRegexp, "o.b")
	checkSearch(t, req, true, FieldRequestBody, StrContainsRegexp, "baz$")
	checkSearch(t, req, true, FieldRequestBody, StrContainsRegexp, "^f.+z")
	checkSearch(t, req, false, FieldRequestBody, StrContainsRegexp, "^baz")
}
