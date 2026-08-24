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
