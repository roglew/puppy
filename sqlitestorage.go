package puppy

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
)

var request_select string = "SELECT id, full_request, response_id, unmangled_id, port, is_ssl, host, start_datetime, end_datetime FROM requests"
var response_select string = "SELECT id, full_response, unmangled_id FROM responses"
var ws_select string = "SELECT id, parent_request, unmangled_id, is_binary, direction, time_sent, contents FROM websocket_messages"

var inmemIdCounter = IdCounter()

type SQLiteStorage struct {
	dbConn *sql.DB
	mtx    sync.Mutex
	logger *log.Logger
	storageWatchers []StorageWatcher
}

/*
SQLiteStorage Implementation
*/

func OpenSQLiteStorage(fname string, logger *log.Logger) (*SQLiteStorage, error) {
	db, err := sql.Open("sqlite3", fname)
	if err != nil {
		return nil, err
	}

	rs := &SQLiteStorage{
		dbConn: db,
	}

	err = UpdateSchema(rs.dbConn, logger)
	if err != nil {
		return nil, err
	}

	rs.logger = logger
	rs.storageWatchers = make([]StorageWatcher, 0)
	return rs, nil
}

func InMemoryStorage(logger *log.Logger) (*SQLiteStorage, error) {
	var toOpen = fmt.Sprintf("file:inmem%d:memory:?mode=memory&cache=shared", inmemIdCounter())
	return OpenSQLiteStorage(toOpen, logger)
}

func (rs *SQLiteStorage) Close() {
	rs.dbConn.Close()
}

func reqFromRow(
	tx *sql.Tx,
	ms *SQLiteStorage,
	db_id sql.NullInt64,
	db_full_request []byte,
	db_response_id sql.NullInt64,
	db_unmangled_id sql.NullInt64,
	db_port sql.NullInt64,
	db_is_ssl sql.NullBool,
	db_host sql.NullString,
	db_start_datetime sql.NullInt64,
	db_end_datetime sql.NullInt64,
) (*ProxyRequest, error) {
	var host string
	var port int
	var useTLS bool

	if !db_id.Valid {
		return nil, fmt.Errorf("id cannot be null")
	}
	reqDbId := strconv.FormatInt(db_id.Int64, 10)

	if db_host.Valid {
		host = db_host.String
	} else {
		host = ""
	}

	if db_port.Valid {
		// Yes we cast an in64 to an int, but a port shouldn't ever fall out of that range
		port = int(db_port.Int64)
	} else {
		// This shouldn't happen so just give it some random valid value
		port = 80
	}

	if db_is_ssl.Valid {
		useTLS = db_is_ssl.Bool
	} else {
		// This shouldn't happen so just give it some random valid value
		useTLS = false
	}

	req, err := ProxyRequestFromBytes(db_full_request, host, port, useTLS)
	if err != nil {
		return nil, fmt.Errorf("Unable to create request (id=%d) from data in database: %s", db_id.Int64, err.Error())
	}
	req.DbId = reqDbId

	if db_start_datetime.Valid {
		req.StartDatetime = time.Unix(0, db_start_datetime.Int64)
	} else {
		req.StartDatetime = time.Unix(0, 0)
	}

	if db_end_datetime.Valid {
		req.EndDatetime = time.Unix(0, db_end_datetime.Int64)
	} else {
		req.EndDatetime = time.Unix(0, 0)
	}

	if db_unmangled_id.Valid {
		unmangledReq, err := ms.loadRequest(tx, strconv.FormatInt(db_unmangled_id.Int64, 10))
		if err != nil {
			return nil, fmt.Errorf("Unable to load unmangled request for reqid=%s: %s", reqDbId, err.Error())
		}
		req.Unmangled = unmangledReq
	}

	if db_response_id.Valid {
		rsp, err := ms.loadResponse(tx, strconv.FormatInt(db_response_id.Int64, 10))
		if err != nil {
			return nil, fmt.Errorf("Unable to load response for reqid=%s: %s", reqDbId, err.Error())
		}
		req.ServerResponse = rsp
	}

	// Load websocket messages from reqid
	rows, err := tx.Query(`
    SELECT id, parent_request, unmangled_id, is_binary, direction, time_sent, contents
    FROM websocket_messages WHERE parent_request=?;
    `, reqDbId)
	if err != nil {
		return nil, fmt.Errorf("Unable to load websocket messages for reqid=%s: %s", reqDbId, err.Error())
	}
	defer rows.Close()

	messages := make([]*ProxyWSMessage, 0)
	for rows.Next() {
		var db_id sql.NullInt64
		var db_parent_request sql.NullInt64
		var db_unmangled_id sql.NullInt64
		var db_is_binary sql.NullBool
		var db_direction sql.NullInt64
		var db_time_sent sql.NullInt64
		var db_contents []byte

		err := rows.Scan(
			&db_id,
			&db_parent_request,
			&db_unmangled_id,
			&db_is_binary,
			&db_direction,
			&db_time_sent,
			&db_contents,
		)
		if err != nil {
			return nil, fmt.Errorf("Unable to load websocket messages for reqid=%s: %s", reqDbId, err.Error())
		}

		wsm, err := wsFromRow(tx, ms, db_id, db_parent_request, db_unmangled_id, db_is_binary, db_direction,
			db_time_sent, db_contents)
		if err != nil {
			return nil, fmt.Errorf("Unable to load websocket messages for reqid=%s: %s", reqDbId, err.Error())
		}
		messages = append(messages, wsm)
	}
	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("Unable to load websocket messages for reqid=%s: %s", reqDbId, err.Error())
	}
	sort.Sort(WSSort(messages))
	req.WSMessages = messages

	// Load tags
	rows, err = tx.Query(`
    SELECT tg.tag
    FROM tagged tgd, tags tg
    WHERE tgd.tagid=tg.id AND tgd.reqid=?;
    `, reqDbId)
	if err != nil {
		return nil, fmt.Errorf("Unable to load tags for reqid=%s: %s", reqDbId, err.Error())
	}
	defer rows.Close()
	for rows.Next() {
		var db_tag sql.NullString
		err := rows.Scan(&db_tag)
		if err != nil {
			return nil, fmt.Errorf("Unable to load tags for reqid=%s: %s", reqDbId, err.Error())
		}
		if !db_tag.Valid {
			return nil, fmt.Errorf("Unable to load tags for reqid=%s: nil tag", reqDbId)
		}
		req.AddTag(db_tag.String)
	}
	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("Unable to load tags for reqid=%s: %s", reqDbId, err.Error())
	}

	return req, nil
}

