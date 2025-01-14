package model

import (
	"time"
)

type CommandUnit struct {
	Time        time.Time
	ThreadID    string
	CommandType string
	Argument    string
	QueryID     string
	Elapsed     float64
	TableList   string
}
