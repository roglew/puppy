package main

import (
	"runtime"
	"testing"
	"time"
)

func testStorage() *SQLiteStorage {
	s, _ := InMemoryStorage(NullLogger())
	return s
}

func checkTags(t *testing.T, result, expected []string) {
	_, f, ln, _ := runtime.Caller(1)

	if len(result) != len(expected) {
		t.Errorf("Failed tag test at %s:%d. Expected %s, got %s", f, ln, expected, result)
		return
	}

	for i, a := range result {
		b := expected[i]
		if a != b {
			t.Errorf("Failed tag test at %s:%d. Expected %s, got %s", f, ln, expected, result)
			return
		}
	}
}

func TestTagging(t *testing.T) {
	req := testReq()
	storage := testStorage()
	defer storage.Close()

	err := SaveNewRequest(storage, req)
	testErr(t, err)
	req1, err := storage.LoadRequest(req.DbId)
	testErr(t, err)
	checkTags(t, req1.Tags(), []string{})

	req.AddTag("foo")
	req.AddTag("bar")
	err = UpdateRequest(storage, req)
	testErr(t, err)
	req2, err := storage.LoadRequest(req.DbId)
	testErr(t, err)
	checkTags(t, req2.Tags(), []string{"foo", "bar"})

	req.RemoveTag("foo")
	err = UpdateRequest(storage, req)
	testErr(t, err)
	req3, err := storage.LoadRequest(req.DbId)
	testErr(t, err)
	checkTags(t, req3.Tags(), []string{"bar"})
}

func TestTime(t *testing.T) {
	req := testReq()
	req.StartDatetime = time.Unix(0, 1234567)
	req.EndDatetime = time.Unix(0, 2234567)
	storage := testStorage()
	defer storage.Close()

	err := SaveNewRequest(storage, req)
	testErr(t, err)

	req1, err := storage.LoadRequest(req.DbId)
	testErr(t, err)
	tstart := req1.StartDatetime.UnixNano()
	tend := req1.EndDatetime.UnixNano()

	if tstart != 1234567 {
		t.Errorf("Start time not saved properly. Expected 1234567, got %d", tstart)
	}

	if tend != 2234567 {
		t.Errorf("End time not saved properly. Expected 1234567, got %d", tend)
	}
}