func rspFromRow(tx *sql.Tx, ms *SQLiteStorage, id sql.NullInt64, db_full_response []byte, db_unmangled_id sql.NullInt64) (*ProxyResponse, error) {
	if !id.Valid {
		return nil, fmt.Errorf("unable to load response: null id value")
	}
	rsp, err := ProxyResponseFromBytes(db_full_response)
	if err != nil {
		return nil, fmt.Errorf("unable to create response from data in datafile: %s", err.Error())
	}
	rsp.DbId = strconv.FormatInt(id.Int64, 10)

	if db_unmangled_id.Valid {
		unmangledRsp, err := ms.loadResponse(tx, strconv.FormatInt(db_unmangled_id.Int64, 10))
		if err != nil {
			return nil, fmt.Errorf("unable to load unmangled response for rspid=%d: %s", id.Int64, err.Error())
		}
		rsp.Unmangled = unmangledRsp
	}
	return rsp, nil
}

func wsFromRow(tx *sql.Tx, ms *SQLiteStorage, id sql.NullInt64, parent_request sql.NullInt64, unmangled_id sql.NullInt64,
	is_binary sql.NullBool, direction sql.NullInt64,
	time_sent sql.NullInt64, contents []byte) (*ProxyWSMessage, error) {

	if !is_binary.Valid || !direction.Valid {
		return nil, fmt.Errorf("invalid null field when loading ws message")
	}

	var mtype int
	if is_binary.Bool {
		mtype = websocket.BinaryMessage
	} else {
		mtype = websocket.TextMessage
	}

	wsm, err := NewProxyWSMessage(mtype, contents, int(direction.Int64))
	if err != nil {
		return nil, fmt.Errorf("Unable to create websocket message from data in datafile: %s", err.Error())
	}

	if !id.Valid {
		return nil, fmt.Errorf("ID cannot be null")
	}
	wsm.DbId = strconv.FormatInt(id.Int64, 10)

	if time_sent.Valid {
		wsm.Timestamp = time.Unix(0, time_sent.Int64)
	} else {
		wsm.Timestamp = time.Unix(0, 0)
	}

	if unmangled_id.Valid {
		unmangledWsm, err := ms.loadWSMessage(tx, strconv.FormatInt(unmangled_id.Int64, 10))
		if err != nil {
			return nil, fmt.Errorf("Unable to load unmangled websocket message for wsid=%d: %s", id.Int64, err.Error())
		}
		wsm.Unmangled = unmangledWsm
	}

	return wsm, nil
}

func addTagsToStorage(tx *sql.Tx, req *ProxyRequest) error {
	// Save the tags
	for _, tag := range req.Tags() {
		var db_tagid sql.NullInt64
		var db_tag sql.NullString
		var tagId int64

		err := tx.QueryRow(`
        SELECT id, tag FROM tags WHERE tag=?
        `, tag).Scan(&db_tagid, &db_tag)
		if err == nil {
			// It exists, get the ID
			if !db_tagid.Valid {
				return fmt.Errorf("error inserting tag into database: %s", err.Error())
			}
			tagId = db_tagid.Int64
		} else if err == sql.ErrNoRows {
			// It doesn't exist, add it to the database
			stmt, err := tx.Prepare(`
            INSERT INTO tags (tag) VALUES (?);
            `)
			if err != nil {
				return fmt.Errorf("error preparing statement to insert request into database: %s", err.Error())
			}
			defer stmt.Close()

			res, err := stmt.Exec(tag)
			if err != nil {
				return fmt.Errorf("error inserting tag into database: %s", err.Error())
			}

			tagId, _ = res.LastInsertId()
		} else if err != nil {
			// Something else happened
			return fmt.Errorf("error inserting tag into database: %s", err.Error())
		}

		stmt, err := tx.Prepare(`
        INSERT INTO tagged (reqid, tagid) VALUES (?, ?);
        `)
		if err != nil {
			return fmt.Errorf("error preparing statement to insert request into database: %s", err.Error())
		}
		defer stmt.Close()

		_, err = stmt.Exec(req.DbId, tagId)
		if err != nil {
			return fmt.Errorf("error inserting tag into database: %s", err.Error())
		}
	}
	return nil
}

func deleteTags(tx *sql.Tx, dbid string) error {
	stmt, err := tx.Prepare("DELETE FROM tagged WHERE reqid=?;")
	if err != nil {
		return err
	}
	defer stmt.Close()
	_, err = stmt.Exec(dbid)
	if err != nil {
		return err
	}
	return nil
}

func cleanTags(tx *sql.Tx) error {
	// Delete tags with no associated requests

	// not efficient if we have tons of tags, but whatever
	stmt, err := tx.Prepare(`
    DELETE FROM tags WHERE id NOT IN (SELECT tagid FROM tagged);
    `)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec()
	if err != nil {
		return err
	}

	return nil
}

