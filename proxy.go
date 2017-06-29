// Puppy provices an interface to create a proxy to intercept and modify HTTP and websocket messages passing through the proxy
package puppy

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var getNextSubId = IdCounter()
var getNextStorageId = IdCounter()

// ProxyWebUIHandler is a function that can be used for handling web requests intended to be handled by the proxy
type ProxyWebUIHandler func(http.ResponseWriter, *http.Request, *InterceptingProxy)

type savedStorage struct {
	storage     MessageStorage
	description string
}

// InterceptingProxy is a struct which represents a proxy which can intercept and modify HTTP and websocket messages
type InterceptingProxy struct {
	slistener    *ProxyListener
	server       *http.Server
	mtx          sync.Mutex
	logger       *log.Logger
	proxyStorage int
	netDial      NetDialer

	usingProxy   bool
	proxyHost    string
	proxyPort    int
	proxyIsSOCKS bool
	proxyCreds   *ProxyCredentials

	requestInterceptor  RequestInterceptor
	responseInterceptor ResponseInterceptor
	wSInterceptor       WSInterceptor
	scopeChecker        RequestChecker
	scopeQuery          MessageQuery

	reqSubs []*ReqIntSub
	rspSubs []*RspIntSub
	wsSubs  []*WSIntSub

	httpHandlers map[string]ProxyWebUIHandler

	messageStorage map[int]*savedStorage
}

// ProxyCredentials are a username/password combination used to represent an HTTP BasicAuth session
type ProxyCredentials struct {
	Username string
	Password string
}

// RequestInterceptor is a function that takes in a ProxyRequest and returns a modified ProxyRequest or nil to represent dropping the request
type RequestInterceptor func(req *ProxyRequest) (*ProxyRequest, error)

// ResponseInterceptor is a function that takes in a ProxyResponse and the original request and returns a modified ProxyResponse or nil to represent dropping the response
type ResponseInterceptor func(req *ProxyRequest, rsp *ProxyResponse) (*ProxyResponse, error)

// WSInterceptor is a function that takes in a ProxyWSMessage and the ProxyRequest/ProxyResponse which made up its handshake and returns and returns a modified ProxyWSMessage or nil to represent dropping the message. A WSInterceptor should be able to modify messages originating from both the client and the remote server.
type WSInterceptor func(req *ProxyRequest, rsp *ProxyResponse, msg *ProxyWSMessage) (*ProxyWSMessage, error)

// ReqIntSub represents an active HTTP request interception session in an InterceptingProxy
type ReqIntSub struct {
	id          int
	Interceptor RequestInterceptor
}

// RspIntSub represents an active HTTP response interception session in an InterceptingProxy
type RspIntSub struct {
	id          int
	Interceptor ResponseInterceptor
}

// WSIntSub represents an active websocket interception session in an InterceptingProxy
type WSIntSub struct {
	id          int
	Interceptor WSInterceptor
}

// SerializeHeader serializes the ProxyCredentials into a value that can be included in an Authorization header
func (creds *ProxyCredentials) SerializeHeader() string {
	toEncode := []byte(fmt.Sprintf("%s:%s", creds.Username, creds.Password))
	encoded := base64.StdEncoding.EncodeToString(toEncode)
	return fmt.Sprintf("Basic %s", encoded)
}

// NewInterceptingProxy will create a new InterceptingProxy and have it log using the provided logger. If logger is nil, the proxy will log to ioutil.Discard
func NewInterceptingProxy(logger *log.Logger) *InterceptingProxy {
	var iproxy InterceptingProxy
	var useLogger *log.Logger
	if logger != nil {
		useLogger = logger
	} else {
		useLogger = log.New(ioutil.Discard, "[*] ", log.Lshortfile)
	}

	iproxy.messageStorage = make(map[int]*savedStorage)
	iproxy.slistener = NewProxyListener(useLogger)
	iproxy.server = newProxyServer(useLogger, &iproxy)
	iproxy.logger = useLogger
	iproxy.httpHandlers = make(map[string]ProxyWebUIHandler)

	go func() {
		iproxy.server.Serve(iproxy.slistener)
	}()
	return &iproxy
}

