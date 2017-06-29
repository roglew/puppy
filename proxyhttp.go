package puppy

/*
Wrappers around http.Request and http.Response to add helper functions needed by the proxy
*/

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/deckarep/golang-set"
	"github.com/gorilla/websocket"
	"golang.org/x/net/proxy"
)

const (
	ToServer = iota
	ToClient
)

// A dialer used to create a net.Conn from a network and address
type NetDialer func(network, addr string) (net.Conn, error)

// ProxyResponse is an http.Response with additional fields for use within the proxy
type ProxyResponse struct {
	http.Response
	bodyBytes []byte

	// Id used to reference this response in its associated MessageStorage. Blank string means it is not saved in any MessageStorage
	DbId string

	// If this response was modified by the proxy, Unmangled is the response before it was modified. If the response was not modified, Unmangled is nil.
	Unmangled *ProxyResponse
}

// ProxyRequest is an http.Request with additional fields for use within the proxy
type ProxyRequest struct {
	http.Request

	// Host where this request is intended to be sent when submitted
	DestHost string
	// Port that should be used when this request is submitted
	DestPort int
	// Whether TLS should be used when this request is submitted
	DestUseTLS bool

	// Response received from the server when this request was submitted. If the request does not have any associated response, ServerResponse is nil.
	ServerResponse *ProxyResponse
	// If the request was the handshake for a websocket session, WSMessages will be a slice of all the messages that were sent over that session.
	WSMessages []*ProxyWSMessage
	// If the request was modified by the proxy, Unmangled will point to the unmodified version of the request. Otherwise it is nil.
	Unmangled *ProxyRequest

	// ID used to reference to this request in its associated MessageStorage
	DbId string
	// The time at which this request was submitted
	StartDatetime time.Time
	// The time at which the response to this request was received
	EndDatetime time.Time

	bodyBytes []byte
	tags      mapset.Set

	// The dialer that should be used when this request is submitted
	NetDial NetDialer
}

// WSSession is an extension of websocket.Conn to contain a reference to the ProxyRequest used for the websocket handshake
type WSSession struct {
	websocket.Conn

	// Request used for handshake
	Request *ProxyRequest
}

// ProxyWSMessage represents one message in a websocket session
type ProxyWSMessage struct {
	// The type of websocket message
	Type int

	// The contents of the message
	Message []byte

	// The direction of the message. Either ToServer or ToClient
	Direction int
	// If the message was modified by the proxy, points to the original unmodified message
	Unmangled *ProxyWSMessage
	// The time at which the message was sent (if sent to the server) or received (if received from the server)
	Timestamp time.Time
	// The request used for the handhsake for the session that this session was used for
	Request *ProxyRequest

	// ID used to reference to this message in its associated MessageStorage
	DbId string
}

// PerformConnect submits a CONNECT request for the given host and port over the given connection
func PerformConnect(conn net.Conn, destHost string, destPort int) error {
	connStr := []byte(fmt.Sprintf("CONNECT %s:%d HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n\r\n", destHost, destPort, destHost))
	conn.Write(connStr)
	rsp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return fmt.Errorf("error performing CONNECT handshake: %s", err.Error())
	}
	if rsp.StatusCode != 200 {
		return fmt.Errorf("error performing CONNECT handshake")
	}
	return nil
}

