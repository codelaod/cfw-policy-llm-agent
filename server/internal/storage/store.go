// Package storage persists immutable audit inputs, business history and bounded execution.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/libtnb/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	ErrNotFound = errors.New("not found")
	ErrBusy     = errors.New("conversation busy")
	ErrConflict = errors.New("idempotency conflict")
	ErrCapacity = errors.New("queue full")
	ErrCursor   = errors.New("invalid cursor")
)

type Job struct {
	ID                string `json:"id" gorm:"column:id"`
	ConversationID    string `json:"conversation_id"`
	ClientRequestID   string `json:"-"`
	RequestDigest     string `json:"-"`
	Kind              string `json:"kind" enums:"e0_probe,audit,collect,connection_check,assistant_turn"`
	PlanID            string `json:"plan_id,omitempty"`
	Status            string `json:"status"`
	ViewSeq           int64  `json:"view_seq"`
	CreatedAt         string `json:"created_at"`
	StartedAt         string `json:"started_at,omitempty"`
	FinishedAt        string `json:"finished_at,omitempty"`
	QueueDeadline     string `json:"queue_deadline"`
	RootDeadline      string `json:"root_deadline"`
	ExecutionDeadline string `json:"execution_deadline,omitempty"`
	ResultRef         string `json:"result_ref,omitempty"`
	ErrorCode         string `json:"error_code,omitempty"`
	BudgetRef         string `json:"budget_ref"`
}

type Event struct {
	SchemaVersion int            `json:"schema_version"`
	JobID         string         `json:"job_id"`
	Seq           int64          `json:"seq"`
	Type          string         `json:"type"`
	OccurredAt    string         `json:"occurred_at"`
	Payload       map[string]any `json:"payload" gorm:"-"`
	PayloadJSON   string         `json:"-" gorm:"column:payload"`
}

type EventPage struct {
	Events  []Event `json:"events"`
	NextSeq int64   `json:"next_seq"`
	HasMore bool    `json:"has_more"`
	LastSeq int64   `json:"last_seq"`
}

type Store struct {
	db   *gorm.DB
	SQL  *sql.DB
	lock *os.File
}