// Close closes all listeners being used by the proxy. Does not shut down internal HTTP server because there is no way to gracefully shut down an http server yet.
func (iproxy *InterceptingProxy) Close() {
	// Will throw errors when the server finally shuts down and tries to call iproxy.slistener.Close a second time
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.slistener.Close()
	//iproxy.server.Close()  // Coming eventually... I hope
}

// LoadCACertificates loads a private/public key pair which should be used when generating self-signed certs for TLS connections
func (iproxy *InterceptingProxy) LoadCACertificates(certFile, keyFile string) error {
	caCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("could not load certificate pair: %s", err.Error())
	}

	iproxy.SetCACertificate(&caCert)
	return nil
}

// SetCACertificate sets certificate which should be used when generating self-signed certs for TLS connections
func (iproxy *InterceptingProxy) SetCACertificate(caCert *tls.Certificate) {
	if iproxy.slistener == nil {
		panic("intercepting proxy does not have a proxy listener")
	}
	iproxy.slistener.SetCACertificate(caCert)
}

// GetCACertificate returns certificate used to self-sign certificates for TLS connections
func (iproxy *InterceptingProxy) GetCACertificate() *tls.Certificate {
	return iproxy.slistener.GetCACertificate()
}

// AddListener will have the proxy listen for HTTP connections on a listener. Proxy will attempt to strip TLS from the connection
func (iproxy *InterceptingProxy) AddListener(l net.Listener) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.slistener.AddListener(l)
}

// Have the proxy listen for HTTP connections on a listener and transparently redirect them to the destination. Listeners added this way can only redirect requests to a single destination. However, it does not rely on the client being aware that it is using an HTTP proxy.
func (iproxy *InterceptingProxy) AddTransparentListener(l net.Listener, destHost string, destPort int, useTLS bool) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.slistener.AddTransparentListener(l, destHost, destPort, useTLS)
}

// RemoveListner will have the proxy stop listening to a listener
func (iproxy *InterceptingProxy) RemoveListener(l net.Listener) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.slistener.RemoveListener(l)
}

// GetMessageStorage takes in a storage ID and returns the storage associated with the ID
func (iproxy *InterceptingProxy) GetMessageStorage(id int) (MessageStorage, string) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	savedStorage, ok := iproxy.messageStorage[id]
	if !ok {
		return nil, ""
	}
	return savedStorage.storage, savedStorage.description
}

// AddMessageStorage associates a MessageStorage with the proxy and returns an ID to be used when referencing the storage in the future
func (iproxy *InterceptingProxy) AddMessageStorage(storage MessageStorage, description string) int {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	id := getNextStorageId()
	iproxy.messageStorage[id] = &savedStorage{storage, description}
	return id
}

// CloseMessageStorage closes a message storage associated with the proxy
func (iproxy *InterceptingProxy) CloseMessageStorage(id int) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	savedStorage, ok := iproxy.messageStorage[id]
	if !ok {
		return
	}
	delete(iproxy.messageStorage, id)
	savedStorage.storage.Close()
}

// SavedStorage represents a storage associated with the proxy
type SavedStorage struct {
	Id          int
	Storage     MessageStorage
	Description string
}

// ListMessageStorage returns a list of storages associated with the proxy
func (iproxy *InterceptingProxy) ListMessageStorage() []*SavedStorage {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	r := make([]*SavedStorage, 0)
	for id, ss := range iproxy.messageStorage {
		r = append(r, &SavedStorage{id, ss.storage, ss.description})
	}
	return r
}

func (iproxy *InterceptingProxy) getRequestSubs() []*ReqIntSub {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	return iproxy.reqSubs
}

func (iproxy *InterceptingProxy) getResponseSubs() []*RspIntSub {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	return iproxy.rspSubs
}

func (iproxy *InterceptingProxy) getWSSubs() []*WSIntSub {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	return iproxy.wsSubs
}

