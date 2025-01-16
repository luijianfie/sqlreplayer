package sqlreplayer

import (
	"bytes"
	"encoding/csv"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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

const TASK_CHAN_LENGTH = 20

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
	sqlid       string
	fingerprint string
	Tablelist   string
	Sqltype     string
	min         uint64
	max         uint64
	timeElapse  uint64
	minsql      string
	maxsql      string
	execution   uint64
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
	QueryOnly          bool
	DryRun             bool
	DrawPic            bool
	Currency           int //num of worker
	Multiplier         int //multiplier
	Thread             int //thread pool for one database instance
	wg                 sync.WaitGroup

	// timestamp for task
	Current time.Time

	Dir               string
	ReplayStatFileDir string
	replayStatFile    *os.File

	SqlID2Fingerprint map[string]*sqlStatus // map[SQLID] -> fingerprint

	// //analyze pharse for generating report
	// SqlID2ReplayUints map[string][]replayUnit // map[SQLID] -> [ru1,ru2,ru3,ru4...]

	QueryID2RelayStats map[string][]*sqlStatus
	//replayer pharse for generating report
	//map[SQLID]->[map[db1],map[db2]...]       map[db1]->[ru1,ru2,ru3,ru4...]
	// QueryID2RelayStats map[string][][]replayUnit

	logger *zap.Logger `json:"-"`

	//controller
	stopchan chan struct{}
	donechan chan struct{}
	taskchan chan int

	//Status
	Status         Status
	ReplayRowCount uint64
}

