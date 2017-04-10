package main

import (
	"fmt"
	"errors"
)

type MessageStorage interface {
	// NOTE: Load func responsible for loading dependent messages, delte function responsible for deleting dependent messages
	//       if it takes an ID, the storage is responsible for dependent messages

	// Close the storage
	Close()
	
	// Update an existing request in the storage. Requires that it has already been saved
	UpdateRequest(req *ProxyRequest) error
	// Save a new instance of the request in the storage regardless of if it has already been saved
	SaveNewRequest(req *ProxyRequest) error
	// Load a request given a unique id
	LoadRequest(reqid string) (*ProxyRequest, error)
	LoadUnmangledRequest(reqid string) (*ProxyRequest, error)
	// Delete a request
	DeleteRequest(reqid string) (error)

	// Update an existing response in the storage. Requires that it has already been saved
	UpdateResponse(rsp *ProxyResponse) error
	// Save a new instance of the response in the storage regardless of if it has already been saved
	SaveNewResponse(rsp *ProxyResponse) error
	// Load a response given a unique id
	LoadResponse(rspid string) (*ProxyResponse, error)
	LoadUnmangledResponse(rspid string) (*ProxyResponse, error)
	// Delete a response
	DeleteResponse(rspid string) (error)

	// Update an existing websocket message in the storage. Requires that it has already been saved
	UpdateWSMessage(req *ProxyRequest, wsm *ProxyWSMessage) error
	// Save a new instance of the websocket message in the storage regardless of if it has already been saved
	SaveNewWSMessage(req *ProxyRequest, wsm *ProxyWSMessage) error
	// Load a websocket given a unique id
	LoadWSMessage(wsmid string) (*ProxyWSMessage, error)
	LoadUnmangledWSMessage(wsmid string) (*ProxyWSMessage, error)
	// Delete a WSMessage
	DeleteWSMessage(wsmid string) (error)

	// Get list of all the request keys
	RequestKeys() ([]string, error)

	// A function to perform a search of requests in the storage. Same arguments as NewRequestChecker
	Search(limit int64, args ...interface{}) ([]*ProxyRequest, error)

	// A function to naively check every function in storage with the given function and return the ones that match
	CheckRequests(limit int64, checker RequestChecker) ([]*ProxyRequest, error)

	// Same as Search() but returns the IDs of the requests instead
	// If Search() starts causing memory errors and I can't assume all the matching requests will fit in memory, I'll implement this or something
	//SearchIDs(args ...interface{}) ([]string, error)

	// Query functions
	AllSavedQueries() ([]*SavedQuery, error)
	SaveQuery(name string, query MessageQuery) (error)
	LoadQuery(name string) (MessageQuery, error)
	DeleteQuery(name string) (error)
}

const QueryNotSupported = ConstErr("custom query not supported")

type ReqSort []*ProxyRequest

type SavedQuery struct {
	Name string
	Query MessageQuery
}

func (reql ReqSort) Len() int {
	return len(reql)
}

func (reql ReqSort) Swap(i int, j int) {
	reql[i], reql[j] = reql[j], reql[i]
}

func (reql ReqSort) Less(i int, j int) bool {
	return reql[j].StartDatetime.After(reql[i].StartDatetime)
}

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

/*
General storage functions
*/

func SaveNewRequest(ms MessageStorage, req *ProxyRequest) error {
	if req.ServerResponse != nil {
		if err := SaveNewResponse(ms, req.ServerResponse); err != nil {
			return fmt.Errorf("error saving server response to request: %s", err.Error())
		}
	}

	if req.Unmangled != nil {
		if req.DbId != "" && req.DbId == req.Unmangled.DbId {
			return errors.New("request has same DbId as unmangled version")
		}
		if err := SaveNewRequest(ms, req.Unmangled); err != nil {
			return fmt.Errorf("error saving unmangled version of request: %s", err.Error())
		}
	}

	if err := ms.SaveNewRequest(req); err != nil {
			return fmt.Errorf("error saving new request: %s", err.Error())
	}

	for _, wsm := range req.WSMessages {
		if err := SaveNewWSMessage(ms, req, wsm); err != nil {
			return fmt.Errorf("error saving request's ws message: %s", err.Error())
		}
	}

	return nil
}