// NewProxyRequest creates a new proxy request with the given destination
func NewProxyRequest(r *http.Request, destHost string, destPort int, destUseTLS bool) *ProxyRequest {
	var retReq *ProxyRequest
	if r != nil {
		// Write/reread the request to make sure we get all the extra headers Go adds into req.Header
		buf := bytes.NewBuffer(make([]byte, 0))
		r.Write(buf)
		httpReq2, err := http.ReadRequest(bufio.NewReader(buf))
		if err != nil {
			panic(err)
		}

		retReq = &ProxyRequest{
			*httpReq2,
			destHost,
			destPort,
			destUseTLS,
			nil,
			make([]*ProxyWSMessage, 0),
			nil,
			"",
			time.Unix(0, 0),
			time.Unix(0, 0),
			make([]byte, 0),
			mapset.NewSet(),
			nil,
		}
	} else {
		newReq, _ := http.NewRequest("GET", "/", nil) // Ignore error since this should be run the same every time and shouldn't error
		newReq.Header.Set("User-Agent", "Puppy-Proxy/1.0")
		newReq.Host = destHost
		retReq = &ProxyRequest{
			*newReq,
			destHost,
			destPort,
			destUseTLS,
			nil,
			make([]*ProxyWSMessage, 0),
			nil,
			"",
			time.Unix(0, 0),
			time.Unix(0, 0),
			make([]byte, 0),
			mapset.NewSet(),
			nil,
		}
	}

	// Load the body
	bodyBuf, _ := ioutil.ReadAll(retReq.Body)
	retReq.SetBodyBytes(bodyBuf)
	return retReq
}

// ProxyRequestFromBytes parses a slice of bytes containing a well-formed HTTP request into a ProxyRequest. Does NOT correct incorrect Content-Length headers
func ProxyRequestFromBytes(b []byte, destHost string, destPort int, destUseTLS bool) (*ProxyRequest, error) {
	buf := bytes.NewBuffer(b)
	httpReq, err := http.ReadRequest(bufio.NewReader(buf))
	if err != nil {
		return nil, err
	}

	return NewProxyRequest(httpReq, destHost, destPort, destUseTLS), nil
}

// NewProxyResponse creates a new ProxyResponse given an http.Response
func NewProxyResponse(r *http.Response) *ProxyResponse {
	// Write/reread the request to make sure we get all the extra headers Go adds into req.Header
	oldClose := r.Close
	r.Close = false
	buf := bytes.NewBuffer(make([]byte, 0))
	r.Write(buf)
	r.Close = oldClose
	httpRsp2, err := http.ReadResponse(bufio.NewReader(buf), nil)
	if err != nil {
		panic(err)
	}
	httpRsp2.Close = false
	retRsp := &ProxyResponse{
		*httpRsp2,
		make([]byte, 0),
		"",
		nil,
	}

	bodyBuf, _ := ioutil.ReadAll(retRsp.Body)
	retRsp.SetBodyBytes(bodyBuf)
	return retRsp
}

// NewProxyResponse parses a ProxyResponse from a slice of bytes containing a well-formed HTTP response. Does NOT correct incorrect Content-Length headers
func ProxyResponseFromBytes(b []byte) (*ProxyResponse, error) {
	buf := bytes.NewBuffer(b)
	httpRsp, err := http.ReadResponse(bufio.NewReader(buf), nil)
	if err != nil {
		return nil, err
	}
	return NewProxyResponse(httpRsp), nil
}

// NewProxyWSMessage creates a new WSMessage given a type, message, and direction
func NewProxyWSMessage(mtype int, message []byte, direction int) (*ProxyWSMessage, error) {
	return &ProxyWSMessage{
		Type:      mtype,
		Message:   message,
		Direction: direction,
		Unmangled: nil,
		Timestamp: time.Unix(0, 0),
		DbId:      "",
	}, nil
}

// DestScheme returns the scheme used by the request (ws, wss, http, or https)
func (req *ProxyRequest) DestScheme() string {
	if req.IsWSUpgrade() {
		if req.DestUseTLS {
			return "wss"
		} else {
			return "ws"
		}
	} else {
		if req.DestUseTLS {
			return "https"
		} else {
			return "http"
		}
	}
}

// FullURL is the same as req.URL but guarantees it will include the scheme, host, and port if necessary
func (req *ProxyRequest) FullURL() *url.URL {
	var u url.URL
	u = *(req.URL) // Copy the original req.URL
	u.Host = req.Host
	u.Scheme = req.DestScheme()
	return &u
}

