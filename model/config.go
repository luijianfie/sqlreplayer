package model

import "go.uber.org/zap"

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
	WorkerNum          int      `yaml:"worker_num"` // number of workers,parallel degree for file

	Dir      string `yaml:"save_dir"`
	Metafile string `yaml:"metafile"`

	Sync   bool
	Logger *zap.Logger
}
