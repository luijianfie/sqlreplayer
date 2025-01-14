package parser

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/luijianfie/sqlreplayer/model"
)

type FileParser interface {
	Parser(filename string, pos int64, handler func(cu *model.CommandUnit) error) (int64, error)
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