// Same as req.FullURL() but uses DestHost and DestPort for the host and port of the URL
func (req *ProxyRequest) DestURL() *url.URL {
	var u url.URL
	u = *(req.URL) // Copy the original req.URL
	u.Scheme = req.DestScheme()

	if req.DestUseTLS && req.DestPort == 443 ||
		!req.DestUseTLS && req.DestPort == 80 {
		u.Host = req.DestHost
	} else {
		u.Host = fmt.Sprintf("%s:%d", req.DestHost, req.DestPort)
	}
	return &u
}

// Submit submits the request over the given connection. Does not take into account DestHost, DestPort, or DestUseTLS
func (req *ProxyRequest) Submit(conn net.Conn) error {
	return req.submit(conn, false, nil)
}

// Submit submits the request in proxy form over the given connection for use with an upstream HTTP proxy. Does not take into account DestHost, DestPort, or DestUseTLS
func (req *ProxyRequest) SubmitProxy(conn net.Conn, creds *ProxyCredentials) error {
	return req.submit(conn, true, creds)
}

func (req *ProxyRequest) submit(conn net.Conn, forProxy bool, proxyCreds *ProxyCredentials) error {
	// Write the request to the connection
	req.StartDatetime = time.Now()
	if forProxy {
		if req.DestUseTLS {
			req.URL.Scheme = "https"
		} else {
			req.URL.Scheme = "http"
		}
		req.URL.Opaque = ""

		if err := req.RepeatableProxyWrite(conn, proxyCreds); err != nil {
			return err
		}
	} else {
		if err := req.RepeatableWrite(conn); err != nil {
			return err
		}
	}

	// Read a response from the server
	httpRsp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return fmt.Errorf("error reading response: %s", err.Error())
	}
	req.EndDatetime = time.Now()

	prsp := NewProxyResponse(httpRsp)
	req.ServerResponse = prsp
	return nil
}

// WSDial performs a websocket handshake over the given connection. Does not take into account DestHost, DestPort, or DestUseTLS
func (req *ProxyRequest) WSDial(conn net.Conn) (*WSSession, error) {
	if !req.IsWSUpgrade() {
		return nil, fmt.Errorf("could not start websocket session: request is not a websocket handshake request")
	}

	upgradeHeaders := make(http.Header)
	for k, v := range req.Header {
		for _, vv := range v {
			if !(k == "Upgrade" ||
				k == "Connection" ||
				k == "Sec-Websocket-Key" ||
				k == "Sec-Websocket-Version" ||
				k == "Sec-Websocket-Extensions" ||
				k == "Sec-Websocket-Protocol") {
				upgradeHeaders.Add(k, vv)
			}
		}
	}

	dialer := &websocket.Dialer{}
	dialer.NetDial = func(network, address string) (net.Conn, error) {
		return conn, nil
	}

	wsconn, rsp, err := dialer.Dial(req.DestURL().String(), upgradeHeaders)
	if err != nil {
		return nil, fmt.Errorf("could not dial WebSocket server: %s", err)
	}
	req.ServerResponse = NewProxyResponse(rsp)
	wsession := &WSSession{
		*wsconn,
		req,
	}
	return wsession, nil
}

// WSDial dials the target server and performs a websocket handshake over the new connection. Uses destination information from the request.
func WSDial(req *ProxyRequest) (*WSSession, error) {
	return wsDial(req, false, "", 0, nil, false)
}

// WSDialProxy dials the HTTP proxy server, performs a CONNECT handshake to connect to the remote server, then performs a websocket handshake over the new connection. Uses destination information from the request.
func WSDialProxy(req *ProxyRequest, proxyHost string, proxyPort int, creds *ProxyCredentials) (*WSSession, error) {
	return wsDial(req, true, proxyHost, proxyPort, creds, false)
}

