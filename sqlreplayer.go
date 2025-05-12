package sqlreplayer

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/gob"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/luijianfie/sqlreplayer/connector"
	"github.com/luijianfie/sqlreplayer/model"
	"github.com/luijianfie/sqlreplayer/parser"
	"github.com/luijianfie/sqlreplayer/utils"

	"go.uber.org/zap"
)

var (
	commandTypePattern string = "(?i)Prepare|Close|Quit"
)

type Status int

const (
	PROCESSING Status = 1 << iota
	STOPPING
	STOPPED
	AFTER_PROCESSING
	DONE
	ANALYZE_ERROR
	REPLAY_ERROR
	EXIT
)

type SQLMode int

const (
	QUERY SQLMode = 1 << iota
	DML
	DDL
	ELSE
)

const TASK_CHAN_LENGTH = 20
const REPLAY_COLLECTOR_LENGTH = 100

func (s Status) String() string {
	switch s {
	case PROCESSING:
		return "processing"
	case STOPPING:
		return "stopping"
	case STOPPED:
		return "stopped"
	case DONE:
		return "done"
	case ANALYZE_ERROR:
		return "analyze error"
	case REPLAY_ERROR:
		return "replay error"
	default:
		return "Unknown"
	}
}

func (s SQLMode) String() string {
	switch s {
	case QUERY:
		return "query"
	case DML:
		return "dml"
	case DDL:
		return "ddl"
	default:
		return "else"
	}
}

type replayUnit struct {
	Sqlid     string
	Tablelist string
	Sqltype   string
	Sql       string
	Time      uint64
}

type task struct {
	taskSeq        int
	parseFilename  string
	parseFullPath  string
	replayFilename string
	replayFullPath string

	parseStatus       bool
	parseFilePos      int64
	parseRows         int64
	replayStatus      bool
	replayFilePos     int64
	connsToReplayRows int64
}

type sqlStatus struct {
	Sqlid           string
	SQL             string
	Fingerprint     string
	Tablelist       string
	Sqltype         string
	Min             int64
	Max             int64
	TimeElapse      int64
	Minsql          string
	Maxsql          string
	Execution       uint64
	DataSourceIndex int
	ErrorCount      uint64
	ErrorStr        string
}

type SQLReplayer struct {
	JobSeq  uint64 //jobSeq is global uniq id
	TaskSeq int    //taskSeq is uniq id in a job

	mutex sync.Mutex

	ExecMode model.ExecType

	LogType string            //file type
	parser  parser.FileParser //parser for file type

	tasks map[int]*task

	// user define time range
	Begin time.Time
	End   time.Time

	Conns []connector.Param

	//option
	GenerateReport     bool
	SaveRawSQLInReport bool
	ReplaySQLType      SQLMode
	ReplayFilter       string
	DryRun             bool
	Currency           int //num of worker
	Multiplier         int //multiplier
	Thread             int //thread pool for one database instance
	wg                 sync.WaitGroup

	// timestamp for task
	Current time.Time

	Dir               string
	ParseStatFileDir  string
	ReplayStatFileDir string

	SqlID2Fingerprint map[string]*sqlStatus // map[SQLID] -> fingerprint

	// //analyze pharse for generating report
	// SqlID2ReplayUints map[string][]replayUnit // map[SQLID] -> [ru1,ru2,ru3,ru4...]

	QueryID2RelayStats map[string][]*sqlStatus
	//replayer pharse for generating report
	//map[SQLID]->[map[db1],map[db2]...]       map[db1]->[ru1,ru2,ru3,ru4...]
	// QueryID2RelayStats map[string][][]replayUnit

	logger *zap.Logger `json:"-"`

	skipCount      int
	execTimeout    int //second
	skipMutex      sync.Mutex
	SQLID          map[string]struct{}
	skipSQLIDCount map[string]int
	skipSQLID      map[string]struct{}

	RawMapping []model.RawMappingRule
	Rules      []model.MappingRule

	//controller
	stopchan            chan struct{}
	donechan            chan struct{}
	taskchan            chan int
	replayCollector     chan *sqlStatus
	replayCollectorDone chan struct{}

	//Status
	Status         Status
	ReplayRowCount uint64
	FileSize       uint64
}

func NewSQLReplayer(jobSeq uint64, c *model.Config) (*SQLReplayer, error) {

	var err error
	var sr SQLReplayer

	if len(c.Metafile) > 0 {

		sr = SQLReplayer{
			logger:              c.Logger,
			stopchan:            make(chan struct{}),
			donechan:            make(chan struct{}),
			taskchan:            make(chan int, TASK_CHAN_LENGTH),
			replayCollector:     make(chan *sqlStatus, REPLAY_COLLECTOR_LENGTH),
			replayCollectorDone: make(chan struct{}),

			SQLID:          make(map[string]struct{}),
			skipSQLIDCount: make(map[string]int),
			skipSQLID:      make(map[string]struct{}),
		}
		sr.logger.Sugar().Infof("begin to load meta file from %s", c.Metafile)
		err := sr.Load(c.Metafile)

		//set status for task
		sr.Status = PROCESSING

		if err != nil {
			return nil, err
		}

		if len(c.RawMapping) > 0 {

			sr.RawMapping = c.RawMapping
			for _, raw := range sr.RawMapping {

				re, err := regexp.Compile(raw.Pattern)
				if err != nil {
					continue
				}
				sr.Rules = append(sr.Rules, model.MappingRule{
					Pattern:     re,
					Replacement: raw.Replacement,
				})

			}
		}

	} else {

		sr = SQLReplayer{
			JobSeq:             jobSeq,
			LogType:            c.LogType,
			Current:            time.Now(),
			SqlID2Fingerprint:  make(map[string]*sqlStatus),
			SaveRawSQLInReport: c.SaveRawSQLInReport,
			GenerateReport:     c.GenerateReport,
			ReplaySQLType:      QUERY,
			ReplayFilter:       c.ReplayFilter,
			DryRun:             c.DryRun,
			QueryID2RelayStats: make(map[string][]*sqlStatus),
			Currency:           c.WorkerNum,
			Multiplier:         c.Multi,
			Thread:             c.Thread,

			logger:              c.Logger,
			stopchan:            make(chan struct{}),
			donechan:            make(chan struct{}),
			taskchan:            make(chan int, TASK_CHAN_LENGTH),
			replayCollector:     make(chan *sqlStatus, REPLAY_COLLECTOR_LENGTH),
			replayCollectorDone: make(chan struct{}),

			Status: PROCESSING,

			SQLID:          make(map[string]struct{}),
			skipSQLIDCount: make(map[string]int),
			skipSQLID:      make(map[string]struct{}),
			execTimeout:    600,
			skipCount:      10,
		}

		sr.Dir = c.Dir + fmt.Sprintf("sqlreplayer_task_%d", sr.JobSeq)

		err = os.MkdirAll(sr.Dir, os.ModePerm)
		if err != nil {
			return nil, errors.New("failed to create directory: " + sr.Dir)
		}

		//replay sql type

		switch strings.ToUpper(c.ExecType) {
		case "ANALYZE":
			sr.ExecMode = model.ANALYZE

		case "REPLAY":
			sr.ExecMode = model.REPLAY

		case "BOTH":
			sr.ExecMode = model.ANALYZE | model.REPLAY

		default:
			return nil, errors.New(" unknown exec type")
		}

		if sr.ExecMode&model.ANALYZE != 0 {
			if len(c.LogType) == 0 {
				return nil, errors.New("analyze mode: log type are needed.")
			}
			sr.LogType = strings.ToUpper(c.LogType)

			//init File
			sr.ParseStatFileDir = sr.Dir + "/rawsql_analyze_report.html"

			// statFile, err := os.Create(sr.ReplayStatFileDir)
			// if err != nil {
			// 	return nil, err
			// }
			// sr.replayStatFile = statFile
		}
		if sr.ExecMode&model.REPLAY != 0 {
			if len(c.Conns) == 0 {
				return nil, errors.New("replay mode: conn are needed.")
			}

			//set replay sql mode
			strs := strings.Split(c.ReplaySQLType, ",")
			for _, str := range strs {
				switch strings.ToUpper(strings.TrimSpace(str)) {
				case "QUERY":
					sr.ReplaySQLType |= QUERY
				case "DML":
					sr.ReplaySQLType |= DML
				case "DDL":
					sr.ReplaySQLType |= DDL
				case "ELSE":
					sr.ReplaySQLType |= ELSE
				case "ALL":
					sr.ReplaySQLType |= QUERY | DML | DDL | ELSE
				}
			}

			//init conn info
			for idx, conn := range c.Conns {
				strs := strings.Split(conn, ":")
				// 0	1		2	  3		4	5
				// type:user1:passwd1:ip1:port1:db1
				if len(strs) < 6 {
					if len(strs) == 5 {
						strs = append(strs, "")
					} else {
						return nil, errors.New(fmt.Sprintf("invalid conn string,conn %d [ip:%s,port:%s,db:%s,user:%s]", idx, strs[3], strs[4], strs[5], strs[1]))
					}
				}
				p := connector.Param{
					User:   strs[1],
					Passwd: strs[2],
					Ip:     strs[3],
					Port:   strs[4],
					DB:     strs[5],
					Thread: c.Thread,
				}

				switch strings.ToUpper(strs[0]) {
				case "MYSQL":
					p.Type = connector.MYSQL
				}
				sr.Conns = append(sr.Conns, p)
			}

			//init File
			sr.ReplayStatFileDir = sr.Dir + "/replay_stats.html"

			// statFile, err := os.Create(sr.ReplayStatFileDir)
			// if err != nil {
			// 	return nil, err
			// }
			// sr.replayStatFile = statFile

			//parse sqlid and skip sqlid into skip map

			if len(c.SQLID) > 0 {
				sr.skipMutex.Lock()
				sqlids := strings.Split(c.SQLID, ",")
				for _, sqlid := range sqlids {
					sqlid = strings.TrimSpace(sqlid)
					sr.SQLID[sqlid] = struct{}{}
				}
				sr.skipMutex.Unlock()
			}

			if len(c.SkipSQLID) > 0 {
				sr.skipMutex.Lock()
				sqlids := strings.Split(c.SkipSQLID, ",")
				for _, sqlid := range sqlids {
					sqlid = strings.TrimSpace(sqlid)
					sr.skipSQLID[sqlid] = struct{}{}
				}
				sr.skipMutex.Unlock()
			}

			if c.SkipCount > 0 {
				sr.skipCount = c.SkipCount
			}
			if c.ExecTimeout > 0 {
				sr.execTimeout = c.ExecTimeout
			}

			if len(c.RawMapping) > 0 {

				sr.RawMapping = c.RawMapping
				for _, raw := range sr.RawMapping {

					re, err := regexp.Compile(raw.Pattern)
					if err != nil {
						continue
					}
					sr.Rules = append(sr.Rules, model.MappingRule{
						Pattern:     re,
						Replacement: raw.Replacement,
					})

				}
			}

		}

		//parse time range
		if len(c.Begin) == 0 {
			c.Begin = "0000-01-01 00:00:00"
		}
		if len(c.End) == 0 {
			c.End = "9999-12-31 23:59:59"
		}
		sr.Begin, err = time.Parse("2006-01-02 15:04:05", c.Begin)
		if err != nil {
			return nil, err
		}

		sr.End, err = time.Parse("2006-01-02 15:04:05", c.End)
		if err != nil {
			return nil, err
		}

	}
	sr.tasks = make(map[int]*task)

	// //init consumers
	// for i := range c.FileList {
	// 	sr.taskchan <- i
	// }
	// close(sr.taskchan)

	//init parser
	if sr.ExecMode&model.ANALYZE != 0 {
		switch strings.ToUpper(sr.LogType) {
		case "GENLOG":
			sr.parser = &parser.GeneralLogParser{}
		case "SLOWLOG":
			sr.parser = &parser.SlowlogParser{}
		case "CSV":
			sr.parser = &parser.CSVParser{}
		default:
			return nil, errors.New("unknow log type.")
		}

		// //check if file exist
		// for _, f := range sr.ParserFileList {
		// 	if !utils.FileExists(f) {
		// 		return nil, errors.New("file:" + f + " not exist.")
		// 	}
		// }
	}

	// if sr.ExecMode&model.REPLAY != 0 {

	// 	//check if file exist
	// 	for _, f := range sr.ReplayFileList {
	// 		if !utils.FileExists(f) {
	// 			return nil, errors.New("file:" + f + " not exist.")
	// 		}
	// 	}
	// }

	return &sr, nil

}

