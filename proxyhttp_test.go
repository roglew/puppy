package main

import (
	"net/url"
	"runtime"
	"testing"

	// "bytes"
	// "net/http"
	// "bufio"
	// "os"
)

type statusLiner interface {
	StatusLine() string
}

func checkStr(t *testing.T, result, expected string) {
	if result != expected {
		_, f, ln, _ := runtime.Caller(1)
		t.Errorf("Failed search test at %s:%d. Expected '%s', got '%s'", f, ln, expected, result)
	}
}

func checkStatusline(t *testing.T, msg statusLiner, expected string) {
	result := msg.StatusLine()
	checkStr(t, expected, result)
}

func TestStatusline(t *testing.T) {
	req := testReq()
	checkStr(t, req.StatusLine(), "POST /?foo=bar HTTP/1.1")

	req.Method = "GET"
	checkStr(t, req.StatusLine(), "GET /?foo=bar HTTP/1.1")

	req.URL.Fragment = "foofrag"
	checkStr(t, req.StatusLine(), "GET /?foo=bar#foofrag HTTP/1.1")

	req.URL.User = url.UserPassword("foo", "bar")
	checkStr(t, req.StatusLine(), "GET /?foo=bar#foofrag HTTP/1.1")

	req.URL.Scheme = "http"
	checkStr(t, req.StatusLine(), "GET /?foo=bar#foofrag HTTP/1.1")

	req.URL.Opaque = "foobaropaque"
	checkStr(t, req.StatusLine(), "GET /?foo=bar#foofrag HTTP/1.1")
	req.URL.Opaque = ""

	req.URL.Host = "foobarhost"
	checkStr(t, req.StatusLine(), "GET /?foo=bar#foofrag HTTP/1.1")

	// rsp.Status is actually "200 OK" but the "200 " gets stripped from the front
	rsp := req.ServerResponse
	checkStr(t, rsp.StatusLine(), "HTTP/1.1 200 OK")

	rsp.StatusCode = 404
	checkStr(t, rsp.StatusLine(), "HTTP/1.1 404 200 OK")

	rsp.Status = "is not there plz"
	checkStr(t, rsp.StatusLine(), "HTTP/1.1 404 is not there plz")

	// Same as with "200 OK"
	rsp.Status = "404 is not there plz"
	checkStr(t, rsp.StatusLine(), "HTTP/1.1 404 is not there plz")
}

func TestEq(t *testing.T) {
	req1 := testReq()
	req2 := testReq()

	// Requests

	if !req1.Eq(req2) {
		t.Error("failed eq")
	}

	if !req2.Eq(req1) {
		t.Error("failed eq")
	}

	req1.Header = map[string][]string{
		"Foo": []string{"Bar", "Baz"},
		"Foo2": []string{"Bar2", "Baz2"},
		"Cookie": []string{"cookie=cocks"},
	}
	req2.Header = map[string][]string{
		"Foo": []string{"Bar", "Baz"},
		"Foo2": []string{"Bar2", "Baz2"},
		"Cookie": []string{"cookie=cocks"},
	}

	if !req1.Eq(req2) {
		t.Error("failed eq")
	}

	req2.Header = map[string][]string{
		"Foo": []string{"Baz", "Bar"},
		"Foo2": []string{"Bar2", "Baz2"},
		"Cookie": []string{"cookie=cocks"},
	}
	if req1.Eq(req2) {
		t.Error("failed eq")
	}

	req2.Header = map[string][]string{
		"Foo": []string{"Bar", "Baz"},
		"Foo2": []string{"Bar2", "Baz2"},
		"Cookie": []string{"cookiee=cocks"},
	}
	if req1.Eq(req2) {
		t.Error("failed eq")
	}

	req2 = testReq()
	req2.URL.Host = "foobar"
	if req1.Eq(req2) {
		t.Error("failed eq")
	}
	req2 = testReq()

	// Responses

	if !req1.ServerResponse.Eq(req2.ServerResponse) {
		t.Error("failed eq")
	}

	if !req2.ServerResponse.Eq(req1.ServerResponse) {
		t.Error("failed eq")
	}

	req2.ServerResponse.StatusCode = 404
	if req1.ServerResponse.Eq(req2.ServerResponse) {
		t.Error("failed eq")
	}

}