func (ms *SQLiteStorage) SaveNewRequest(req *ProxyRequest) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return err
	}
	err = ms.saveNewRequest(tx, req)
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	for _, watcher := range ms.storageWatchers {
		watcher.NewRequestSaved(ms, req)
	}
	return nil
}

func (ms *SQLiteStorage) saveNewRequest(tx *sql.Tx, req *ProxyRequest) error {
	var rspid *string
	var unmangledId *string

	if req.ServerResponse != nil {
		if req.ServerResponse.DbId == "" {
			return errors.New("response has not been saved yet, cannot save request")
		}
		rspid = new(string)
		*rspid = req.ServerResponse.DbId
	} else {
		rspid = nil
	}

	if req.Unmangled != nil {
		if req.Unmangled.DbId == "" {
			return errors.New("unmangled request has not been saved yet, cannot save request")
		}
		unmangledId = new(string)
		*unmangledId = req.Unmangled.DbId
	} else {
		unmangledId = nil
	}

	stmt, err := tx.Prepare(`
    INSERT INTO requests (
            full_request,
            submitted,
            response_id,
            unmangled_id,
            port,
            is_ssl,
            host,
            plugin_data,
            start_datetime,
            end_datetime
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
    `)
	if err != nil {
		return fmt.Errorf("error preparing statement to insert request into database: %s", err.Error())
	}
	defer stmt.Close()

	res, err := stmt.Exec(
		req.FullMessage(), true, rspid, unmangledId, &req.DestPort, &req.DestUseTLS, &req.DestHost, "",
		req.StartDatetime.UnixNano(), req.EndDatetime.UnixNano(),
	)
	if err != nil {
		return fmt.Errorf("error inserting request into database: %s", err.Error())
	}

	var insertedId int64
	insertedId, _ = res.LastInsertId()
	req.DbId = strconv.FormatInt(insertedId, 10)

	addTagsToStorage(tx, req)

	return nil
}

func (ms *SQLiteStorage) UpdateRequest(req *ProxyRequest) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return err
	}
	err = ms.updateRequest(tx, req)
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	for _, watcher := range ms.storageWatchers {
		watcher.RequestUpdated(ms, req)
	}
	return nil
}

func (ms *SQLiteStorage) updateRequest(tx *sql.Tx, req *ProxyRequest) error {
	if req.DbId == "" {
		return fmt.Errorf("Request must be saved to datafile before it can be updated")
	}

	var rspid *string
	var unmangledId *string

	if req.ServerResponse != nil {
		if req.ServerResponse.DbId == "" {
			return errors.New("response has not been saved yet, cannot update request")
		}
		rspid = new(string)
		*rspid = req.ServerResponse.DbId
	} else {
		rspid = nil
	}

	if req.Unmangled != nil {
		if req.Unmangled.DbId == "" {
			return errors.New("unmangled request has not been saved yet, cannot update request")
		}
		unmangledId = new(string)
		*unmangledId = req.Unmangled.DbId
	} else {
		unmangledId = nil
	}

	stmt, err := tx.Prepare(`
    UPDATE requests SET 
            full_request=?,
            submitted=?,
            response_id=?,
            unmangled_id=?,
            port=?,
            is_ssl=?,
            host=?,
            plugin_data=?,
            start_datetime=?,
            end_datetime=?
    WHERE id=?;
    `)
	if err != nil {
		return fmt.Errorf("error preparing statement to update request with id=%d in datafile: %s", req.DbId, err.Error())
	}
	defer stmt.Close()

	_, err = stmt.Exec(
		req.FullMessage(), true, rspid, unmangledId, &req.DestPort, &req.DestUseTLS, &req.DestHost, "",
		req.StartDatetime.UnixNano(), req.EndDatetime.UnixNano(), req.DbId,
	)
	if err != nil {
		return fmt.Errorf("error inserting request into database: %s", err.Error())
	}

	// Save the tags
	err = deleteTags(tx, req.DbId)
	if err != nil {
		return err
	}

	err = addTagsToStorage(tx, req) // Add the tags back
	if err != nil {
		return err
	}
	err = cleanTags(tx) // Clean up tags
	if err != nil {
		return err
	}

	return nil
}

func (ms *SQLiteStorage) LoadRequest(reqid string) (*ProxyRequest, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return nil, err
	}
	req, err := ms.loadRequest(tx, reqid)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return req, nil
}

func (ms *SQLiteStorage) loadRequest(tx *sql.Tx, reqid string) (*ProxyRequest, error) {
	dbId, err := strconv.ParseInt(reqid, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("Invalid request id: %s", reqid)
	}

	var db_id sql.NullInt64
	var db_full_request []byte
	var db_response_id sql.NullInt64
	var db_unmangled_id sql.NullInt64
	var db_port sql.NullInt64
	var db_is_ssl sql.NullBool
	var db_host sql.NullString
	var db_start_datetime sql.NullInt64
	var db_end_datetime sql.NullInt64

	// err = tx.QueryRow(`
	//     SELECT
	//     id, full_request, response_id, unmangled_id, port, is_ssl, host, start_datetime, end_datetime
	//     FROM requests WHERE id=?`, dbId).Scan(
	err = tx.QueryRow(request_select+" WHERE id=?", dbId).Scan(
		&db_id,
		&db_full_request,
		&db_response_id,
		&db_unmangled_id,
		&db_port,
		&db_is_ssl,
		&db_host,
		&db_start_datetime,
		&db_end_datetime,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("Request with id %d does not exist", dbId)
	} else if err != nil {
		return nil, fmt.Errorf("Error loading data from datafile: %s", err.Error())
	}

	req, err := reqFromRow(tx, ms, db_id, db_full_request, db_response_id, db_unmangled_id,
		db_port, db_is_ssl, db_host, db_start_datetime, db_end_datetime)
	if err != nil {
		return nil, fmt.Errorf("Error loading data from datafile: %s", err.Error())
	}

	return req, nil
}