// deal with replay info here
func (sr *SQLReplayer) startReplayCollector() {

	sr.logger.Sugar().Infof("start replay collector")

	errMap := make(map[string]int)

	errorFileDir := sr.Dir + "/replay_error"
	errorFile, err := os.OpenFile(errorFileDir, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		sr.logger.Error(err.Error())
		return
	}

	defer func() {

		for k, v := range errMap {

			_, err := errorFile.WriteString(k + "," + strconv.Itoa(v) + "\n")
			if err != nil {
				sr.logger.Sugar().Errorln("write error to file error:%s", err.Error())
				break
			}
		}

		errorFile.Close()
	}()

	defer close(sr.replayCollectorDone)
	for {
		select {
		case <-sr.stopchan:
			sr.logger.Sugar().Infof("recieve stop signal, exit replay collector")
			return
		case status, ok := <-sr.replayCollector:
			if !ok {
				sr.logger.Sugar().Infof("replayer collecter exit.")
				return
			} else {

				_, ok := sr.QueryID2RelayStats[status.Sqlid]
				if !ok {
					sr.QueryID2RelayStats[status.Sqlid] = make([]*sqlStatus, len(sr.Conns))
				}

				//not exist
				if sr.QueryID2RelayStats[status.Sqlid][status.DataSourceIndex] == nil {

					if sr.SaveRawSQLInReport {
						status.Minsql = status.SQL
						status.Maxsql = status.SQL
					}

					sr.QueryID2RelayStats[status.Sqlid][status.DataSourceIndex] = status

				} else {
					s := sr.QueryID2RelayStats[status.Sqlid][status.DataSourceIndex]
					s.Execution += 1
					s.ErrorCount += status.ErrorCount

					if status.ErrorCount == 0 {

						if s.Min == -1 {
							//the first execution meet error
							//set min and max to current time elapse
							s.Min = status.TimeElapse
							s.Max = status.TimeElapse
							if sr.SaveRawSQLInReport {
								s.Minsql = s.SQL
								s.Maxsql = s.SQL
							}

						} else {

							s.TimeElapse += status.TimeElapse
							if s.Min > s.TimeElapse {
								s.Min = s.TimeElapse
								if sr.SaveRawSQLInReport {
									s.Minsql = s.SQL
								}
							}

							if s.Max < s.TimeElapse {
								s.Max = s.TimeElapse
								if sr.SaveRawSQLInReport {
									s.Maxsql = s.SQL
								}
							}
						}
					}
				}

				//write error to file
				if status.ErrorCount > 0 {

					errKey := "Conn_" + strconv.Itoa(status.DataSourceIndex) + "," + status.Sqlid + "," + status.ErrorStr
					errMap[errKey] += 1

					_, err := errorFile.WriteString("Conn_" + strconv.Itoa(status.DataSourceIndex) + "," + status.Sqlid + "," + status.ErrorStr + "," + status.SQL + "\n")
					if err != nil {
						sr.logger.Sugar().Errorln("write error to file error:%s", err.Error())
						break
					}

				}

			}

		}
	}
}

func (sr *SQLReplayer) Start() {

	sr.wg.Add(sr.Currency)

	if sr.ExecMode&model.REPLAY != 0 {
		go sr.startReplayCollector()
	}

	for i := 0; i < sr.Currency; i++ {

		go func(index int) {
			defer sr.wg.Done()
			sr.logger.Sugar().Infof("worker %d start.", index)
			for {

				select {
				case <-sr.stopchan:
					sr.logger.Sugar().Infof("stop sqlreplayer, jobid %d", sr.JobSeq)
					return
				case t, ok := <-sr.taskchan:
					{
						if !ok {
							sr.logger.Sugar().Infof("worker %d exit.", index)
							return
						}

						sr.mutex.Lock()
						task := sr.tasks[t]

						fileName := ""
						if len(task.parseFilename) > 0 {
							fileName = task.parseFullPath
						} else {
							fileName = task.replayFullPath
						}

						//get file size of file
						fileStat, err := os.Stat(fileName)
						if err == nil {
							sr.FileSize = sr.FileSize + uint64(fileStat.Size())
						}

						sr.mutex.Unlock()

						//file parse pharse
						if sr.ExecMode&model.ANALYZE != 0 {
							if !task.parseStatus {
								err := sr.analyze(task)

								if err != nil {

									if err.Error() == "STOP" {
										sr.logger.Sugar().Infof("recieve STOP signal,worker %d exit.", index)
									} else {
										sr.Status = ANALYZE_ERROR
										sr.logger.Error(err.Error())
										return
									}
								} else {
									sr.logger.Sugar().Infof("finish parse %s %s", sr.LogType, task.parseFullPath)
								}

							}
						}

						//replay pharse
						if sr.ExecMode&model.REPLAY != 0 {
							if !task.replayStatus {
								err := sr.replayRawSQL(task)
								if err != nil {
									if err.Error() == "STOP" {
										sr.logger.Sugar().Infof("recieve STOP signal,worker %d exit.", index)
									} else {
										sr.Status = REPLAY_ERROR
										sr.logger.Error(err.Error())
									}
									return
								}
							}
						}

					}

				}

				if sr.Status == STOPPING {
					return
				}
			}
		}(i)
	}

	sr.wg.Wait()

	//all task is done,close replay collector
	close(sr.replayCollector)

	sr.mutex.Lock()
	if sr.Status == STOPPING {
		sr.Status = STOPPED
		sr.Save()
	} else if sr.Status == PROCESSING {
		sr.Status = DONE

		if sr.GenerateReport {
			if sr.ExecMode&model.ANALYZE != 0 {
				sr.generateRawSQLReport()
			}

			if sr.ExecMode&model.REPLAY != 0 {
				<-sr.replayCollectorDone
				sr.generateReplayReport()
			}
		}

		sr.logger.Info("task finished.")
	} else {
		sr.logger.Sugar().Infof("task failed. %s.", sr.Status)
	}

	sr.mutex.Unlock()

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	// print mem stat
	sr.logger.Info("Memory statistic ")
	sr.logger.Info("Allocated Memory: " + strconv.FormatUint(memStats.Alloc/1024, 10) + " KB")
	sr.logger.Info("Total Allocated Memory: " + strconv.FormatUint(memStats.TotalAlloc/1024, 10) + " KB")
	sr.logger.Info("Heap Memory: " + strconv.FormatUint(memStats.HeapAlloc/1024, 10) + " KB")
	sr.logger.Info("Heap Memory System: " + strconv.FormatUint(memStats.HeapSys/1024, 10) + " KB")
	sr.logger.Info("MaxHeapAlloc: " + strconv.FormatUint(memStats.HeapInuse/1024, 10) + " KB")

	sr.logger.Info("exit.")

	sr.Close()

}

