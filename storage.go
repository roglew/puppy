package puppy

import (
	"errors"
	"fmt"
)

// An interface that represents something that can be used to store data from the proxy
type MessageStorage interface {

	// Close the storage
	Close()

	// Update an existing request in the storage. Requires that it has already been saved
	UpdateRequest(req *ProxyRequest) error
	// Save a new instance of the request in the storage regardless of if it has already been saved
	SaveNewRequest(req *ProxyRequest) error
	// Load a request given a unique id
	LoadRequest(reqid string) (*ProxyRequest, error)
    // Load the unmangled version of a request given a unique id
	LoadUnmangledRequest(reqid string) (*ProxyRequest, error)
	// Delete a request given a unique id
	DeleteRequest(reqid string) error

	// Update an existing response in the storage. Requires that it has already been saved
	UpdateResponse(rsp *ProxyResponse) error
	// Save a new instance of the response in the storage regardless of if it has already been saved
	SaveNewResponse(rsp *ProxyResponse) error
	// Load a response given a unique id
	LoadResponse(rspid string) (*ProxyResponse, error)
    // Load the unmangled version of a response given a unique id
	LoadUnmangledResponse(rspid string) (*ProxyResponse, error)
	// Delete a response given a unique id
	DeleteResponse(rspid string) error

	// Update an existing websocket message in the storage. Requires that it has already been saved
	UpdateWSMessage(req *ProxyRequest, wsm *ProxyWSMessage) error
	// Save a new instance of the websocket message in the storage regardless of if it has already been saved
	SaveNewWSMessage(req *ProxyRequest, wsm *ProxyWSMessage) error
	// Load a websocket message given a unique id
	LoadWSMessage(wsmid string) (*ProxyWSMessage, error)
    // Load the unmangled version of a websocket message given a unique id
	LoadUnmangledWSMessage(wsmid string) (*ProxyWSMessage, error)
	// Delete a websocket message given a unique id
	DeleteWSMessage(wsmid string) error

	// Get list of the keys for all of the stored requests
	RequestKeys() ([]string, error)

	// A function to perform a search of requests in the storage. Same arguments as NewRequestChecker
	Search(limit int64, args ...interface{}) ([]*ProxyRequest, error)

	// A function to naively check every function in storage with the given function and return the ones that match
	CheckRequests(limit int64, checker RequestChecker) ([]*ProxyRequest, error)

	// Return a list of all the queries stored in the MessageStorage
	AllSavedQueries() ([]*SavedQuery, error)
    // Save a query in the storage with a given name. If the name is already in storage, it should be overwritten
	SaveQuery(name string, query MessageQuery) error
    // Load a query by name from the storage
	LoadQuery(name string) (MessageQuery, error)
    // Delete a query by name from the storage
	DeleteQuery(name string) error

	// Add a storage watcher to make callbacks to on message saves
	Watch(watcher StorageWatcher) error
	// Remove a storage watcher from the storage
	EndWatch(watcher StorageWatcher) error
}

type StorageWatcher interface {
	// Callback for when a new request is saved
	NewRequestSaved(ms MessageStorage, req *ProxyRequest)
	// Callback for when a request is updated
	RequestUpdated(ms MessageStorage, req *ProxyRequest)
	// Callback for when a request is deleted
	RequestDeleted(ms MessageStorage, DbId string)

	// Callback for when a new response is saved
	NewResponseSaved(ms MessageStorage, rsp *ProxyResponse)
	// Callback for when a response is updated
	ResponseUpdated(ms MessageStorage, rsp *ProxyResponse)
	// Callback for when a response is deleted
	ResponseDeleted(ms MessageStorage, DbId string)

	// Callback for when a new wsmessage is saved
	NewWSMessageSaved(ms MessageStorage, req *ProxyRequest, wsm *ProxyWSMessage)
	// Callback for when a wsmessage is updated
	WSMessageUpdated(ms MessageStorage, req *ProxyRequest, wsm *ProxyWSMessage)
	// Callback for when a wsmessage is deleted
	WSMessageDeleted(ms MessageStorage, DbId string)
}

// An error to be returned if a query is not supported
const QueryNotSupported = ConstErr("custom query not supported")

// A type representing a search query that is stored in a MessageStorage
type SavedQuery struct {
	Name  string
	Query MessageQuery
}

/*
General storage functions
*/

// Save a new request and new versions of all its dependant messages (response, websocket messages, and unmangled versions of everything).
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

// Update a request and all its dependent messages. If the request has a DbId it will be updated, otherwise it will be inserted into the database and have its DbId updated. Same for all dependent messages
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

// Save a new response/unmangled response to the message storage regardless of the existence of a DbId
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

// Update a response and its unmangled version in the database. If it has a DbId, it will be updated, otherwise a new version will be saved in the database
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

// Save a new websocket emssage/unmangled version to the message storage regardless of the existence of a DbId
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

// Update a websocket message and its unmangled version in the database. If it has a DbId, it will be updated, otherwise a new version will be saved in the database
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