func (ms *SQLiteStorage) LoadUnmangledRequest(reqid string) (*ProxyRequest, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return nil, err
	}
	req, err := ms.loadUnmangledRequest(tx, reqid)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return req, nil
}

func (ms *SQLiteStorage) loadUnmangledRequest(tx *sql.Tx, reqid string) (*ProxyRequest, error) {
	dbId, err := strconv.ParseInt(reqid, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("Invalid request id: %s", reqid)
	}

	var db_unmangled_id sql.NullInt64

	err = tx.QueryRow("SELECT unmangled_id FROM requests WHERE id=?", dbId).Scan(&db_unmangled_id)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("request has no unmangled version")
	} else if err != nil {
		return nil, fmt.Errorf("error loading data from datafile: %s", err.Error())
	}

	if !db_unmangled_id.Valid {
		return nil, fmt.Errorf("request has no unmangled version")
	}

	return ms.loadRequest(tx, strconv.FormatInt(db_unmangled_id.Int64, 10))
}

func (ms *SQLiteStorage) DeleteRequest(reqid string) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return err
	}
	err = ms.deleteRequest(tx, reqid)
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	for _, watcher := range ms.storageWatchers {
		watcher.RequestDeleted(ms, reqid)
	}
	return nil
}

func (ms *SQLiteStorage) deleteRequest(tx *sql.Tx, reqid string) error {
	if reqid == "" {
		return nil
	}

	dbId, err := strconv.ParseInt(reqid, 10, 64)
	if err != nil {
		return fmt.Errorf("Invalid request id: %s", reqid)
	}

	// Get IDs
	var db_unmangled_id sql.NullInt64
	var db_response_id sql.NullInt64
	err = tx.QueryRow("SELECT unmangled_id, response_id FROM requests WHERE id=?", dbId).Scan(
		&db_unmangled_id,
		&db_response_id,
	)

	// Delete unmangled
	if db_unmangled_id.Valid {
		if err := ms.deleteRequest(tx, strconv.FormatInt(db_unmangled_id.Int64, 10)); err != nil {
			return err
		}
	}

	// Delete response
	if db_unmangled_id.Valid {
		if err := ms.deleteResponse(tx, strconv.FormatInt(db_response_id.Int64, 10)); err != nil {
			return err
		}
	}

	// Delete websockets
	stmt, err := tx.Prepare(`
    DELETE FROM websocket_messages WHERE parent_request=?;
    `)
	if err != nil {
		return fmt.Errorf("error preparing statement to delete websocket message: %s", err.Error())
	}
	defer stmt.Close()

	_, err = stmt.Exec(dbId)
	if err != nil {
		return fmt.Errorf("error deleting request from database: %s", err.Error())
	}

	// Delete the tags
	err = deleteTags(tx, strconv.FormatInt(dbId, 10))
	if err != nil {
		return fmt.Errorf("error deleting request from database: %s", err.Error())
	}

	// Delete the request
	stmt, err = tx.Prepare(`
    DELETE FROM requests WHERE id=?;
    `)
	if err != nil {
		return fmt.Errorf("error preparing statement to delete request with id=%d into database: %s", dbId, err.Error())
	}
	defer stmt.Close()

	_, err = stmt.Exec(dbId)
	if err != nil {
		return fmt.Errorf("error deleting request from database: %s", err.Error())
	}

	return nil
}

func (ms *SQLiteStorage) SaveNewResponse(rsp *ProxyResponse) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return err
	}
	err = ms.saveNewResponse(tx, rsp)
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	for _, watcher := range ms.storageWatchers {
		watcher.NewResponseSaved(ms, rsp)
	}
	return nil
}

func (ms *SQLiteStorage) saveNewResponse(tx *sql.Tx, rsp *ProxyResponse) error {
	var unmangledId *string

	if rsp.Unmangled != nil {
		if rsp.Unmangled.DbId == "" {
			return errors.New("unmangled response has not been saved yet, cannot save response")
		}
		unmangledId = new(string)
		*unmangledId = rsp.Unmangled.DbId
	} else {
		unmangledId = nil
	}

	stmt, err := tx.Prepare(`
    INSERT INTO responses (
            full_response,
            unmangled_id
    ) VALUES (?, ?);
    `)
	if err != nil {
		return fmt.Errorf("error preparing statement to insert response with id=%d into database: %s", rsp.DbId, err.Error())
	}
	defer stmt.Close()

	res, err := stmt.Exec(
		rsp.FullMessage(), unmangledId,
	)
	if err != nil {
		return fmt.Errorf("error inserting response into database: %s", err.Error())
	}

	var insertedId int64
	insertedId, _ = res.LastInsertId()
	rsp.DbId = strconv.FormatInt(insertedId, 10)
	return nil
}

func (ms *SQLiteStorage) UpdateResponse(rsp *ProxyResponse) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return err
	}
	err = ms.updateResponse(tx, rsp)
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	for _, watcher := range ms.storageWatchers {
		watcher.ResponseUpdated(ms, rsp)
	}
	return nil
}