// WSDialSOCKSProxy connects to the target host through the SOCKS proxy and performs a websocket handshake over the new connection. Uses destination information from the request.
func WSDialSOCKSProxy(req *ProxyRequest, proxyHost string, proxyPort int, creds *ProxyCredentials) (*WSSession, error) {
	return wsDial(req, true, proxyHost, proxyPort, creds, true)
}

func wsDial(req *ProxyRequest, useProxy bool, proxyHost string, proxyPort int, proxyCreds *ProxyCredentials, proxyIsSOCKS bool) (*WSSession, error) {
	var conn net.Conn
	var dialer NetDialer
	var err error

	if req.NetDial != nil {
		dialer = req.NetDial
	} else {
		dialer = net.Dial
	}

	if useProxy {
		if proxyIsSOCKS {
			var socksCreds *proxy.Auth
			if proxyCreds != nil {
				socksCreds = &proxy.Auth{
					User:     proxyCreds.Username,
					Password: proxyCreds.Password,
				}
			}
			socksDialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("%s:%d", proxyHost, proxyPort), socksCreds, proxy.Direct)
			if err != nil {
				return nil, fmt.Errorf("error creating SOCKS dialer: %s", err.Error())
			}
			conn, err = socksDialer.Dial("tcp", fmt.Sprintf("%s:%d", req.DestHost, req.DestPort))
			if err != nil {
				return nil, fmt.Errorf("error dialing host: %s", err.Error())
			}
			defer conn.Close()
		} else {
			conn, err = dialer("tcp", fmt.Sprintf("%s:%d", proxyHost, proxyPort))
			if err != nil {
				return nil, fmt.Errorf("error dialing proxy: %s", err.Error())
			}

			// always perform a CONNECT for websocket regardless of SSL
			if err := PerformConnect(conn, req.DestHost, req.DestPort); err != nil {
				return nil, err
			}
		}
	} else {
		conn, err = dialer("tcp", fmt.Sprintf("%s:%d", req.DestHost, req.DestPort))
		if err != nil {
			return nil, fmt.Errorf("error dialing host: %s", err.Error())
		}
	}

	if req.DestUseTLS {
		tls_conn := tls.Client(conn, &tls.Config{
			InsecureSkipVerify: true,
		})
		conn = tls_conn
	}

	return req.WSDial(conn)
}

// IsWSUpgrade returns whether the request is used to initiate a websocket handshake
func (req *ProxyRequest) IsWSUpgrade() bool {
	for k, v := range req.Header {
		for _, vv := range v {
			if strings.ToLower(k) == "upgrade" && strings.Contains(vv, "websocket") {
				return true
			}
		}
	}
	return false
}

// StripProxyHeaders removes headers associated with requests made to a proxy from the request
func (req *ProxyRequest) StripProxyHeaders() {
	if !req.IsWSUpgrade() {
		req.Header.Del("Connection")
	}
	req.Header.Del("Accept-Encoding")
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Proxy-Authenticate")
	req.Header.Del("Proxy-Authorization")
}

// Eq checks whether the request is the same as another request and has the same destination information
func (req *ProxyRequest) Eq(other *ProxyRequest) bool {
	if req.StatusLine() != other.StatusLine() ||
		!reflect.DeepEqual(req.Header, other.Header) ||
		bytes.Compare(req.BodyBytes(), other.BodyBytes()) != 0 ||
		req.DestHost != other.DestHost ||
		req.DestPort != other.DestPort ||
		req.DestUseTLS != other.DestUseTLS {
		return false
	}

	return true
}

// Clone returns a request with the same contents and destination information as the original
func (req *ProxyRequest) Clone() *ProxyRequest {
	buf := bytes.NewBuffer(make([]byte, 0))
	req.RepeatableWrite(buf)
	newReq, err := ProxyRequestFromBytes(buf.Bytes(), req.DestHost, req.DestPort, req.DestUseTLS)
	if err != nil {
		panic(err)
	}
	newReq.DestHost = req.DestHost
	newReq.DestPort = req.DestPort
	newReq.DestUseTLS = req.DestUseTLS
	newReq.Header = copyHeader(req.Header)
	return newReq
}

