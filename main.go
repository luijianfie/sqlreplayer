package main

import (
	"database/sql"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/luijianfie/sqlreplayer/utils"
)

var (
	execType             string
	fileName             string
	logType              string
	beginStr             string
	endStr               string
	connStr              string
	dbs                  []*sql.DB
	charSet              string
	threads              int
	multiplier           int
	isSelectOnly         bool
	ifGenerateReport     bool
	ifSaveRawSQLInReport bool
	RawSQLCSVPath        string
	CurrentTimeStr       string = time.Now().Format("20060102_150405")

	sqlID2Fingerprint map[string]string = make(map[string]string)

	parser LogParser

	logger *log.Logger = log.New(os.Stdout, "[init]", log.LstdFlags)

	commandTypePattern string = "(?i)Prepare|Close|Quit"
)

type sqlReplay struct {
	sqlid   string
	sqltype string
	sql     string
	time    uint64
}

func main() {

	flag.StringVar(&execType, "exec", "", "exec type [analyze|replay|both]\nanalyze:generate raw sql from log file.\nreplay:replay raw sql in connections.")
	flag.StringVar(&fileName, "f", "", "filename")
	flag.StringVar(&logType, "logtype", "", "log type [genlog|slowlog|csv]")
	flag.StringVar(&beginStr, "begin", "0000-01-01 00:00:00", "filter sql according to specified begin time from log,format 2023-01-01 13:01:01")
	flag.StringVar(&endStr, "end", "9999-12-31 23:59:59", "filter sql according to specified end time from log,format 2023-01-01 13:01:01")
	flag.StringVar(&connStr, "conn", "", "mysql connection string,support multiple connections seperated by ',' which can be used for comparation,format user1:passwd1:ip1:port1:db1[,user2:passwd2:ip2:port2:db2]")
	flag.StringVar(&charSet, "charset", "utf8mb4", "charset of connection")
	flag.IntVar(&threads, "threads", 1, "thread num while replaying")
	flag.IntVar(&multiplier, "m", 1, "number of times a raw sql to be executed while replaying")
	flag.BoolVar(&isSelectOnly, "select-only", false, "replay select statement only")
	flag.BoolVar(&ifGenerateReport, "generate-report", false, "generate report for analyze phrase")
	flag.BoolVar(&ifSaveRawSQLInReport, "save-raw-sql", false, "save raw sql in report")
	flag.Parse()

	if flagParseNotValid() {
		return
	}

	//parse time
	begin, err := time.Parse("2006-01-02 15:04:05", beginStr)
	if err != nil {
		logger.Println(err)
	}

	end, err := time.Parse("2006-01-02 15:04:05", endStr)
	if err != nil {
		logger.Println(err)
		return
	}

	//read file
	file, err := os.Open(fileName)
	if err != nil {
		logger.Println(err.Error())
		return
	}
	defer file.Close()

	switch execType {
	case "analyze":
	case "replay", "both":
		//initialize database connections
		conns := strings.Split(connStr, ",")
		for idx, conn := range conns {
			params := strings.Split(conn, ":")

			if len(params) < 5 {
				logger.Printf("invalid conn string [user,password,ip,port,db]\n")
				logger.Println(params)
				return
			}
			db, err := initConnection(params[0], params[1], params[2], params[3], params[4], threads)
			if err != nil {
				logger.Println(err)
				return
			}
			defer db.Close()
			dbs = append(dbs, db)
			logger.Printf("conn %d [ip:%s,port:%s,db:%s,user:%s]\n", idx, params[2], params[3], params[4], params[0])
		}

		RawSQLCSVPath = fileName
	}

	logger = log.New(os.Stdout, "[analyze]", log.LstdFlags)

	//analyze file
	switch execType {
	case "analyze", "both":

		var fingerprint string
		sqlID2RawSQL := make(map[string][]sqlReplay)

		logger.Printf("begin to read %s %s\n", logType, fileName)
		RawSQLCSVPath = CurrentTimeStr + "_rawsql.csv"
		switch logType {

		case "genlog":
			parser = &GeneralLogParser{}
		case "slowlog":
			parser = &SlowlogParser{}
		case "csv":
			parser = &CSVParser{}
		}

		rawSQLFile, err := os.Create(RawSQLCSVPath)
		if err != nil {
			panic(err)
		}
		csvWriter := csv.NewWriter(rawSQLFile)

		//deal with command unit
		err = parser.Parser(file, func(cu *CommandUnit) {

			//filter
			commandTypeMatch, _ := regexp.MatchString(commandTypePattern, cu.CommandType)
			if commandTypeMatch || (cu.Time.After(end) || cu.Time.Before(begin)) {
				return
			}

			//generate query id
			cu.QueryID, fingerprint = GetQueryID(cu.Argument)

			_, ok := sqlID2Fingerprint[cu.QueryID]
			if !ok {
				sqlID2Fingerprint[cu.QueryID] = fingerprint
			}

			//save to raw sql file
			err := csvWriter.Write([]string{cu.Argument, cu.QueryID, cu.Time.Format("20060102 15:04:05"), cu.CommandType, strconv.FormatFloat(cu.Elapsed*1000, 'f', 2, 64)})
			if err != nil {
				panic(err)
			}

			if ifGenerateReport {
				sr := sqlReplay{sqlid: cu.QueryID, sqltype: cu.CommandType, time: uint64(cu.Elapsed * 1000)}

				if ifSaveRawSQLInReport {
					sr.sql = cu.Argument
				}

				sqlID2RawSQL[cu.QueryID] = append(sqlID2RawSQL[cu.QueryID], sr)
			}

		})

		if err != nil {
			logger.Println(err)
			return
		}

		csvWriter.Flush()
		rawSQLFile.Close()

		logger.Printf("finish reading %s %s\n", logType, fileName)
		logger.Printf("raw sql save to %s\n", RawSQLCSVPath)

		if ifGenerateReport {
			generateAnalyzeReport(sqlID2RawSQL)
		}

	case "replay":
		RawSQLCSVPath = fileName
	}

	if execType == "analyze" {
		return
	}
	logger = log.New(os.Stdout, "[replay]", log.LstdFlags)

	replayRawSQL(dbs, RawSQLCSVPath, threads, multiplier)

}