// Open requires an explicit migrate flag for schema creation and acquires the
// process lock before reading or recovering task state. Target: Linux/WSL.
func Open(path string, migrate bool) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(abs+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("data directory is already in use")
	}
	cleanup := func() { syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); lock.Close() }
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		cleanup()
		return nil, err
	}
	file.Close()
	u := url.URL{Scheme: "file", Path: abs}
	q := url.Values{"_txlock": {"immediate"}, "_pragma": {"journal_mode(WAL)", "foreign_keys(1)", "busy_timeout(5000)"}}
	db, err := gorm.Open(sqlite.Open(u.String()+"?"+q.Encode()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), SkipDefaultTransaction: true})
	if err != nil {
		cleanup()
		return nil, err
	}
	pool, err := db.DB()
	if err != nil {
		cleanup()
		return nil, err
	}
	pool.SetMaxOpenConns(8)
	pool.SetMaxIdleConns(4)
	s := &Store{db: db, SQL: pool, lock: lock}
	if migrate {
		err = s.migrate()
	} else {
		var v int
		err = db.Raw("SELECT version FROM e0_schema").Scan(&v).Error
		if err == nil && v != 1 {
			err = errors.New("unsupported E0 schema")
		}
		if err == nil {
			v = 0
			err = db.Raw("SELECT version FROM business_schema").Scan(&v).Error
			if err == nil && v != BusinessSchemaVersion {
				err = errors.New("unsupported business schema")
			}
			if err == nil {
				err = checkAdmissionShape(db)
			}
		}
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("schema unavailable; run explicit --migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	err := s.SQL.Close()
	syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	s.lock.Close()
	return err
}

func (s *Store) migrate() error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS e0_schema (version INTEGER PRIMARY KEY CHECK(version=1))`,
			`INSERT OR IGNORE INTO e0_schema(version) VALUES(1)`,
			`CREATE TABLE IF NOT EXISTS e0_jobs (id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, client_request_id TEXT UNIQUE NOT NULL, status TEXT NOT NULL CHECK(status IN ('queued','running','cancelling','completed','failed','cancelled','interrupted')), view_seq INTEGER NOT NULL DEFAULT 0 CHECK(view_seq>=0), created_at TEXT NOT NULL, started_at TEXT NOT NULL DEFAULT '', finished_at TEXT NOT NULL DEFAULT '')`,
			`CREATE UNIQUE INDEX IF NOT EXISTS e0_one_active ON e0_jobs(conversation_id) WHERE status IN ('queued','running','cancelling')`,
			`CREATE TABLE IF NOT EXISTS e0_events (job_id TEXT NOT NULL REFERENCES e0_jobs(id), seq INTEGER NOT NULL CHECK(seq>0), schema_version INTEGER NOT NULL CHECK(schema_version=1), type TEXT NOT NULL, occurred_at TEXT NOT NULL, payload TEXT NOT NULL CHECK(json_valid(payload)), PRIMARY KEY(job_id,seq))`,
		}
		for _, q := range statements {
			if err := tx.Exec(q).Error; err != nil {
				return err
			}
		}
		return migrateBusiness(tx)
	})
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func terminal(s string) bool {
	return s == "completed" || s == "partial" || s == "failed" || s == "cancelled" || s == "interrupted"
}
func Terminal(s string) bool { return terminal(s) }

func get(tx *gorm.DB, id string) (Job, error) {
	var j Job
	r := tx.Raw("SELECT * FROM e0_jobs WHERE id=?", id).Scan(&j)
	if r.Error != nil {
		return j, r.Error
	}
	if r.RowsAffected == 0 {
		return j, ErrNotFound
	}
	return j, nil
}
func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	return get(s.db.WithContext(ctx), id)
}

// appendEvent is always called inside the same transaction as the state change.
func appendEvent(tx *gorm.DB, j *Job, typ string, payload map[string]any) error {
	if len(payload) == 0 {
		return errors.New("missing payload")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	j.ViewSeq++
	e := Event{SchemaVersion: 1, JobID: j.ID, Seq: j.ViewSeq, Type: typ, OccurredAt: now(), Payload: payload}
	encoded, err := json.Marshal(e)
	if err != nil || len(encoded) > 4096 {
		return errors.New("event too large")
	}
	if err = tx.Exec("INSERT INTO e0_events(job_id,seq,schema_version,type,occurred_at,payload) VALUES(?,?,?,?,?,?)", j.ID, j.ViewSeq, 1, typ, e.OccurredAt, string(b)).Error; err != nil {
		return err
	}
	return tx.Exec("UPDATE e0_jobs SET view_seq=? WHERE id=?", j.ViewSeq, j.ID).Error
}

func (s *Store) Create(ctx context.Context, conversation, request string) (j Job, replayed bool, err error) {
	return s.createProbe(ctx, conversation, request)
}

func (s *Store) Claim(ctx context.Context) (j Job, found bool, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := expireQueued(tx); e != nil {
			return e
		}
		var active int64
		if e := tx.Raw("SELECT count(*) FROM e0_jobs WHERE status IN ('running','cancelling')").Scan(&active).Error; e != nil {
			return e
		}
		if active >= 3 {
			return nil
		}
		var candidate Job
		r := tx.Raw("SELECT * FROM e0_jobs WHERE status='queued' ORDER BY created_at,id LIMIT 1").Scan(&candidate)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 0 {
			return nil
		}
		_, execution := timingFor(candidate.Kind)
		at := time.Now().UTC()
		deadline := at.Add(execution)
		root, e := time.Parse(time.RFC3339Nano, candidate.RootDeadline)
		if e != nil {
			return e
		}
		if root.Before(deadline) {
			deadline = root
		}
		r = tx.Raw("UPDATE e0_jobs SET status='running',started_at=?,execution_deadline=? WHERE id=? AND status='queued' RETURNING *", at.Format(time.RFC3339Nano), deadline.Format(time.RFC3339Nano), candidate.ID).Scan(&j)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 0 {
			return nil
		}
		found = true
		if e := appendEvent(tx, &j, "job_status", map[string]any{"status": "running"}); e != nil {
			return e
		}
		stage := "input_preparation"
		if j.Kind == "e0_probe" {
			stage = "local_mock"
		} else {
			if e := tx.Exec("UPDATE conversation_turns SET state='running' WHERE job_id=?", j.ID).Error; e != nil {
				return e
			}
		}
		return appendEvent(tx, &j, "stage_changed", map[string]any{"stage": stage, "phase": "L1"})
	})
	return
}

func (s *Store) Finish(ctx context.Context, id, status string) error {
	return s.FinishWithCode(ctx, id, status, "")
}

func (s *Store) Cancel(ctx context.Context, id string) (j Job, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		j, e = get(tx, id)
		if e != nil {
			return e
		}
		if terminal(j.Status) || j.Status == "cancelling" {
			return nil
		}
		if j.Status == "queued" {
			j.Status = "cancelled"
			j.FinishedAt = now()
		} else {
			j.Status = "cancelling"
		}
		if e = tx.Exec("UPDATE e0_jobs SET status=?,finished_at=? WHERE id=?", j.Status, j.FinishedAt, id).Error; e != nil {
			return e
		}
		typ := "job_status"
		if terminal(j.Status) {
			typ = "job_terminal"
		}
		if terminal(j.Status) {
			if e = tx.Exec("UPDATE conversation_turns SET state='cancelled' WHERE job_id=?", id).Error; e != nil {
				return e
			}
		}
		return appendEvent(tx, &j, typ, map[string]any{"status": j.Status})
	})
	return
}

// Recovery is called only after Open owns the process lock. Active executions
// are interrupted without replay; valid never-started plans keep their budget.
func (s *Store) Recover(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := expireQueued(tx); e != nil {
			return e
		}
		var jobs []Job
		if e := tx.Raw("SELECT * FROM e0_jobs WHERE status IN ('running','cancelling') OR (status='queued' AND kind='e0_probe')").Scan(&jobs).Error; e != nil {
			return e
		}
		for i := range jobs {
			if e := finishJob(tx, &jobs[i], "interrupted", "JOB_INTERRUPTED"); e != nil {
				return e
			}
		}
		return nil
	})
}

func (s *Store) Page(ctx context.Context, id string, after int64, limit int) (p EventPage, j Job, err error) {
	p.Events = []Event{}
	p.NextSeq = after
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		j, e = get(tx, id)
		if e != nil {
			return e
		}
		p.LastSeq = j.ViewSeq
		if after < 0 || after > j.ViewSeq {
			return ErrCursor
		}
		if e = tx.Raw("SELECT * FROM e0_events WHERE job_id=? AND seq>? ORDER BY seq LIMIT ?", id, after, limit).Scan(&p.Events).Error; e != nil {
			return e
		}
		for i := range p.Events {
			if e = json.Unmarshal([]byte(p.Events[i].PayloadJSON), &p.Events[i].Payload); e != nil {
				return e
			}
			p.NextSeq = p.Events[i].Seq
		}
		p.HasMore = p.NextSeq < p.LastSeq
		return nil
	})
	return
}