func (sr *SQLReplayer) AddTask(fileFullPath string) {

	sr.mutex.Lock()
	t := task{taskSeq: sr.TaskSeq}
	sr.TaskSeq++

	//analyze phrase
	if sr.ExecMode&model.ANALYZE != 0 {
		t.parseFullPath = fileFullPath
		t.parseFilename = filepath.Base(fileFullPath)

		// for csv file, don't need to write to file again, thus , replay file is the same as parse file
		if strings.ToUpper(sr.LogType) == "CSV" {
			t.replayFilename = filepath.Base(fileFullPath)
			t.replayFullPath = fileFullPath
		} else {
			t.replayFilename = "rawsql_" + strconv.Itoa(t.taskSeq) + ".csv"
			t.replayFullPath = filepath.Join(sr.Dir, t.replayFilename)
		}
	} else {
		t.replayFilename = filepath.Base(fileFullPath)
		t.replayFullPath = fileFullPath
	}

	sr.tasks[t.taskSeq] = &t
	sr.mutex.Unlock()
	sr.taskchan <- t.taskSeq
}

func (sr *SQLReplayer) CloseTaskChan() {
	close(sr.taskchan)
}

func (sr *SQLReplayer) Save() {
	sr.logger.Sugar().Infof("begin to save context for task, metafile %s.", sr.Dir+"/taskmeta")

	var buffer bytes.Buffer
	encoder := gob.NewEncoder(&buffer)
	err := encoder.Encode(sr)
	if err != nil {
		sr.logger.Error(err.Error())
		return
	}

	file, err := os.Create(sr.Dir + "/taskmeta")
	if err != nil {
		sr.logger.Error(err.Error())
		return
	}
	defer file.Close()

	_, err = file.Write(buffer.Bytes())
	if err != nil {
		sr.logger.Error(err.Error())
		return
	}
}

func (sr *SQLReplayer) Done() {
	<-sr.donechan
}

func (sr *SQLReplayer) Stop() {
	sr.mutex.Lock()
	sr.Status = STOPPING
	sr.mutex.Unlock()
	close(sr.stopchan)
}

func (sr *SQLReplayer) Load(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	var buffer bytes.Buffer
	_, err = buffer.ReadFrom(file)
	if err != nil {
		return err
	}

	decoder := gob.NewDecoder(&buffer)
	err = decoder.Decode(&sr)
	return err
}

func (sr *SQLReplayer) Close() {
	// if sr.replayStatFile != nil {
	// 	sr.replayStatFile.Close()
	// }
	close(sr.donechan)
}

// analyze will parse file and save it in csv format
// param [in] file: log file dir
func (sr *SQLReplayer) analyze(t *task) error {

	file := t.parseFullPath
	pos := t.parseFilePos

	l := sr.logger

	l.Sugar().Infof("begin to analyze %s from pos %d", file, pos)

	//output file in analyze phrase is replay full path in "replay" phrase.
	rawSQLFileDir := t.replayFullPath

	rawSQLFile, err := os.OpenFile(rawSQLFileDir, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		sr.logger.Info(err.Error())
		return err
	}
	defer rawSQLFile.Close()

	strings.ToUpper(sr.LogType)

	var csvWriter *csv.Writer

	//for csv file, don't need to write to file again
	if strings.ToUpper(sr.LogType) != "CSV" {
		csvWriter = csv.NewWriter(rawSQLFile)
		defer csvWriter.Flush()
	}

	//save sqlid to finggerprint
	tmpSqlID2Fingerprint := make(map[string]*sqlStatus)

	//deal with command unit
	curPos, err := sr.parser.Parser(file, pos, func(cu *model.CommandUnit) error {

		select {
		case <-sr.stopchan:
			return errors.New("STOP")
		default:
		}

		var fingerprint string

		//command type filter
		//time filter
		commandTypeMatch, _ := regexp.MatchString(commandTypePattern, cu.CommandType)
		if commandTypeMatch || (cu.Time.After(sr.End) || cu.Time.Before(sr.Begin)) {
			return nil
		}

		//generate query id
		cu.QueryID, fingerprint = utils.GetQueryID(cu.Argument)
		cu.TableList, _ = utils.ExtractTableNames(cu.Argument)
		ret, err := utils.GetSQLStatement(cu.Argument)
		if err != nil {
			cu.CommandType = "UNKNOWN"
		} else {
			if len(ret) > 0 {
				cu.CommandType = ret[0]
			} else {
				cu.CommandType = "UNKNOWN"
			}
		}

		//save to raw sql file
		if csvWriter != nil {
			err := csvWriter.Write([]string{cu.Argument, cu.QueryID, cu.Time.Format("20060102 15:04:05"), cu.CommandType, cu.TableList, strconv.FormatFloat(cu.Elapsed*1000, 'f', 2, 64)})
			if err != nil {
				return err
			}
		}

		_, ok := tmpSqlID2Fingerprint[cu.QueryID]
		if !ok {
			s := &sqlStatus{
				Sqlid:       cu.QueryID,
				Fingerprint: fingerprint,
				Sqltype:     cu.CommandType,
				Execution:   1,
				TimeElapse:  int64(cu.Elapsed * 1000),
				Tablelist:   cu.TableList,
				Min:         int64(cu.Elapsed * 1000),
				Max:         int64(cu.Elapsed * 1000),
			}
			if sr.GenerateReport {
				s.Minsql = cu.Argument
				s.Maxsql = cu.Argument
			}
			tmpSqlID2Fingerprint[cu.QueryID] = s
		} else {

			elapse := int64(cu.Elapsed * 1000)
			s := tmpSqlID2Fingerprint[cu.QueryID]
			s.Execution++
			s.TimeElapse += elapse

			if s.Min > elapse {
				s.Min = elapse
				if sr.GenerateReport {
					s.Minsql = cu.Argument
				}
			}

			if s.Max < elapse {
				s.Max = elapse
				if sr.GenerateReport {
					s.Maxsql = cu.Argument
				}
			}
		}

		return nil

	})

	t.parseFilePos = curPos

	sr.mutex.Lock()

	if len(tmpSqlID2Fingerprint) == 0 {
		fmt.Println(len(tmpSqlID2Fingerprint))
	}
	for k, v := range tmpSqlID2Fingerprint {
		_, ok := sr.SqlID2Fingerprint[k]
		if !ok {
			sr.SqlID2Fingerprint[k] = v
		} else {
			s := sr.SqlID2Fingerprint[k]

			s.Execution += v.Execution
			s.TimeElapse += v.TimeElapse

			if s.Min > v.Min {
				s.Min = v.Min
				s.Minsql = v.Minsql
			}

			if s.Max < v.Max {
				s.Max = v.Max
				s.Maxsql = v.Maxsql
			}

		}
	}

	sr.mutex.Unlock()

	if err == nil {
		t.parseStatus = true
	}

	return err
}

type SQLRecord struct {
	SQLID       string
	Fingerprint string
	TableList   string
	Min         string
	MinSQL      string
	Max         string
	MaxSQL      string
	Avg         string
	Execution   string
}

