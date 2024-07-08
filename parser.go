package main

import (
	"bufio"
	"encoding/csv"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/percona/go-mysql/query"
)

type LogParser interface {
	Parser(io.Reader, func(cu *CommandUnit)) error
}

var (
	yearPrefix string = strconv.Itoa(time.Now().Year() / 100)
	//general log
	//ref:git@github.com:winebarrel/genlog.git
	reIgnore *regexp.Regexp = regexp.MustCompile(
		`^(?s)(?:` + strings.Join([]string{
			`\S+, Version: \S+ (.+). started with:.*`,
			`Tcp port: \d+  Unix socket: \S+.*`,
			`Time                 Id Command    Argument.*`,
		}, "|") + `)$`)
	reGenlog56 *regexp.Regexp = regexp.MustCompile(`(?s)^(\d{6}\s+\d{1,2}:\d{2}:\d{2}|\t)\s+(\d+)\s+([^\t]+)\t(.*)`)

	reGenlog57 *regexp.Regexp = regexp.MustCompile(`(?s)^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})\.\d{6}\+\d{2}:\d{2}\s+(\d+)\s+([^\t]+)\t(.*)`)

	slowlogReIgnore *regexp.Regexp = regexp.MustCompile(
		`^(?s)(?:` + strings.Join([]string{
			`\S+, Version: \S+ (.+). started with:.*`,
			`Tcp port: \d+  Unix socket: \S+.*`,
			`Time                 Id Command    Argument.*`,
		}, "|") + `)|^(?i)(use |set ).*$`)
	slowlogUserHostRe  *regexp.Regexp = regexp.MustCompile(`^(?s)(?:# User@Host: .*)`)
	slowlogTimeRe56    *regexp.Regexp = regexp.MustCompile(`(?s)^# Time: (\d{6}\s+\d{1,2}:\d{2}:\d{2})`)
	slowlogTimeRe      *regexp.Regexp = regexp.MustCompile(`(?s)^# Time: (\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})\.\d{6}\+\d{2}:\d{2}`)
	slowlogQuerytimeRe *regexp.Regexp = regexp.MustCompile(`(?s)^# Query_time: (\d+\.\d+).*`)
)

type CommandUnit struct {
	Time        time.Time
	ThreadID    string
	CommandType string
	Argument    string
	QueryID     string
	Elapsed     float64
}

type GeneralLogParser struct {
}

func (g *GeneralLogParser) Parser(fd io.Reader, handler func(cu *CommandUnit)) error {

	var (
		re          *regexp.Regexp
		cmdUnit     *CommandUnit
		err         error
		currentTime time.Time
		isEOF       bool
	)

	reader := bufio.NewReader(fd)

	for !isEOF {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				isEOF = true
			} else {
				logger.Println(err.Error())
				return err
			}
		}

		if reIgnore.MatchString(line) {
			continue
		}

		if re == nil {
			if reGenlog56.MatchString(line) {
				re = reGenlog56

			} else if reGenlog57.MatchString(line) {
				re = reGenlog57
			} else {
				continue
			}
		}

		if m := re.FindStringSubmatch(line); m != nil {

			// mysql5.6 don't save current time for each sql but only keep one timestamp every second
			if re == reGenlog56 {
				if m[1] != "\t" {
					currentTime, err = time.Parse("20060102 15:04:05", yearPrefix+m[1])

					//almost impossible
					if err != nil {
						return err
					}
				}
			} else {
				currentTime, err = time.Parse("2006-01-02T15:04:05", m[1])
				//almost impossible
				if err != nil {
					return err
				}
			}

			//match new sql,need to save last sql
			if cmdUnit != nil {
				handler(cmdUnit)
			}

			//create new command uint
			cmdUnit = &CommandUnit{Time: currentTime, ThreadID: m[2], CommandType: m[3], Argument: m[4]}

		} else if cmdUnit != nil {

			//unlikely that cmdUnit is nil
			cmdUnit.Argument += line
		}

		if isEOF {
			handler(cmdUnit)
		}

	}

	return err
}

type SlowlogParser struct {
}

func (s *SlowlogParser) Parser(fd io.Reader, handler func(cu *CommandUnit)) error {
	var (
		cmdUnit *CommandUnit
		err     error
		// currentTime time.Time
		isEOF       bool
		timeRe      *regexp.Regexp
		currentTime time.Time
		timeElasped float64
	)

	reader := bufio.NewReader(fd)

	for !isEOF {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				isEOF = true
			} else {
				logger.Println(err.Error())
				return err
			}
		}

		if slowlogReIgnore.MatchString(line) {
			continue
		}

		// if timeRe == nil {
		if slowlogTimeRe.MatchString(line) {
			timeRe = slowlogTimeRe
		} else if slowlogTimeRe56.MatchString(line) {
			timeRe = slowlogTimeRe56
		}
		// }

		if timeRe != nil {
			if m := timeRe.FindStringSubmatch(line); m != nil {

				// mysql5.6 don't save current time for each sql but only keep one timestamp every second
				if timeRe == slowlogTimeRe56 {
					currentTime, err = time.Parse("20060102 15:04:05", yearPrefix+m[1])
					//almost impossible
					if err != nil {
						return err
					}
				} else {
					currentTime, err = time.Parse("2006-01-02T15:04:05", m[1])
					//almost impossible
					if err != nil {
						return err
					}
				}
				continue
			}
		}

		if slowlogUserHostRe.MatchString(line) {
			if cmdUnit != nil {
				handler(cmdUnit)
				cmdUnit = nil
			}
			continue
		}

		if m := slowlogQuerytimeRe.FindStringSubmatch(line); m != nil {

			queryTime, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				logger.Println("Error converting string to float64:", err)
			}
			timeElasped = queryTime
			continue
		}

		if cmdUnit == nil {
			cmdUnit = &CommandUnit{Time: currentTime, Elapsed: timeElasped}
		}
		cmdUnit.Argument += line
	}

	if isEOF {
		handler(cmdUnit)
	}

	return err
}

type CSVParser struct {
}

func (c *CSVParser) Parser(fd io.Reader, handler func(cu *CommandUnit)) error {

	var (
		cmdUnit *CommandUnit
	)

	reader := csv.NewReader(fd)
	reader.LazyQuotes = true

	for {
		record, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				logger.Println("reach the end of log file.")
				break
			}
			return err
		}

		if record[0] == "" {
			continue
		}

		cmdUnit = &CommandUnit{Argument: record[0]}

		//if querid has been set, then use it , otherwise generate queryid for it
		if len(record) < 2 {
			cmdUnit.QueryID, _ = GetQueryID(record[0])
		} else {
			cmdUnit.QueryID = record[1]
		}

		handler(cmdUnit)

	}

	return nil
}

// GetQueryID returns a fingerprint and queryid of the given SQL statement.
func GetQueryID(sql string) (queryid string, fingerprint string) {
	fingerprint = query.Fingerprint(sql)
	queryid = query.Id(fingerprint)
	return queryid, fingerprint
}
