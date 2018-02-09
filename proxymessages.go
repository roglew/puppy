package puppy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Creates a MessageListener that implements the default puppy handlers. See the message docs for more info
func NewProxyMessageListener(logger *log.Logger, iproxy *InterceptingProxy) *MessageListener {
	l := NewMessageListener(logger, iproxy)

	l.AddHandler("ping", pingHandler)
	l.AddHandler("submit", submitHandler)
	l.AddHandler("savenew", saveNewHandler)
	l.AddHandler("storagequery", storageQueryHandler)
	l.AddHandler("validatequery", validateQueryHandler)
	l.AddHandler("checkrequest", checkRequestHandler)
	l.AddHandler("setscope", setScopeHandler)
	l.AddHandler("viewscope", viewScopeHandler)
	l.AddHandler("addtag", addTagHandler)
	l.AddHandler("removetag", removeTagHandler)
	l.AddHandler("cleartag", clearTagHandler)
	l.AddHandler("intercept", interceptHandler)
	l.AddHandler("allsavedqueries", allSavedQueriesHandler)
	l.AddHandler("savequery", saveQueryHandler)
	l.AddHandler("loadquery", loadQueryHandler)
	l.AddHandler("deletequery", deleteQueryHandler)
	l.AddHandler("addlistener", addListenerHandler)
	l.AddHandler("removelistener", removeListenerHandler)
	l.AddHandler("getlisteners", getListenersHandler)
	l.AddHandler("loadcerts", loadCertificatesHandler)
	l.AddHandler("setcerts", setCertificatesHandler)
	l.AddHandler("clearcerts", clearCertificatesHandler)
	l.AddHandler("gencerts", generateCertificatesHandler)
	l.AddHandler("genpemcerts", generatePEMCertificatesHandler)
	l.AddHandler("addsqlitestorage", addSQLiteStorageHandler)
	l.AddHandler("addinmemorystorage", addInMemoryStorageHandler)
	l.AddHandler("closestorage", closeStorageHandler)
	l.AddHandler("setproxystorage", setProxyStorageHandler)
	l.AddHandler("liststorage", listProxyStorageHandler)
	l.AddHandler("setproxy", setProxyHandler)
	l.AddHandler("watchstorage", watchStorageHandler)

	return l
}

// JSON data representing a ProxyRequest
type RequestJSON struct {
	DestHost   string
	DestPort   int
	UseTLS     bool
	Method     string
	Path       string
	ProtoMajor int
	ProtoMinor int
	Headers    map[string][]string
	Body       string
	Tags       []string

	StartTime int64 `json:"StartTime,omitempty"`
	EndTime   int64 `json:"EndTime,omitempty"`

	Unmangled  *RequestJSON     `json:"Unmangled,omitempty"`
	Response   *ResponseJSON    `json:"Response,omitempty"`
	WSMessages []*WSMessageJSON `json:"WSMessages,omitempty"`
	DbId       string           `json:"DbId,omitempty"`
}

// JSON data representing a ProxyResponse
type ResponseJSON struct {
	ProtoMajor int
	ProtoMinor int
	StatusCode int
	Reason     string

	Headers map[string][]string
	Body    string

	Unmangled *ResponseJSON `json:"Unmangled,omitempty"`
	DbId      string
}

// JSON data representing a ProxyWSMessage
type WSMessageJSON struct {
	Message   string
	IsBinary  bool
	ToServer  bool
	Timestamp int64

	Unmangled *WSMessageJSON `json:"Unmangled,omitempty"`
	DbId      string
}

// Check that the RequestJSON contains valid data
func (reqd *RequestJSON) Validate() error {
	if reqd.DestHost == "" {
		return errors.New("request is missing target host")
	}

	if reqd.DestPort == 0 {
		return errors.New("request is missing target port")
	}

	return nil
}

// Convert RequestJSON into a ProxyRequest
func (reqd *RequestJSON) Parse() (*ProxyRequest, error) {
	if err := reqd.Validate(); err != nil {
		return nil, err
	}
	dataBuf := new(bytes.Buffer)
	statusLine := fmt.Sprintf("%s %s HTTP/%d.%d", reqd.Method, reqd.Path, reqd.ProtoMajor, reqd.ProtoMinor)
	dataBuf.Write([]byte(statusLine))
	dataBuf.Write([]byte("\r\n"))

	for k, vs := range reqd.Headers {
		for _, v := range vs {
			if strings.ToLower(k) != "content-length" {
				dataBuf.Write([]byte(k))
				dataBuf.Write([]byte(": "))
				dataBuf.Write([]byte(v))
				dataBuf.Write([]byte("\r\n"))
			}
		}
	}

	body, err := base64.StdEncoding.DecodeString(reqd.Body)
	if err != nil {
		return nil, err
	}

	dataBuf.Write([]byte("Content-Length"))
	dataBuf.Write([]byte(": "))
	dataBuf.Write([]byte(strconv.Itoa(len(body))))
	dataBuf.Write([]byte("\r\n\r\n"))

	dataBuf.Write(body)

	req, err := ProxyRequestFromBytes(dataBuf.Bytes(), reqd.DestHost, reqd.DestPort, reqd.UseTLS)
	if err != nil {
		return nil, err
	}

	if req.Host == "" {
		req.Host = reqd.DestHost
	}

	if reqd.StartTime > 0 {
		req.StartDatetime = time.Unix(0, reqd.StartTime)
	}

	if reqd.EndTime > 0 {
		req.EndDatetime = time.Unix(0, reqd.EndTime)
	}

	for _, tag := range reqd.Tags {
		req.AddTag(tag)
	}

	if reqd.Response != nil {
		rsp, err := reqd.Response.Parse()
		if err != nil {
			return nil, err
		}
		req.ServerResponse = rsp
	}

	for _, wsmd := range reqd.WSMessages {
		wsm, err := wsmd.Parse()
		if err != nil {
			return nil, err
		}
		req.WSMessages = append(req.WSMessages, wsm)
	}
	sort.Sort(WSSort(req.WSMessages))

	return req, nil
}

