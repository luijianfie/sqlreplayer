package sqlreplayer

import (
	"bytes"
	"encoding/csv"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luijianfie/sqlreplayer/connection"
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
	EXIT
)

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

type SQLReplayer struct {
	JobSeq, TaskSeq uint64 //jobSeq is global uniq id , taskSeq is uniq id in a job

	mutex sync.Mutex

	ExecMode model.ExecType

	LogType string            //file type
	parser  parser.FileParser //parser for file type

	ParserFileList, ReplayFileList    []string
	ParserFilStatus, ReplayFileStatus []bool  // 1 for finished
	ParserFilePos, ReplayFilePos      []int64 // pos for file

	// user define time range
	Begin time.Time
	End   time.Time

	Conns []connection.Param

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

	SqlID2Fingerprint map[string]string // map[SQLID] -> fingerprint

	//analyze pharse for generating report
	SqlID2ReplayUints map[string][]replayUnit // map[SQLID] -> [ru1,ru2,ru3,ru4...]

	//replayer pharse for generating report
	//map[SQLID]->[map[db1],map[db2]...]       map[db1]->[ru1,ru2,ru3,ru4...]
	QueryID2RelayStats map[string][][]replayUnit

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
		}
		sr.logger.Sugar().Infof("begin to load meta file from %s", c.Metafile)
		err := sr.Load(c.Metafile)

		//set status for task
		sr.Status = PROCESSING

		if err != nil {
			return nil, err
		}

		if len(sr.ParserFileList) > 0 {
			sr.taskchan = make(chan int, len(sr.ParserFileList))
		} else {
			sr.taskchan = make(chan int, len(sr.ReplayFileList))
		}

	} else {

		sr = SQLReplayer{
			JobSeq:             jobSeq,
			LogType:            c.LogType,
			Current:            time.Now(),
			SqlID2Fingerprint:  make(map[string]string),
			SaveRawSQLInReport: c.SaveRawSQLInReport,
			GenerateReport:     c.GenerateReport,
			QueryOnly:          c.QueryOnly,
			DryRun:             c.DryRun,
			DrawPic:            c.DrawPic,
			SqlID2ReplayUints:  make(map[string][]replayUnit),
			QueryID2RelayStats: make(map[string][][]replayUnit),
			Currency:           c.WorkerNum,
			Multiplier:         c.Multi,

			logger:   c.Logger,
			stopchan: make(chan struct{}),
			donechan: make(chan struct{}),
			taskchan: make(chan int, len(c.FileList)),

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
			sr.ParserFileList = c.FileList
			sr.ParserFilStatus = make([]bool, len(sr.ParserFileList))
			sr.ParserFilePos = make([]int64, len(sr.ParserFileList))
		case "REPLAY":
			sr.ExecMode = model.REPLAY
			sr.ReplayFileList = c.FileList
			sr.ReplayFileStatus = make([]bool, len(sr.ReplayFileList))
			sr.ReplayFilePos = make([]int64, len(sr.ReplayFileList))

		case "BOTH":
			sr.ExecMode = model.ANALYZE | model.REPLAY
			sr.ParserFileList = c.FileList
			sr.ParserFilStatus = make([]bool, len(sr.ParserFileList))
			sr.ParserFilePos = make([]int64, len(sr.ParserFileList))

			sr.ReplayFileList = make([]string, len(sr.ParserFileList))
			sr.ReplayFileStatus = make([]bool, len(sr.ReplayFileList))
			sr.ReplayFilePos = make([]int64, len(sr.ReplayFileList))

		default:
			return nil, errors.New(" unknown exec type")
		}

		if sr.ExecMode&model.ANALYZE != 0 {
			if len(c.FileList) == 0 || len(c.LogType) == 0 {
				return nil, errors.New("analyze: filename and log type are need.")
			}
			sr.LogType = strings.ToUpper(c.LogType)
		}
		if sr.ExecMode&model.REPLAY != 0 {
			if len(c.FileList) == 0 || len(c.Conns) == 0 {
				return nil, errors.New("replay: filename and conn are need.")
			}

			for idx, conn := range c.Conns {
				strs := strings.Split(conn, ":")
				// 0	1		2	  3		4	5
				// type:user1:passwd1:ip1:port1:db1
				if len(strs) < 6 {
					return nil, errors.New(fmt.Sprintf("invalid conn string,conn %d [ip:%s,port:%s,db:%s,user:%s]", idx, strs[3], strs[4], strs[5], strs[1]))
				}
				p := connection.Param{
					User:   strs[1],
					Passwd: strs[2],
					Ip:     strs[3],
					Port:   strs[4],
					DB:     strs[5],
					Thread: c.Thread,
				}

				switch strings.ToUpper(strs[0]) {
				case "MYSQL":
					p.Type = connection.MYSQL
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

	//init consumers
	for i := range c.FileList {
		sr.taskchan <- i
	}
	close(sr.taskchan)

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

		//check if file exist
		for _, f := range sr.ParserFileList {
			if !utils.FileExists(f) {
				return nil, errors.New("file:" + f + " not exist.")
			}
		}
	}

	if sr.ExecMode&model.REPLAY != 0 {

		//check if file exist
		for _, f := range sr.ReplayFileList {
			if !utils.FileExists(f) {
				return nil, errors.New("file:" + f + " not exist.")
			}
		}
	}

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
				case taskIndex, ok := <-sr.taskchan:
					{
						if !ok {
							sr.logger.Sugar().Infof("worker %d exit.", index)
							return
						}

						//file parse pharse
						if sr.ExecMode&model.ANALYZE != 0 {
							if !sr.ParserFilStatus[taskIndex] {
								err := sr.analyze(taskIndex)

								if err != nil {

									if err.Error() == "STOP" {
										sr.logger.Sugar().Infof("recieve STOP signal,worker %d exit.", index)
									} else {
										sr.logger.Error(err.Error())
										return
									}
								} else {
									sr.logger.Sugar().Infof("finish parse %s %s", sr.LogType, sr.ParserFileList[taskIndex])
								}

							}
						}

						//replay pharse
						if sr.ExecMode&model.REPLAY != 0 {
							if !sr.ReplayFileStatus[taskIndex] {
								err := sr.replayRawSQL(taskIndex)
								if err != nil {
									if err.Error() == "STOP" {
										sr.logger.Sugar().Infof("recieve STOP signal,worker %d exit.", index)
									} else {
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
	} else {
		sr.Status = DONE

		if sr.GenerateReport {
			sr.generateRawSQLReport()
		}

		sr.logger.Info("task finished.")
	}

	sr.mutex.Unlock()

	sr.Close()

	sr.logger.Info("exit.")
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
func (sr *SQLReplayer) analyze(index int) error {

	file := sr.ParserFileList[index]
	pos := sr.ParserFilePos[index]

	l := sr.logger

	l.Sugar().Infof("begin to %s %s from pos %d", sr.ExecMode, file, pos)

	rawSQLFileDir := sr.Dir + "/rawsql_" + strconv.Itoa(index) + ".csv"

	rawSQLFile, err := os.OpenFile(rawSQLFileDir, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		sr.logger.Info(err.Error())
		return err
	}
	defer rawSQLFile.Close()

	csvWriter := csv.NewWriter(rawSQLFile)
	defer csvWriter.Flush()

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

		sr.mutex.Lock()
		_, ok := sr.SqlID2Fingerprint[cu.QueryID]
		if !ok {
			sr.SqlID2Fingerprint[cu.QueryID] = fingerprint
		}
		sr.mutex.Unlock()

		//save to raw sql file
		err := csvWriter.Write([]string{cu.Argument, cu.QueryID, cu.Time.Format("20060102 15:04:05"), cu.CommandType, cu.TableList, strconv.FormatFloat(cu.Elapsed*1000, 'f', 2, 64)})
		if err != nil {
			return err
		}

		if sr.GenerateReport {
			ru := replayUnit{Sqlid: cu.QueryID, Tablelist: cu.TableList, Sqltype: cu.CommandType, Time: uint64(cu.Elapsed * 1000)}

			if sr.SaveRawSQLInReport {
				ru.Sql = cu.Argument
			}
			sr.mutex.Lock()
			sr.SqlID2ReplayUints[cu.QueryID] = append(sr.SqlID2ReplayUints[cu.QueryID], ru)
			sr.mutex.Unlock()

		}
		return nil

	})

	sr.mutex.Lock()
	sr.ParserFilePos[index] = curPos
	if err == nil {
		sr.ParserFilStatus[index] = true
	}
	sr.mutex.Unlock()

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
		"p25(ms)", "p25-sql",
		"p50(ms)", "p50-sql",
		"p75(ms)", "p75-sql",
		"p90(ms)", "p90-sql",
		"p99(ms)", "p99-sql",
		"max(ms)", "max-sql",
		"avg(ms)", "execution"}
	header = append(header, subHeaders...)

	err = writer.Write(header)
	if err != nil {
		panic(err)
	}

	for sqlID, sqls := range sr.SqlID2ReplayUints {
		row := []string{sqlID, sr.SqlID2Fingerprint[sqlID], sqls[0].Sqltype, sqls[0].Tablelist}
		srMin, sr25, sr50, sr75, sr90, sr99, srMax, count, avg, _ := analyzer(sqls)
		row = append(row, []string{
			strconv.FormatUint(srMin.Time, 10), srMin.Sql,
			strconv.FormatUint(sr25.Time, 10), sr25.Sql,
			strconv.FormatUint(sr50.Time, 10), sr50.Sql,
			strconv.FormatUint(sr75.Time, 10), sr75.Sql,
			strconv.FormatUint(sr90.Time, 10), sr90.Sql,
			strconv.FormatUint(sr99.Time, 10), sr99.Sql,
			strconv.FormatUint(srMax.Time, 10), srMax.Sql,
			strconv.FormatFloat(avg, 'f', 2, 64), strconv.FormatUint(count, 10)}...)
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

func (sr *SQLReplayer) replayRawSQL(index int) error {

	//load raw sql from csv , and locate the postion from metadata
	sr.mutex.Lock()
	filePath := sr.ReplayFileList[index]
	curPos := sr.ParserFilePos[index]
	sr.mutex.Unlock()

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
	dbs, err := connection.InitConnections(sr.Conns)
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

	for {

		select {
		case <-sr.stopchan:
			for _, db := range dbs {
				db.Close()
			}
			return errors.New("STOP")
		default:
		}
		var sqlid, fingerprint string
		record, err := reader.Read()

		if err != nil {
			if errors.Is(err, io.EOF) {
				sr.logger.Sugar().Infoln("reach the end of file.")
				readFileDone <- struct{}{}
				break
			} else {
				sr.logger.Sugar().Infoln("failed to read next record.")
				return err
			}
		}

		//first column for raw sql
		//second column for sqlid
		sql := record[0]

		//check if sql is a SELECT statement
		if sr.QueryOnly {
			rets, err := utils.IsSelectStatement(sql)

			if err != nil {
				sr.logger.Sugar().Infof("failed to parse sql [%s],err [%s]. skip it.", sql, err.Error())
				continue
			}

			//not select statement,skip it
			if len(rets) == 0 || !rets[0] {
				continue
			}
		}

		//generate sqlid,table list for sql
		sqlid, fingerprint = utils.GetQueryID(sql)
		tablelist, _ := utils.ExtractTableNames(sql)

		wg.Add(1)
		sr.mutex.Lock()
		sr.ReplayRowCount++

		_, ok := sr.SqlID2Fingerprint[sqlid]
		if !ok {
			sr.SqlID2Fingerprint[sqlid] = fingerprint
		}

		_, ok = sr.QueryID2RelayStats[sqlid]
		if !ok {
			sr.QueryID2RelayStats[sqlid] = make([][]replayUnit, len(sr.Conns))
		}

		sr.mutex.Unlock()

		ch <- struct{}{}

		go func() {
			defer chanExit(ch)
			defer wg.Done()
			for i := 0; i < sr.Multiplier; i++ {

				for ind, db := range dbs {

					ru := replayUnit{}

					start := time.Now()

					_, err := db.Exec(sql)

					if err != nil {
						sr.logger.Sugar().Infof("error while executing sql. error:%s. \n sqlid:%s,%s", sqlid, sql, err.Error())
						continue
					}

					elapsed := time.Since(start)
					elapsedMilliseconds := elapsed.Milliseconds()

					ru.Time = uint64(elapsedMilliseconds)
					ru.Tablelist = tablelist
					if sr.SaveRawSQLInReport {
						ru.Sql = sql
					}
					sr.mutex.Lock()
					conns2ReplayStats := sr.QueryID2RelayStats[sqlid]
					conns2ReplayStats[ind] = append(conns2ReplayStats[ind], ru)
					sr.mutex.Unlock()
				}

			}
		}()

	}

	<-readFileDone
	wg.Wait()

	elapsed := time.Since(begin).Seconds()
	sr.logger.Sugar().Infof("sql replay finish for file,time elasped %fs.", sr.ReplayFileList[index], elapsed)
	return nil

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
	sr.mutex.Lock()
	defer sr.mutex.Unlock()

	if sr.ReplayRowCount > 0 && !sr.DryRun {

		writer := csv.NewWriter(sr.replayStatFile)
		defer writer.Flush()

		sr.logger.Sugar().Infof("begin to generate report for replay phrase. num of sqlid %d.", len(sr.SqlID2Fingerprint))

		header := []string{"sqlid", "fingerprint", "sqltype", "tablelist"}
		for i := 0; i < len(sr.Conns); i++ {
			prefix := fmt.Sprintf("conn_%d_", i)
			subHeaders := []string{prefix + "min(ms)", prefix + "min-sql",
				prefix + "p25(ms)", prefix + "p25-sql",
				prefix + "p50(ms)", prefix + "p50-sql",
				prefix + "p75(ms)", prefix + "p75-sql",
				prefix + "p90(ms)", prefix + "p90-sql",
				prefix + "p99(ms)", prefix + "p99-sql",
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
			if dbsToStats[0] == nil {
				continue
			}
			row := []string{sqlid, sr.SqlID2Fingerprint[sqlid], dbsToStats[0][0].Sqltype, dbsToStats[0][0].Tablelist}
			for i := 0; i < len(dbsToStats); i++ {
				srMin, sr25, sr50, sr75, sr90, sr99, srMax, count, avg, seqs := analyzer(dbsToStats[i])
				row = append(row, []string{
					strconv.FormatUint(srMin.Time, 10), srMin.Sql,
					strconv.FormatUint(sr25.Time, 10), sr25.Sql,
					strconv.FormatUint(sr50.Time, 10), sr50.Sql,
					strconv.FormatUint(sr75.Time, 10), sr75.Sql,
					strconv.FormatUint(sr90.Time, 10), sr90.Sql,
					strconv.FormatUint(sr99.Time, 10), sr99.Sql,
					strconv.FormatUint(srMax.Time, 10), srMax.Sql,
					strconv.FormatFloat(avg, 'f', 2, 64), strconv.FormatUint(count, 10)}...)

				if sr.DrawPic {

					fname := "Conn" + strconv.Itoa(i) + "_" + sqlid
					utils.Draw(fname, sr.Dir+"/"+fname, seqs, float64(sr25.Time), float64(sr50.Time), float64(sr75.Time), float64(sr90.Time))
					sr.logger.Sugar().Infoln("draw picture for sqlid:" + sqlid + " to " + sr.Dir + "/" + fname + ".png.")

				}

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