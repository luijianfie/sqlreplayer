package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/luijianfie/sqlreplayer"
	"github.com/luijianfie/sqlreplayer/logger"
	"github.com/luijianfie/sqlreplayer/model"

	"gopkg.in/yaml.v2"
)

const version = "3.2.0"

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
	ifDryRun             bool
	verbose              bool
	dir                  string
	workNum              int
	metafile             string
	execTimeout          int
	skipCount            int
	skipSQLID            string
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

	flag.BoolVar(&verbose, "v", false, "display version information")
	flag.StringVar(&configPath, "config", "config.yaml", "Path to the configuration file")

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

		cf.SetDefaults()

		return &cf
	}

	return nil

}