func flagParseNotValid() bool {

	switch execType {
	case "analyze":
		if len(fileName) == 0 || len(logType) == 0 {
			logger.Printf("analyze: filename and log type are need.\n")
			return true
		}

	case "replay":
		if len(fileName) == 0 || len(connStr) == 0 {
			logger.Printf("replay: filename and conn are need.\n")
			return true
		}
	case "both":
		if len(fileName) == 0 || len(logType) == 0 || len(connStr) == 0 {
			logger.Printf("both: filename,logtype,conn are need.\n")
			return true
		}

	case "":
		logger.Printf("-exec can't be empty")
		return true
	}

	return false
}

func initConnection(user, passwd, ip, port, dbName string, threads int) (*sql.DB, error) {

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s", user, passwd, ip, port, dbName)

	db, err := sql.Open("mysql", dsn)

	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(threads)
	db.SetMaxIdleConns(threads)
	return db, nil

}

func replayRawSQL(dbs []*sql.DB, filePath string, threads, multiplier int) {

	//load raw sql from csv
	file, err := os.Open(filePath)
	if err != nil {
		logger.Printf("fail to load csv file %s,err:%s", filePath, err.Error())
		return
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.LazyQuotes = true

	//create file to save stats info
	statsOutFile := CurrentTimeStr + "_replay_stats.csv"
	statsFile, err := os.Create(statsOutFile)
	if err != nil {
		logger.Printf(err.Error())
	}
	defer statsFile.Close()
	writer := csv.NewWriter(statsFile)
	defer writer.Flush()

	//begin time of stats
	begin := time.Now()

	//dealed row til now
	rowCount := 0

	go func() {

		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				logger.Printf("rowCount:%d", rowCount)
			}
		}

	}()

	mu := sync.Mutex{}
	ch := make(chan struct{}, threads)
	wg := sync.WaitGroup{}
	readFileDone := make(chan struct{}, 1)

	queryID2RelayStats := make(map[string][][]sqlReplay)

	for {

		var sqlid, fingerprint string
		record, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				logger.Println("reach the end of file.")
				readFileDone <- struct{}{}
				break
			} else {
				logger.Println("failed to read next record.")
				return
			}
		}

		//first column for raw sql
		//second column for sqlid
		sql := record[0]

		//check if sql is a SELECT statement
		if isSelectOnly {
			rets, err := utils.IsSelectStatement(sql)

			if err != nil || len(rets) == 0 {
				logger.Printf("failed to parse sql [%s],err [%s]. skip it.", sql, err.Error())
				continue
			}
			//not select statement,skip it
			if !rets[0] {
				continue
			}
		}

		//generate sqlid for sql if empty
		sqlid, fingerprint = GetQueryID(sql)

		wg.Add(1)
		rowCount++
		_, ok := sqlID2Fingerprint[sqlid]
		if !ok {
			sqlID2Fingerprint[sqlid] = fingerprint
		}

		mu.Lock()
		_, ok = queryID2RelayStats[sqlid]
		if !ok {
			queryID2RelayStats[sqlid] = make([][]sqlReplay, len(dbs))
		}
		mu.Unlock()

		ch <- struct{}{}

		go func() {
			defer chanExit(ch)
			defer wg.Done()
			for i := 0; i < multiplier; i++ {

				for ind, db := range dbs {

					sr := sqlReplay{}

					start := time.Now()

					_, err := db.Exec(sql)

					if err != nil {
						logger.Printf("error for sql:%s,error:%s\n", sql, err.Error())
						continue
					}

					elapsed := time.Since(start)
					elapsedMilliseconds := elapsed.Milliseconds()

					sr.time = uint64(elapsedMilliseconds)
					if ifSaveRawSQLInReport {
						sr.sql = sql
					}
					mu.Lock()

					conns2ReplayStats := queryID2RelayStats[sqlid]
					conns2ReplayStats[ind] = append(conns2ReplayStats[ind], sr)
					mu.Unlock()
				}

			}
		}()

	}

	<-readFileDone
	wg.Wait()

	elapsed := time.Since(begin).Seconds()
	logger.Printf("sql replay finish ,num of raw sql %d,time elasped %fs.\n", rowCount, elapsed)

	logger.Printf("begin to generate report for replay phrase. num of sqlid %d.\n", len(sqlID2Fingerprint))

	if rowCount > 0 {

		header := []string{"sqlid", "fingerprint", "sqltype"}
		for i := 0; i < len(dbs); i++ {
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
		err = writer.Write(header)
		if err != nil {
			panic(err)
		}

		for sqlid, dbsToStats := range queryID2RelayStats {

			//in some cases, failed to execute sql
			//dbsToStats[0] may be nil,skip this
			if dbsToStats[0] == nil {
				continue
			}
			row := []string{sqlid, sqlID2Fingerprint[sqlid], dbsToStats[0][0].sqltype}
			for i := 0; i < len(dbsToStats); i++ {
				srMin, sr25, sr50, sr75, sr90, sr99, srMax, count, avg := analyzer(dbsToStats[i])
				row = append(row, []string{
					strconv.FormatUint(srMin.time, 10), srMin.sql,
					strconv.FormatUint(sr25.time, 10), sr25.sql,
					strconv.FormatUint(sr50.time, 10), sr50.sql,
					strconv.FormatUint(sr75.time, 10), sr75.sql,
					strconv.FormatUint(sr90.time, 10), sr90.sql,
					strconv.FormatUint(sr99.time, 10), sr99.sql,
					strconv.FormatUint(srMax.time, 10), srMax.sql,
					strconv.FormatFloat(avg, 'f', 2, 64), strconv.FormatUint(count, 10)}...)

			}
			err = writer.Write(row)
			if err != nil {
				panic(err)
			}

		}
	}

	logger.Printf("save replay result to %s\n", statsOutFile)

}

