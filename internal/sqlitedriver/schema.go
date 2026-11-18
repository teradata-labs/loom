// Copyright 2026 Teradata

package sqlitedriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// ExecSchema runs a schema script for the database file at path. Stores call
// it from their constructor to create or migrate their tables (see
// evals.NewStore). path is the path the caller gave to DSN. The script must be
// idempotent (CREATE ... IF NOT EXISTS) and must not hold its own BEGIN or
// COMMIT; SQLite allows DDL inside a transaction.
//
// Several goroutines and processes can open the same file at once (issue
// #604). In autocommit mode a script is one transaction for each statement,
// and each one asks for the write lock again, so an opener could use up its
// busy_timeout and fail with "database is locked". ExecSchema avoids that in
// three steps:
//
//  1. In this process, callers for the same path run one at a time. They wait
//     on a Go mutex, not on SQLite's busy handler, so goroutines of one
//     process do not use up each other's busy timeout.
//  2. The script runs in one deferred transaction. When the schema already
//     exists, every statement is a read, so no write lock is taken: a
//     read-only file opens, and an open does not wait for another
//     connection's write. When a statement must write, the transaction asks
//     for the write lock once, and the whole script commits or none of it.
//  3. A deferred transaction that has read and then must write gets
//     SQLITE_BUSY at once when another connection holds the write lock;
//     SQLite does not wait, because waiting could deadlock. ExecSchema then
//     rolls back and runs the script again under BEGIN IMMEDIATE, which takes
//     the write lock before the first statement and waits for it under
//     busy_timeout. SQLite locks are file locks, so this covers other
//     processes as well as other connections of this one.
//
// The connection must have a busy timeout (Options.BusyTimeoutMS); without
// one, step 3 fails at once under contention.
//
// ":memory:" and "" are private to each connection, so ExecSchema does not
// lock them.
func ExecSchema(ctx context.Context, db *sql.DB, path, script string) error {
	unlock, err := lockSchemaPath(ctx, path)
	if err != nil {
		return err
	}
	defer unlock()

	err = execScriptTx(ctx, db, "BEGIN", script)
	if err != nil && isBusy(err) {
		err = execScriptTx(ctx, db, "BEGIN IMMEDIATE", script)
	}
	return err
}

// execScriptTx runs script on one connection of db inside one transaction
// that begin starts ("BEGIN" or "BEGIN IMMEDIATE"), and commits it.
// ExecSchema calls it.
//
// On failure it rolls the transaction back. If the rollback also fails, it
// discards the connection, so the pool never hands out a connection that is
// still inside a transaction. The returned error wraps the driver error, so
// isBusy can see it.
func execScriptTx(ctx context.Context, db *sql.DB, begin, script string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sqlitedriver: get connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, begin); err != nil {
		return fmt.Errorf("sqlitedriver: %s: %w", strings.ToLower(begin), err)
	}
	if _, err := conn.ExecContext(ctx, script); err != nil {
		return errors.Join(fmt.Errorf("sqlitedriver: exec script: %w", err), rollback(ctx, conn))
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return errors.Join(fmt.Errorf("sqlitedriver: commit: %w", err), rollback(ctx, conn))
	}
	return nil
}

// rollback ends the open transaction on conn after a failed statement. It
// runs even when ctx is already cancelled, because a cancelled ctx is a
// common reason for the failure. If the ROLLBACK fails, the connection may
// still be inside the transaction, so rollback marks it bad and database/sql
// closes it instead of returning it to the pool.
func rollback(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	if err == nil {
		return nil
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	return fmt.Errorf("sqlitedriver: rollback: %w", err)
}

// schemaLock is one entry of schemaLocks. sem is a one-slot semaphore, so a
// waiter can stop waiting when its ctx ends. refs counts the callers that
// hold or wait for the entry; the last one removes it from the map.
type schemaLock struct {
	sem  chan struct{}
	refs int
}

var (
	schemaLocksMu sync.Mutex
	schemaLocks   = map[string]*schemaLock{}
)

// lockSchemaPath takes the in-process schema lock for path and returns the
// function that releases it. ExecSchema calls it. It returns ctx.Err() if ctx
// ends while it waits. The map holds an entry only while some caller holds or
// waits for it, so it does not grow with the number of files a process opens.
func lockSchemaPath(ctx context.Context, path string) (func(), error) {
	key, ok := schemaLockKey(path)
	if !ok {
		return func() {}, nil
	}

	schemaLocksMu.Lock()
	l := schemaLocks[key]
	if l == nil {
		l = &schemaLock{sem: make(chan struct{}, 1)}
		schemaLocks[key] = l
	}
	l.refs++
	schemaLocksMu.Unlock()

	release := func() {
		schemaLocksMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(schemaLocks, key)
		}
		schemaLocksMu.Unlock()
	}

	select {
	case l.sem <- struct{}{}:
		return func() {
			<-l.sem
			release()
		}, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

// schemaLockKey maps a DSN path to the key of its schema lock. It drops any
// query string and makes a plain file path absolute, so "x.db" and "./x.db"
// share one lock. It returns false for databases that each connection holds
// privately (":memory:", "", and URIs whose query has "mode=memory"), which
// need no lock. Two different paths to one file (a symlink, for example) get
// two locks; the transaction in ExecSchema still protects them.
func schemaLockKey(path string) (string, bool) {
	if path == "" || path == ":memory:" {
		return "", false
	}
	p, query, _ := strings.Cut(path, "?")
	for _, kv := range strings.Split(query, "&") {
		if kv == "mode=memory" {
			return "", false
		}
	}
	p = strings.TrimPrefix(p, "file:")
	if p == "" || p == ":memory:" {
		return "", false
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p), true
}