func (ms *SQLiteStorage) updateResponse(tx *sql.Tx, rsp *ProxyResponse) error {
	if rsp.DbId == "" {
		return fmt.Errorf("Response must be saved to datafile before it can be updated")
	}

	var unmangledId *string

	if rsp.Unmangled != nil {
		if rsp.Unmangled.DbId == "" {
			return errors.New("unmangled response has not been saved yet, cannot update response")
		}
		unmangledId = new(string)
		*unmangledId = rsp.Unmangled.DbId
	} else {
		unmangledId = nil
	}

	stmt, err := tx.Prepare(`
    UPDATE responses SET 
            full_response=?,
            unmangled_id=?
    WHERE id=?;
    `)
	if err != nil {
		return fmt.Errorf("error preparing statement to update response with id=%d in datafile: %s", rsp.DbId, err.Error())
	}
	defer stmt.Close()

	_, err = stmt.Exec(
		rsp.FullMessage(), unmangledId, rsp.DbId,
	)
	if err != nil {
		return fmt.Errorf("error inserting response into database: %s", err.Error())
	}

	return nil
}

func (ms *SQLiteStorage) LoadResponse(rspid string) (*ProxyResponse, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return nil, err
	}
	rsp, err := ms.loadResponse(tx, rspid)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return rsp, nil
}

func (ms *SQLiteStorage) loadResponse(tx *sql.Tx, rspid string) (*ProxyResponse, error) {
	dbId, err := strconv.ParseInt(rspid, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("Invalid response id: %s", rspid)
	}

	var db_id sql.NullInt64
	var db_full_response []byte
	var db_unmangled_id sql.NullInt64

	err = tx.QueryRow(response_select+" WHERE id=?", dbId).Scan(
		&db_id,
		&db_full_response,
		&db_unmangled_id,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("Response with id %d does not exist", dbId)
	} else if err != nil {
		return nil, fmt.Errorf("Error loading data from datafile: %s", err.Error())
	}

	rsp, err := rspFromRow(tx, ms, db_id, db_full_response, db_unmangled_id)
	if err != nil {
		return nil, fmt.Errorf("Error loading data from datafile: %s", err.Error())
	}
	return rsp, nil
}

func (ms *SQLiteStorage) LoadUnmangledResponse(rspid string) (*ProxyResponse, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return nil, err
	}
	rsp, err := ms.loadUnmangledResponse(tx, rspid)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return rsp, nil
}

func (ms *SQLiteStorage) loadUnmangledResponse(tx *sql.Tx, rspid string) (*ProxyResponse, error) {
	dbId, err := strconv.ParseInt(rspid, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("Invalid response id: %s", rspid)
	}

	var db_unmangled_id sql.NullInt64

	err = tx.QueryRow("SELECT unmangled_id FROM responses WHERE id=?", dbId).Scan(&db_unmangled_id)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("response has no unmangled version")
	} else if err != nil {
		return nil, fmt.Errorf("error loading data from datafile: %s", err.Error())
	}

	if !db_unmangled_id.Valid {
		return nil, fmt.Errorf("response has no unmangled version")
	}

	return ms.loadResponse(tx, strconv.FormatInt(db_unmangled_id.Int64, 10))
}

func (ms *SQLiteStorage) DeleteResponse(rspid string) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return err
	}
	err = ms.deleteResponse(tx, rspid)
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	for _, watcher := range ms.storageWatchers {
		watcher.ResponseDeleted(ms, rspid)
	}
	return nil
}

func (ms *SQLiteStorage) deleteResponse(tx *sql.Tx, rspid string) error {
	if rspid == "" {
		return nil
	}

	dbId, err := strconv.ParseInt(rspid, 10, 64)
	if err != nil {
		return fmt.Errorf("Invalid respone id: %s", rspid)
	}

	// TODO: Use transactions to avoid partially deleting data

	// Get IDs
	var db_unmangled_id sql.NullInt64
	err = tx.QueryRow("SELECT unmangled_id FROM responses WHERE id=?", dbId).Scan(
		&db_unmangled_id,
	)

	// Delete unmangled
	if db_unmangled_id.Valid {
		if err := ms.deleteResponse(tx, strconv.FormatInt(db_unmangled_id.Int64, 10)); err != nil {
			return err
		}
	}

	// Delete the response
	stmt, err := tx.Prepare(`
    DELETE FROM responses WHERE id=?;
    `)
	if err != nil {
		return fmt.Errorf("error preparing statement to delete response with id=%d into database: %s", dbId, err.Error())
	}
	defer stmt.Close()

	_, err = stmt.Exec(dbId)
	if err != nil {
		return fmt.Errorf("error deleting response from database: %s", err.Error())
	}

	return nil
}

func (ms *SQLiteStorage) SaveNewWSMessage(req *ProxyRequest, wsm *ProxyWSMessage) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return err
	}
	err = ms.saveNewWSMessage(tx, req, wsm)
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	for _, watcher := range ms.storageWatchers {
		watcher.NewWSMessageSaved(ms, req, wsm)
	}
	return nil
}

func (ms *SQLiteStorage) saveNewWSMessage(tx *sql.Tx, req *ProxyRequest, wsm *ProxyWSMessage) error {
	if req != nil && req.DbId == "" {
		return fmt.Errorf("Associated request must have been saved already.")
	}

	var unmangledId *string

	if wsm.Unmangled != nil {
		unmangledId = new(string)
		*unmangledId = wsm.Unmangled.DbId
	} else {
		unmangledId = nil
	}

	stmt, err := tx.Prepare(`
    INSERT INTO websocket_messages (
            parent_request,
            unmangled_id,
            is_binary,
            direction,
            time_sent,
            contents
    ) VALUES (?, ?, ?, ?, ?, ?);
    `)
	if err != nil {
		return fmt.Errorf("error preparing statement to insert response with id=%d into database: %s", wsm.DbId, err.Error())
	}
	defer stmt.Close()

	var isBinary = false
	if wsm.Type == websocket.BinaryMessage {
		isBinary = true
	}

	var parent_id int64 = 0
	if req != nil {
		parent_id, err = strconv.ParseInt(req.DbId, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid reqid: %s", req.DbId)
		}
	}

	res, err := stmt.Exec(
		parent_id,
		unmangledId,
		isBinary,
		wsm.Direction,
		wsm.Timestamp.UnixNano(),
		wsm.Message,
	)
	if err != nil {
		return fmt.Errorf("error inserting websocket message into database: %s", err.Error())
	}

	var insertedId int64
	insertedId, _ = res.LastInsertId()
	wsm.DbId = strconv.FormatInt(insertedId, 10)
	return nil

}

