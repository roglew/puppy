package puppy

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"sort"
	"strings"
)

type schemaUpdater func(tx *sql.Tx) error

type tableNameRow struct {
	name string
}

var schemaUpdaters = []schemaUpdater{
	schema8,
	schema9,
}

func UpdateSchema(db *sql.DB, logger *log.Logger) error {
	currSchemaVersion := 0
	var tableName string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='schema_meta';").Scan(&tableName); err == sql.ErrNoRows {
		logger.Println("No datafile schema, initializing schema")
		currSchemaVersion = -1
	} else if err != nil {
		return err
	} else {
		svr := new(int)
		if err := db.QueryRow("SELECT version FROM schema_meta;").Scan(svr); err != nil {
			return err
		}
		currSchemaVersion = *svr
		if currSchemaVersion-7 < len(schemaUpdaters) {
			logger.Println("Schema out of date. Updating...")
		}
	}

	if currSchemaVersion >= 0 && currSchemaVersion < 8 {
		return fmt.Errorf("This is a PappyProxy datafile that is not the most recent schema version supported by PappyProxy. Load this datafile using the most recent version of Pappy to upgrade the schema and try importing it again.")
	}

	var updaterInd = 0
	if currSchemaVersion > 0 {
		updaterInd = currSchemaVersion - 7
	}

	if currSchemaVersion-7 < len(schemaUpdaters) {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for i := updaterInd; i < len(schemaUpdaters); i++ {
			logger.Printf("Updating schema to version %d...", i+8)
			err := schemaUpdaters[i](tx)
			if err != nil {
				logger.Println("Error updating schema:", err)
				logger.Println("Rolling back")
				tx.Rollback()
				return err
			}
		}
		logger.Printf("Schema update successful")
		tx.Commit()
	}
	return nil
}

func execute(tx *sql.Tx, cmd string) error {
	err := executeNoDebug(tx, cmd)
	if err != nil {
		_, f, ln, _ := runtime.Caller(1)
		return fmt.Errorf("sql error at %s:%d: %s", f, ln, err.Error())
	}
	return nil
}

func executeNoDebug(tx *sql.Tx, cmd string) error {
	stmt, err := tx.Prepare(cmd)
	defer stmt.Close()
	if err != nil {
		return err
	}

	if _, err := tx.Stmt(stmt).Exec(); err != nil {
		return err
	}
	return nil
}

func executeMultiple(tx *sql.Tx, cmds []string) error {
	for _, cmd := range cmds {
		err := executeNoDebug(tx, cmd)
		if err != nil {
			_, f, ln, _ := runtime.Caller(1)
			return fmt.Errorf("sql error at %s:%d: %s", f, ln, err.Error())
		}
	}
	return nil
}

/*
SCHEMA 8 / INITIAL
*/

func schema8(tx *sql.Tx) error {
	// Create a schema that is the same as pappy's last version

	cmds := []string{

		`
    CREATE TABLE schema_meta (
            version INTEGER NOT NULL
        );
    `,

		`
    INSERT INTO "schema_meta" VALUES(8);
    `,

		`
    CREATE TABLE responses (
        id             INTEGER   PRIMARY KEY  AUTOINCREMENT,
        full_response  BLOB  NOT NULL,
        unmangled_id   INTEGER   REFERENCES responses(id)
    );
    `,

		`
    CREATE TABLE scope (
            filter_order    INTEGER  NOT NULL,
            filter_string   TEXT  NOT NULL
        );
    `,

		`
    CREATE TABLE tags (
            id       INTEGER   PRIMARY KEY  AUTOINCREMENT,
            tag      TEXT      NOT NULL
        );
    `,

		`
    CREATE TABLE tagged (
            reqid       INTEGER,
            tagid       INTEGER
        );
    `,

		`
    CREATE TABLE "requests" (
            id                INTEGER   PRIMARY KEY  AUTOINCREMENT,
            full_request      BLOB      NOT NULL,
            submitted         INTEGER   NOT NULL,
            response_id       INTEGER   REFERENCES responses(id),
            unmangled_id      INTEGER   REFERENCES requests(id),
            port              INTEGER,
            is_ssl            INTEGER,
            host              TEXT,
            plugin_data       TEXT,
            start_datetime    REAL,
            end_datetime      REAL
        );
    `,

		`
    CREATE TABLE saved_contexts (
            id                INTEGER   PRIMARY KEY  AUTOINCREMENT,
            context_name      TEXT      UNIQUE,
            filter_strings    TEXT
        );
    `,

		`
    CREATE TABLE websocket_messages (
            id                INTEGER   PRIMARY KEY  AUTOINCREMENT,
            parent_request    INTEGER   REFERENCES requests(id),
            unmangled_id      INTEGER   REFERENCES websocket_messages(id),
            is_binary         INTEGER,
            direction         INTEGER,
            time_sent         REAL,
            contents          BLOB
        );
    `,

		`
    CREATE INDEX ind_start_time ON requests(start_datetime);
    `,
	}

	err := executeMultiple(tx, cmds)
	if err != nil {
		return err
	}

	return nil
}