func (sr *SQLReplayer) generateRawSQLReport() error {
	reportFile, err := os.Create(sr.ParseStatFileDir)
	if err != nil {
		return err
	}
	defer reportFile.Close()

	// 准备数据
	var data []map[string]string
	// 修改基本列顺序
	header := []string{"sqlid", "sqltype", "fingerprint", "tablelist", "execution"}

	// 添加统计数据结构
	var TotalSQLs uint64
	type Summary struct {
		TotalSQLs      string            // 总SQL执行数
		FingerprintNum string            // SQL指纹数量
		SQLTypeStats   map[string]uint64 // SQL类型统计
		TableNumsStats map[uint]uint64   // 表数量统计
		FileCount      string            // 文件总数
		TotalFileSize  string            // 文件累计大小
	}

	// 计算统计数据
	summary := Summary{
		SQLTypeStats:   make(map[string]uint64),
		TableNumsStats: make(map[uint]uint64),
	}

	summary.FingerprintNum = humanize.Comma(int64(len(sr.SqlID2Fingerprint)))
	sr.mutex.Unlock()
	tableMap := sr.ShowTableJoinFrequency()
	sr.mutex.Lock()
	for k, v := range tableMap {
		strs := strings.Split(k, ",")
		count := uint(0)
		for _, str := range strs {
			if len(strings.TrimSpace(str)) > 0 {
				count++
			}
		}
		summary.TableNumsStats[count] += v
	}

	// 如果是慢日志，添加性能相关的列
	additionalInfo := sr.LogType == strings.ToUpper("slowlog")
	if additionalInfo {
		header = append(header, "min(ms)", "min_sql", "max(ms)", "max_sql", "avg(ms)")
	}

	// 构造数据
	for sqlID, sqlStatus := range sr.SqlID2Fingerprint {

		row := map[string]string{
			"sqlid":       sqlID,
			"sqltype":     sqlStatus.Sqltype,
			"fingerprint": sqlStatus.Fingerprint,
			"tablelist":   sqlStatus.Tablelist,
			"execution":   strconv.FormatUint(sqlStatus.Execution, 10),
		}
		if additionalInfo {
			avg := float64(sqlStatus.TimeElapse) / float64(sqlStatus.Execution)
			row["min"] = strconv.FormatInt(sqlStatus.Min, 10)
			row["min_sql"] = sqlStatus.Minsql
			row["max"] = strconv.FormatInt(sqlStatus.Max, 10)
			row["max_sql"] = sqlStatus.Maxsql
			row["avg"] = strconv.FormatFloat(avg, 'f', 1, 64)
		}

		data = append(data, row)

		TotalSQLs += sqlStatus.Execution
		summary.SQLTypeStats[sqlStatus.Sqltype] += sqlStatus.Execution

	}

	summary.TotalSQLs = humanize.Comma(int64(TotalSQLs))

	// 计算文件总数和文件累计大小
	summary.FileCount = humanize.Comma(int64(sr.TaskSeq))
	summary.TotalFileSize = humanize.Bytes(sr.FileSize)

	// HTML 模板
	const tmpl = `
    <!DOCTYPE html>
    <html lang="en">
    <head>
        <meta charset="UTF-8">
        <title>SQL Analysis Report</title>
        <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
        <style>
            body {
                font-family: Arial, sans-serif;
                margin: 20px;
                background-color: #f5f5f5;
            }
            .container {
                background-color: white;
                border-radius: 8px;
                padding: 20px;
                box-shadow: 0 2px 4px rgba(0,0,0,0.1);
            }
            .control-panel {
                margin-bottom: 20px;
                padding: 15px;
                background-color: #f8f9fa;
                border-radius: 4px;
            }
            .table-container {
                overflow-x: auto;
                max-height: 80vh;
                position: relative;
            }
            table { 
                width: 100%;
                border-collapse: collapse;
                margin-top: 10px;
            }
            thead {
                position: sticky;
                top: 0;
                background-color: #f8f9fa;
                z-index: 1;
            }
            th, td { 
                padding: 12px;
                text-align: left;
                border: 1px solid #dee2e6;
            }
            th { 
                background-color: #f8f9fa;
                cursor: pointer;
                white-space: nowrap;
            }
            th:hover {
                background-color: #e9ecef;
            }
            .sql-cell {
                max-width: 300px;
                overflow: hidden;
                text-overflow: ellipsis;
                white-space: nowrap;
                font-family: monospace;
            }
            .sql-cell.expanded {
                max-width: none;
                white-space: pre-wrap;
                word-break: break-word;
            }
            .toggle-btn {
                display: block;
                color: #0d6efd;
                cursor: pointer;
                font-size: 0.9em;
                margin-top: 4px;
            }
            .toggle-btn:hover {
                text-decoration: underline;
            }
            tr:nth-child(even) {
                background-color: #f8f9fa;
            }
            tr:hover {
                background-color: #f2f2f2;
            }
            .search-box {
                margin: 10px 0;
                padding: 8px;
                width: 200px;
                border: 1px solid #ddd;
                border-radius: 4px;
            }
            .summary-section {
                margin: 20px 0;
                padding: 20px;
                background-color: #f8f9fa;
                border-radius: 8px;
                box-shadow: 0 1px 3px rgba(0,0,0,0.1);
            }
            .summary-grid {
                display: grid;
                grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
                gap: 20px;
                margin-bottom: 20px;
            }
            .summary-item {
                padding: 15px;
                background-color: white;
                border-radius: 4px;
                box-shadow: 0 1px 2px rgba(0,0,0,0.05);
            }
            .summary-item h4 {
                margin: 0 0 10px 0;
                color: #2c3e50;
            }
            .summary-value {
                font-size: 24px;
                font-weight: bold;
                color: #0d6efd;
            }
            .chart-container {
                background-color: white;
                border-radius: 4px;
                box-shadow: 0 1px 2px rgba(0,0,0,0.05);
                padding: 15px;
                margin: 20px 0;
                width: 100%;
            }
            .chart-wrapper {
                width: 400px;
                margin: 0 auto;
            }
            /* SQL 类型样式 */
            .sqltype-cell {
                font-weight: 500;
                padding: 4px 8px;
                border-radius: 4px;
                display: inline-block;
                font-size: 0.9em;
            }
            .sqltype-QUERY { 
                background-color: #cce5ff; 
                color: #004085; 
            }
            .sqltype-DML { 
                background-color: #d4edda; 
                color: #155724; 
            }
            .sqltype-DDL { 
                background-color: #fff3cd; 
                color: #856404; 
            }
            .sqltype-ELSE { 
                background-color: #e2e3e5; 
                color: #383d41; 
            }
            .sqltype-UNKNOWN { 
                background-color: #f8d7da; 
                color: #721c24; 
            }
            .charts-row {
                display: flex;
                justify-content: space-between;
                gap: 20px;
                margin: 20px 0;
            }
            .chart-container {
                background-color: white;
                border-radius: 4px;
                box-shadow: 0 1px 2px rgba(0,0,0,0.05);
                padding: 15px;
                flex: 1;
            }
            .chart-wrapper {
                width: 100%;
                height: 300px;
                margin: 0 auto;
            }
        </style>
    </head>
    <body>
        <div class="container">
            <h1>SQL Analysis Report</h1>

            <!-- Add Summary Section -->
            <div class="summary-section">
                <h3>Analysis Summary</h3>
                <div class="summary-grid">
		          	<div class="summary-item">
                        <h4>File Nums</h4>
                        <div class="summary-value">{{.Summary.FileCount}}</div>
                    </div>
                    <div class="summary-item">
                        <h4>File Size</h4>
                        <div class="summary-value">{{.Summary.TotalFileSize}}</div>
                    </div>
                    <div class="summary-item">
                        <h4>SQL Fingerprints</h4>
                        <div class="summary-value">{{.Summary.FingerprintNum}}</div>
                    </div>
                    <div class="summary-item">
                        <h4>Total SQL Executions</h4>
                        <div class="summary-value">{{.Summary.TotalSQLs}}</div>
                    </div>
                </div>
                
                <div class="charts-row">
                    <!-- SQL Type Distribution Chart -->
                    <div class="chart-container">
                        <h4 style="margin: 0 0 15px 0; color: #2c3e50;">SQL Type Distribution</h4>
                        <div class="chart-wrapper">
                            <canvas id="sqlTypeChart"></canvas>
                        </div>
                    </div>

                    <!-- Table Count Distribution Chart -->
                    <div class="chart-container">
                        <h4 style="margin: 0 0 15px 0; color: #2c3e50;">Nums of Join Tables Distribution</h4>
                        <div class="chart-wrapper">
                            <canvas id="tableCountChart"></canvas>
                        </div>
                    </div>
                </div>
            </div>

            <div class="control-panel">
                <h3>Display Settings</h3>
                
                <div>
                    <input type="text" class="search-box" id="searchInput" placeholder="Search...">
                </div>
                <div>
                    <button onclick="toggleAllColumns(true)">Show All Columns</button>
                    <button onclick="toggleAllColumns(false)">Hide All Columns</button>
                </div>
                <div id="columnToggles">
                    <!-- Column toggle buttons will be generated by JS -->
                </div>
            </div>

            <div class="table-container">
                <table id="sqlTable">
                    <thead>
                        <tr>
                            {{range $index, $header := .Header}}
                                <th onclick="sortTable({{$index}})" 
                                    data-column="{{$header}}">
                                    {{$header}}
                                    <span class="sort-icon">⇅</span>
                                </th>
                            {{end}}
                        </tr>
                    </thead>
                    <tbody>
                        {{range $row := .Data}}
                            <tr>
                                {{range $header := $.Header}}
                                    <td>
                                        {{if or (hasSuffix $header "_sql") (eq $header "fingerprint") (eq $header "tablelist")}}
                                            <div class="sql-cell">{{index $row $header}}</div>
                                            {{if ne (index $row $header) ""}}
                                                <span class="toggle-btn" onclick="toggleCell(this)">Expand</span>
                                            {{end}}
                                        {{else if eq $header "sqltype"}}
                                            <span class="sqltype-cell sqltype-{{index $row $header}}">{{index $row $header}}</span>
                                        {{else}}
                                            {{index $row $header}}
                                        {{end}}
                                    </td>
                                {{end}}
                            </tr>
                        {{end}}
                    </tbody>
                </table>
            </div>
        </div>

        <script>
            // Toggle column visibility
            function toggleColumn(columnName) {
                const table = document.getElementById('sqlTable');
                const index = Array.from(table.rows[0].cells).findIndex(
                    cell => cell.dataset.column === columnName
                );
                if (index > -1) {
                    const isVisible = document.querySelector(` + "`" + `.column-toggle[data-column="${columnName}"]` + "`" + `).checked;
                    Array.from(table.rows).forEach(row => {
                        const cell = row.cells[index];
                        cell.style.display = isVisible ? '' : 'none';
                    });
                }
            }

            // Toggle all columns
            function toggleAllColumns(show) {
                const checkboxes = document.querySelectorAll('.column-toggle');
                checkboxes.forEach(checkbox => {
                    checkbox.checked = show;
                    toggleColumn(checkbox.dataset.column);
                });
            }

            // Generate column toggle buttons
            function generateColumnToggles() {
                const headers = Array.from(document.querySelectorAll('th')).map(th => th.dataset.column);
                const container = document.getElementById('columnToggles');
                
                const groupDiv = document.createElement('div');
                groupDiv.style.marginBottom = '10px';
                
                headers.forEach(column => {
                    const label = document.createElement('label');
                    label.style.marginRight = '15px';
                    
                    const checkbox = document.createElement('input');
                    checkbox.type = 'checkbox';
                    checkbox.checked = true;
                    checkbox.className = 'column-toggle';
                    checkbox.dataset.column = column;
                    checkbox.onchange = () => toggleColumn(column);
                    
                    label.appendChild(checkbox);
                    label.appendChild(document.createTextNode(column));
                    groupDiv.appendChild(label);
                });

                container.appendChild(groupDiv);
            }

            // Table sorting
            function sortTable(colIndex) {
                const table = document.getElementById('sqlTable');
                const tbody = table.querySelector('tbody');
                const rows = Array.from(tbody.rows);
                const th = table.rows[0].cells[colIndex];
                const isAsc = th.classList.contains('asc');

                // Clear all sort indicators
                table.querySelectorAll('th').forEach(header => {
                    header.classList.remove('asc', 'desc');
                });

                // Set new sort direction
                th.classList.add(isAsc ? 'desc' : 'asc');

                rows.sort((a, b) => {
                    let aVal = a.cells[colIndex].innerText.trim();
                    let bVal = b.cells[colIndex].innerText.trim();

                    // Try numeric sort
                    const aNum = parseFloat(aVal);
                    const bNum = parseFloat(bVal);
                    if (!isNaN(aNum) && !isNaN(bNum)) {
                        return isAsc ? bNum - aNum : aNum - bNum;
                    }

                    // String sort
                    return isAsc ? 
                        bVal.localeCompare(aVal) : 
                        aVal.localeCompare(bVal);
                });

                // Reinsert sorted rows
                rows.forEach(row => tbody.appendChild(row));
            }

            // Toggle SQL cell expand/collapse
            function toggleCell(btn) {
                const cell = btn.previousElementSibling;
                if (cell.classList.contains('expanded')) {
                    cell.classList.remove('expanded');
                    btn.textContent = 'Expand';
                } else {
                    cell.classList.add('expanded');
                    btn.textContent = 'Collapse';
                }
            }

            // Search functionality
            document.getElementById('searchInput').addEventListener('input', function(e) {
                const searchText = e.target.value.toLowerCase();
                const tbody = document.querySelector('tbody');
                Array.from(tbody.rows).forEach(row => {
                    const text = Array.from(row.cells)
                        .map(cell => cell.textContent)
                        .join(' ')
                        .toLowerCase();
                    row.style.display = text.includes(searchText) ? '' : 'none';
                });
            });

            // Add Chart Initialization
            document.addEventListener('DOMContentLoaded', function() {
                generateColumnToggles();
                
                // Initialize SQL Type Chart
                const ctx = document.getElementById('sqlTypeChart').getContext('2d');
                new Chart(ctx, {
                    type: 'pie',
                    data: {
                        labels: {{.SQLTypeLabels}},
                        datasets: [{
                            data: {{.SQLTypeValues}},
                            backgroundColor: [
                                '#4e73df', '#1cc88a', '#36b9cc', '#f6c23e', '#e74a3b',
                                '#858796', '#5a5c69', '#2c9faf', '#3c5a9a', '#e83e8c'
                            ]
                        }]
                    },
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        layout: {
                            padding: {
                                left: 20,
                                right: 20
                            }
                        },
                        plugins: {
                            legend: {
                                position: 'right',
                                align: 'center'
                            },
                            tooltip: {
                                callbacks: {
                                    label: function(context) {
                                        const label = context.label || '';
                                        const value = context.parsed;
                                        const total = context.dataset.data.reduce((a, b) => a + b, 0);
                                        const percentage = ((value * 100) / total).toFixed(1);
                                        return ` + "`${label}: ${value} (${percentage}%)`" + `;
                                    }
                                }
                            }
                        }
                    }
                });

                // Initialize Table Count Chart
                const ctxTable = document.getElementById('tableCountChart').getContext('2d');
                new Chart(ctxTable, {
                    type: 'bar',
                    data: {
                        labels: Object.keys({{.Summary.TableNumsStats}}),
                        datasets: [{
                            label: 'SQL Executions',
                            data: Object.values({{.Summary.TableNumsStats}}),
                            backgroundColor: '#36b9cc',
                            borderColor: '#2c9faf',
                            borderWidth: 1
                        }]
                    },
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        scales: {
                            y: {
                                type: 'logarithmic',
                                title: {
                                    display: true,
                                    text: 'SQL Count (log scale)'
                                }
                            },
                            x: {
                                title: {
                                    display: true,
                                    text: 'Number of Tables'
                                }
                            }
                        },
                        plugins: {
                            legend: {
                                display: false
                            },
                            tooltip: {
                                callbacks: {
                                    label: function(context) {
                                        return ` + "`SQL Count: ${context.parsed.y}`" + `;
                                    }
                                }
                            }
                        }
                    }
                });
            });
        </script>
    </body>
    </html>`

	// 准备图表数据
	var sqlTypeLabels []string
	var sqlTypeValues []uint64
	for sqlType, count := range summary.SQLTypeStats {
		sqlTypeLabels = append(sqlTypeLabels, sqlType)
		sqlTypeValues = append(sqlTypeValues, count)
	}

	// 准备模板数据
	templateData := struct {
		Header        []string
		Data          []map[string]string
		Summary       Summary
		SQLTypeLabels []string
		SQLTypeValues []uint64
	}{
		Header:        header,
		Data:          data,
		Summary:       summary,
		SQLTypeLabels: sqlTypeLabels,
		SQLTypeValues: sqlTypeValues,
	}

	// 解析模板
	t := template.Must(template.New("report").Funcs(template.FuncMap{
		"hasSuffix": strings.HasSuffix,
	}).Parse(tmpl))

	// 渲染 HTML
	if err := t.Execute(reportFile, templateData); err != nil {
		sr.logger.Error("Error generating report: " + err.Error())
		return err
	}

	sr.logger.Sugar().Infof("Raw SQL report saved to %s", sr.ParseStatFileDir)
	return nil
}

