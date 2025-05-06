package utils

import (
	"os"

	"github.com/luijianfie/sqlreplayer/model"
)

func FileExists(filename string) bool {
	_, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return err == nil
}

func SQLMapper(sql string, rules []model.MappingRule) string {
	for _, rule := range rules {

		sql = rule.Pattern.ReplaceAllStringFunc(sql, func(match string) string {
			submatches := rule.Pattern.FindStringSubmatch(match)
			if len(submatches) >= 2 {
				return rule.Replacement + "."
			}
			return match
		})
	}
	return sql
}