// LoadScope loads the scope from the given storage and applies it to the proxy
func (iproxy *InterceptingProxy) LoadScope(storageId int) error {
	// Try and set the scope
	savedStorage, ok := iproxy.messageStorage[storageId]
	if !ok {
		return fmt.Errorf("proxy has no associated storage")
	}
	iproxy.logger.Println("loading scope")
	if scope, err := savedStorage.storage.LoadQuery("__scope"); err == nil {
		if err := iproxy.setScopeQuery(scope); err != nil {
			iproxy.logger.Println("error setting scope:", err.Error())
		}
	} else {
		iproxy.logger.Println("error loading scope:", err.Error())
	}
	return nil
}

// GetScopeChecker creates a RequestChecker which checks if a request matches the proxy's current scope
func (iproxy *InterceptingProxy) GetScopeChecker() RequestChecker {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	return iproxy.scopeChecker
}

// SetScopeChecker has the proxy use a specific RequestChecker to check if a request is in scope. If the checker returns true for a request it is considered in scope. Otherwise it is considered out of scope.
func (iproxy *InterceptingProxy) SetScopeChecker(checker RequestChecker) error {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	savedStorage, ok := iproxy.messageStorage[iproxy.proxyStorage]
	if !ok {
		savedStorage = nil
	}
	iproxy.scopeChecker = checker
	iproxy.scopeQuery = nil
	emptyQuery := make(MessageQuery, 0)
	if savedStorage != nil {
		savedStorage.storage.SaveQuery("__scope", emptyQuery) // Assume it clears it I guess
	}
	return nil
}

// GetScopeQuery returns the query associated with the proxy's scope. If the scope was set using SetScopeChecker, nil is returned
func (iproxy *InterceptingProxy) GetScopeQuery() MessageQuery {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	return iproxy.scopeQuery
}

// SetScopeQuery sets the scope of the proxy to include any request which matches the given MessageQuery
func (iproxy *InterceptingProxy) SetScopeQuery(query MessageQuery) error {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	return iproxy.setScopeQuery(query)
}

func (iproxy *InterceptingProxy) setScopeQuery(query MessageQuery) error {
	checker, err := CheckerFromMessageQuery(query)
	if err != nil {
		return err
	}
	savedStorage, ok := iproxy.messageStorage[iproxy.proxyStorage]
	if !ok {
		savedStorage = nil
	}
	iproxy.scopeChecker = checker
	iproxy.scopeQuery = query
	if savedStorage != nil {
		if err = savedStorage.storage.SaveQuery("__scope", query); err != nil {
			return fmt.Errorf("could not save scope to storage: %s", err.Error())
		}
	}

	return nil
}

// ClearScope removes all scope checks from the proxy so that all requests passing through the proxy will be considered in-scope
func (iproxy *InterceptingProxy) ClearScope() error {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.scopeChecker = nil
	iproxy.scopeChecker = nil
	emptyQuery := make(MessageQuery, 0)
	savedStorage, ok := iproxy.messageStorage[iproxy.proxyStorage]
	if !ok {
		savedStorage = nil
	}
	if savedStorage != nil {
		if err := savedStorage.storage.SaveQuery("__scope", emptyQuery); err != nil {
			return fmt.Errorf("could not clear scope in storage: %s", err.Error())
		}
	}
	return nil
}

// SetNetDial sets the NetDialer that should be used to create outgoing connections when submitting HTTP requests. Overwrites the request's NetDialer
func (iproxy *InterceptingProxy) SetNetDial(dialer NetDialer) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.netDial = dialer
}

// NetDial returns the dialer currently being used to create outgoing connections when submitting HTTP requests
func (iproxy *InterceptingProxy) NetDial() NetDialer {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	return iproxy.netDial
}

// ClearUpstreamProxy stops the proxy from using an upstream proxy for future connections
func (iproxy *InterceptingProxy) ClearUpstreamProxy() {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.usingProxy = false
	iproxy.proxyHost = ""
	iproxy.proxyPort = 0
	iproxy.proxyIsSOCKS = false
}