/*
SCHEMA 9
*/

func pappyFilterToStrArgList(f string) ([]string, error) {
	parts := strings.Split(f, " ")

	// Validate the arguments
	goArgs, err := CheckArgsStrToGo(parts)
	if err != nil {
		return nil, fmt.Errorf("error converting filter string \"%s\": %s", f, err)
	}

	strArgs, err := CheckArgsGoToStr(goArgs)
	if err != nil {
		return nil, fmt.Errorf("error converting filter string \"%s\": %s", f, err)
	}

	return strArgs, nil
}

func pappyListToStrMessageQuery(f []string) (StrMessageQuery, error) {
	retFilter := make(StrMessageQuery, len(f))

	for i, s := range f {
		strArgs, err := pappyFilterToStrArgList(s)
		if err != nil {
			return nil, err
		}

		newPhrase := make(StrQueryPhrase, 1)
		newPhrase[0] = strArgs

		retFilter[i] = newPhrase
	}

	return retFilter, nil
}

type s9ScopeStr struct {
	Order  int64
	Filter string
}

type s9ScopeSort []*s9ScopeStr

func (ls s9ScopeSort) Len() int {
	return len(ls)
}

func (ls s9ScopeSort) Swap(i int, j int) {
	ls[i], ls[j] = ls[j], ls[i]
}

func (ls s9ScopeSort) Less(i int, j int) bool {
	return ls[i].Order < ls[j].Order
}

