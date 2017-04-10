package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var getNextSubId = IdCounter()
var getNextStorageId = IdCounter()

type savedStorage struct {
	storage MessageStorage
	description string
}

type InterceptingProxy struct {
	slistener *ProxyListener
	server    *http.Server
	mtx       sync.Mutex
	logger    *log.Logger
	proxyStorage int

	requestInterceptor RequestInterceptor
	responseInterceptor ResponseInterceptor
	wSInterceptor WSInterceptor
	scopeChecker RequestChecker
	scopeQuery MessageQuery

	reqSubs []*ReqIntSub
	rspSubs []*RspIntSub
	wsSubs []*WSIntSub

	messageStorage map[int]*savedStorage
}

type RequestInterceptor func(req *ProxyRequest) (*ProxyRequest, error)
type ResponseInterceptor func(req *ProxyRequest, rsp *ProxyResponse) (*ProxyResponse, error)
type WSInterceptor func(req *ProxyRequest, rsp *ProxyResponse, msg *ProxyWSMessage) (*ProxyWSMessage, error)

type proxyHandler struct {
	Logger *log.Logger
	IProxy *InterceptingProxy
}

type ReqIntSub struct {
	id int
	Interceptor RequestInterceptor
}

type RspIntSub struct {
	id int
	Interceptor ResponseInterceptor
}

type WSIntSub struct {
	id int
	Interceptor WSInterceptor
}

func NewInterceptingProxy(logger *log.Logger) *InterceptingProxy {
	var iproxy InterceptingProxy
	iproxy.messageStorage = make(map[int]*savedStorage)
	iproxy.slistener = NewProxyListener(logger)
	iproxy.server = newProxyServer(logger, &iproxy)
	iproxy.logger = logger

	go func() {
		iproxy.server.Serve(iproxy.slistener)
	}()
	return &iproxy
}

func (iproxy *InterceptingProxy) Close() {
	// Closes all associated listeners, but does not shut down the server because there is no way to gracefully shut down an http server yet :|
	// Will throw errors when the server finally shuts down and tries to call iproxy.slistener.Close a second time
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.slistener.Close() 
	//iproxy.server.Close()  // Coming eventually... I hope
}

func (iproxy *InterceptingProxy) SetCACertificate(caCert *tls.Certificate) {
	if iproxy.slistener == nil {
		panic("intercepting proxy does not have a proxy listener")
	}
	iproxy.slistener.SetCACertificate(caCert)
}

func (iproxy *InterceptingProxy) GetCACertificate() (*tls.Certificate) {
	return iproxy.slistener.GetCACertificate()
}

func (iproxy *InterceptingProxy) AddListener(l net.Listener) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.slistener.AddListener(l)
}

func (iproxy *InterceptingProxy) RemoveListener(l net.Listener) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	iproxy.slistener.RemoveListener(l)
}

func (iproxy *InterceptingProxy) GetMessageStorage(id int) (MessageStorage, string) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	savedStorage, ok := iproxy.messageStorage[id]
	if !ok {
		return nil, ""
	}
	return savedStorage.storage, savedStorage.description
}

func (iproxy *InterceptingProxy) AddMessageStorage(storage MessageStorage, description string) (int) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	id := getNextStorageId()
	iproxy.messageStorage[id] = &savedStorage{storage, description}
	return id
}

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

type SavedStorage struct {
	Id int
	Storage MessageStorage
	Description string
}

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

func (iproxy *InterceptingProxy) GetScopeChecker() (RequestChecker) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	return iproxy.scopeChecker
}

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

func (iproxy *InterceptingProxy) GetScopeQuery() (MessageQuery) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	return iproxy.scopeQuery
}

func (iproxy *InterceptingProxy) SetScopeQuery(query MessageQuery) (error) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()
	return iproxy.setScopeQuery(query)
}

func (iproxy *InterceptingProxy) setScopeQuery(query MessageQuery) (error) {
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

func (iproxy *InterceptingProxy) ClearScope() (error) {
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

func (iproxy *InterceptingProxy) AddReqIntSub(f RequestInterceptor) (*ReqIntSub) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	sub := &ReqIntSub{
		id: getNextSubId(),
		Interceptor: f,
	}
	iproxy.reqSubs = append(iproxy.reqSubs, sub)
	return sub
}

func (iproxy *InterceptingProxy) RemoveReqIntSub(sub *ReqIntSub) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	for i, checkSub := range iproxy.reqSubs {
		if checkSub.id == sub.id {
			iproxy.reqSubs = append(iproxy.reqSubs[:i], iproxy.reqSubs[i+1:]...)
			return
		}
	}
}

