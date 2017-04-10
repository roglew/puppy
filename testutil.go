package main

import (
	"testing"
	"runtime"
)

func testReq() (*ProxyRequest) {
	testReq, _ := ProxyRequestFromBytes(
		[]byte("POST /?foo=bar HTTP/1.1\r\nFoo: Bar\r\nCookie: cookie=choco\r\nContent-Length: 7\r\n\r\nfoo=baz"),
		"foobaz",
		80,
		false,
	)

	testRsp, _ := ProxyResponseFromBytes(
		[]byte("HTTP/1.1 200 OK\r\nSet-Cookie: cockie=cocks\r\nContent-Length: 4\r\n\r\nBBBB"),
	)

	testReq.ServerResponse = testRsp

	return testReq
}

func testErr(t *testing.T, err error) {
	if err != nil {
		_, f, ln, _ := runtime.Caller(1)
		t.Errorf("Failed test with error at %s:%d. Error: %s", f, ln, err)
	}

}