func schema9(tx *sql.Tx) error {
	/*
	   Converts the floating point timestamps into integers representing nanoseconds from jan 1 1970
	*/

	// Rename the old requests table
	if err := execute(tx, "ALTER TABLE requests RENAME TO requests_old"); err != nil {
		return err
	}

	if err := execute(tx, "ALTER TABLE websocket_messages RENAME TO websocket_messages_old"); err != nil {
		return err
	}

	// Create new requests table with integer datetime
	cmds := []string{`
    CREATE TABLE "requests" (
            id                INTEGER   PRIMARY KEY  AUTOINCREMENT,
            full_request      BLOB      NOT NULL,
            submitted         INTEGER   NOT NULL,
            response_id       INTEGER   REFERENCES responses(id),
            unmangled_id      INTEGER   REFERENCES requests(id),
            port              INTEGER,
            is_ssl            INTEGER,
            host              TEXT,
            plugin_data       TEXT,
            start_datetime    INTEGER,
            end_datetime      INTEGER
        );
    `,

		`
    INSERT INTO requests
    SELECT id, full_request, submitted, response_id, unmangled_id, port, is_ssl, host, plugin_data, 0, 0
    FROM requests_old
    `,

		`
    CREATE TABLE websocket_messages (
            id                INTEGER   PRIMARY KEY  AUTOINCREMENT,
            parent_request    INTEGER   REFERENCES requests(id),
            unmangled_id      INTEGER   REFERENCES websocket_messages(id),
            is_binary         INTEGER,
            direction         INTEGER,
            time_sent         INTEGER,
            contents          BLOB
        );
    `,

		`
    INSERT INTO websocket_messages
    SELECT id, parent_request, unmangled_id, is_binary, direction, 0, contents
    FROM websocket_messages_old
    `,
	}
	if err := executeMultiple(tx, cmds); err != nil {
		return err
	}

	// Update time values to use unix time nanoseconds
	rows, err := tx.Query("SELECT id, start_datetime, end_datetime FROM requests_old;")
	if err != nil {
		return err
	}
	defer rows.Close()

	var reqid int64
	var startDT sql.NullFloat64
	var endDT sql.NullFloat64
	var newStartDT int64
	var newEndDT int64

	for rows.Next() {
		if err := rows.Scan(&reqid, &startDT, &endDT); err != nil {
			return err
		}

		if startDT.Valid {
			// Convert to nanoseconds
			newStartDT = int64(startDT.Float64 * 1000000000)
		} else {
			newStartDT = 0
		}

		if endDT.Valid {
			newEndDT = int64(endDT.Float64 * 1000000000)
		} else {
			newEndDT = 0
		}

		// Save the new value
		stmt, err := tx.Prepare("UPDATE requests SET start_datetime=?, end_datetime=? WHERE id=?")
		if err != nil {
			return err
		}
		defer stmt.Close()

		if _, err := tx.Stmt(stmt).Exec(newStartDT, newEndDT, reqid); err != nil {
			return err
		}
	}

	// Update websocket time values to use unix time nanoseconds
	rows, err = tx.Query("SELECT id, time_sent FROM websocket_messages_old;")
	if err != nil {
		return err
	}
	defer rows.Close()

	var wsid int64
	var sentDT sql.NullFloat64
	var newSentDT int64

	for rows.Next() {
		if err := rows.Scan(&wsid, &sentDT); err != nil {
			return err
		}

		if sentDT.Valid {
			// Convert to nanoseconds
			newSentDT = int64(startDT.Float64 * 1000000000)
		} else {
			newSentDT = 0
		}

		// Save the new value
		stmt, err := tx.Prepare("UPDATE websocket_messages SET time_sent=? WHERE id=?")
		if err != nil {
			return err
		}
		defer stmt.Close()

		if _, err := tx.Stmt(stmt).Exec(newSentDT, reqid); err != nil {
			return err
		}
	}
	err = rows.Err()
	if err != nil {
		return err
	}

	if err := execute(tx, "DROP TABLE requests_old"); err != nil {
		return err
	}

	if err := execute(tx, "DROP TABLE websocket_messages_old"); err != nil {
		return err
	}

	// Update saved contexts
	rows, err = tx.Query("SELECT id, context_name, filter_strings FROM saved_contexts")
	if err != nil {
		return err
	}
	defer rows.Close()

	var contextId int64
	var contextName sql.NullString
	var filterStrings sql.NullString

	for rows.Next() {
		if err := rows.Scan(&contextId, &contextName, &filterStrings); err != nil {
			return err
		}

		if !contextName.Valid {
			continue
		}

		if !filterStrings.Valid {
			continue
		}

		if contextName.String == "__scope" {
			// hopefully this doesn't break anything critical, but we want to store the scope
			// as a saved context now with the name __scope
			continue
		}

		var pappyFilters []string
		err = json.Unmarshal([]byte(filterStrings.String), &pappyFilters)
		if err != nil {
			return err
		}

		newFilter, err := pappyListToStrMessageQuery(pappyFilters)
		if err != nil {
			// We're just ignoring filters that we can't convert :|
			continue
		}

		newFilterStr, err := json.Marshal(newFilter)
		if err != nil {
			return err
		}

		stmt, err := tx.Prepare("UPDATE saved_contexts SET filter_strings=? WHERE id=?")
		if err != nil {
			return err
		}
		defer stmt.Close()

		if _, err := tx.Stmt(stmt).Exec(newFilterStr, contextId); err != nil {
			return err
		}
	}
	err = rows.Err()
	if err != nil {
		return err
	}

	// Move scope to a saved context
	rows, err = tx.Query("SELECT filter_order, filter_string FROM scope")
	if err != nil {
		return err
	}
	defer rows.Close()

	var filterOrder sql.NullInt64
	var filterString sql.NullString

	vals := make([]*s9ScopeStr, 0)
	for rows.Next() {
		if err := rows.Scan(&filterOrder, &filterString); err != nil {
			return err
		}

		if !filterOrder.Valid {
			continue
		}

		if !filterString.Valid {
			continue
		}

		vals = append(vals, &s9ScopeStr{filterOrder.Int64, filterString.String})
	}
	err = rows.Err()
	if err != nil {
		return err
	}

	// Put the scope in the right order
	sort.Sort(s9ScopeSort(vals))

	// Convert it into a list of filters
	filterList := make([]string, len(vals))
	for i, ss := range vals {
		filterList[i] = ss.Filter
	}

	newScopeStrFilter, err := pappyListToStrMessageQuery(filterList)
	if err != nil {
		// We'll only convert the scope if we can, otherwise we'll drop it
		err := execute(tx, `INSERT INTO saved_contexts (context_name, filter_strings) VALUES("__scope", "[]")`)
		if err != nil {
			return err
		}
	} else {
		stmt, err := tx.Prepare(`INSERT INTO saved_contexts (context_name, filter_strings) VALUES("__scope", ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		newScopeFilterStr, err := json.Marshal(newScopeStrFilter)
		if err != nil {
			return err
		}

		if _, err := tx.Stmt(stmt).Exec(newScopeFilterStr); err != nil {
			return err
		}
	}

	if err := execute(tx, "DROP TABLE scope"); err != nil {
		return err
	}

	// Update schema number
	if err := execute(tx, `UPDATE schema_meta SET version=9`); err != nil {
		return err
	}
	return nil
}