// DeepClone returns a request with the same contents, destination, and storage information information as the original along with a deep clone of the associated response, the unmangled version of the request, and any websocket messages
func (req *ProxyRequest) DeepClone() *ProxyRequest {
	// Returns a request with the same request, response, and associated websocket messages
	newReq := req.Clone()
	newReq.DbId = req.DbId

	if req.Unmangled != nil {
		newReq.Unmangled = req.Unmangled.DeepClone()
	}

	if req.ServerResponse != nil {
		newReq.ServerResponse = req.ServerResponse.DeepClone()
	}

	for _, wsm := range req.WSMessages {
		newReq.WSMessages = append(newReq.WSMessages, wsm.DeepClone())
	}

	return newReq
}

func (req *ProxyRequest) resetBodyReader() {
	// yes I know this method isn't the most efficient, I'll fix it if it causes problems later
	req.Body = ioutil.NopCloser(bytes.NewBuffer(req.BodyBytes()))
}

// RepeatableWrite is the same as http.Request.Write except that it can be safely called multiple times
func (req *ProxyRequest) RepeatableWrite(w io.Writer) error {
	defer req.resetBodyReader()
	return req.Write(w)
}

// RepeatableWrite is the same as http.Request.ProxyWrite except that it can be safely called multiple times
func (req *ProxyRequest) RepeatableProxyWrite(w io.Writer, proxyCreds *ProxyCredentials) error {
	defer req.resetBodyReader()
	if proxyCreds != nil {
		authHeader := proxyCreds.SerializeHeader()
		req.Header.Set("Proxy-Authorization", authHeader)
		defer func() { req.Header.Del("Proxy-Authorization") }()
	}
	return req.WriteProxy(w)
}

// BodyBytes returns the bytes of the request body
func (req *ProxyRequest) BodyBytes() []byte {
	return DuplicateBytes(req.bodyBytes)
}

// SetBodyBytes sets the bytes of the request body and updates the Content-Length header
func (req *ProxyRequest) SetBodyBytes(bs []byte) {
	req.bodyBytes = bs
	req.resetBodyReader()

	// Parse the form if we can, ignore errors
	req.ParseMultipartForm(1024 * 1024 * 1024) // 1GB for no good reason
	req.ParseForm()
	req.resetBodyReader()
	req.Header.Set("Content-Length", strconv.Itoa(len(bs)))
}

// FullMessage returns a slice of bytes containing the full HTTP message for the request
func (req *ProxyRequest) FullMessage() []byte {
	buf := bytes.NewBuffer(make([]byte, 0))
	req.RepeatableWrite(buf)
	return buf.Bytes()
}

// PostParameters attempts to parse POST parameters from the body of the request
func (req *ProxyRequest) PostParameters() (url.Values, error) {
	vals, err := url.ParseQuery(string(req.BodyBytes()))
	if err != nil {
		return nil, err
	}
	return vals, nil
}

// SetPostParameter sets the value of a post parameter in the message body. If the body does not contain well-formed data, it is deleted replaced with a well-formed body containing only the new parameter
func (req *ProxyRequest) SetPostParameter(key string, value string) {
	req.PostForm.Set(key, value)
	req.SetBodyBytes([]byte(req.PostForm.Encode()))
}

// AddPostParameter adds a post parameter to the body of the request even if a duplicate exists. If the body does not contain well-formed data, it is deleted replaced with a well-formed body containing only the new parameter
func (req *ProxyRequest) AddPostParameter(key string, value string) {
	req.PostForm.Add(key, value)
	req.SetBodyBytes([]byte(req.PostForm.Encode()))
}