// Convert a ProxyRequest into JSON data. If headersOnly is true, the JSON data will only contain the headers and metadata of the message
func NewRequestJSON(req *ProxyRequest, headersOnly bool) *RequestJSON {

	newHeaders := make(map[string][]string)
	for k, vs := range req.Header {
		for _, v := range vs {
			l, ok := newHeaders[k]
			if ok {
				newHeaders[k] = append(l, v)
			} else {
				newHeaders[k] = make([]string, 1)
				newHeaders[k][0] = v
			}
		}
	}

	var unmangled *RequestJSON = nil
	if req.Unmangled != nil {
		unmangled = NewRequestJSON(req.Unmangled, headersOnly)
	}

	var rsp *ResponseJSON = nil
	if req.ServerResponse != nil {
		rsp = NewResponseJSON(req.ServerResponse, headersOnly)
	}

	wsms := make([]*WSMessageJSON, 0)
	for _, wsm := range req.WSMessages {
		wsms = append(wsms, NewWSMessageJSON(wsm))
	}

	ret := &RequestJSON{
		DestHost:   req.DestHost,
		DestPort:   req.DestPort,
		UseTLS:     req.DestUseTLS,
		Method:     req.Method,
		Path:       req.HTTPPath(),
		ProtoMajor: req.ProtoMajor,
		ProtoMinor: req.ProtoMinor,
		Headers:    newHeaders,
		Tags:       req.Tags(),

		StartTime: req.StartDatetime.UnixNano(),
		EndTime:   req.EndDatetime.UnixNano(),

		Unmangled:  unmangled,
		Response:   rsp,
		WSMessages: wsms,
		DbId:       req.DbId,
	}
	if !headersOnly {
		ret.Body = base64.StdEncoding.EncodeToString(req.BodyBytes())
	}

	return ret
}

// Ensure that response JSON data is valid
func (rspd *ResponseJSON) Validate() error {
	return nil
}

// Convert response JSON data into a ProxyResponse
func (rspd *ResponseJSON) Parse() (*ProxyResponse, error) {
	if err := rspd.Validate(); err != nil {
		return nil, err
	}
	dataBuf := new(bytes.Buffer)
	statusLine := fmt.Sprintf("HTTP/%d.%d %03d %s", rspd.ProtoMajor, rspd.ProtoMinor, rspd.StatusCode, rspd.Reason)
	dataBuf.Write([]byte(statusLine))
	dataBuf.Write([]byte("\r\n"))

	for k, vs := range rspd.Headers {
		for _, v := range vs {
			if strings.ToLower(k) != "content-length" {
				dataBuf.Write([]byte(k))
				dataBuf.Write([]byte(": "))
				dataBuf.Write([]byte(v))
				dataBuf.Write([]byte("\r\n"))
			}
		}
	}

	body, err := base64.StdEncoding.DecodeString(rspd.Body)
	if err != nil {
		return nil, err
	}

	dataBuf.Write([]byte("Content-Length"))
	dataBuf.Write([]byte(": "))
	dataBuf.Write([]byte(strconv.Itoa(len(rspd.Body))))
	dataBuf.Write([]byte("\r\n\r\n"))

	dataBuf.Write(body)

	rsp, err := ProxyResponseFromBytes(dataBuf.Bytes())
	if err != nil {
		return nil, err
	}

	if rspd.Unmangled != nil {
		ursp, err := rspd.Unmangled.Parse()
		if err != nil {
			return nil, err
		}
		rsp.Unmangled = ursp
	}

	return rsp, nil
}

// Serialize a ProxyResponse into JSON data. If headersOnly is true, the JSON data will only contain the headers and metadata of the message
func NewResponseJSON(rsp *ProxyResponse, headersOnly bool) *ResponseJSON {
	newHeaders := make(map[string][]string)
	for k, vs := range rsp.Header {
		for _, v := range vs {
			l, ok := newHeaders[k]
			if ok {
				newHeaders[k] = append(l, v)
			} else {
				newHeaders[k] = make([]string, 1)
				newHeaders[k][0] = v
			}
		}
	}

	var unmangled *ResponseJSON = nil
	if rsp.Unmangled != nil {
		unmangled = NewResponseJSON(rsp.Unmangled, headersOnly)
	}

	ret := &ResponseJSON{
		ProtoMajor: rsp.ProtoMajor,
		ProtoMinor: rsp.ProtoMinor,
		StatusCode: rsp.StatusCode,
		Reason:     rsp.HTTPStatus(),
		Headers:    newHeaders,
		DbId:       rsp.DbId,
		Unmangled:  unmangled,
	}

	if !headersOnly {
		ret.Body = base64.StdEncoding.EncodeToString(rsp.BodyBytes())
	}

	return ret
}

// Parse websocket message JSON data into a ProxyWSMEssage
func (wsmd *WSMessageJSON) Parse() (*ProxyWSMessage, error) {
	var Direction int
	if wsmd.ToServer {
		Direction = ToServer
	} else {
		Direction = ToClient
	}

	var mtype int
	if wsmd.IsBinary {
		mtype = websocket.BinaryMessage
	} else {
		mtype = websocket.TextMessage
	}

	message, err := base64.StdEncoding.DecodeString(wsmd.Message)
	if err != nil {
		return nil, err
	}

	var unmangled *ProxyWSMessage
	if wsmd.Unmangled != nil {
		unmangled, err = wsmd.Unmangled.Parse()
		if err != nil {
			return nil, err
		}
	}

	retData := &ProxyWSMessage{
		Message:   message,
		Type:      mtype,
		Direction: Direction,
		Timestamp: time.Unix(0, wsmd.Timestamp),
		Unmangled: unmangled,
		DbId:      wsmd.DbId,
	}

	return retData, nil
}

// Serialize a websocket message into JSON data
func NewWSMessageJSON(wsm *ProxyWSMessage) *WSMessageJSON {
	isBinary := false
	if wsm.Type == websocket.BinaryMessage {
		isBinary = true
	}

	toServer := false
	if wsm.Direction == ToServer {
		toServer = true
	}

	var unmangled *WSMessageJSON
	if wsm.Unmangled != nil {
		unmangled = NewWSMessageJSON(wsm.Unmangled)
	}

	ret := &WSMessageJSON{
		Message:   base64.StdEncoding.EncodeToString(wsm.Message),
		IsBinary:  isBinary,
		ToServer:  toServer,
		Timestamp: wsm.Timestamp.UnixNano(),
		Unmangled: unmangled,
		DbId:      wsm.DbId,
	}

	return ret
}

// Clears metadata (start/end time, DbId) and dependent message data (response, websocket messages, and unmangled versions) from the RequestJSON
func CleanReqJSON(req *RequestJSON) {
	req.StartTime = 0
	req.EndTime = 0
	req.Unmangled = nil
	req.Response = nil
	req.WSMessages = nil
	req.DbId = ""
}