// SetUpstreamProxy causes the proxy to begin using an upstream HTTP proxy for submitted HTTP requests
func (iproxy *InterceptingProxy) SetUpstreamProxy(proxyHost string, proxyPort int, creds *ProxyCredentials) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.usingProxy = true
	iproxy.proxyHost = proxyHost
	iproxy.proxyPort = proxyPort
	iproxy.proxyIsSOCKS = false
	iproxy.proxyCreds = creds
}

// SetUpstreamSOCKSProxy causes the proxy to begin using an upstream SOCKS proxy for submitted HTTP requests
func (iproxy *InterceptingProxy) SetUpstreamSOCKSProxy(proxyHost string, proxyPort int, creds *ProxyCredentials) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.usingProxy = true
	iproxy.proxyHost = proxyHost
	iproxy.proxyPort = proxyPort
	iproxy.proxyIsSOCKS = true
	iproxy.proxyCreds = creds
}

// SubmitRequest submits a ProxyRequest. Does not automatically save the request/results to proxy storage
func (iproxy *InterceptingProxy) SubmitRequest(req *ProxyRequest) error {
	oldDial := req.NetDial
	defer func() { req.NetDial = oldDial }()
	req.NetDial = iproxy.NetDial()

	if iproxy.usingProxy {
		if iproxy.proxyIsSOCKS {
			return SubmitRequestSOCKSProxy(req, iproxy.proxyHost, iproxy.proxyPort, iproxy.proxyCreds)
		} else {
			return SubmitRequestProxy(req, iproxy.proxyHost, iproxy.proxyPort, iproxy.proxyCreds)
		}
	}
	return SubmitRequest(req)
}

// WSDial dials a remote server and submits the given request to initiate the handshake
func (iproxy *InterceptingProxy) WSDial(req *ProxyRequest) (*WSSession, error) {
	oldDial := req.NetDial
	defer func() { req.NetDial = oldDial }()
	req.NetDial = iproxy.NetDial()

	if iproxy.usingProxy {
		if iproxy.proxyIsSOCKS {
			return WSDialSOCKSProxy(req, iproxy.proxyHost, iproxy.proxyPort, iproxy.proxyCreds)
		} else {
			return WSDialProxy(req, iproxy.proxyHost, iproxy.proxyPort, iproxy.proxyCreds)
		}
	}
	return WSDial(req)
}

// AddReqInterceptor adds a RequestInterceptor to the proxy which will be used to modify HTTP requests as they pass through the proxy. Returns a struct representing the active interceptor.
func (iproxy *InterceptingProxy) AddReqInterceptor(f RequestInterceptor) *ReqIntSub {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	sub := &ReqIntSub{
		id:          getNextSubId(),
		Interceptor: f,
	}
	iproxy.reqSubs = append(iproxy.reqSubs, sub)
	return sub
}

// RemoveReqInterceptor removes an active request interceptor from the proxy
func (iproxy *InterceptingProxy) RemoveReqInterceptor(sub *ReqIntSub) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	for i, checkSub := range iproxy.reqSubs {
		if checkSub.id == sub.id {
			iproxy.reqSubs = append(iproxy.reqSubs[:i], iproxy.reqSubs[i+1:]...)
			return
		}
	}
}

// AddRspInterceptor adds a ResponseInterceptor to the proxy which will be used to modify HTTP responses as they pass through the proxy. Returns a struct representing the active interceptor.
func (iproxy *InterceptingProxy) AddRspInterceptor(f ResponseInterceptor) *RspIntSub {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	sub := &RspIntSub{
		id:          getNextSubId(),
		Interceptor: f,
	}
	iproxy.rspSubs = append(iproxy.rspSubs, sub)
	return sub
}

// RemoveRspInterceptor removes an active response interceptor from the proxy
func (iproxy *InterceptingProxy) RemoveRspInterceptor(sub *RspIntSub) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	for i, checkSub := range iproxy.rspSubs {
		if checkSub.id == sub.id {
			iproxy.rspSubs = append(iproxy.rspSubs[:i], iproxy.rspSubs[i+1:]...)
			return
		}
	}
}

