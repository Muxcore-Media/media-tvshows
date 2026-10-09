package internal

import "database/sql"

const sqliteMaxOpenConns = 4

// dbConn returns the SQLite handle without holding the module mutex across queries.
// Reads used to take m.mu.RLock for entire list RPCs while writers held m.mu.Lock
// during bulk metadata imports; with MaxOpenConns(1) that starved list calls and
// wedged the consumer home page.
func (m *Module) dbConn() *sql.DB {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	return db
}

// sqliteDSN is the database path with busy_timeout applied to EVERY pooled
// connection. The pool opens up to sqliteMaxOpenConns connections and a PRAGMA
// only affects the one it runs on, so without this a second connection fails
// instantly with SQLITE_BUSY while another writes (for example SetContentRating
// racing a metadata refresh).
func (m *Module) sqliteDSN() string {
	return m.dbPath + "?_pragma=busy_timeout(5000)"
}