func (iproxy *InterceptingProxy) AddRspIntSub(f ResponseInterceptor) (*RspIntSub) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	sub := &RspIntSub{
		id: getNextSubId(),
		Interceptor: f,
	}
	iproxy.rspSubs = append(iproxy.rspSubs, sub)
	return sub
}

func (iproxy *InterceptingProxy) RemoveRspIntSub(sub *RspIntSub) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	for i, checkSub := range iproxy.rspSubs {
		if checkSub.id == sub.id {
			iproxy.rspSubs = append(iproxy.rspSubs[:i], iproxy.rspSubs[i+1:]...)
			return
		}
	}
}

func (iproxy *InterceptingProxy) AddWSIntSub(f WSInterceptor) (*WSIntSub) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	sub := &WSIntSub{
		id: getNextSubId(),
		Interceptor: f,
	}
	iproxy.wsSubs = append(iproxy.wsSubs, sub)
	return sub
}

func (iproxy *InterceptingProxy) RemoveWSIntSub(sub *WSIntSub) {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	for i, checkSub := range iproxy.wsSubs {
		if checkSub.id == sub.id {
			iproxy.wsSubs = append(iproxy.wsSubs[:i], iproxy.wsSubs[i+1:]...)
			return
		}
	}
}

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

func (iproxy *InterceptingProxy) GetProxyStorage() MessageStorage {
	iproxy.mtx.Lock()
	defer iproxy.mtx.Unlock()

	savedStorage, ok := iproxy.messageStorage[iproxy.proxyStorage]
	if !ok {
		return nil
	}
	return savedStorage.storage
}

func ParseProxyRequest(r *http.Request) (*ProxyRequest, error) {
	host, port, useTLS, err := DecodeRemoteAddr(r.RemoteAddr)
	if err != nil {
		return nil, nil
	}
	pr := NewProxyRequest(r, host, port, useTLS)
	return pr, nil
}

func BlankResponse(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-control", "no-cache")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Cache-control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(200)
}

func ErrResponse(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (p proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	req, _ := ParseProxyRequest(r)
	p.Logger.Println("Received request to", req.FullURL().String())
	req.StripProxyHeaders()

	ms := p.IProxy.GetProxyStorage()
	scopeChecker := p.IProxy.GetScopeChecker()

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
		reqSubs := p.IProxy.getRequestSubs()
		for _, sub := range reqSubs {
			var err error = nil
			newReq, err := sub.Interceptor(newReq)
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
				p.Logger.Println("Request modified by interceptor")
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
		rspSubs := p.IProxy.getResponseSubs()
		p.Logger.Printf("%d interceptors", len(rspSubs))
		for _, sub := range rspSubs {
			p.Logger.Println("mangling rsp...")
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
				p.Logger.Println("Response for", req.FullURL(), "modified by interceptor")
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
		wsSubs := p.IProxy.getWSSubs()
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
				p.Logger.Println("Message modified by interceptor")
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
		p.Logger.Println("Detected websocket request. Upgrading...")

		rc, err := req.WSDial()
		if err != nil {
			p.Logger.Println("error dialing ws server:", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
			p.Logger.Println("error upgrading connection:", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
					p.Logger.Println("error with receiving server message:", err)
					wg.Done()
					return
				}
				pws, err := NewProxyWSMessage(mtype, msg, ToClient)
				if err != nil {
					p.Logger.Println("error creating ws object:", err.Error())
					continue
				}
				pws.Timestamp = time.Now()

				if checkScope(req) {
					newMsg, mangled, err := mangleWS(req, req.ServerResponse, pws)
					if err != nil {
						p.Logger.Println("error mangling ws:", err)
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
					p.Logger.Println("error saving request:", err)
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
					p.Logger.Println("error with receiving client message:", err)
					wg.Done()
					return
				}
				pws, err := NewProxyWSMessage(mtype, msg, ToServer)
				if err != nil {
					p.Logger.Println("error creating ws object:", err.Error())
					continue
				}
				pws.Timestamp = time.Now()

				if checkScope(req) {
					newMsg, mangled, err := mangleWS(req, req.ServerResponse, pws)
					if err != nil {
						p.Logger.Println("error mangling ws:", err)
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
					p.Logger.Println("error saving request:", err)
					continue
				}
				rc.WriteMessage(pws.Type, pws.Message)
			}
		}()
		wg.Wait()
		p.Logger.Println("Websocket session complete!")
	} else {
		err := req.Submit()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
		Handler: proxyHandler{
			Logger: logger,
			IProxy: iproxy,
		},
		ErrorLog: logger,
	}
	return server
}
