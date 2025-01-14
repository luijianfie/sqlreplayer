package connection

import (
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
)

type Connector interface {
	InitConn() *sql.DB
}

type DBtype int

const (
	MYSQL DBtype = iota
	OCEANBASE
)

func (t DBtype) String() string {
	switch t {
	case MYSQL:
		return "mysql"
	case OCEANBASE:
		return "oceanbase"
	default:
		return "Unknown"
	}
}

type Param struct {
	Ip     string
	Port   string
	User   string
	Passwd string
	DB     string
	Type   DBtype
	Thread int
}

type Mysql struct {
	Param
}

func (m *Mysql) InitConn() *sql.DB {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s", m.User, m.Passwd, m.Ip, m.Port, m.DB)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil
	}

	db.SetMaxOpenConns(m.Thread)
	db.SetMaxIdleConns(m.Thread)
	return db
}

type Oceanbase struct {
	Param
}

func (m *Oceanbase) InitConn() *sql.DB {

	return nil
}

func InitConnections(conns []Param) ([]*sql.DB, error) {

	var dbs []*sql.DB
	for _, conn := range conns {

		var connector Connector
		switch conn.Type {
		case MYSQL:
			connector = &Mysql{Param: conn}
		}

		db := connector.InitConn()
		if db == nil {
			return dbs, errors.New("failed to init connection")
		} else {
			dbs = append(dbs, db)
		}

	}

	return dbs, nil
}