func (ms *SQLiteStorage) UpdateWSMessage(req *ProxyRequest, wsm *ProxyWSMessage) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return err
	}
	err = ms.updateWSMessage(tx, req, wsm)
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	for _, watcher := range ms.storageWatchers {
		watcher.WSMessageUpdated(ms, req, wsm)
	}
	return nil
}

func (ms *SQLiteStorage) updateWSMessage(tx *sql.Tx, req *ProxyRequest, wsm *ProxyWSMessage) error {
	if req != nil && req.DbId == "" {
		return fmt.Errorf("associated request must have been saved already.")
	}

	if wsm.DbId == "" {
		return fmt.Errorf("websocket message must be saved to datafile before it can be updated")
	}

	var unmangledId *string

	if wsm.Unmangled != nil {
		unmangledId = new(string)
		*unmangledId = wsm.Unmangled.DbId
	} else {
		unmangledId = nil
	}

	stmt, err := tx.Prepare(`
    UPDATE websocket_messages SET 
            parent_request=?,
            unmangled_id=?,
            is_binary=?,
            direction=?,
            time_sent=?,
            contents=?
    WHERE id=?;
    `)
	if err != nil {
		return fmt.Errorf("error preparing statement to update response with id=%d in datafile: %s", wsm.DbId, err.Error())
	}
	defer stmt.Close()

	isBinary := false
	if wsm.Type == websocket.BinaryMessage {
		isBinary = true
	}

	var parent_id int64 = 0
	if req != nil {
		parent_id, err = strconv.ParseInt(req.DbId, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid reqid: %s", req.DbId)
		}
	}

	_, err = stmt.Exec(
		parent_id,
		unmangledId,
		isBinary,
		wsm.Direction,
		wsm.Timestamp.UnixNano(),
		wsm.Message,
		wsm.DbId,
	)
	if err != nil {
		return fmt.Errorf("error inserting response into database: %s", err.Error())
	}

	return nil
}

func (ms *SQLiteStorage) LoadWSMessage(wsmid string) (*ProxyWSMessage, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return nil, err
	}
	wsm, err := ms.loadWSMessage(tx, wsmid)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return wsm, nil
}

func (ms *SQLiteStorage) loadWSMessage(tx *sql.Tx, wsmid string) (*ProxyWSMessage, error) {
	dbId, err := strconv.ParseInt(wsmid, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("Invalid wsmid: %s", wsmid)
	}

	var db_id sql.NullInt64
	var db_parent_request sql.NullInt64
	var db_unmangled_id sql.NullInt64
	var db_is_binary sql.NullBool
	var db_direction sql.NullInt64
	var db_time_sent sql.NullInt64
	var db_contents []byte

	err = tx.QueryRow(ws_select+" WHERE id=?", dbId).Scan(
		&db_id,
		&db_parent_request,
		&db_unmangled_id,
		&db_is_binary,
		&db_direction,
		&db_time_sent,
		&db_contents,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("Message with id %d does not exist", dbId)
	} else if err != nil {
		return nil, fmt.Errorf("Error loading data from datafile: %s", err.Error())
	}

	wsm, err := wsFromRow(tx, ms, db_id, db_parent_request, db_unmangled_id, db_is_binary, db_direction,
		db_time_sent, db_contents)

	if db_unmangled_id.Valid {
		unmangledWsm, err := ms.loadWSMessage(tx, strconv.FormatInt(db_unmangled_id.Int64, 10))
		if err != nil {
			return nil, fmt.Errorf("Unable to load unmangled ws for wsmid=%s: %s", wsmid, err.Error())
		}
		wsm.Unmangled = unmangledWsm
	}

	if err != nil {
		return nil, fmt.Errorf("Error loading data from datafile: %s", err.Error())
	}
	return wsm, nil
}

func (ms *SQLiteStorage) LoadUnmangledWSMessage(wsmid string) (*ProxyWSMessage, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return nil, err
	}
	wsm, err := ms.loadUnmangledWSMessage(tx, wsmid)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return wsm, nil
}

func (ms *SQLiteStorage) loadUnmangledWSMessage(tx *sql.Tx, wsmid string) (*ProxyWSMessage, error) {
	dbId, err := strconv.ParseInt(wsmid, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("Invalid  id: %s", wsmid)
	}

	var db_unmangled_id sql.NullInt64

	err = tx.QueryRow("SELECT unmangled_id FROM requests WHERE id=?", dbId).Scan(&db_unmangled_id)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("message has no unmangled version")
	} else if err != nil {
		return nil, fmt.Errorf("error loading data from datafile: %s", err.Error())
	}

	if !db_unmangled_id.Valid {
		return nil, fmt.Errorf("request has no unmangled version")
	}

	return ms.loadWSMessage(tx, strconv.FormatInt(db_unmangled_id.Int64, 10))
}

func (ms *SQLiteStorage) DeleteWSMessage(wsmid string) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return err
	}
	err = ms.deleteWSMessage(tx, wsmid)
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	for _, watcher := range ms.storageWatchers {
		watcher.WSMessageDeleted(ms, wsmid)
	}
	return nil
}