// DeletePostParameter removes a parameter from the body of the request. If the body does not contain well-formed data, it is deleted replaced with a well-formed body containing only the new parameter
func (req *ProxyRequest) DeletePostParameter(key string) {
	req.PostForm.Del(key)
	req.SetBodyBytes([]byte(req.PostForm.Encode()))
}

// SetURLParameter sets the value of a URL parameter and updates ProxyRequest.URL
func (req *ProxyRequest) SetURLParameter(key string, value string) {
	q := req.URL.Query()
	q.Set(key, value)
	req.URL.RawQuery = q.Encode()
	req.ParseForm()
}

// URLParameters returns the values of the request's URL parameters
func (req *ProxyRequest) URLParameters() url.Values {
	vals := req.URL.Query()
	return vals
}

// AddURLParameter adds a URL parameter to the request ignoring duplicates
func (req *ProxyRequest) AddURLParameter(key string, value string) {
	q := req.URL.Query()
	q.Add(key, value)
	req.URL.RawQuery = q.Encode()
	req.ParseForm()
}

// DeleteURLParameter removes a URL parameter from the request
func (req *ProxyRequest) DeleteURLParameter(key string) {
	q := req.URL.Query()
	q.Del(key)
	req.URL.RawQuery = q.Encode()
	req.ParseForm()
}

// AddTag adds a tag to the request
func (req *ProxyRequest) AddTag(tag string) {
	req.tags.Add(tag)
}

// CheckTag returns whether the request has a given tag
func (req *ProxyRequest) CheckTag(tag string) bool {
	return req.tags.Contains(tag)
}

// RemoveTag removes a tag from the request
func (req *ProxyRequest) RemoveTag(tag string) {
	req.tags.Remove(tag)
}

// ClearTag removes all of the tags associated with the request
func (req *ProxyRequest) ClearTags() {
	req.tags.Clear()
}

// Tags returns a slice containing all of the tags associated with the request
func (req *ProxyRequest) Tags() []string {
	items := req.tags.ToSlice()
	retslice := make([]string, 0)
	for _, item := range items {
		str, ok := item.(string)
		if ok {
			retslice = append(retslice, str)
		}
	}
	return retslice
}

// HTTPPath returns the path of the associated with the request
func (req *ProxyRequest) HTTPPath() string {
	// The path used in the http request
	u := *req.URL
	u.Scheme = ""
	u.Host = ""
	u.Opaque = ""
	u.User = nil
	return u.String()
}

// StatusLine returns the status line associated with the request
func (req *ProxyRequest) StatusLine() string {
	return fmt.Sprintf("%s %s %s", req.Method, req.HTTPPath(), req.Proto)
}

// HeaderSection returns the header section of the request without the additional \r\n at the end
func (req *ProxyRequest) HeaderSection() string {
	retStr := req.StatusLine()
	retStr += "\r\n"
	for k, vs := range req.Header {
		for _, v := range vs {
			retStr += fmt.Sprintf("%s: %s\r\n", k, v)
		}
	}
	return retStr
}

func (rsp *ProxyResponse) resetBodyReader() {
	// yes I know this method isn't the most efficient, I'll fix it if it causes problems later
	rsp.Body = ioutil.NopCloser(bytes.NewBuffer(rsp.BodyBytes()))
}

// RepeatableWrite is the same as http.Response.Write except that it can safely be called multiple times
func (rsp *ProxyResponse) RepeatableWrite(w io.Writer) error {
	defer rsp.resetBodyReader()
	return rsp.Write(w)
}

// BodyBytes returns the bytes contained in the body of the response
func (rsp *ProxyResponse) BodyBytes() []byte {
	return DuplicateBytes(rsp.bodyBytes)
}