// Clears metadata (DbId) and dependent message data (unmangled version) from the ResponseJSON
func CleanRspJSON(rsp *ResponseJSON) {
	rsp.Unmangled = nil
	rsp.DbId = ""
}

// Clears metadata (timestamp, DbId) and dependent message data (unmangled version) from the WSMessageJSON
func CleanWSJSON(wsm *WSMessageJSON) {
	wsm.Timestamp = 0
	wsm.Unmangled = nil
	wsm.DbId = ""
}

type successResult struct {
	Success bool
}

/*
Ping
*/
type pingMessage struct{}

type pingResponse struct {
	Success bool
	Ping    string
}

func pingHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	rsp := pingResponse{Success: true, Ping: "Pong"}
	MessageResponse(c, rsp)
}

/*
Submit
*/
type submitMessage struct {
	Request *RequestJSON
	Storage int
}

type submitResponse struct {
	Success          bool
	SubmittedRequest *RequestJSON
}

func submitHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := submitMessage{}

	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, fmt.Sprintf("error parsing submit message: %s", err.Error()))
		return
	}

	req, err := mreq.Request.Parse()
	if err != nil {
		ErrorResponse(c, fmt.Sprintf("error parsing http request: %s", err.Error()))
		return
	}

	if mreq.Storage > 0 {
		storage, _ := iproxy.GetMessageStorage(mreq.Storage)
		if storage == nil {
			ErrorResponse(c, fmt.Sprintf("storage with id %d does not exist", mreq.Storage))
			return
		}
		SaveNewRequest(storage, req)
	}
	logger.Println("Submitting request to", req.FullURL(), "...")
	if err := iproxy.SubmitRequest(req); err != nil {
		ErrorResponse(c, err.Error())
		return
	}
	if mreq.Storage > 0 {
		storage, _ := iproxy.GetMessageStorage(mreq.Storage)
		if storage == nil {
			ErrorResponse(c, fmt.Sprintf("storage with id %d does not exist", mreq.Storage))
			return
		}
		UpdateRequest(storage, req)
	}

	result := NewRequestJSON(req, false)
	response := submitResponse{Success: true, SubmittedRequest: result}
	MessageResponse(c, response)
}

/*
SaveNew
*/
type saveNewMessage struct {
	Request *RequestJSON
	Storage int
}

type saveNewResponse struct {
	Success bool
	DbId    string
}

func saveNewHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := submitMessage{}

	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, fmt.Sprintf("error parsing submit message: %s", err.Error()))
		return
	}

	if mreq.Storage == 0 {
		ErrorResponse(c, "storage is required")
		return
	}

	req, err := mreq.Request.Parse()
	if err != nil {
		ErrorResponse(c, fmt.Sprintf("error parsing http request: %s", err.Error()))
		return
	}

	storage, _ := iproxy.GetMessageStorage(mreq.Storage)
	if storage == nil {
		ErrorResponse(c, fmt.Sprintf("storage with id %d does not exist", mreq.Storage))
		return
	}

	err = SaveNewRequest(storage, req)
	if err != nil {
		ErrorResponse(c, fmt.Sprintf("error saving http request: %s", err.Error()))
		return
	}

	response := &saveNewResponse{
		Success: true,
		DbId:    req.DbId,
	}
	MessageResponse(c, response)
}

/*
QueryRequests
*/
type storageQueryMessage struct {
	Query       StrMessageQuery
	HeadersOnly bool
	MaxResults  int64
	Storage     int
}

type storageQueryResult struct {
	Success bool
	Results []*RequestJSON
}

func storageQueryHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := storageQueryMessage{
		Query:       nil,
		HeadersOnly: false,
		MaxResults:  0,
	}

	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing query message")
		return
	}

	if mreq.Query == nil {
		ErrorResponse(c, "query is required")
		return
	}

	if mreq.Storage == 0 {
		ErrorResponse(c, "storage is required")
		return
	}

	storage, _ := iproxy.GetMessageStorage(mreq.Storage)
	if storage == nil {
		ErrorResponse(c, fmt.Sprintf("storage with id %d does not exist", mreq.Storage))
		return
	}

	var searchResults []*ProxyRequest
	if len(mreq.Query) == 1 && len(mreq.Query[0]) == 1 {
		args, err := CheckArgsStrToGo(mreq.Query[0][0])
		if err != nil {
			ErrorResponse(c, err.Error())
			return
		}
		logger.Println("Search query is one phrase, sending directly to storage...")
		searchResults, err = storage.Search(mreq.MaxResults, args...)
		if err != nil {
			ErrorResponse(c, err.Error())
			return
		}
	} else {
		logger.Println("Search query is multple sets of arguments, creating checker and checking naively...")
		goQuery, err := StrQueryToMsgQuery(mreq.Query)
		if err != nil {
			ErrorResponse(c, err.Error())
			return
		}

		checker, err := CheckerFromMessageQuery(goQuery)
		if err != nil {
			ErrorResponse(c, err.Error())
			return
		}

		searchResults, err = storage.CheckRequests(mreq.MaxResults, checker)
		if err != nil {
			ErrorResponse(c, err.Error())
			return
		}
	}

	var result storageQueryResult
	reqResults := make([]*RequestJSON, len(searchResults))
	for i, req := range searchResults {
		reqResults[i] = NewRequestJSON(req, mreq.HeadersOnly)
	}
	result.Success = true
	result.Results = reqResults
	MessageResponse(c, &result)
}

/*
ValidateQuery
*/

type validateQueryMessage struct {
	Query StrMessageQuery
}

func validateQueryHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := validateQueryMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing query message")
		return
	}

	goQuery, err := StrQueryToMsgQuery(mreq.Query)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	_, err = CheckerFromMessageQuery(goQuery)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}
	MessageResponse(c, &successResult{Success: true})
}

/*
CheckRequest
*/

type checkRequestMessage struct {
	Query       StrMessageQuery
	Request     *RequestJSON
	DbId        string
	StorageId   int
}

type checkRequestResponse struct {
	Result  bool
	Success bool
}

func checkRequestHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := checkRequestMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing query message")
		return
	}

	var req *ProxyRequest
	var err error

	if mreq.DbId != "" {
		storage, _ := iproxy.GetMessageStorage(mreq.StorageId)
		if storage == nil {
			ErrorResponse(c, fmt.Sprintf("storage with id %d does not exist", mreq.StorageId))
			return
		}
		searchResults, err := storage.Search(1, FieldId, StrIs, mreq.DbId)
		if err != nil {
			ErrorResponse(c, err.Error())
			return
		}
	    if len(searchResults) == 0 {
			ErrorResponse(c, fmt.Sprintf("message with id=%s does not exist", mreq.DbId))
			return
		}
		req = searchResults[0]
	} else {
		req, err = mreq.Request.Parse()
		if err != nil {
			ErrorResponse(c, fmt.Sprintf("error parsing http request: %s", err.Error()))
			return
		}
	}

	goQuery, err := StrQueryToMsgQuery(mreq.Query)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	checker, err := CheckerFromMessageQuery(goQuery)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	result := &checkRequestResponse{
		Success: true,
		Result:  checker(req),
	}

	MessageResponse(c, result)
}

/*
SetScope
*/

type setScopeMessage struct {
	Query StrMessageQuery
}

func setScopeHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := setScopeMessage{}

	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing query message")
		return
	}

	goQuery, err := StrQueryToMsgQuery(mreq.Query)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	err = iproxy.SetScopeQuery(goQuery)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	MessageResponse(c, &successResult{Success: true})
}

/*
ViewScope
*/

type viewScopeMessage struct {
}

type viewScopeResult struct {
	Success  bool
	IsCustom bool
	Query    StrMessageQuery
}

func viewScopeHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	scopeQuery := iproxy.GetScopeQuery()
	scopeChecker := iproxy.GetScopeChecker()

	if scopeQuery == nil && scopeChecker != nil {
		MessageResponse(c, &viewScopeResult{
			Success:  true,
			IsCustom: true,
		})
		return
	}

	var err error
	strQuery, err := MsgQueryToStrQuery(scopeQuery)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	MessageResponse(c, &viewScopeResult{
		Success:  true,
		IsCustom: false,
		Query:    strQuery,
	})
}

/*
Tag messages
*/

type addTagMessage struct {
	ReqId   string
	Tag     string
	Storage int
}

func addTagHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := addTagMessage{}

	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, fmt.Sprintf("error parsing message: %s", err.Error()))
		return
	}

	if mreq.Storage == 0 {
		ErrorResponse(c, "storage is required")
		return
	}

	storage, _ := iproxy.GetMessageStorage(mreq.Storage)
	if storage == nil {
		ErrorResponse(c, fmt.Sprintf("storage with id %d does not exist", mreq.Storage))
		return
	}

	if mreq.ReqId == "" || mreq.Tag == "" {
		ErrorResponse(c, "both request id and tag are required")
		return
	}

	req, err := storage.LoadRequest(mreq.ReqId)
	if err != nil {
		ErrorResponse(c, fmt.Sprintf("error loading request: %s", err.Error()))
		return
	}

	req.AddTag(mreq.Tag)
	err = UpdateRequest(storage, req)
	if err != nil {
		ErrorResponse(c, fmt.Sprintf("error saving request: %s", err.Error()))
		return
	}

	MessageResponse(c, &successResult{Success: true})
}

type removeTagMessage struct {
	ReqId   string
	Tag     string
	Storage int
}

func removeTagHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := removeTagMessage{}

	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, fmt.Sprintf("error parsing message: %s", err.Error()))
		return
	}

	if mreq.Storage == 0 {
		ErrorResponse(c, "storage is required")
		return
	}

	storage, _ := iproxy.GetMessageStorage(mreq.Storage)
	if storage == nil {
		ErrorResponse(c, fmt.Sprintf("storage with id %d does not exist", mreq.Storage))
		return
	}

	if mreq.ReqId == "" || mreq.Tag == "" {
		ErrorResponse(c, "both request id and tag are required")
		return
	}

	req, err := storage.LoadRequest(mreq.ReqId)
	if err != nil {
		ErrorResponse(c, fmt.Sprintf("error loading request: %s", err.Error()))
		return
	}

	req.RemoveTag(mreq.Tag)
	err = UpdateRequest(storage, req)
	if err != nil {
		ErrorResponse(c, fmt.Sprintf("error saving request: %s", err.Error()))
		return
	}

	MessageResponse(c, &successResult{Success: true})
}

type clearTagsMessage struct {
	ReqId   string
	Storage int
}

func clearTagHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := clearTagsMessage{}

	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, fmt.Sprintf("error parsing message: %s", err.Error()))
		return
	}

	if mreq.Storage == 0 {
		ErrorResponse(c, "storage is required")
		return
	}

	storage, _ := iproxy.GetMessageStorage(mreq.Storage)
	if storage == nil {
		ErrorResponse(c, fmt.Sprintf("storage with id %d does not exist", mreq.Storage))
		return
	}

	req, err := storage.LoadRequest(mreq.ReqId)
	if err != nil {
		ErrorResponse(c, fmt.Sprintf("error loading request: %s", err.Error()))
		return
	}

	req.ClearTags()
	err = UpdateRequest(storage, req)
	if err != nil {
		ErrorResponse(c, fmt.Sprintf("error saving request: %s", err.Error()))
		return
	}

	MessageResponse(c, &successResult{Success: true})
}

/*
Intercept
*/

type interceptMessage struct {
	InterceptRequests  bool
	InterceptResponses bool
	InterceptWS        bool

	UseQuery bool
	Query    MessageQuery
}

type intHandshakeResult struct {
	Success bool
}

// id func
var getNextIntId = IdCounter()

type intRequest struct {
	// A request to have a message mangled
	Id      int
	Type    string
	Success bool
	Result  chan *intResponse `json:"-"`

	Request   *RequestJSON   `json:"Request,omitempty"`
	Response  *ResponseJSON  `json:"Response,omitempty"`
	WSMessage *WSMessageJSON `json:"WSMessage,omitempty"`
}

type intResponse struct {
	// response from the client with a mangled http request
	Id      int
	Dropped bool

	Request   *RequestJSON   `json:"Request,omitempty"`
	Response  *ResponseJSON  `json:"Response,omitempty"`
	WSMessage *WSMessageJSON `json:"WSMessage,omitempty"`
}

type intErrorMessage struct {
	// a message template for sending an error to client if there is an error
	// with the mangled message they sent
	Id      int
	Success bool
	Reason  string
}

func intErrorResponse(id int, conn net.Conn, reason string) {
	m := &intErrorMessage{
		Id:      id,
		Success: false,
		Reason:  reason,
	}
	MessageResponse(conn, m)
}

func interceptHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := interceptMessage{
		InterceptRequests:  false,
		InterceptResponses: false,
		InterceptWS:        false,
		UseQuery:           false,
	}

	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, fmt.Sprintf("error parsing message: %s", err.Error()))
		return
	}

	if !mreq.InterceptRequests && !mreq.InterceptResponses && !mreq.InterceptWS {
		ErrorResponse(c, "must intercept at least one message type")
		return
	}

	pendingRequests := make(map[int]*intRequest)
	var pendingRequestsMtx sync.Mutex

	// helper functions for managing pending requests
	getPendingRequest := func(id int) (*intRequest, error) {
		pendingRequestsMtx.Lock()
		defer pendingRequestsMtx.Unlock()
		ret, ok := pendingRequests[id]
		if !ok {
			return nil, fmt.Errorf("pending request with id %d does not exist", id)
		}
		return ret, nil
	}

	addPendingRequest := func(pendingReq *intRequest) {
		pendingRequestsMtx.Lock()
		defer pendingRequestsMtx.Unlock()
		pendingRequests[pendingReq.Id] = pendingReq
	}

	removePendingRequest := func(pendingReq *intRequest) {
		pendingRequestsMtx.Lock()
		defer pendingRequestsMtx.Unlock()
		delete(pendingRequests, pendingReq.Id)
	}

	// parse the checker
	var checker RequestChecker = nil
	if mreq.UseQuery {
		var err error
		checker, err = CheckerFromMessageQuery(mreq.Query)
		if err != nil {
			ErrorResponse(c, fmt.Sprintf("error with message query: %s", err.Error()))
			return
		}
	}

	MessageResponse(c, &intHandshakeResult{Success: true})

	// hook the request interceptor
	var reqSub *ReqIntSub = nil
	if mreq.InterceptRequests {
		logger.Println("Adding request interceptor...")
		// Create a function that sends requests to client and wait for the client to respond
		reqIntFunc := func(req *ProxyRequest) (*ProxyRequest, error) {
			// if it doesn't pass the query, return the request unmodified
			if checker != nil && !checker(req) {
				return req, nil
			}

			// JSON serialize the request
			reqData := NewRequestJSON(req, false)
			CleanReqJSON(reqData)

			// convert request data to an intRequest
			intReq := &intRequest{
				Id:      getNextIntId(),
				Type:    "httprequest",
				Result:  make(chan *intResponse),
				Success: true,

				Request: reqData,
			}

			// add bookkeeping for results, defer cleanup
			addPendingRequest(intReq)
			defer removePendingRequest(intReq)

			// submit the request
			MessageResponse(c, intReq)

			// wait for result
			intRsp, ok := <-intReq.Result
			if !ok {
				// if it closed, just pass the request along
				return req, nil
			}

			if intRsp.Dropped {
				// if it's dropped, return nil
				return nil, nil
			}

			newReq := intRsp.Request
			CleanReqJSON(newReq)

			ret, err := newReq.Parse()
			if err != nil {
				return nil, err
			}

			return ret, nil
		}
		reqSub = iproxy.AddReqInterceptor(reqIntFunc)
	}

	var rspSub *RspIntSub = nil
	if mreq.InterceptResponses {
		logger.Println("Adding response interceptor...")
		rspIntFunc := func(req *ProxyRequest, rsp *ProxyResponse) (*ProxyResponse, error) {
			logger.Println("Intercepted response!")
			// if it doesn't pass the query, return the request unmodified
			if checker != nil && !checker(req) {
				return rsp, nil
			}

			reqData := NewRequestJSON(req, false)
			CleanReqJSON(reqData)

			rspData := NewResponseJSON(rsp, false)
			CleanRspJSON(rspData)

			intReq := &intRequest{
				Id:      getNextIntId(),
				Type:    "httpresponse",
				Result:  make(chan *intResponse),
				Success: true,

				Request:  reqData,
				Response: rspData,
			}

			// add bookkeeping for results, defer cleanup
			addPendingRequest(intReq)
			defer removePendingRequest(intReq)

			// submit the request
			MessageResponse(c, intReq)

			// wait for result
			intRsp, ok := <-intReq.Result
			if !ok {
				// it closed, pass response along unmodified
				return rsp, nil
			}

			if intRsp.Dropped {
				// if it's dropped, return nil
				return nil, nil
			}

			newRsp := intRsp.Response
			CleanRspJSON(newRsp)

			ret, err := newRsp.Parse()
			if err != nil {
				return nil, err
			}
			return ret, nil
		}
		rspSub = iproxy.AddRspInterceptor(rspIntFunc)
	}

	var wsSub *WSIntSub = nil
	if mreq.InterceptWS {
		logger.Println("Adding websocket interceptor...")
		wsIntFunc := func(req *ProxyRequest, rsp *ProxyResponse, wsm *ProxyWSMessage) (*ProxyWSMessage, error) {
			// if it doesn't pass the query, return the request unmodified
			if checker != nil && !checker(req) {
				return wsm, nil
			}

			wsData := NewWSMessageJSON(wsm)
			var msgType string
			if wsData.ToServer {
				msgType = "wstoserver"
			} else {
				msgType = "wstoclient"
			}

			CleanWSJSON(wsData)

			reqData := NewRequestJSON(req, false)
			CleanReqJSON(reqData)

			rspData := NewResponseJSON(rsp, false)
			CleanRspJSON(rspData)

			intReq := &intRequest{
				Id:      getNextIntId(),
				Type:    msgType,
				Result:  make(chan *intResponse),
				Success: true,

				Request:   reqData,
				Response:  rspData,
				WSMessage: wsData,
			}

			// add bookkeeping for results, defer cleanup
			addPendingRequest(intReq)
			defer removePendingRequest(intReq)

			// submit the request
			MessageResponse(c, intReq)

			// wait for result
			intRsp, ok := <-intReq.Result
			if !ok {
				// it closed, pass message along unmodified
				return wsm, nil
			}

			if intRsp.Dropped {
				// if it's dropped, return nil
				return nil, nil
			}

			newWsm := intRsp.WSMessage
			CleanWSJSON(newWsm)

			ret, err := newWsm.Parse()
			if err != nil {
				return nil, err
			}
			return ret, nil
		}
		wsSub = iproxy.AddWSInterceptor(wsIntFunc)
	}

	closeAll := func() {
		if reqSub != nil {
			// close req sub
			iproxy.RemoveReqInterceptor(reqSub)
		}

		if rspSub != nil {
			// close rsp sub
			iproxy.RemoveRspInterceptor(rspSub)
		}

		if wsSub != nil {
			// close websocket sub
			iproxy.RemoveWSInterceptor(wsSub)
		}

		// Close all pending requests
		pendingRequestsMtx.Lock()
		defer pendingRequestsMtx.Unlock()
		for _, req := range pendingRequests {
			close(req.Result)
		}
	}
	defer closeAll()

	// Read from the connection and process mangled requests
	reader := bufio.NewReader(c)
	for {
		// read line from conn
		logger.Println("Waiting on next message...")
		m, err := ReadMessage(reader)
		if err != nil {
			if err != io.EOF {
				logger.Println("Error reading message:", err.Error())
				intErrorResponse(0, c, "error reading message")
				continue
			}
			logger.Println("Connection closed")
			return
		}

		// convert line to appropriate struct
		var intRsp intResponse
		if err := json.Unmarshal(m, &intRsp); err != nil {
			intErrorResponse(0, c, fmt.Sprintf("error parsing message: %s", err.Error()))
			continue
		}

		// get the pending request
		pendingReq, err := getPendingRequest(intRsp.Id)
		if err != nil {
			intErrorResponse(intRsp.Id, c, err.Error())
			continue
		}

		// Validate the data contained in the response
		switch pendingReq.Type {
		case "httprequest":
			if intRsp.Request == nil {
				intErrorResponse(intRsp.Id, c, "missing request")
				continue
			}
		case "httpresponse":
			if intRsp.Response == nil {
				intErrorResponse(intRsp.Id, c, "missing response")
				continue
			}
		case "wstoserver", "wstoclient":
			if intRsp.WSMessage == nil {
				intErrorResponse(intRsp.Id, c, "missing websocket message")
				continue
			}
			intRsp.WSMessage.ToServer = (pendingReq.Type == "wstoserver")
		default:
			intErrorResponse(intRsp.Id, c, "internal error, stored message has invalid type")
			continue
		}

		// pass along message
		removePendingRequest(pendingReq)
		pendingReq.Result <- &intRsp
	}
}