// AddWSInterceptor adds a WSInterceptor to the proxy which will be used to modify both incoming and outgoing websocket messages as they pass through the proxy. Returns a struct representing the active interceptor.
func (iproxy *InterceptingProxy) AddWSInterceptor(f WSInterceptor) *WSIntSub {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	sub := &WSIntSub{
		id:          getNextSubId(),
		Interceptor: f,
	}
	iproxy.wsSubs = append(iproxy.wsSubs, sub)
	return sub
}

// RemoveWSInterceptor removes an active websocket interceptor from the proxy
func (iproxy *InterceptingProxy) RemoveWSInterceptor(sub *WSIntSub) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	for i, checkSub := range iproxy.wsSubs {
		if checkSub.id == sub.id {
			iproxy.wsSubs = append(iproxy.wsSubs[:i], iproxy.wsSubs[i+1:]...)
			return
		}
	}
}

// SetProxyStorage sets which storage should be used to store messages as they pass through the proxy
func (iproxy *InterceptingProxy) SetProxyStorage(storageId int) error {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	iproxy.proxyStorage = storageId

	_, ok := iproxy.messageStorage[iproxy.proxyStorage]
	if !ok {
		return fmt.Errorf("no storage with id %d", storageId)
	}

	iproxy.LoadScope(storageId)
	return nil
}

// GetProxyStorage returns the storage being used to save messages as they pass through the proxy
func (iproxy *InterceptingProxy) GetProxyStorage() MessageStorage {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	savedStorage, ok := iproxy.messageStorage[iproxy.proxyStorage]
	if !ok {
		return nil
	}
	return savedStorage.storage
}

// AddHTTPHandler causes the proxy to redirect requests to a host to an HTTPHandler. This can be used, for example, to create an internal web inteface. Be careful with what actions are allowed through the interface because the interface could be vulnerable to cross-site request forgery attacks.
func (iproxy *InterceptingProxy) AddHTTPHandler(host string, handler ProxyWebUIHandler) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.httpHandlers[host] = handler
}

// GetHTTPHandler returns the HTTP handler for a given host
func (iproxy *InterceptingProxy) GetHTTPHandler(host string) (ProxyWebUIHandler, error) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	handler, ok := iproxy.httpHandlers[host]
	if !ok {
		return nil, fmt.Errorf("no handler for host %s", host)
	}
	return handler, nil
}

// RemoveHTTPHandler removes the HTTP handler for a given host
func (iproxy *InterceptingProxy) RemoveHTTPHandler(host string) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	delete(iproxy.httpHandlers, host)
}

// ParseProxyRequest converts an http.Request read from a connection from a ProxyListener into a ProxyRequest
func ParseProxyRequest(r *http.Request) (*ProxyRequest, error) {
	host, port, useTLS, err := DecodeRemoteAddr(r.RemoteAddr)
	if err != nil {
		return nil, nil
	}
	pr := NewProxyRequest(r, host, port, useTLS)
	return pr, nil
}

// BlankResponse writes a blank response to a http.ResponseWriter. Used when a request/response is dropped.
func BlankResponse(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-control", "no-cache")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Cache-control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(200)
}