func NewSQLReplayer(jobSeq uint64, c *model.Config) (*SQLReplayer, error) {

	var err error
	var sr SQLReplayer

	if len(c.Metafile) > 0 {

		sr = SQLReplayer{
			logger:   c.Logger,
			stopchan: make(chan struct{}),
			donechan: make(chan struct{}),
			taskchan: make(chan int, TASK_CHAN_LENGTH),
		}
		sr.logger.Sugar().Infof("begin to load meta file from %s", c.Metafile)
		err := sr.Load(c.Metafile)

		//set status for task
		sr.Status = PROCESSING

		if err != nil {
			return nil, err
		}

	} else {

		sr = SQLReplayer{
			JobSeq:             jobSeq,
			LogType:            c.LogType,
			Current:            time.Now(),
			SqlID2Fingerprint:  make(map[string]*sqlStatus),
			SaveRawSQLInReport: c.SaveRawSQLInReport,
			GenerateReport:     c.GenerateReport,
			QueryOnly:          c.QueryOnly,
			DryRun:             c.DryRun,
			DrawPic:            c.DrawPic,
			QueryID2RelayStats: make(map[string][]*sqlStatus),
			Currency:           c.WorkerNum,
			Multiplier:         c.Multi,
			Thread:             c.Thread,

			logger:   c.Logger,
			stopchan: make(chan struct{}),
			donechan: make(chan struct{}),
			taskchan: make(chan int, TASK_CHAN_LENGTH),

			Status: PROCESSING,
		}

		sr.Dir = c.Dir + fmt.Sprintf("sqlreplayer_task_%d", sr.JobSeq)

		err = os.MkdirAll(sr.Dir, os.ModePerm)
		if err != nil {
			return nil, errors.New("failed to create directory: " + sr.Dir)
		}

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
		}
		if sr.ExecMode&model.REPLAY != 0 {
			if len(c.Conns) == 0 {
				return nil, errors.New("replay mode: conn are needed.")
			}

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
			sr.ReplayStatFileDir = sr.Dir + "/replay_stats.csv"

			statFile, err := os.Create(sr.ReplayStatFileDir)
			if err != nil {
				return nil, err
			}
			sr.replayStatFile = statFile

		}

		//parse time
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

func (sr *SQLReplayer) Start() {

	sr.wg.Add(sr.Currency)

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
				generateReplayReport(sr)
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

	if sr.ExecMode&model.ANALYZE != 0 {
		t.parseFullPath = fileFullPath
		t.parseFilename = filepath.Base(fileFullPath)
		t.replayFilename = "rawsql_" + strconv.Itoa(t.taskSeq) + ".csv"
		t.replayFullPath = filepath.Join(sr.Dir, t.replayFilename)
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
	if sr.replayStatFile != nil {
		sr.replayStatFile.Close()
	}
	close(sr.donechan)
}

// analyze will parse file and save it in csv format
// param [in] file: log file dir
func (sr *SQLReplayer) analyze(t *task) error {

	file := t.parseFullPath
	pos := t.parseFilePos

	l := sr.logger

	l.Sugar().Infof("begin to %s %s from pos %d", sr.ExecMode, file, pos)

	//output file in analyze phrase is replay full path in "replay" phrase.
	rawSQLFileDir := t.replayFullPath

	rawSQLFile, err := os.OpenFile(rawSQLFileDir, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		sr.logger.Info(err.Error())
		return err
	}
	defer rawSQLFile.Close()

	csvWriter := csv.NewWriter(rawSQLFile)
	defer csvWriter.Flush()

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

		//save to raw sql file
		err := csvWriter.Write([]string{cu.Argument, cu.QueryID, cu.Time.Format("20060102 15:04:05"), cu.CommandType, cu.TableList, strconv.FormatFloat(cu.Elapsed*1000, 'f', 2, 64)})
		if err != nil {
			return err
		}

		_, ok := tmpSqlID2Fingerprint[cu.QueryID]
		if !ok {
			s := &sqlStatus{
				sqlid:       cu.QueryID,
				fingerprint: fingerprint,
				execution:   1,
				timeElapse:  uint64(cu.Elapsed * 1000),
				Tablelist:   cu.TableList,
				min:         uint64(cu.Elapsed * 1000),
				max:         uint64(cu.Elapsed * 1000),
			}
			if sr.GenerateReport {
				s.minsql = cu.Argument
				s.maxsql = cu.Argument
			}
			tmpSqlID2Fingerprint[cu.QueryID] = s
		} else {

			elapse := uint64(cu.Elapsed * 1000)
			s := tmpSqlID2Fingerprint[cu.QueryID]
			s.execution++
			s.timeElapse += elapse

			if s.min > elapse {
				s.min = elapse
				if sr.GenerateReport {
					s.minsql = cu.Argument
				}
			}

			if s.max < elapse {
				s.max = elapse
				if sr.GenerateReport {
					s.maxsql = cu.Argument
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

			s.execution += v.execution
			s.timeElapse += v.timeElapse

			if s.min > v.min {
				s.min = v.min
				s.minsql = v.minsql
			}

			if s.max < v.max {
				s.max = v.max
				s.maxsql = v.maxsql
			}

		}
	}

	sr.mutex.Unlock()

	if err == nil {
		t.parseStatus = true
	}

	return err
}

func (sr *SQLReplayer) generateRawSQLReport() error {

	analyzeReportFile := sr.Dir + "/rawsql_analyze_report.csv"
	reportFile, err := os.Create(analyzeReportFile)
	if err != nil {
		return err
	}
	defer reportFile.Close()
	writer := csv.NewWriter(reportFile)
	defer writer.Flush()

	header := []string{"sqlid", "fingerprint", "sqltype", "tablelist"}
	subHeaders := []string{"min(ms)", "min-sql",
		"max(ms)", "max-sql",
		"avg(ms)", "execution"}
	header = append(header, subHeaders...)

	err = writer.Write(header)
	if err != nil {
		panic(err)
	}

	for sqlID, sqlStatus := range sr.SqlID2Fingerprint {
		row := []string{sqlID, sqlStatus.fingerprint, sqlStatus.Sqltype, sqlStatus.Tablelist}

		row = append(row, []string{
			strconv.FormatUint(sqlStatus.min, 10), sqlStatus.minsql,
			strconv.FormatUint(sqlStatus.max, 10), sqlStatus.maxsql,
			strconv.FormatFloat(float64(sqlStatus.timeElapse)/float64(sqlStatus.execution), 'f', 2, 64), strconv.FormatUint(sqlStatus.execution, 10)}...)
		err = writer.Write(row)
		if err != nil {
			return err
		}
	}
	sr.logger.Sugar().Infof("raw sql report save to %s", analyzeReportFile)

	return nil
}

func analyzer(arr []replayUnit) (min, p25, p50, p75, p90, p99, max replayUnit, count uint64, average float64, seqs []uint64) {

	if len(arr) < 1 {
		return
	}
	seqs = make([]uint64, len(arr))

	sum := uint64(0)
	for i, sr := range arr {
		sum += sr.Time
		seqs[i] = sr.Time
	}

	sort.Slice(arr, func(i, j int) bool {
		return arr[i].Time < arr[j].Time
	})

	index := int(float64(len(arr)-1) * 0.25)
	p25 = arr[index]

	index = int(float64(len(arr)-1) * 0.50)
	p50 = arr[index]

	index = int(float64(len(arr)-1) * 0.75)
	p75 = arr[index]

	index = int(float64(len(arr)-1) * 0.90)
	p90 = arr[index]

	index = int(float64(len(arr)-1) * 0.99)
	p99 = arr[index]

	min = arr[0]
	max = arr[len(arr)-1]

	average = float64(sum) / float64(len(arr))
	count = uint64(len(arr))
	return
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

	for {
		select {
		case <-sr.stopchan:
			for _, db := range dbs {
				db.Close()
			}
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

			//check if sql is a SELECT statement
			if sr.QueryOnly {
				rets, err := utils.IsSelectStatement(sql)

				if err != nil {
					sr.logger.Sugar().Debugf("failed to parse sql [%s],err [%s]. skip it.", sql, err.Error())
					continue
				}

				//not select statement,skip it
				if len(rets) == 0 || !rets[0] {
					sr.logger.Sugar().Debugf("not select statement,skip ip. sql:%s", sql)
					continue
				}
			}

			//generate sqlid,table list for sql
			sqlid, fingerprint = utils.GetQueryID(sql)
			tablelist, _ := utils.ExtractTableNames(sql)

			wg.Add(1)
			sr.mutex.Lock()
			sr.ReplayRowCount++

			// _, ok := sr.SqlID2Fingerprint[sqlid]
			// if !ok {
			// 	sr.SqlID2Fingerprint[sqlid] = &sqlStatus{fingerprint: fingerprint}
			// }

			_, ok := sr.QueryID2RelayStats[sqlid]
			if !ok {
				sr.QueryID2RelayStats[sqlid] = make([]*sqlStatus, len(sr.Conns))
			}

			sr.mutex.Unlock()

			ch <- struct{}{}

			go func() {
				defer chanExit(ch)
				defer wg.Done()
				for i := 0; i < sr.Multiplier; i++ {

					for ind, db := range dbs {

						start := time.Now()

						_, err := db.Exec(sql)

						if err != nil {
							sr.logger.Sugar().Infof("error while executing sql. error:%s. \n sqlid:%s,%s", sqlid, sql, err.Error())
							continue
						}

						elapsed := time.Since(start)
						elapsedMilliseconds := elapsed.Milliseconds()

						sr.mutex.Lock()

						if sr.QueryID2RelayStats[sqlid][ind] == nil {
							s := &sqlStatus{
								sqlid:       sqlid,
								fingerprint: fingerprint,
								Tablelist:   tablelist,
								execution:   1,
								timeElapse:  uint64(elapsedMilliseconds),
								min:         uint64(elapsedMilliseconds),
								max:         uint64(elapsedMilliseconds),
							}

							if sr.SaveRawSQLInReport {
								s.minsql = sql
								s.maxsql = sql
							}
							sr.QueryID2RelayStats[sqlid][ind] = s
						} else {
							s := sr.QueryID2RelayStats[sqlid][ind]
							s.execution += 1
							s.timeElapse += uint64(elapsedMilliseconds)

							if s.min > uint64(elapsedMilliseconds) {
								s.min = uint64(elapsedMilliseconds)
								if sr.SaveRawSQLInReport {
									s.minsql = sql
								}
							}

							if s.max < uint64(elapsedMilliseconds) {
								s.max = uint64(elapsedMilliseconds)
								if sr.SaveRawSQLInReport {
									s.maxsql = sql
								}
							}
						}
						sr.mutex.Unlock()
					}

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

func generateReplayReport(sr *SQLReplayer) error {
	// sr.mutex.Lock()
	// defer sr.mutex.Unlock()

	if sr.ReplayRowCount > 0 && !sr.DryRun {

		writer := csv.NewWriter(sr.replayStatFile)
		defer writer.Flush()

		sr.logger.Sugar().Infof("begin to generate report for replay phrase. num of sqlid %d.", len(sr.SqlID2Fingerprint))

		header := []string{"sqlid", "fingerprint", "sqltype", "tablelist"}
		for i := 0; i < len(sr.Conns); i++ {
			prefix := fmt.Sprintf("conn_%d_", i)
			subHeaders := []string{prefix + "min(ms)", prefix + "min-sql",
				prefix + "max(ms)", prefix + "max-sql",
				prefix + "avg(ms)", prefix + "execution"}
			header = append(header, subHeaders...)
		}
		err := writer.Write(header)
		if err != nil {
			return err
		}

		for sqlid, dbsToStats := range sr.QueryID2RelayStats {

			//in some cases, failed to execute sql
			//dbsToStats[0] may be nil,skip this
			if dbsToStats == nil {
				continue
			}
			row := []string{sqlid, dbsToStats[0].fingerprint, dbsToStats[0].Sqltype, dbsToStats[0].Tablelist}
			for i := 0; i < len(dbsToStats); i++ {
				row = append(row, []string{
					strconv.FormatUint(dbsToStats[i].min, 10), dbsToStats[i].minsql,
					strconv.FormatUint(dbsToStats[i].max, 10), dbsToStats[i].maxsql,
					strconv.FormatFloat(float64(dbsToStats[i].timeElapse)/float64(dbsToStats[i].execution), 'f', 2, 64), strconv.FormatUint(dbsToStats[i].execution, 10)}...)
				// if sr.DrawPic {
				// 	fname := "Conn" + strconv.Itoa(i) + "_" + sqlid
				// 	utils.Draw(fname, sr.Dir+"/"+fname, seqs, float64(sr25.Time), float64(sr50.Time), float64(sr75.Time), float64(sr90.Time))
				// 	sr.logger.Sugar().Infoln("draw picture for sqlid:" + sqlid + " to " + sr.Dir + "/" + fname + ".png.")
				// }
			}
			err = writer.Write(row)
			if err != nil {
				return err
			}

		}
		sr.logger.Sugar().Infof("save replay result to %s.", sr.ReplayStatFileDir)

	}

	return nil
}