/*
Query management
*/

type allSavedQueriesMessage struct {
	Storage int
}

type allSavedQueriesResponse struct {
	Success bool
	Queries []*StrSavedQuery
}

type StrSavedQuery struct {
	Name  string
	Query StrMessageQuery
}

func allSavedQueriesHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := allSavedQueriesMessage{}

	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, fmt.Sprintf("error parsing message: %s", err.Error()))
		return
	}

	if mreq.Storage == 0 {
		ErrorResponse(c, "storage is required")
		return
	}

	storage, _ := iproxy.GetMessageStorage(mreq.Storage)
	if storage == nil {
		ErrorResponse(c, fmt.Sprintf("storage with id %d does not exist", mreq.Storage))
		return
	}

	goQueries, err := storage.AllSavedQueries()
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}
	savedQueries := make([]*StrSavedQuery, 0)
	for _, q := range goQueries {
		strSavedQuery := &StrSavedQuery{
			Name:  q.Name,
			Query: nil,
		}
		sq, err := MsgQueryToStrQuery(q.Query)
		if err == nil {
			strSavedQuery.Query = sq
			savedQueries = append(savedQueries, strSavedQuery)
		}
	}
	MessageResponse(c, &allSavedQueriesResponse{
		Success: true,
		Queries: savedQueries,
	})
}

type saveQueryMessage struct {
	Name    string
	Query   StrMessageQuery
	Storage int
}

func saveQueryHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := saveQueryMessage{}

	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	if mreq.Storage == 0 {
		ErrorResponse(c, "storage is required")
		return
	}

	storage, _ := iproxy.GetMessageStorage(mreq.Storage)
	if storage == nil {
		ErrorResponse(c, fmt.Sprintf("storage with id %d does not exist", mreq.Storage))
		return
	}

	if mreq.Name == "" || mreq.Query == nil {
		ErrorResponse(c, "name and query are required")
		return
	}

	goQuery, err := StrQueryToMsgQuery(mreq.Query)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}
	_, err = CheckerFromMessageQuery(goQuery)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	err = storage.SaveQuery(mreq.Name, goQuery)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	MessageResponse(c, &successResult{Success: true})
}

type loadQueryMessage struct {
	Name    string
	Storage int
}

type loadQueryResult struct {
	Success bool
	Query   StrMessageQuery
}

func loadQueryHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := loadQueryMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	if mreq.Name == "" {
		ErrorResponse(c, "name is required")
		return
	}

	if mreq.Storage == 0 {
		ErrorResponse(c, "storage is required")
		return
	}

	storage, _ := iproxy.GetMessageStorage(mreq.Storage)
	if storage == nil {
		ErrorResponse(c, fmt.Sprintf("storage with id %d does not exist", mreq.Storage))
		return
	}

	query, err := storage.LoadQuery(mreq.Name)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	strQuery, err := MsgQueryToStrQuery(query)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	result := &loadQueryResult{
		Success: true,
		Query:   strQuery,
	}

	MessageResponse(c, result)
}

type deleteQueryMessage struct {
	Name    string
	Storage int
}

func deleteQueryHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := deleteQueryMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	if mreq.Storage == 0 {
		ErrorResponse(c, "storage is required")
		return
	}

	storage, _ := iproxy.GetMessageStorage(mreq.Storage)
	if storage == nil {
		ErrorResponse(c, fmt.Sprintf("storage with id %d does not exist", mreq.Storage))
		return
	}

	err := storage.DeleteQuery(mreq.Name)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}
	MessageResponse(c, &successResult{Success: true})
}

/*
Listener management
*/

type activeListener struct {
	Id       int
	Listener net.Listener `json:"-"`
	Type     string
	Addr     string
}

type addListenerMessage struct {
	Type string
	Addr string

	TransparentMode bool
	DestHost        string
	DestPort        int
	DestUseTLS      bool
}

type addListenerResult struct {
	Success bool
	Id      int
}

var getNextMsgListenerId = IdCounter()

// may want to move these into the iproxy to avoid globals since this assumes exactly one iproxy
var msgActiveListenersMtx sync.Mutex
var msgActiveListeners map[int]*activeListener = make(map[int]*activeListener)

func addListenerHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := addListenerMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	if mreq.Type == "" || mreq.Addr == "" {
		ErrorResponse(c, "type and addr are required")
		return
	}

	// why did I add support to listen on unix sockets? I have no idea but I'm gonna leave it
	if !(mreq.Type == "tcp" ||
		mreq.Type == "unix") {
		ErrorResponse(c, "type must be \"tcp\" or \"unix\"")
		return
	}

	listener, err := net.Listen(mreq.Type, mreq.Addr)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	if mreq.TransparentMode {
		iproxy.AddTransparentListener(listener, mreq.DestHost,
			mreq.DestPort, mreq.DestUseTLS)
	} else {
		iproxy.AddListener(listener)
	}

	alistener := &activeListener{
		Id:       getNextMsgListenerId(),
		Listener: listener,
		Type:     mreq.Type,
		Addr:     mreq.Addr,
	}

	msgActiveListenersMtx.Lock()
	defer msgActiveListenersMtx.Unlock()
	msgActiveListeners[alistener.Id] = alistener
	result := &addListenerResult{
		Success: true,
		Id:      alistener.Id,
	}

	MessageResponse(c, result)
}

type removeListenerMessage struct {
	Id int
}

func removeListenerHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := removeListenerMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	msgActiveListenersMtx.Lock()
	defer msgActiveListenersMtx.Unlock()
	alistener, ok := msgActiveListeners[mreq.Id]
	if !ok {
		ErrorResponse(c, "listener does not exist")
		return
	}

	iproxy.RemoveListener(alistener.Listener)
	delete(msgActiveListeners, alistener.Id)
	MessageResponse(c, &successResult{Success: true})
}

type getListenersMessage struct{}

type getListenersResult struct {
	Success bool
	Results []*activeListener
}

func getListenersHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	result := &getListenersResult{
		Success: true,
		Results: make([]*activeListener, 0),
	}
	msgActiveListenersMtx.Lock()
	defer msgActiveListenersMtx.Unlock()
	for _, alistener := range msgActiveListeners {
		result.Results = append(result.Results, alistener)
	}
	MessageResponse(c, result)
}

/*
Certificate Management
*/

type loadCertificatesMessage struct {
	KeyFile         string
	CertificateFile string
}

func loadCertificatesHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := loadCertificatesMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	if mreq.KeyFile == "" || mreq.CertificateFile == "" {
		ErrorResponse(c, "both KeyFile and CertificateFile are required")
		return
	}

	err := iproxy.LoadCACertificates(mreq.CertificateFile, mreq.KeyFile)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	MessageResponse(c, &successResult{Success: true})
}

type setCertificatesMessage struct {
	KeyPEMData         []byte
	CertificatePEMData []byte
}

func setCertificatesHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := setCertificatesMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	if len(mreq.KeyPEMData) == 0 || len(mreq.CertificatePEMData) == 0 {
		ErrorResponse(c, "both KeyPEMData and CertificatePEMData are required")
		return
	}

	caCert, err := tls.X509KeyPair(mreq.CertificatePEMData, mreq.KeyPEMData)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	iproxy.SetCACertificate(&caCert)
	MessageResponse(c, &successResult{Success: true})
}

func clearCertificatesHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	iproxy.SetCACertificate(nil)
	MessageResponse(c, &successResult{Success: true})
}

type generateCertificatesMessage struct {
	KeyFile  string
	CertFile string
}

func generateCertificatesHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := generateCertificatesMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

    _, err := GenerateCACertsToDisk(mreq.CertFile, mreq.KeyFile)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	MessageResponse(c, &successResult{Success: true})
}

type generatePEMCertificatesResult struct {
	Success            bool
	KeyPEMData         []byte
	CertificatePEMData []byte
}

func generatePEMCertificatesHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := generateCertificatesMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	pair, err := GenerateCACerts()
	if err != nil {
		ErrorResponse(c, "error generating certificates: "+err.Error())
		return
	}

	result := &generatePEMCertificatesResult{
		Success:            true,
		KeyPEMData:         pair.PrivateKeyPEM(),
		CertificatePEMData: pair.CACertPEM(),
	}
	MessageResponse(c, result)
}

/*
Storage functions
*/

type addSQLiteStorageMessage struct {
	Path        string
	Description string
}

type addSQLiteStorageResult struct {
	Success   bool
	StorageId int
}

func addSQLiteStorageHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := addSQLiteStorageMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	if mreq.Path == "" {
		ErrorResponse(c, "file path is required")
		return
	}

	storage, err := OpenSQLiteStorage(mreq.Path, logger)
	if err != nil {
		ErrorResponse(c, "error opening SQLite databae: "+err.Error())
		return
	}

	sid := iproxy.AddMessageStorage(storage, mreq.Description)
	result := &addSQLiteStorageResult{
		Success:   true,
		StorageId: sid,
	}
	MessageResponse(c, result)
}

type addInMemoryStorageMessage struct {
	Description string
}

type addInMemoryStorageResult struct {
	Success   bool
	StorageId int
}

func addInMemoryStorageHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := addInMemoryStorageMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	storage, err := InMemoryStorage(logger)
	if err != nil {
		ErrorResponse(c, "error creating in memory storage: "+err.Error())
		return
	}

	sid := iproxy.AddMessageStorage(storage, mreq.Description)
	result := &addInMemoryStorageResult{
		Success:   true,
		StorageId: sid,
	}
	MessageResponse(c, result)
}

type closeStorageMessage struct {
	StorageId int
}

type closeStorageResult struct {
	Success bool
}

func closeStorageHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := closeStorageMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	if mreq.StorageId == 0 {
		ErrorResponse(c, "StorageId is required")
		return
	}

	iproxy.CloseMessageStorage(mreq.StorageId)
	MessageResponse(c, &successResult{Success: true})
}

type setProxyStorageMessage struct {
	StorageId int
}

func setProxyStorageHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := setProxyStorageMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	if mreq.StorageId == 0 {
		ErrorResponse(c, "StorageId is required")
		return
	}

	err := iproxy.SetProxyStorage(mreq.StorageId)
	if err != nil {
		ErrorResponse(c, err.Error())
		return
	}

	MessageResponse(c, &successResult{Success: true})
}

type savedStorageJSON struct {
	Id          int
	Description string
}

type listProxyStorageResult struct {
	Storages []*savedStorageJSON
	Success  bool
}

func listProxyStorageHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	storages := iproxy.ListMessageStorage()
	storagesJSON := make([]*savedStorageJSON, len(storages))
	for i, ss := range storages {
		storagesJSON[i] = &savedStorageJSON{ss.Id, ss.Description}
	}
	result := &listProxyStorageResult{
		Storages: storagesJSON,
		Success:  true,
	}
	MessageResponse(c, result)
}

