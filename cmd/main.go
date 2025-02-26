package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/luijianfie/sqlreplayer"
	"github.com/luijianfie/sqlreplayer/logger"
	"github.com/luijianfie/sqlreplayer/model"

	"gopkg.in/yaml.v2"
)

const version = "2.0.2"

var (
	execType             string
	fileList             string
	logType              string
	beginStr             string
	endStr               string
	connStr              string
	charSet              string
	configPath           string
	threads              int
	multiplier           int
	replaySQLType        string
	replayFilter         string
	ifGenerateReport     bool
	ifSaveRawSQLInReport bool
	ifDrawPic            bool
	ifDryRun             bool
	verbose              bool
	dir                  string
	workNum              int
	metafile             string
)

func main() {

	cf := parseParam()

	if len(cf.Dir) == 0 {
		cf.Dir = "./"
	}

	cf.Sync = true
	cf.Logger = logger.LogInit()

	jobID, _ := strconv.Atoi(time.Now().Format("20060102150405"))

	if cf != nil {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

		sr, err := sqlreplayer.LanuchAnalyzeTask(uint64(jobID), cf)
		if err != nil {
			fmt.Println(err.Error())
			return
		}

		go func() {
			sig := <-sigChan
			fmt.Println()
			fmt.Printf("Received signal: %s\n", sig)
			sr.Stop()
		}()

		sr.Done()
	} else {
		fmt.Println("failed to generate config for task")
	}
}

func parseParam() *model.Config {

	flag.StringVar(&execType, "exec", "", "exec type [analyze|replay|all]\nanalyze:generate raw sql from log file.\nreplay:replay raw sql in connections.")
	flag.StringVar(&fileList, "filelist", "", "filename,multiple file seperated by ','")
	flag.StringVar(&logType, "logtype", "", "log type [genlog|slowlog|csv]")
	flag.StringVar(&beginStr, "begin", "0000-01-01 00:00:00", "filter sql according to specified begin time from log,format 2023-01-01 13:01:01")
	flag.StringVar(&endStr, "end", "9999-12-31 23:59:59", "filter sql according to specified end time from log,format 2023-01-01 13:01:01")
	flag.StringVar(&connStr, "conn", "", "mysql connection string,support multiple connections seperated by ',' which can be used for comparation,format type:user1:passwd1:ip1:port1:db1[,type:user2:passwd2:ip2:port2:db2]")
	flag.StringVar(&charSet, "charset", "utf8mb4", "charset of connection")
	flag.IntVar(&threads, "threads", 1, "thread num while replaying")
	flag.IntVar(&multiplier, "multi", 1, "number of times a raw sql to be executed while replaying")
	flag.IntVar(&workNum, "worker-num", 4, "number of worker while parsing or replaying multiple files")
	flag.StringVar(&replaySQLType, "sql-type", "query", "replay statement [query|dml|ddl|all], moer than one type can be specified by comma, for example query,ddl,default:query")
	flag.StringVar(&replayFilter, "sql-filter", "", "regular expression to filter sql")
	flag.BoolVar(&ifGenerateReport, "generate-report", false, "generate report for analyze phrase")
	flag.BoolVar(&ifSaveRawSQLInReport, "save-raw-sql", false, "save raw sql in report")
	flag.BoolVar(&ifDrawPic, "draw-pic", false, "draw elasped picture for each sqlid")
	flag.BoolVar(&ifDryRun, "dry-run", false, "replay raw sql without collecting any extra info")
	flag.BoolVar(&verbose, "v", false, "display version information")
	flag.StringVar(&configPath, "config", "", "Path to the configuration file")
	flag.StringVar(&dir, "output", "", "output path")
	flag.StringVar(&metafile, "metafile", "", "meta file from task which needed to resume")

	flag.Parse()

	if verbose {
		fmt.Printf("sqlreplayer version: %s\n", version)
		os.Exit(0)
	}

	// init para
	if len(configPath) > 0 {
		var cf model.Config
		fmt.Println("Using configuration file: " + configPath)

		data, err := os.ReadFile(configPath)
		if err != nil {
			fmt.Println(err.Error())
			return nil
		}
		err = yaml.Unmarshal(data, &cf)
		if err != nil {
			fmt.Println(err.Error())
			return nil
		}

		return &cf
	} else {

		fileNames := strings.Split(fileList, ",")
		connList := strings.Split(connStr, ",")

		return &model.Config{
			ExecType:           execType,
			FileList:           fileNames,
			LogType:            logType,
			Begin:              beginStr,
			End:                endStr,
			Conns:              connList,
			Charset:            charSet,
			Thread:             threads,
			Multi:              multiplier,
			ReplaySQLType:      replaySQLType,
			ReplayFilter:       replayFilter,
			SaveRawSQLInReport: ifSaveRawSQLInReport,
			GenerateReport:     ifGenerateReport,
			DrawPic:            ifDrawPic,
			DryRun:             ifDryRun,
			Dir:                dir,
			WorkerNum:          workNum,
			Metafile:           metafile,
		}
	}

}