func (ms *SQLiteStorage) deleteWSMessage(tx *sql.Tx, wsmid string) error {
	if wsmid == "" {
		return nil
	}

	dbId, err := strconv.ParseInt(wsmid, 10, 64)
	if err != nil {
		return fmt.Errorf("Invalid websocket id: %s", wsmid)
	}

	// TODO: Use transactions to avoid partially deleting data

	// Get IDs
	var db_unmangled_id sql.NullInt64
	err = tx.QueryRow("SELECT unmangled_id FROM websocket_messages WHERE id=?", dbId).Scan(
		&db_unmangled_id,
	)

	// Delete unmangled
	if db_unmangled_id.Valid {
		if err := ms.deleteWSMessage(tx, strconv.FormatInt(db_unmangled_id.Int64, 10)); err != nil {
			return err
		}
	}

	// Delete the response
	stmt, err := tx.Prepare(`
    DELETE FROM websocket_messages WHERE id=?;
    `)
	if err != nil {
		return fmt.Errorf("error preparing statement to delete websocket message with id=%d into database: %s", dbId, err.Error())
	}
	defer stmt.Close()

	_, err = stmt.Exec(dbId)
	if err != nil {
		return fmt.Errorf("error deleting websocket message from database: %s", err.Error())
	}

	return nil
}

func (ms *SQLiteStorage) RequestKeys() ([]string, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return nil, err
	}
	strs, err := ms.requestKeys(tx)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return strs, nil
}

func (ms *SQLiteStorage) requestKeys(tx *sql.Tx) ([]string, error) {

	rows, err := tx.Query("SELECT id FROM requests;")
	if err != nil {
		return nil, fmt.Errorf("could not get keys from datafile: %s", err.Error())
	}
	defer rows.Close()

	var db_id sql.NullInt64
	keys := make([]string, 0)
	for rows.Next() {
		err := rows.Scan(&db_id)
		if err != nil {
			return nil, fmt.Errorf("could not get keys from datafile: %s", err.Error())
		}
		if db_id.Valid {
			keys = append(keys, strconv.FormatInt(db_id.Int64, 10))
		}
	}
	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("could not get keys from datafile: %s", err.Error())
	}
	return keys, nil
}

func (ms *SQLiteStorage) reqSearchHelper(tx *sql.Tx, limit int64, checker RequestChecker, sqlTail string) ([]*ProxyRequest, error) {
	rows, err := tx.Query(request_select + sqlTail + " ORDER BY start_datetime DESC;")
	if err != nil {
		return nil, errors.New("error with sql query: " + err.Error())
	}
	defer rows.Close()

	var db_id sql.NullInt64
	var db_full_request []byte
	var db_response_id sql.NullInt64
	var db_unmangled_id sql.NullInt64
	var db_port sql.NullInt64
	var db_is_ssl sql.NullBool
	var db_host sql.NullString
	var db_start_datetime sql.NullInt64
	var db_end_datetime sql.NullInt64

	results := make([]*ProxyRequest, 0)
	for rows.Next() {
		err := rows.Scan(
			&db_id,
			&db_full_request,
			&db_response_id,
			&db_unmangled_id,
			&db_port,
			&db_is_ssl,
			&db_host,
			&db_start_datetime,
			&db_end_datetime,
		)
		if err != nil {
			return nil, errors.New("error loading row from database: " + err.Error())
		}
		req, err := reqFromRow(tx, ms, db_id, db_full_request, db_response_id, db_unmangled_id,
			db_port, db_is_ssl, db_host, db_start_datetime, db_end_datetime)
		if err != nil {
			return nil, errors.New("error creating request: " + err.Error())
		}

		if checker(req) {
			results = append(results, req)
			if limit > 0 && int64(len(results)) >= limit {
				break
			}
		}
	}
	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("error loading requests: " + err.Error())
	}
	return results, nil
}

func (ms *SQLiteStorage) Search(limit int64, args ...interface{}) ([]*ProxyRequest, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return nil, err
	}
	reqs, err := ms.search(tx, limit, args...)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return reqs, nil
}

func (ms *SQLiteStorage) search(tx *sql.Tx, limit int64, args ...interface{}) ([]*ProxyRequest, error) {
	tail := ""

	// Check for `id is`
	if len(args) == 3 {
		field, ok := args[0].(SearchField)
		if ok && field == FieldId {
			comparer, ok := args[1].(StrComparer)
			if ok && comparer == StrIs {
				reqid, ok := args[2].(string)
				if ok {
					req, err := ms.loadRequest(tx, reqid)
					if err != nil {
						return nil, err
					}
					ret := make([]*ProxyRequest, 1)
					ret[0] = req
					return ret, nil
				}
			}
		}
	}

	// Can't optimize, just make a checker and do a naive implementation
	checker, err := NewRequestChecker(args...)
	if err != nil {
		return nil, err
	}
	return ms.reqSearchHelper(tx, limit, checker, tail)
}

func (ms *SQLiteStorage) CheckRequests(limit int64, checker RequestChecker) ([]*ProxyRequest, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return nil, err
	}
	reqs, err := ms.checkRequests(tx, limit, checker)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return reqs, err
}

func (ms *SQLiteStorage) checkRequests(tx *sql.Tx, limit int64, checker RequestChecker) ([]*ProxyRequest, error) {
	return ms.reqSearchHelper(tx, limit, checker, "")
}

func (ms *SQLiteStorage) SaveQuery(name string, query MessageQuery) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return err
	}
	err = ms.saveQuery(tx, name, query)
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	return nil
}