func chanExit(c chan struct{}) {
	<-c
}

func analyzer(arr []sqlReplay) (min, p25, p50, p75, p90, p99, max sqlReplay, count uint64, average float64) {

	if len(arr) < 1 {
		return
	}

	sort.Slice(arr, func(i, j int) bool {
		return arr[i].time < arr[j].time
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

	sum := uint64(0)
	for _, sr := range arr {
		sum += sr.time
	}
	average = float64(sum) / float64(len(arr))
	count = uint64(len(arr))
	return
}

func generateAnalyzeReport(sqlID2sql map[string][]sqlReplay) {

	analyzeReportFile := CurrentTimeStr + "_analyze_report.csv"
	reportFile, err := os.Create(analyzeReportFile)
	if err != nil {
		logger.Printf(err.Error())
	}
	defer reportFile.Close()
	writer := csv.NewWriter(reportFile)
	defer writer.Flush()

	header := []string{"sqlid", "fingerprint", "sqltype"}
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

	for sqlID, sqls := range sqlID2sql {
		row := []string{sqlID, sqlID2Fingerprint[sqlID], sqls[0].sqltype}
		srMin, sr25, sr50, sr75, sr90, sr99, srMax, count, avg := analyzer(sqls)
		row = append(row, []string{
			strconv.FormatUint(srMin.time, 10), srMin.sql,
			strconv.FormatUint(sr25.time, 10), sr25.sql,
			strconv.FormatUint(sr50.time, 10), sr50.sql,
			strconv.FormatUint(sr75.time, 10), sr75.sql,
			strconv.FormatUint(sr90.time, 10), sr90.sql,
			strconv.FormatUint(sr99.time, 10), sr99.sql,
			strconv.FormatUint(srMax.time, 10), srMax.sql,
			strconv.FormatFloat(avg, 'f', 2, 64), strconv.FormatUint(count, 10)}...)
		err = writer.Write(row)
		if err != nil {
			panic(err)
		}
	}
	logger.Printf("raw sql save to %s\n", analyzeReportFile)
}