// SetBodyBytes sets the bytes in the body of the response and updates the Content-Length header
func (rsp *ProxyResponse) SetBodyBytes(bs []byte) {
	rsp.bodyBytes = bs
	rsp.resetBodyReader()
	rsp.Header.Set("Content-Length", strconv.Itoa(len(bs)))
}

// Clone returns a response with the same status line, headers, and body as the response
func (rsp *ProxyResponse) Clone() *ProxyResponse {
	buf := bytes.NewBuffer(make([]byte, 0))
	rsp.RepeatableWrite(buf)
	newRsp, err := ProxyResponseFromBytes(buf.Bytes())
	if err != nil {
		panic(err)
	}
	return newRsp
}

// DeepClone returns a response with the same status line, headers, and body as the original response along with a deep clone of its unmangled version if it exists
func (rsp *ProxyResponse) DeepClone() *ProxyResponse {
	newRsp := rsp.Clone()
	newRsp.DbId = rsp.DbId
	if rsp.Unmangled != nil {
		newRsp.Unmangled = rsp.Unmangled.DeepClone()
	}
	return newRsp
}

// Eq returns whether the response has the same contents as another response
func (rsp *ProxyResponse) Eq(other *ProxyResponse) bool {
	if rsp.StatusLine() != other.StatusLine() ||
		!reflect.DeepEqual(rsp.Header, other.Header) ||
		bytes.Compare(rsp.BodyBytes(), other.BodyBytes()) != 0 {
		return false
	}
	return true
}

// FullMessage returns the full HTTP message of the response
func (rsp *ProxyResponse) FullMessage() []byte {
	buf := bytes.NewBuffer(make([]byte, 0))
	rsp.RepeatableWrite(buf)
	return buf.Bytes()
}

// Returns the status text to be used in the http request
func (rsp *ProxyResponse) HTTPStatus() string {
	// The status text to be used in the http request. Relies on being the same implementation as http.Response
	text := rsp.Status
	if text == "" {
		text = http.StatusText(rsp.StatusCode)
		if text == "" {
			text = "status code " + strconv.Itoa(rsp.StatusCode)
		}
	} else {
		// Just to reduce stutter, if user set rsp.Status to "200 OK" and StatusCode to 200.
		// Not important.
		text = strings.TrimPrefix(text, strconv.Itoa(rsp.StatusCode)+" ")
	}
	return text
}

// StatusLine returns the status line of the response
func (rsp *ProxyResponse) StatusLine() string {
	// Status line, stolen from net/http/response.go
	return fmt.Sprintf("HTTP/%d.%d %03d %s", rsp.ProtoMajor, rsp.ProtoMinor, rsp.StatusCode, rsp.HTTPStatus())
}

// HeaderSection returns the header section of the response (without the extra trailing \r\n)
func (rsp *ProxyResponse) HeaderSection() string {
	retStr := rsp.StatusLine()
	retStr += "\r\n"
	for k, vs := range rsp.Header {
		for _, v := range vs {
			retStr += fmt.Sprintf("%s: %s\r\n", k, v)
		}
	}
	return retStr
}

func (msg *ProxyWSMessage) String() string {
	var dirStr string
	if msg.Direction == ToClient {
		dirStr = "ToClient"
	} else {
		dirStr = "ToServer"
	}
	return fmt.Sprintf("{WS Message  msg=\"%s\", type=%d, dir=%s}", string(msg.Message), msg.Type, dirStr)
}

// Clone returns a copy of the original message. It will have the same type, message, direction, timestamp, and request
func (msg *ProxyWSMessage) Clone() *ProxyWSMessage {
	var retMsg ProxyWSMessage
	retMsg.Type = msg.Type
	retMsg.Message = msg.Message
	retMsg.Direction = msg.Direction
	retMsg.Timestamp = msg.Timestamp
	retMsg.Request = msg.Request
	return &retMsg
}

