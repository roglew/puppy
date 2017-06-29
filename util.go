package puppy

import (
	"io/ioutil"
	"log"
	"sync"
)

// A type that can be used to create specific error types. ie `const QueryNotSupported = ConstErr("custom query not supported")`
type ConstErr string

func (e ConstErr) Error() string { return string(e) }

func DuplicateBytes(bs []byte) []byte {
	retBs := make([]byte, len(bs))
	copy(retBs, bs)
	return retBs
}

func IdCounter() func() int {
	var nextId int = 1
	var nextIdMtx sync.Mutex
	return func() int {
		nextIdMtx.Lock()
		defer nextIdMtx.Unlock()
		ret := nextId
		nextId++
		return ret
	}
}

func NullLogger() *log.Logger {
	return log.New(ioutil.Discard, "", log.Lshortfile)
}

// A helper type to sort requests by submission time: ie sort.Sort(ReqSort(reqs))
type ReqSort []*ProxyRequest

func (reql ReqSort) Len() int {
	return len(reql)
}

func (reql ReqSort) Swap(i int, j int) {
	reql[i], reql[j] = reql[j], reql[i]
}

func (reql ReqSort) Less(i int, j int) bool {
	return reql[j].StartDatetime.After(reql[i].StartDatetime)
}

// A helper type to sort websocket messages by timestamp: ie sort.Sort(WSSort(req.WSMessages))
type WSSort []*ProxyWSMessage

func (wsml WSSort) Len() int {
	return len(wsml)
}

func (wsml WSSort) Swap(i int, j int) {
	wsml[i], wsml[j] = wsml[j], wsml[i]
}

func (wsml WSSort) Less(i int, j int) bool {
	return wsml[j].Timestamp.After(wsml[i].Timestamp)
}