func UpdateRequest(ms MessageStorage, req *ProxyRequest) error {
	if req.ServerResponse != nil {
		if err := UpdateResponse(ms, req.ServerResponse); err != nil {
			return fmt.Errorf("error saving server response to request: %s", err.Error())
		}
	}

	if req.Unmangled != nil {
		if req.DbId != "" && req.DbId == req.Unmangled.DbId {
			return errors.New("request has same DbId as unmangled version")
		}
		if err := UpdateRequest(ms, req.Unmangled); err != nil {
			return fmt.Errorf("error saving unmangled version of request: %s", err.Error())
		}
	}

	if req.DbId == "" {
		if err := ms.SaveNewRequest(req); err != nil {
			return fmt.Errorf("error saving new request: %s", err.Error())
		}
	} else {
		if err := ms.UpdateRequest(req); err != nil {
			return fmt.Errorf("error updating request: %s", err.Error())
		}
	}

	for _, wsm := range req.WSMessages {
		if err := UpdateWSMessage(ms, req, wsm); err != nil {
			return fmt.Errorf("error saving request's ws message: %s", err.Error())
		}
	}

	return nil
}

func SaveNewResponse(ms MessageStorage, rsp *ProxyResponse) error {
	if rsp.Unmangled != nil {
		if rsp.DbId != "" && rsp.DbId == rsp.Unmangled.DbId {
			return errors.New("response has same DbId as unmangled version")
		}
		if err := SaveNewResponse(ms, rsp.Unmangled); err != nil {
			return fmt.Errorf("error saving unmangled version of response: %s", err.Error())
		}
	}

	return ms.SaveNewResponse(rsp)
}

func UpdateResponse(ms MessageStorage, rsp *ProxyResponse) error {
	if rsp.Unmangled != nil {
		if rsp.DbId != "" && rsp.DbId == rsp.Unmangled.DbId {
			return errors.New("response has same DbId as unmangled version")
		}
		if err := UpdateResponse(ms, rsp.Unmangled); err != nil {
			return fmt.Errorf("error saving unmangled version of response: %s", err.Error())
		}
	}

	if rsp.DbId == "" {
		return ms.SaveNewResponse(rsp)
	} else {
		return ms.UpdateResponse(rsp)
	}
}

func SaveNewWSMessage(ms MessageStorage, req *ProxyRequest, wsm *ProxyWSMessage) error {
	if wsm.Unmangled != nil {
		if wsm.DbId != "" && wsm.DbId == wsm.Unmangled.DbId {
			return errors.New("websocket message has same DbId as unmangled version")
		}
		if err := SaveNewWSMessage(ms, nil, wsm.Unmangled); err != nil {
			return fmt.Errorf("error saving unmangled version of websocket message: %s", err.Error())
		}
	}

	return ms.SaveNewWSMessage(req, wsm)
}

func UpdateWSMessage(ms MessageStorage, req *ProxyRequest, wsm *ProxyWSMessage) error {
	if wsm.Unmangled != nil {
		if wsm.DbId != "" && wsm.Unmangled.DbId == wsm.DbId {
			return errors.New("websocket message has same DbId as unmangled version")
		}
		if err := UpdateWSMessage(ms, nil, wsm.Unmangled); err != nil {
			return fmt.Errorf("error saving unmangled version of websocket message: %s", err.Error())
		}
	}

	if wsm.DbId == "" {
		return ms.SaveNewWSMessage(req, wsm)
	} else {
		return ms.UpdateWSMessage(req, wsm)
	}
}