func (sr *SQLReplayer) replayRawSQL(t *task) error {

	//load raw sql from csv , and locate the postion from metadata
	filePath := t.replayFullPath
	curPos := t.replayFilePos

	fd, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fd.Close()

	_, err = fd.Seek(curPos, 0)

	if err != nil {
		return err
	}

	reader := csv.NewReader(fd)
	reader.LazyQuotes = true

	// init db connection
	dbs, err := connector.InitConnections(sr.Conns)
	if err != nil {
		return err
	}

	defer func() {
		for _, db := range dbs {
			db.Close()
		}
	}()

	//begin time of stats
	begin := time.Now()

	// control currency for replaying
	ch := make(chan struct{}, sr.Thread)
	wg := sync.WaitGroup{}

	//only when file read is done will we try to wait for the rest to finish.  and it must be no more sql on the way
	readFileDone := make(chan struct{}, 1)

	//mark status,if status is 1, then it reach EOF. 2 for "STOP" signal.
	status := 0
	var e error

	//init replay filter
	var filter *regexp.Regexp
	if len(sr.ReplayFilter) > 0 {
		var err error
		filter, err = regexp.Compile("(?i)" + sr.ReplayFilter)
		if err != nil {
			return err
		}
	}

	for {
		select {
		case <-sr.stopchan:
			status = 2
			e = errors.New("STOP")
			break
		default:
			var sqlid, fingerprint string
			record, err := reader.Read()

			if err != nil {
				if errors.Is(err, io.EOF) {
					sr.logger.Sugar().Infoln("reach the end of file.")
					status = 1
					readFileDone <- struct{}{}
					break
				} else {
					sr.logger.Sugar().Infof("failed to read next record,%s", err.Error())
					//also impossible
					status = 3
					e = err
					readFileDone <- struct{}{}
					break
				}
			}

			//first column for raw sql
			//second column for sqlid
			sql := record[0]

			//filter sql
			if filter != nil {
				if filter.MatchString(sql) {
					continue
				}
			}

			//mapping
			sql = utils.SQLMapper(sql, sr.Rules)

			//check if sql is a SELECT statement
			rets, err := utils.GetSQLStatement(sql)

			if err != nil {
				sr.logger.Sugar().Debugf("failed to parse sql [%s],err [%s]. skip it.", sql, err.Error())
				continue
			}

			if len(rets) == 0 {
				sr.logger.Sugar().Debugf("stmt node is empty,skip ip. sql:%s", sql)
				continue
			}

			var mode SQLMode
			switch rets[0] {
			case "DDL":
				mode = DDL
			case "DML":
				mode = DML
			case "QUERY":
				mode = QUERY
			case "ELSE":
				mode = ELSE
			}

			if mode&sr.ReplaySQLType == 0 {
				sr.logger.Sugar().Debugf("skip sql,sql type %s is not match,sql:%s", mode, sql)
				continue
			}

			//generate sqlid,table list for sql
			sqlid, fingerprint = utils.GetQueryID(sql)
			tablelist, _ := utils.ExtractTableNames(sql)

			//we won't modify map sr.SQLID , it is save to read it without lock
			if len(sr.SQLID) > 0 {
				if _, ok := sr.SQLID[sqlid]; !ok {
					continue
				}
			}

			sr.skipMutex.Lock()
			_, ok := sr.skipSQLID[sqlid]
			sr.skipMutex.Unlock()

			//we use skipFlag to markdown wether we should skip this sql
			skipFlag := false
			if ok {
				skipFlag = true
			}

			wg.Add(1)

			ch <- struct{}{}

			go func() {
				defer chanExit(ch)
				defer wg.Done()
				for i := 0; i < sr.Multiplier; i++ {

					connwg := sync.WaitGroup{}

					for ind := range dbs {

						connwg.Add(1)

						go func(index int) {
							defer connwg.Done()

							ctx, cancel := context.WithTimeout(context.Background(), time.Duration(sr.execTimeout)*time.Second)
							defer cancel()

							db := dbs[index]

							errCount := 0
							errStr := ""
							start := time.Now()

							if skipFlag {
								sql = "explain " + sql
							}

							_, err := db.ExecContext(ctx, sql)

							elapsed := time.Since(start)
							elapsedMilliseconds := elapsed.Milliseconds()

							if err != nil {
								sr.logger.Sugar().Infof("error while executing sql. error:%s.sqlid:%s,%s.", err.Error(), sqlid, sql)
								errCount++
								errStr = err.Error()

								if err == context.DeadlineExceeded {

									sr.skipMutex.Lock()

									sr.skipSQLIDCount[sqlid]++
									if sr.skipSQLIDCount[sqlid] > sr.skipCount {
										sr.skipSQLID[sqlid] = struct{}{}
										sr.logger.Sugar().Infof("sqlid:%s timeout more than %d times, skip it.", sqlid, sr.skipCount)
									}
									sr.skipMutex.Unlock()

									errStr = "exec more than " + strconv.Itoa(sr.execTimeout) + "s timeout"
								}
							}

							//if skipFlag is false we always send result to collector
							//if skipFlag is true and we only send result to collector when err !=nil ,that means sql can't be parsed.
							if !skipFlag || (skipFlag && err != nil) {
								s := &sqlStatus{
									Sqlid:           sqlid,
									SQL:             sql,
									Fingerprint:     fingerprint,
									Tablelist:       tablelist,
									Execution:       1,
									TimeElapse:      elapsedMilliseconds,
									Min:             elapsedMilliseconds,
									Max:             elapsedMilliseconds,
									DataSourceIndex: index,
									ErrorCount:      uint64(errCount),
									ErrorStr:        errStr,
								}

								//mark down this is a error sql
								if errCount > 0 {
									s.TimeElapse = -1
									s.Min = -1
									s.Max = -1
								}

								sr.replayCollector <- s
							}

						}(ind)
					}

					connwg.Wait()

				}
			}()

		}

		if status != 0 {
			break
		}

	}

	<-readFileDone
	wg.Wait()

	elapsed := time.Since(begin).Seconds()

	if status == 1 {
		sr.logger.Sugar().Infof("sql replay finish for file %s,time elasped %fs.", t.replayFullPath, elapsed)
		t.replayStatus = true

	} else {
		sr.logger.Sugar().Infof("file %s replay stop.", t.replayFullPath, elapsed)
	}
	return e

}