/*
SetProxy
*/

type setProxyMessage struct {
	UseProxy       bool
	ProxyHost      string
	ProxyPort      int
	ProxyIsSOCKS   bool
	UseCredentials bool
	Username       string
	Password       string
}

func setProxyHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := setProxyMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	var creds *ProxyCredentials = nil
	if mreq.UseCredentials {
		creds = &ProxyCredentials{
			Username: mreq.Username,
			Password: mreq.Password,
		}
	}

	if !mreq.UseProxy {
		iproxy.ClearUpstreamProxy()
	} else {
		if mreq.ProxyIsSOCKS {
			iproxy.SetUpstreamSOCKSProxy(mreq.ProxyHost, mreq.ProxyPort, creds)
		} else {
			iproxy.SetUpstreamProxy(mreq.ProxyHost, mreq.ProxyPort, creds)
		}
	}

	MessageResponse(c, &successResult{Success: true})
}

/*
WatchStorage
*/
type watchStorageMessage struct {
	StorageId   int
	HeadersOnly bool
}

type proxyMsgStorageWatcher struct {
	connMtx sync.Mutex
	storageId int
	headersOnly bool
	conn net.Conn
}

type storageUpdateResponse struct {
	StorageId int
	Action string
	MessageId string
	Request *RequestJSON
	Response *ResponseJSON
	WSMessage *WSMessageJSON
}

// Implement watcher

func (sw *proxyMsgStorageWatcher) NewRequestSaved(ms MessageStorage, req *ProxyRequest) {
	var msgRsp storageUpdateResponse
	msgRsp.Request = NewRequestJSON(req, sw.headersOnly)
	msgRsp.Action = "NewRequest"
	msgRsp.StorageId = sw.storageId
	MessageResponse(sw.conn, msgRsp)
}

func (sw *proxyMsgStorageWatcher) RequestUpdated(ms MessageStorage, req *ProxyRequest) {
	var msgRsp storageUpdateResponse
	msgRsp.Request = NewRequestJSON(req, sw.headersOnly)
	msgRsp.Action = "RequestUpdated"
	msgRsp.StorageId = sw.storageId
	MessageResponse(sw.conn, msgRsp)
}

func (sw *proxyMsgStorageWatcher) RequestDeleted(ms MessageStorage, DbId string) {
	var msgRsp storageUpdateResponse
	msgRsp.Action = "RequestDeleted"
	msgRsp.MessageId = DbId
	msgRsp.StorageId = sw.storageId
	MessageResponse(sw.conn, msgRsp)
}

func (sw *proxyMsgStorageWatcher) NewResponseSaved(ms MessageStorage, rsp *ProxyResponse) {
	var msgRsp storageUpdateResponse
	msgRsp.Response = NewResponseJSON(rsp, sw.headersOnly)
	msgRsp.Action = "NewResponse"
	msgRsp.StorageId = sw.storageId
	MessageResponse(sw.conn, msgRsp)
}

func (sw *proxyMsgStorageWatcher) ResponseUpdated(ms MessageStorage, rsp *ProxyResponse) {
	var msgRsp storageUpdateResponse
	msgRsp.Response = NewResponseJSON(rsp, sw.headersOnly)
	msgRsp.Action = "ResponseUpdated"
	msgRsp.StorageId = sw.storageId
	MessageResponse(sw.conn, msgRsp)
}

func (sw *proxyMsgStorageWatcher) ResponseDeleted(ms MessageStorage, DbId string) {
	var msgRsp storageUpdateResponse
	msgRsp.Action = "ResponseDeleted"
	msgRsp.MessageId = DbId
	msgRsp.StorageId = sw.storageId
	MessageResponse(sw.conn, msgRsp)
}

func (sw *proxyMsgStorageWatcher) NewWSMessageSaved(ms MessageStorage, req *ProxyRequest, wsm *ProxyWSMessage) {
	var msgRsp storageUpdateResponse
	msgRsp.Request = NewRequestJSON(req, sw.headersOnly)
	msgRsp.WSMessage = NewWSMessageJSON(wsm)
	msgRsp.Action = "NewWSMessage"
	msgRsp.StorageId = sw.storageId
	MessageResponse(sw.conn, msgRsp)
}

func (sw *proxyMsgStorageWatcher) WSMessageUpdated(ms MessageStorage, req *ProxyRequest, wsm *ProxyWSMessage) {
	var msgRsp storageUpdateResponse
	msgRsp.Request = NewRequestJSON(req, sw.headersOnly)
	msgRsp.WSMessage = NewWSMessageJSON(wsm)
	msgRsp.Action = "WSMessageUpdated"
	msgRsp.StorageId = sw.storageId
	MessageResponse(sw.conn, msgRsp)
}

func (sw *proxyMsgStorageWatcher) WSMessageDeleted(ms MessageStorage, DbId string) {
	var msgRsp storageUpdateResponse
	msgRsp.Action = "WSMessageDeleted"
	msgRsp.MessageId = DbId
	msgRsp.StorageId = sw.storageId
	MessageResponse(sw.conn, msgRsp)
}

// Actual handler

func watchStorageHandler(b []byte, c net.Conn, logger *log.Logger, iproxy *InterceptingProxy) {
	mreq := watchStorageMessage{}
	if err := json.Unmarshal(b, &mreq); err != nil {
		ErrorResponse(c, "error parsing message")
		return
	}

	// Parse storageId
	storages := make([]*SavedStorage, 0)
	if mreq.StorageId == -1 {
		storages = iproxy.ListMessageStorage()
	} else {
		ms, desc := iproxy.GetMessageStorage(mreq.StorageId)
		if ms == nil {
			ErrorResponse(c, "invalid storage id")
			return
		}

		storage := &SavedStorage{
			Id: mreq.StorageId,
			Storage: ms,
			Description: desc,
		}
		storages = append(storages, storage)
	}

	// Create the watchers
	for _, storage := range storages {
		watcher := &proxyMsgStorageWatcher{
			storageId: storage.Id,
			headersOnly: mreq.HeadersOnly,
			conn: c,
		}
		// Apply the watcher, kill them at end of connection
		storage.Storage.Watch(watcher)
		defer storage.Storage.EndWatch(watcher)
	}

	// Keep the connection open
	MessageResponse(c, &successResult{Success: true})
	tmpbuf := make([]byte, 1)
	var err error = nil
	for err == nil {
		_, err = c.Read(tmpbuf)
	}
}