func TestDeepClone(t *testing.T) {
	req1 := testReq()
	req2 := req1.DeepClone()

	if !req1.Eq(req2) {
		t.Errorf("cloned request does not match original.\nExpected:\n%s\n-----\nGot:\n%s\n-----",
			string(req1.FullMessage()), string(req2.FullMessage()))
	}

	if !req1.ServerResponse.Eq(req2.ServerResponse) {
		t.Errorf("cloned response does not match original.\nExpected:\n%s\n-----\nGot:\n%s\n-----",
			string(req1.ServerResponse.FullMessage()), string(req2.ServerResponse.FullMessage()))
	}

	rsp1 := req1.ServerResponse.Clone()
	rsp1.Status = "foobarbaz"
	rsp2 := rsp1.Clone()
	if !rsp1.Eq(rsp2) {
		t.Errorf("cloned response does not match original.\nExpected:\n%s\n-----\nGot:\n%s\n-----",
			string(rsp1.FullMessage()), string(rsp2.FullMessage()))
	}

	rsp1 = req1.ServerResponse.Clone()
	rsp1.ProtoMinor = 7
	rsp2 = rsp1.Clone()
	if !rsp1.Eq(rsp2) {
		t.Errorf("cloned response does not match original.\nExpected:\n%s\n-----\nGot:\n%s\n-----",
			string(rsp1.FullMessage()), string(rsp2.FullMessage()))
	}

	rsp1 = req1.ServerResponse.Clone()
	rsp1.StatusCode = 234
	rsp2 = rsp1.Clone()
	if !rsp1.Eq(rsp2) {
		t.Errorf("cloned response does not match original.\nExpected:\n%s\n-----\nGot:\n%s\n-----",
			string(rsp1.FullMessage()), string(rsp2.FullMessage()))
	}
}

// func TestFromBytes(t *testing.T) {
// 	rsp, err := ProxyResponseFromBytes([]byte("HTTP/1.1 200 OK\r\nFoo: Bar\r\n\r\nAAAA"))
// 	if err != nil {
// 		panic(err)
// 	}
// 	checkStr(t, string(rsp.BodyBytes()), "AAAA")
// 	checkStr(t, string(rsp.Header.Get("Content-Length")[0]), "4")

// 	//rspbytes := []byte("HTTP/1.0 200 OK\r\nServer: BaseHTTP/0.3 Python/2.7.11\r\nDate: Fri, 10 Mar 2017 18:21:27 GMT\r\n\r\nCLIENT VALUES:\nclient_address=('127.0.0.1', 62069) (1.0.0.127.in-addr.arpa)\ncommand=GET\npath=/?foo=foobar\nreal path=/\nquery=foo=foobar\nrequest_version=HTTP/1.1\n\nSERVER VALUES:\nserver_version=BaseHTTP/0.3\nsys_version=Python/2.7.11\nprotocol_version=HTTP/1.0")
// 	rspbytes := []byte("HTTP/1.0 200 OK\r\n\r\nAAAA")
// 	buf := bytes.NewBuffer(rspbytes)
// 	httpRsp, err := http.ReadResponse(bufio.NewReader(buf), nil)
// 	httpRsp.Close = false
// 	//rsp2 := NewProxyResponse(httpRsp)
// 	buf2 := bytes.NewBuffer(make([]byte, 0))
// 	httpRsp.Write(buf2)
// 	httpRsp2, err := http.ReadResponse(bufio.NewReader(buf2), nil)
// 	// fmt.Println(string(rsp2.FullMessage()))
// 	// fmt.Println(rsp2.Header)
// 	// if len(rsp2.Header["Connection"]) > 1 {
// 	// 	t.Errorf("too many connection headers")
// 	// }
// }
