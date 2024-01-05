package utils

import (
	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

type colX struct {
	colNames []string
}

func (v *colX) Enter(in ast.Node) (ast.Node, bool) {
	if name, ok := in.(*ast.ColumnName); ok {
		v.colNames = append(v.colNames, name.Name.O)
	}
	return in, false
}

func (v *colX) Leave(in ast.Node) (ast.Node, bool) {
	return in, true
}

type tableX struct {
	tableNames []string
}

func (v *tableX) Enter(in ast.Node) (ast.Node, bool) {
	if name, ok := in.(*ast.TableName); ok {
		v.tableNames = append(v.tableNames, name.Name.O)
	}
	return in, false
}

func (v *tableX) Leave(in ast.Node) (ast.Node, bool) {
	return in, true
}

type ParserResult struct {
	sql         string
	sqlType     string
	tableList   []string
	tableToCols map[string][]string
}

func SQLParser(sql string) (rets []ParserResult, err error) {
	p := parser.New()

	stmtNodes, _, err := p.ParseSQL(sql)
	if err != nil {
		return nil, err
	}

	for _, stmtNode := range stmtNodes {
		pr := ParserResult{}
		pr.tableToCols = make(map[string][]string)
		pr.sql = stmtNode.OriginalText()

		switch stmtNode.(type) {
		case *ast.CreateTableStmt, *ast.AlterTableStmt, *ast.DropTableStmt, *ast.TruncateTableStmt:
			pr.sqlType = "DDL"
		case *ast.SelectStmt, *ast.UpdateStmt, *ast.InsertStmt, *ast.DeleteStmt:
			pr.sqlType = "DML"
		default:
			pr.sqlType = "ELSE"
		}

		t := &tableX{}
		stmtNode.Accept(t)
		pr.tableList = t.tableNames

		switch pr.sqlType {
		case "DDL":
			c := &colX{}
			stmtNode.Accept(c)
			//almost impossible，ddl但是没有具体表名
			if len(pr.tableList) == 0 {
				continue
			}
			pr.tableToCols[pr.tableList[0]] = c.colNames
		}

		rets = append(rets, pr)

	}

	return rets, nil

}


func IsSelectStatement(sql string) (rets []bool, err error) {
	p := parser.New()

	stmtNodes, _, err := p.ParseSQL(sql)
	if err != nil {
		return nil, err
	}

	

	for _, stmtNode := range stmtNodes {
		pr := ParserResult{}
		pr.tableToCols = make(map[string][]string)
		pr.sql = stmtNode.OriginalText()

		switch stmtNode.(type) {

		case *ast.SelectStmt:
			rets=append(rets, true)
		default:
			rets=append(rets, false)
		}
	}

	return rets,nil 
}