func chanExit(c chan struct{}) {
	<-c
}

func counter(sr *SQLReplayer) {

	//move outside
	go func() {

		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				sr.mutex.Lock()
				sr.logger.Sugar().Infof("rowCount:%d", sr.ReplayRowCount)
				sr.mutex.Unlock()
			}
		}

	}()

}

func (sr *SQLReplayer) generateReplayReport() error {
	reportFile, err := os.Create(sr.ReplayStatFileDir)
	if err != nil {
		return err
	}
	defer reportFile.Close()

	var data []map[string]string
	header := []string{"sqlid", "fingerprint", "tablelist"}

	// performance summary
	type Summary struct {
		TotalSQLs   []int     // total sql count for each conn
		TotalTime   []int64   // total execution time for each conn
		TotalExec   []uint64  // total execution count for each conn
		TotalError  []uint64  // total error count for each conn
		AvgResponse []float64 // average response time for each conn
		BetterCount []int     // better performance count for each conn
	}

	// calculate performance summary
	connCount := len(sr.Conns)
	summary := Summary{
		TotalSQLs:   make([]int, connCount),
		TotalTime:   make([]int64, connCount),
		TotalExec:   make([]uint64, connCount),
		TotalError:  make([]uint64, connCount),
		AvgResponse: make([]float64, connCount),
		BetterCount: make([]int, connCount),
	}

	// calculate performance summary while constructing data
	for sqlid, dbsToStats := range sr.QueryID2RelayStats {
		// almost impossible dbsToStats nil
		// any sqlid will init its own dbsToStats,but when sql execution failed,it will be nil
		if dbsToStats == nil || dbsToStats[0] == nil {
			continue
		}

		row := map[string]string{
			"sqlid":       sqlid,
			"fingerprint": dbsToStats[0].Fingerprint,
			"tablelist":   dbsToStats[0].Tablelist,
		}

		// calculate average response time for baseline conn(conn_0)
		var baselineAvg float64
		if dbsToStats[0] != nil && dbsToStats[0].Execution > 0 {
			baselineAvg = float64(dbsToStats[0].TimeElapse) / float64(dbsToStats[0].Execution-dbsToStats[0].ErrorCount)
		}

		for i := 0; i < len(dbsToStats); i++ {
			if dbsToStats[i] == nil {
				continue
			}

			// accumulate stats data
			summary.TotalSQLs[i]++
			summary.TotalTime[i] += dbsToStats[i].TimeElapse
			summary.TotalExec[i] += dbsToStats[i].Execution
			summary.TotalError[i] += dbsToStats[i].ErrorCount

			// calculate average response time for current sql in current conn
			currentAvg := float64(dbsToStats[i].TimeElapse) / float64(dbsToStats[i].Execution-dbsToStats[i].ErrorCount)

			// if not baseline conn and better performance, increase better count
			if i > 0 && currentAvg < baselineAvg {
				summary.BetterCount[i]++
			}

			prefix := fmt.Sprintf("conn_%d_", i)
			row[prefix+"min"] = strconv.FormatInt(dbsToStats[i].Min, 10)
			row[prefix+"min_sql"] = dbsToStats[i].Minsql
			row[prefix+"max"] = strconv.FormatInt(dbsToStats[i].Max, 10)
			row[prefix+"max_sql"] = dbsToStats[i].Maxsql
			row[prefix+"avg"] = strconv.FormatFloat(currentAvg, 'f', 1, 64)
			row[prefix+"execution"] = strconv.FormatUint(dbsToStats[i].Execution, 10)
			row[prefix+"error"] = strconv.FormatUint(dbsToStats[i].ErrorCount, 10)
		}
		data = append(data, row)
	}

	// calculate overall average response time
	for i := range summary.AvgResponse {
		if summary.TotalExec[i] > 0 && summary.TotalError[i] != summary.TotalExec[i] {
			summary.AvgResponse[i] = math.Round(float64(summary.TotalTime[i])/float64(summary.TotalExec[i]-summary.TotalError[i])*10) / 10
		}
	}
	// generate table header
	for i := 0; i < len(sr.Conns); i++ {
		prefix := fmt.Sprintf("conn_%d_", i)
		header = append(header,
			prefix+"min",
			prefix+"min_sql",
			prefix+"max",
			prefix+"max_sql",
			prefix+"avg",
			prefix+"execution",
			prefix+"error")
	}

	// HTML template
	const tmpl = `
    <!DOCTYPE html>
    <html lang="en">
    <head>
        <meta charset="UTF-8">
        <title>SQL Replay Report</title>
        <style>
            body {
                font-family: Arial, sans-serif;
                margin: 20px;
                background-color: #f5f5f5;
            }
            .container {
                background-color: white;
                border-radius: 8px;
                padding: 20px;
                box-shadow: 0 2px 4px rgba(0,0,0,0.1);
            }
            .control-panel {
                margin-bottom: 20px;
                padding: 15px;
                background-color: #f8f9fa;
                border-radius: 4px;
            }
            .column-toggle {
                margin: 5px;
                padding: 5px;
            }
            .table-container {
                overflow-x: auto;
                max-height: 80vh;
                position: relative;
            }
            table { 
                width: 100%;
                border-collapse: collapse;
                margin-top: 10px;
                white-space: nowrap;
            }
            thead {
                position: sticky;
                top: 0;
                background-color: #f8f9fa;
                z-index: 1;
            }
            th, td { 
                padding: 12px;
                text-align: left;
                border: 1px solid #dee2e6;
            }
            th { 
                background-color: #f8f9fa;
                cursor: pointer;
                white-space: nowrap;
            }
            th:hover {
                background-color: #e9ecef;
            }
            .sql-cell {
                max-width: 300px;
                overflow: hidden;
                text-overflow: ellipsis;
                white-space: nowrap;
                font-family: monospace;
            }
            .sql-cell.expanded {
                max-width: none;
                white-space: pre-wrap;
                word-break: break-word;
            }
            .toggle-btn {
                display: block;
                color: #0d6efd;
                cursor: pointer;
                font-size: 0.9em;
                margin-top: 4px;
            }
            .toggle-btn:hover {
                text-decoration: underline;
            }
            .conn-group {
                border-left: 2px solid #dee2e6;
                background-color: #fafafa;
            }
            tr:nth-child(even) {
                background-color: #f8f9fa;
            }
            tr:hover {
                background-color: #f2f2f2;
            }
            .search-box {
                margin: 10px 0;
                padding: 8px;
                width: 200px;
                border: 1px solid #ddd;
                border-radius: 4px;
            }

            /* Performance comparison styles */
            .performance-better {
                background-color: #e6ffe6 !important;
            }
            .performance-worse {
                background-color: #ffe6e6 !important;
            }
            
            /* Performance difference indicator */
            .diff-indicator {
                font-size: 0.8em;
                margin-left: 5px;
            }
            .diff-better {
                color: #28a745;
            }
            .diff-worse {
                color: #dc3545;
            }

            /* Quick filter buttons */
            .quick-filters {
                margin: 10px 0;
            }
            .quick-filter-btn {
                margin: 0 5px;
                padding: 8px 15px;
                border: none;
                border-radius: 4px;
                background-color: #f8f9fa;
                cursor: pointer;
            }
            .quick-filter-btn:hover {
                background-color: #e9ecef;
            }
            .quick-filter-btn.active {
                background-color: #007bff;
                color: white;
            }

            /* Performance summary styles */
            .performance-summary {
                margin: 20px 0;
                padding: 20px;
                background-color: #f8f9fa;
                border-radius: 8px;
            }
            .performance-summary h3 {
                margin: 0 0 20px 0;
                color: #2c3e50;
                font-size: 1.5em;
            }
            .summary-grid {
                display: grid;
                grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
                gap: 20px;
            }
            .summary-item {
                padding: 20px;
                background-color: white;
                border-radius: 8px;
                box-shadow: 0 2px 8px rgba(0,0,0,0.05);
            }
            .summary-item h4 {
                margin: 0 0 15px 0;
                color: #2c3e50;
                font-size: 1.2em;
                border-bottom: 2px solid #e9ecef;
                padding-bottom: 10px;
            }
            .conn-info {
                margin: 15px 0;
                font-size: 0.95em;
                color: #495057;
            }
            .conn-info-item {
                display: flex;
                align-items: center;
                margin: 8px 0;
                padding: 6px 10px;
                background-color: #f8f9fa;
                border-radius: 4px;
            }
            .conn-info-label {
                font-weight: 600;
                min-width: 50px;
                margin-right: 10px;
                color: #6c757d;
            }
            .conn-info-value {
                font-family: monospace;
                color: #212529;
            }
            .summary-stats {
                margin-top: 15px;
                padding-top: 15px;
                border-top: 1px solid #e9ecef;
            }
            .stat-item {
                display: flex;
                justify-content: space-between;
                align-items: center;
                margin: 10px 0;
                padding: 8px 12px;
                background-color: #f8f9fa;
                border-radius: 4px;
                transition: background-color 0.2s;
            }
            .stat-item:hover {
                background-color: #e9ecef;
            }
            .stat-label {
                font-weight: 500;
                color: #495057;
            }
            .stat-value {
                font-weight: 600;
                color: #0d6efd;
            }
            .better-count {
                color: #198754;
            }
            .error-count {
                color: #dc3545;
            }
            .charts-row {
                display: flex;
                justify-content: space-between;
                gap: 20px;
                margin: 20px 0;
            }
            .chart-container {
                background-color: white;
                border-radius: 4px;
                box-shadow: 0 1px 2px rgba(0,0,0,0.05);
                padding: 15px;
                flex: 1;
            }
            .chart-wrapper {
                width: 100%;
                height: 300px;
                margin: 0 auto;
            }
        </style>
    </head>
    <body>
        <div class="container">
            <h1>SQL Replay Report</h1>

            <!-- Add Performance Summary Section -->
            <div class="performance-summary">
                <h3>Performance Summary</h3>
                <div class="summary-grid">
                    {{range $i := .ConnIndices}}
                        <div class="summary-item">
                            <h4>Connection {{$i}}{{if eq $i 0}} (Baseline){{end}}</h4>
                            <div class="conn-info">
                                <div class="conn-info-item">
                                    <span class="conn-info-label">IP:</span>
                                    <span class="conn-info-value">{{index $.Conns $i | getIP}}</span>
                                </div>
                                <div class="conn-info-item">
                                    <span class="conn-info-label">Port:</span>
                                    <span class="conn-info-value">{{index $.Conns $i | getPort}}</span>
                                </div>
                                <div class="conn-info-item">
                                    <span class="conn-info-label">User:</span>
                                    <span class="conn-info-value">{{index $.Conns $i | getUser}}</span>
                                </div>
                            </div>
                            <div class="summary-stats">
                                <div class="stat-item">
                                    <span class="stat-label">Total SQLs</span>
                                    <span class="stat-value">{{index $.Summary.TotalSQLs $i}}</span>
                                </div>
                                <div class="stat-item">
                                    <span class="stat-label">Avg Response</span>
                                    <span class="stat-value">{{index $.Summary.AvgResponse $i}}ms</span>
                                </div>
                                <div class="stat-item">
                                    <span class="stat-label">Error Count</span>
                                    <span class="stat-value error-count">{{index $.Summary.TotalError $i}}</span>
                                </div>
                                {{if ne $i 0}}
                                    <div class="stat-item">
                                        <span class="stat-label">Better Performance</span>
                                        <span class="stat-value better-count">{{index $.Summary.BetterCount $i}}</span>
                                    </div>
                                {{end}}
                            </div>
                        </div>
                    {{end}}
                </div>
            </div>

            <div class="control-panel">
                <h3>Display Settings</h3>
                
                <!-- Add Quick Filters -->
                <div class="quick-filters">
                    <button class="quick-filter-btn active" onclick="applyFilter('all')">All SQLs</button>
                    {{range $i := .ConnIndices}}
                        {{if ne $i 0}}
                            <button class="quick-filter-btn" onclick="applyFilter('better-{{$i}}')">
                                Better in Conn {{$i}}
                            </button>
                            <button class="quick-filter-btn" onclick="applyFilter('worse-{{$i}}')">
                                Worse in Conn {{$i}}
                            </button>
                        {{end}}
                    {{end}}
                </div>

                <div>
                    <input type="text" class="search-box" id="searchInput" placeholder="Search...">
                </div>
                <div>
                    <button onclick="toggleAllColumns(true)">Show All Columns</button>
                    <button onclick="toggleAllColumns(false)">Hide All Columns</button>
                </div>
                <div id="columnToggles">
                    <!-- Column toggle buttons will be generated by JS -->
                </div>
            </div>

            <div class="table-container">
                <table id="sqlTable">
                    <thead>
                        <tr>
                            {{range $index, $header := .Header}}
                                <th onclick="sortTable({{$index}})" 
                                    class="{{if hasPrefix $header "conn_"}}conn-group{{end}}"
                                    data-column="{{$header}}">
                                    {{$header}}
                                    <span class="sort-icon">⇅</span>
                                </th>
                            {{end}}
                        </tr>
                    </thead>
                    <tbody>
                        {{range $row := .Data}}
                            <tr>
                                {{range $header := $.Header}}
                                    <td class="{{if hasPrefix $header "conn_"}}conn-group{{end}}"
                                        {{if hasPrefix $header "conn_"}}
                                            data-conn="{{index (split $header "_") 1}}"
                                            data-type="{{last (split $header "_")}}"
                                        {{end}}>
                                        {{if or (hasSuffix $header "_sql") (eq $header "fingerprint") (eq $header "tablelist")}}
                                            <div class="sql-cell">{{index $row $header}}</div>
                                            {{if ne (index $row $header) ""}}
                                                <span class="toggle-btn" onclick="toggleCell(this)">Expand</span>
                                            {{end}}
                                        {{else if eq $header "sqltype"}}
                                            <span class="sqltype-cell sqltype-{{index $row $header}}">{{index $row $header}}</span>
                                        {{else}}
                                            {{index $row $header}}
                                        {{end}}
                                    </td>
                                {{end}}
                            </tr>
                        {{end}}
                    </tbody>
                </table>
            </div>
        </div>

        <script>
            // Toggle column visibility
            function toggleColumn(columnName) {
                const table = document.getElementById('sqlTable');
                const index = Array.from(table.rows[0].cells).findIndex(
                    cell => cell.dataset.column === columnName
                );
                if (index > -1) {
                    const isVisible = document.querySelector(` + "`" + `.column-toggle[data-column="${columnName}"]` + "`" + `).checked;
                    Array.from(table.rows).forEach(row => {
                        const cell = row.cells[index];
                        cell.style.display = isVisible ? '' : 'none';
                    });
                }
            }

            // Toggle all columns
            function toggleAllColumns(show) {
                const checkboxes = document.querySelectorAll('.column-toggle');
                checkboxes.forEach(checkbox => {
                    checkbox.checked = show;
                    toggleColumn(checkbox.dataset.column);
                });
            }

            // Generate column toggle buttons
            function generateColumnToggles() {
                const headers = Array.from(document.querySelectorAll('th')).map(th => th.dataset.column);
                const container = document.getElementById('columnToggles');
                
                // Group by connection
                const groups = {};
                headers.forEach(header => {
                    const group = header.startsWith('conn_') ? header.split('_')[1] : 'basic';
                    if (!groups[group]) {
                        groups[group] = [];
                    }
                    groups[group].push(header);
                });

                // Create toggle buttons for each group
                Object.entries(groups).forEach(([group, columns]) => {
                    const groupDiv = document.createElement('div');
                    groupDiv.style.marginBottom = '10px';
                    
                    const groupLabel = document.createElement('strong');
                    groupLabel.textContent = group === 'basic' ? 'Basic Info: ' : ` + "`Connection ${group}: `" + `;
                    groupDiv.appendChild(groupLabel);

                    columns.forEach(column => {
                        const label = document.createElement('label');
                        label.style.marginRight = '15px';
                        
                        const checkbox = document.createElement('input');
                        checkbox.type = 'checkbox';
                        checkbox.checked = true;
                        checkbox.className = 'column-toggle';
                        checkbox.dataset.column = column;
                        checkbox.onchange = () => toggleColumn(column);
                        
                        label.appendChild(checkbox);
                        label.appendChild(document.createTextNode(column));
                        groupDiv.appendChild(label);
                    });

                    container.appendChild(groupDiv);
                });

                // Add event listeners for show/hide all buttons
                document.querySelector('button[onclick="toggleAllColumns(true)"]').addEventListener('click', () => toggleAllColumns(true));
                document.querySelector('button[onclick="toggleAllColumns(false)"]').addEventListener('click', () => toggleAllColumns(false));
            }

            // Table sorting
            function sortTable(colIndex) {
                const table = document.getElementById('sqlTable');
                const tbody = table.querySelector('tbody');
                const rows = Array.from(tbody.rows);
                const th = table.rows[0].cells[colIndex];
                const isAsc = th.classList.contains('asc');

                // Clear all sort indicators
                table.querySelectorAll('th').forEach(header => {
                    header.classList.remove('asc', 'desc');
                });

                // Set new sort direction
                th.classList.add(isAsc ? 'desc' : 'asc');

                rows.sort((a, b) => {
                    let aVal = a.cells[colIndex].innerText.trim();
                    let bVal = b.cells[colIndex].innerText.trim();

                    // Try numeric sort
                    const aNum = parseFloat(aVal);
                    const bNum = parseFloat(bVal);
                    if (!isNaN(aNum) && !isNaN(bNum)) {
                        return isAsc ? bNum - aNum : aNum - bNum;
                    }

                    // String sort
                    return isAsc ? 
                        bVal.localeCompare(aVal) : 
                        aVal.localeCompare(bVal);
                });

                // Reinsert sorted rows
                rows.forEach(row => tbody.appendChild(row));
            }

            // Toggle SQL cell expand/collapse
            function toggleCell(btn) {
                const cell = btn.previousElementSibling;
                if (cell.classList.contains('expanded')) {
                    cell.classList.remove('expanded');
                    btn.textContent = 'Expand';
                } else {
                    cell.classList.add('expanded');
                    btn.textContent = 'Collapse';
                }
            }

            // Search functionality
            document.getElementById('searchInput').addEventListener('input', function(e) {
                const searchText = e.target.value.toLowerCase();
                const tbody = document.querySelector('tbody');
                Array.from(tbody.rows).forEach(row => {
                    const text = Array.from(row.cells)
                        .map(cell => cell.textContent)
                        .join(' ')
                        .toLowerCase();
                    row.style.display = text.includes(searchText) ? '' : 'none';
                });
            });

            // Add performance comparison functions
            function calculatePerformanceDiff(row) {
                const avgCells = Array.from(row.querySelectorAll('td[data-type="avg"]'));
                if (avgCells.length < 2) return;

                const baselineAvg = parseFloat(avgCells[0].textContent);
                if (isNaN(baselineAvg)) return;

                avgCells.forEach((cell, i) => {
                    if (i === 0) return; // Skip baseline
                    const currentAvg = parseFloat(cell.textContent);
                    if (isNaN(currentAvg)) return;

                    const diff = ((currentAvg - baselineAvg) / baselineAvg * 100).toFixed(1);
                    const indicator = document.createElement('span');
                    indicator.className = ` + "`diff-indicator ${diff < 0 ? 'diff-better' : 'diff-worse'}`" + `;
                    indicator.textContent = ` + "`${diff > 0 ? '+' : ''}${diff}%`" + `;
                    cell.appendChild(indicator);
                });
            }

            function applyFilter(filterType) {
                const tbody = document.querySelector('tbody');
                const rows = Array.from(tbody.rows);

                // Update active filter button
                document.querySelectorAll('.quick-filter-btn').forEach(btn => {
                    btn.classList.remove('active');
                });
                event.target.classList.add('active');

                rows.forEach(row => {
                    const avgCells = Array.from(row.querySelectorAll('td[data-type="avg"]'));
                    if (avgCells.length < 2) return;

                    const baselineAvg = parseFloat(avgCells[0].textContent);
                    if (isNaN(baselineAvg)) return;

                    let show = true;
                    if (filterType === 'all') {
                        show = true;
                    } else if (filterType.startsWith('better-')) {
                        const connIndex = parseInt(filterType.split('-')[1]);
                        const compAvg = parseFloat(avgCells[connIndex].textContent);
                        show = !isNaN(compAvg) && compAvg < baselineAvg;
                    } else if (filterType.startsWith('worse-')) {
                        const connIndex = parseInt(filterType.split('-')[1]);
                        const compAvg = parseFloat(avgCells[connIndex].textContent);
                        show = !isNaN(compAvg) && compAvg > baselineAvg;
                    }
                    
                    row.style.display = show ? '' : 'none';
                });
            }

            // Initialize
            document.addEventListener('DOMContentLoaded', function() {
                generateColumnToggles();
                
                // Calculate and show performance differences
                const rows = document.querySelectorAll('tbody tr');
                rows.forEach(calculatePerformanceDiff);

            });
        </script>
    </body>
    </html>`

	// prepare template data
	templateData := struct {
		Header      []string
		Data        []map[string]string
		ConnIndices []int
		Summary     Summary
		Conns       []connector.Param // add conn info
	}{
		Header:      header,
		Data:        data,
		ConnIndices: make([]int, connCount),
		Summary:     summary,
		Conns:       sr.Conns, // conn info
	}
	for i := range templateData.ConnIndices {
		templateData.ConnIndices[i] = i
	}

	// parse template
	t := template.Must(template.New("report").Funcs(template.FuncMap{
		"hasSuffix": strings.HasSuffix,
		"hasPrefix": strings.HasPrefix,
		"split":     strings.Split,
		"last": func(s []string) string {
			if len(s) > 0 {
				return s[len(s)-1]
			}
			return ""
		},
		"getIP": func(conn connector.Param) string {
			return conn.Ip
		},
		"getPort": func(conn connector.Param) string {
			return conn.Port
		},
		"getUser": func(conn connector.Param) string {
			return conn.User
		},
	}).Parse(tmpl))

	// render HTML
	if err := t.Execute(reportFile, templateData); err != nil {
		sr.logger.Error("Error generating report: " + err.Error())
		return err
	}

	sr.logger.Sugar().Infof("Replay report saved to %s", sr.ReplayStatFileDir)
	return nil
}