// DeepClone returns a clone of the original message and a deep clone of the unmangled version if it exists
func (msg *ProxyWSMessage) DeepClone() *ProxyWSMessage {
	retMsg := msg.Clone()
	retMsg.DbId = msg.DbId
	if msg.Unmangled != nil {
		retMsg.Unmangled = msg.Unmangled.DeepClone()
	}
	return retMsg
}

// Eq checks if the message has the same type, direction, and message as another message
func (msg *ProxyWSMessage) Eq(other *ProxyWSMessage) bool {
	if msg.Type != other.Type ||
		msg.Direction != other.Direction ||
		bytes.Compare(msg.Message, other.Message) != 0 {
		return false
	}
	return true
}

func copyHeader(hd http.Header) http.Header {
	var ret http.Header = make(http.Header)
	for k, vs := range hd {
		for _, v := range vs {
			ret.Add(k, v)
		}
	}
	return ret
}

func submitRequest(req *ProxyRequest, useProxy bool, proxyHost string,
	proxyPort int, proxyCreds *ProxyCredentials, proxyIsSOCKS bool) error {
	var dialer NetDialer = req.NetDial
	if dialer == nil {
		dialer = net.Dial
	}

	var conn net.Conn
	var err error
	var proxyFormat bool = false
	if useProxy {
		if proxyIsSOCKS {
			var socksCreds *proxy.Auth
			if proxyCreds != nil {
				socksCreds = &proxy.Auth{
					User:     proxyCreds.Username,
					Password: proxyCreds.Password,
				}
			}
			socksDialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("%s:%d", proxyHost, proxyPort), socksCreds, proxy.Direct)
			if err != nil {
				return fmt.Errorf("error creating SOCKS dialer: %s", err.Error())
			}
			conn, err = socksDialer.Dial("tcp", fmt.Sprintf("%s:%d", req.DestHost, req.DestPort))
			if err != nil {
				return fmt.Errorf("error dialing host: %s", err.Error())
			}
			defer conn.Close()
		} else {
			conn, err = dialer("tcp", fmt.Sprintf("%s:%d", proxyHost, proxyPort))
			if err != nil {
				return fmt.Errorf("error dialing proxy: %s", err.Error())
			}
			defer conn.Close()
			if req.DestUseTLS {
				if err := PerformConnect(conn, req.DestHost, req.DestPort); err != nil {
					return err
				}
				proxyFormat = false
			} else {
				proxyFormat = true
			}
		}
	} else {
		conn, err = dialer("tcp", fmt.Sprintf("%s:%d", req.DestHost, req.DestPort))
		if err != nil {
			return fmt.Errorf("error dialing host: %s", err.Error())
		}
		defer conn.Close()
	}

	if req.DestUseTLS {
		tls_conn := tls.Client(conn, &tls.Config{
			InsecureSkipVerify: true,
		})
		conn = tls_conn
	}

	if proxyFormat {
		return req.SubmitProxy(conn, proxyCreds)
	} else {
		return req.Submit(conn)
	}
}

// SubmitRequest opens a connection to the request's DestHost:DestPort, using TLS if DestUseTLS is set, submits the request, and sets req.Response with the response when a response is received
func SubmitRequest(req *ProxyRequest) error {
	return submitRequest(req, false, "", 0, nil, false)
}

// SubmitRequestProxy connects to the given HTTP proxy, performs neccessary handshakes, and submits the request to its destination. req.Response will be set once a response is received
func SubmitRequestProxy(req *ProxyRequest, proxyHost string, proxyPort int, creds *ProxyCredentials) error {
	return submitRequest(req, true, proxyHost, proxyPort, creds, false)
}

// SubmitRequestProxy connects to the given SOCKS proxy, performs neccessary handshakes, and submits the request to its destination. req.Response will be set once a response is received
func SubmitRequestSOCKSProxy(req *ProxyRequest, proxyHost string, proxyPort int, creds *ProxyCredentials) error {
	return submitRequest(req, true, proxyHost, proxyPort, creds, true)
}