// ErrResponse writes an error response to the given http.ResponseWriter. Used to give proxy error information to the browser
func ErrResponse(w http.ResponseWriter, err error) {
	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-control", "no-cache")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Cache-control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// ServeHTTP is used to implement the interface required to have the proxy behave as an HTTP server
func (iproxy *InterceptingProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler, err := iproxy.GetHTTPHandler(r.Host)
	if err == nil {
		handler(w, r, iproxy)
		return
	}

	req, _ := ParseProxyRequest(r)
	iproxy.logger.Println("Received request to", req.FullURL().String())
	req.StripProxyHeaders()

	ms := iproxy.GetProxyStorage()
	scopeChecker := iproxy.GetScopeChecker()

	// Helper functions
	checkScope := func(req *ProxyRequest) bool {
		if scopeChecker != nil {
			return scopeChecker(req)
		}
		return true
	}

	saveIfExists := func(req *ProxyRequest) error {
		if ms != nil && checkScope(req) {
			if err := UpdateRequest(ms, req); err != nil {
				return err
			}
		}
		return nil
	}

	/*
		functions to mangle messages using the iproxy's manglers
		each return the new message, whether it was modified, and an error
	*/

	mangleRequest := func(req *ProxyRequest) (*ProxyRequest, bool, error) {
		newReq := req.Clone()
		reqSubs := iproxy.getRequestSubs()
		for _, sub := range reqSubs {
			var err error = nil
			newReq, err = sub.Interceptor(newReq)
			if err != nil {
				e := fmt.Errorf("error with request interceptor: %s", err)
				return nil, false, e
			}
			if newReq == nil {
				break
			}
		}

		if newReq != nil {
			newReq.StartDatetime = time.Now()
			if !req.Eq(newReq) {
				iproxy.logger.Println("Request modified by interceptor")
				return newReq, true, nil
			}
		} else {
			return nil, true, nil
		}
		return req, false, nil
	}

	mangleResponse := func(req *ProxyRequest, rsp *ProxyResponse) (*ProxyResponse, bool, error) {
		reqCopy := req.Clone()
		newRsp := rsp.Clone()
		rspSubs := iproxy.getResponseSubs()
		iproxy.logger.Printf("%d interceptors", len(rspSubs))
		for _, sub := range rspSubs {
			iproxy.logger.Println("mangling rsp...")
			var err error = nil
			newRsp, err = sub.Interceptor(reqCopy, newRsp)
			if err != nil {
				e := fmt.Errorf("error with response interceptor: %s", err)
				return nil, false, e
			}
			if newRsp == nil {
				break
			}
		}

		if newRsp != nil {
			if !rsp.Eq(newRsp) {
				iproxy.logger.Println("Response for", req.FullURL(), "modified by interceptor")
				// it was mangled
				return newRsp, true, nil
			}
		} else {
			// it was dropped
			return nil, true, nil
		}

		// it wasn't changed
		return rsp, false, nil
	}

	mangleWS := func(req *ProxyRequest, rsp *ProxyResponse, ws *ProxyWSMessage) (*ProxyWSMessage, bool, error) {
		newMsg := ws.Clone()
		reqCopy := req.Clone()
		rspCopy := rsp.Clone()
		wsSubs := iproxy.getWSSubs()
		for _, sub := range wsSubs {
			var err error = nil
			newMsg, err = sub.Interceptor(reqCopy, rspCopy, newMsg)
			if err != nil {
				e := fmt.Errorf("error with ws interceptor: %s", err)
				return nil, false, e
			}
			if newMsg == nil {
				break
			}
		}

		if newMsg != nil {
			if !ws.Eq(newMsg) {
				newMsg.Timestamp = time.Now()
				newMsg.Direction = ws.Direction
				iproxy.logger.Println("Message modified by interceptor")
				return newMsg, true, nil
			}
		} else {
			return nil, true, nil
		}
		return ws, false, nil
	}

	req.StartDatetime = time.Now()

	if checkScope(req) {
		if err := saveIfExists(req); err != nil {
			ErrResponse(w, err)
			return
		}
		newReq, mangled, err := mangleRequest(req)
		if err != nil {
			ErrResponse(w, err)
			return
		}
		if mangled {
			if newReq == nil {
				req.ServerResponse = nil
				if err := saveIfExists(req); err != nil {
					ErrResponse(w, err)
					return
				}
				BlankResponse(w)
				return
			}
			newReq.Unmangled = req
			req = newReq
			req.StartDatetime = time.Now()
			if err := saveIfExists(req); err != nil {
				ErrResponse(w, err)
				return
			}
		}
	}

	if req.IsWSUpgrade() {
		iproxy.logger.Println("Detected websocket request. Upgrading...")

		rc, err := iproxy.WSDial(req)
		if err != nil {
			iproxy.logger.Println("error dialing ws server:", err)
			http.Error(w, fmt.Sprintf("error dialing websocket server: %s", err.Error()), http.StatusInternalServerError)
			return
		}
		defer rc.Close()
		req.EndDatetime = time.Now()
		if err := saveIfExists(req); err != nil {
			ErrResponse(w, err)
			return
		}

		var upgrader = websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		}

		lc, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			iproxy.logger.Println("error upgrading connection:", err)
			http.Error(w, fmt.Sprintf("error upgrading connection: %s", err.Error()), http.StatusInternalServerError)
			return
		}
		defer lc.Close()

		var wg sync.WaitGroup
		var reqMtx sync.Mutex
		addWSMessage := func(req *ProxyRequest, wsm *ProxyWSMessage) {
			reqMtx.Lock()
			defer reqMtx.Unlock()
			req.WSMessages = append(req.WSMessages, wsm)
		}

		// Get messages from server
		wg.Add(1)
		go func() {
			for {
				mtype, msg, err := rc.ReadMessage()
				if err != nil {
					lc.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
					iproxy.logger.Println("error with receiving server message:", err)
					wg.Done()
					return
				}
				pws, err := NewProxyWSMessage(mtype, msg, ToClient)
				if err != nil {
					iproxy.logger.Println("error creating ws object:", err.Error())
					continue
				}
				pws.Timestamp = time.Now()

				if checkScope(req) {
					newMsg, mangled, err := mangleWS(req, req.ServerResponse, pws)
					if err != nil {
						iproxy.logger.Println("error mangling ws:", err)
						return
					}
					if mangled {
						if newMsg == nil {
							continue
						} else {
							newMsg.Unmangled = pws
							pws = newMsg
							pws.Request = nil
						}
					}
				}

				addWSMessage(req, pws)
				if err := saveIfExists(req); err != nil {
					iproxy.logger.Println("error saving request:", err)
					continue
				}
				lc.WriteMessage(pws.Type, pws.Message)
			}
		}()

		// Get messages from client
		wg.Add(1)
		go func() {
			for {
				mtype, msg, err := lc.ReadMessage()
				if err != nil {
					rc.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
					iproxy.logger.Println("error with receiving client message:", err)
					wg.Done()
					return
				}
				pws, err := NewProxyWSMessage(mtype, msg, ToServer)
				if err != nil {
					iproxy.logger.Println("error creating ws object:", err.Error())
					continue
				}
				pws.Timestamp = time.Now()

				if checkScope(req) {
					newMsg, mangled, err := mangleWS(req, req.ServerResponse, pws)
					if err != nil {
						iproxy.logger.Println("error mangling ws:", err)
						return
					}
					if mangled {
						if newMsg == nil {
							continue
						} else {
							newMsg.Unmangled = pws
							pws = newMsg
							pws.Request = nil
						}
					}
				}

				addWSMessage(req, pws)
				if err := saveIfExists(req); err != nil {
					iproxy.logger.Println("error saving request:", err)
					continue
				}
				rc.WriteMessage(pws.Type, pws.Message)
			}
		}()
		wg.Wait()
		iproxy.logger.Println("Websocket session complete!")
	} else {
		err := iproxy.SubmitRequest(req)
		if err != nil {
			http.Error(w, fmt.Sprintf("error submitting request: %s", err.Error()), http.StatusInternalServerError)
			return
		}
		req.EndDatetime = time.Now()
		if err := saveIfExists(req); err != nil {
			ErrResponse(w, err)
			return
		}

		if checkScope(req) {
			newRsp, mangled, err := mangleResponse(req, req.ServerResponse)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if mangled {
				if newRsp == nil {
					req.ServerResponse = nil
					if err := saveIfExists(req); err != nil {
						ErrResponse(w, err)
						return
					}
					BlankResponse(w)
					return
				}
				newRsp.Unmangled = req.ServerResponse
				req.ServerResponse = newRsp
				if err := saveIfExists(req); err != nil {
					ErrResponse(w, err)
					return
				}
			}
		}

		for k, v := range req.ServerResponse.Header {
			for _, vv := range v {
				w.Header().Add(k, vv)
			}
		}
		w.WriteHeader(req.ServerResponse.StatusCode)
		w.Write(req.ServerResponse.BodyBytes())
		return
	}
}

func newProxyServer(logger *log.Logger, iproxy *InterceptingProxy) *http.Server {
	server := &http.Server{
		Handler:  iproxy,
		ErrorLog: logger,
	}
	return server
}