type jobStatus struct {
	ExecMode     string
	TaskNumTotal int
	Finish       int
	FileSize     uint64
}

func (sr *SQLReplayer) ShowJobStatus() jobStatus {

	js := jobStatus{}

	sr.mutex.Lock()

	//analyze or both
	if sr.ExecMode&model.ANALYZE != 0 {
		if sr.ExecMode&model.REPLAY != 0 {
			js.ExecMode = "both"
		} else {
			js.ExecMode = "analyze"
		}
	} else {
		js.ExecMode = "replay"
	}

	js.FileSize = sr.FileSize

	for _, t := range sr.tasks {

		//for "replay" taks
		if sr.ExecMode&model.REPLAY != 0 {

			if t.replayStatus {
				js.Finish += 1
			}
		} else {
			if t.parseStatus {
				js.Finish += 1
			}

		}
		js.TaskNumTotal += 1
	}

	sr.mutex.Unlock()

	return js
}

// return tableList and execution for this table composition
func (sr *SQLReplayer) ShowTableJoinFrequency() map[string]uint64 {

	ret := make(map[string]uint64)

	sr.mutex.Lock()

	for _, sqlStatus := range sr.SqlID2Fingerprint {
		_, ok := ret[sqlStatus.Tablelist]
		if ok {
			ret[sqlStatus.Tablelist] = sqlStatus.Execution + ret[sqlStatus.Tablelist]
		} else {
			ret[sqlStatus.Tablelist] = sqlStatus.Execution
		}
	}

	sr.mutex.Unlock()

	return ret
}