func (ms *SQLiteStorage) saveQuery(tx *sql.Tx, name string, query MessageQuery) error {
	strQuery, err := MsgQueryToStrQuery(query)
	if err != nil {
		return fmt.Errorf("error creating string version of query: %s", err.Error())
	}

	jsonQuery, err := json.Marshal(strQuery)
	if err != nil {
		return fmt.Errorf("error marshaling query to json: %s", err.Error())
	}

	if err := ms.deleteQuery(tx, name); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`
    INSERT INTO saved_contexts (
            context_name,
            filter_strings
    ) VALUES (?, ?);
    `)
	if err != nil {
		return fmt.Errorf("error preparing statement to insert request into database: %s", err.Error())
	}
	defer stmt.Close()

	_, err = stmt.Exec(name, jsonQuery)
	if err != nil {
		return fmt.Errorf("error inserting request into database: %s", err.Error())
	}

	return nil
}

func (ms *SQLiteStorage) LoadQuery(name string) (MessageQuery, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return nil, err
	}
	query, err := ms.loadQuery(tx, name)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return query, nil
}

func (ms *SQLiteStorage) loadQuery(tx *sql.Tx, name string) (MessageQuery, error) {
	var queryStr sql.NullString
	err := tx.QueryRow(`SELECT filter_strings FROM saved_contexts WHERE context_name=?`, name).Scan(
		&queryStr,
	)
	if err == sql.ErrNoRows || !queryStr.Valid {
		return nil, fmt.Errorf("context with name %s does not exist", name)
	} else if err != nil {
		return nil, fmt.Errorf("error loading data from datafile: %s", err.Error())
	}

	var strRetQuery StrMessageQuery
	err = json.Unmarshal([]byte(queryStr.String), &strRetQuery)
	if err != nil {
		return nil, err
	}

	retQuery, err := StrQueryToMsgQuery(strRetQuery)
	if err != nil {
		return nil, err
	}

	return retQuery, nil
}

func (ms *SQLiteStorage) DeleteQuery(name string) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return err
	}
	err = ms.deleteQuery(tx, name)
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	return nil
}

func (ms *SQLiteStorage) deleteQuery(tx *sql.Tx, name string) error {
	stmt, err := tx.Prepare(`DELETE FROM saved_contexts WHERE context_name=?;`)
	if err != nil {
		return fmt.Errorf("error preparing statement to insert request into database: %s", err.Error())
	}
	defer stmt.Close()

	_, err = stmt.Exec(name)
	if err != nil {
		return fmt.Errorf("error deleting query: %s", err.Error())
	}

	return nil
}

func (ms *SQLiteStorage) AllSavedQueries() ([]*SavedQuery, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return nil, err
	}
	queries, err := ms.allSavedQueries(tx)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return queries, nil
}

func (ms *SQLiteStorage) allSavedQueries(tx *sql.Tx) ([]*SavedQuery, error) {
	rows, err := tx.Query("SELECT context_name, filter_strings FROM saved_contexts;")
	if err != nil {
		return nil, fmt.Errorf("could not get context names from datafile: %s", err.Error())
	}
	defer rows.Close()

	var name sql.NullString
	var queryStr sql.NullString
	savedQueries := make([]*SavedQuery, 0)
	for rows.Next() {
		err := rows.Scan(&name, &queryStr)
		if err != nil {
			return nil, fmt.Errorf("could not get context names from datafile: %s", err.Error())
		}
		if name.Valid && queryStr.Valid {
			var strQuery StrMessageQuery
			err = json.Unmarshal([]byte(queryStr.String), &strQuery)

			goQuery, err := StrQueryToMsgQuery(strQuery)
			if err != nil {
				return nil, err
			}
			savedQueries = append(savedQueries, &SavedQuery{Name: name.String, Query: goQuery})
		}
	}
	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("could not get context names from datafile: %s", err.Error())
	}
	return savedQueries, nil
}

func (ms *SQLiteStorage) Watch(watcher StorageWatcher) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	ms.storageWatchers = append(ms.storageWatchers, watcher)
	return nil
}

func (ms *SQLiteStorage) EndWatch(watcher StorageWatcher) error {
	ms.mtx.Lock()
	var newWatched = make([]StorageWatcher, 0)
	for _, testWatcher := range ms.storageWatchers {
		if (testWatcher != watcher) {
			newWatched = append(newWatched, testWatcher)
		}
	}
	ms.storageWatchers = newWatched
	ms.mtx.Unlock()
	return nil
}

func (ms *SQLiteStorage) SetPluginValue(key string, value string) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return err
	}
	err = ms.setPluginValue(tx, key, value)
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	return nil
}

func (ms *SQLiteStorage) setPluginValue(tx *sql.Tx, key string, value string) error {
	stmt, err := tx.Prepare(`
    INSERT OR REPLACE INTO plugin_data (
            key,
            value
    ) VALUES (?, ?);
    `)
	if err != nil {
		return fmt.Errorf("error preparing statement to insert request into database: %s", err.Error())
	}
	defer stmt.Close()

	_, err = stmt.Exec(key, value)
	if err != nil {
		return fmt.Errorf("error inserting plugin data into database: %s", err.Error())
	}

	return nil
}

func (ms *SQLiteStorage) GetPluginValue(key string) (string, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	tx, err := ms.dbConn.Begin()
	if err != nil {
		return "", err
	}
	value, err := ms.getPluginValue(tx, key)
	if err != nil {
		tx.Rollback()
		return "", err
	}
	tx.Commit()
	return value, nil
}

func (ms *SQLiteStorage) getPluginValue(tx *sql.Tx, key string) (string, error) {
	var value sql.NullString
	err := tx.QueryRow(`SELECT value FROM plugin_data WHERE key=?`, key).Scan(
		&value,
	)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("plugin data with key %s does not exist", key)
	} else if err != nil {
		return "", fmt.Errorf("error loading data from datafile: %s", err.Error())
	}
	return value.String, nil
}
