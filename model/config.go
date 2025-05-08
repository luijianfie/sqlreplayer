package model

import (
	"regexp"

	"go.uber.org/zap"
)

type Config struct {
	ExecType           string   `yaml:"exec_type"`
	FileList           []string `yaml:"file_list"`
	LogType            string   `yaml:"log_type"`
	Begin              string   `yaml:"begin_time"`
	End                string   `yaml:"end_time"`
	Conns              []string `yaml:"conns"`
	Charset            string   `yaml:"charset"`
	Thread             int      `yaml:"thread"` // number of threads for each worker in replay mode
	Multi              int      `yaml:"multiplier"`
	ReplaySQLType      string   `yaml:"sql_type"`
	ReplayFilter       string   `yaml:"sql_filter"`
	GenerateReport     bool     `yaml:"generate_report"`
	SaveRawSQLInReport bool     `yaml:"save_raw_sql"` // save raw sql in report
	DryRun             bool     `yaml:"dry_run"`
	WorkerNum          int      `yaml:"worker_num"`   // number of workers,parallel degree for file
	SQLID              string   `yaml:"sqlid"`        // only replay this sqlid in replay phrase, seperate by comma
	SkipSQLID          string   `yaml:"skip_sqlid"`   // skip sqlid in replay phrase, seperate by comma
	ExecTimeout        int      `yaml:"exec_timeout"` // timeout for each sql in replay phrase
	SkipCount          int      `yaml:"skip_count"`   // timeout exceed skip_count,will skip this sqlid in replay phrase

	RawMapping []RawMappingRule `yaml:"replay_mapping_rules"` // mapping rules

	Dir      string `yaml:"save_dir"`
	Metafile string `yaml:"metafile"`

	Sync   bool
	Logger *zap.Logger
}

type RawMappingRule struct {
	Pattern     string `yaml:"pattern"`
	Replacement string `yaml:"replacement"`
}

type MappingRule struct {
	Pattern     *regexp.Regexp
	Replacement string
}

func (c *Config) SetDefaults() {

	if c.Begin == "" {
		c.Begin = "0000-01-01 00:00:00"
	}

	if c.End == "" {
		c.End = "9999-12-31 23:59:59"
	}

	if c.Charset == "" {
		c.Charset = "utf8mb4"
	}

	if c.Thread <= 0 {
		c.Thread = 1
	}

	if c.Multi <= 0 {
		c.Multi = 1
	}

	if c.WorkerNum <= 0 {
		c.WorkerNum = 4
	}

	if c.ReplaySQLType == "" {
		c.ReplaySQLType = "query"
	}

	if c.ExecTimeout <= 0 {
		c.ExecTimeout = 600
	}

	if c.SkipCount <= 0 {
		c.SkipCount = 10
	}

	if c.Dir == "" {
		c.Dir = "./"
	}

}
