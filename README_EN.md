[简体中文](./README.md)|[English](./README_EN.md)

# What is sqlreplayer
sqlreplayer can extract raw SQL from MySQL's general log, slow log, and CSV files, and replay them on multiple databases that support the MySQL protocol, generating statistical analysis reports of SQL execution.

The analyze component supports MySQL 5.6, 5.7, 8.0 general log and slow log.

# Why sqlreplayer

sqlreplayer is a tool specifically designed for database performance evaluation and SQL analysis. It primarily addresses the needs in the following scenarios:

1. **SQL Pattern Analysis and Optimization**
   - Analyze SQL patterns from multiple log files (e.g., logs from sharded databases)
   - Extract SQL fingerprints and perform aggregation analysis to identify similar SQL statements
   - Generate detailed SQL statistics reports to help optimize database performance

2. **Database Traffic Analysis**
   - Real-time collection and analysis of database access patterns
   - Monitor SQL execution frequency and performance metrics
   - Identify potential performance bottlenecks

3. **Database Migration and Upgrade Assessment**
   - Evaluate SQL compatibility between different database versions
   - Compare and analyze performance differences
   - Provide detailed compatibility reports and performance comparison data

# Three Modes

analyze: Collect raw SQL from logs and optionally aggregate them by SQL ID to generate statistical reports  
replay: Replay specified raw SQL on multiple data sources and compare execution results  
both: Combination of analyze and replay modes  

## analyze

generate raw sql from general log,slow log or csv which can be used in sql replay, it will also genarate a brief sql report for these log.

<div align="center">
  <div style="max-width: 1000px; margin: 20px auto; padding: 20px; box-shadow: 0 0 10px rgba(0,0,0,0.1); border-radius: 8px; background-color: #fff;">
    <img src="example/analyze_report_p1.png" alt="analyze statistics" style="width: 100%; height: auto; display: block; margin: 0 auto;">
    <p style="margin-top: 15px; color: #666; font-size: 14px; font-style: italic;">
            SQL Analysis Report Example Part 1 - Shows SQL type distribution and table join count statistics
    </p>
  </div>
</div>

<div align="center">
  <div style="max-width: 1000px; margin: 20px auto; padding: 20px; box-shadow: 0 0 10px rgba(0,0,0,0.1); border-radius: 8px; background-color: #fff;">
    <img src="example/analyze_report_p2.png" alt="analyze statistics" style="width: 100%; height: auto; display: block; margin: 0 auto;">
    <p style="margin-top: 15px; color: #666; font-size: 14px; font-style: italic;">
            SQL Analysis Report Example Part 2 - Shows SQL types and execution counts
    </p>
  </div>
</div>

<div align="center">
  <div style="max-width: 1000px; margin: 20px auto;">
    <a href="https://luijianfie.github.io/sqlreplayer/example/rawsql_analyze_report.html" style="display: inline-block; padding: 12px 24px; background-color: #007bff; color: white; text-decoration: none; border-radius: 4px; font-weight: bold; transition: background-color 0.3s;">
      👉 View Complete Analysis Report
    </a>
  </div>
</div>

## replay

Replay performs raw SQL playback. For example, the following command line replays raw SQL on two data sources to compare performance differences and generates a replay report.

<div align="center">
  <div style="max-width: 1000px; margin: 20px auto; padding: 20px; box-shadow: 0 0 10px rgba(0,0,0,0.1); border-radius: 8px; background-color: #fff;">
    <img src="example/replay_report_p1.png" alt="replay statistics" style="width: 100%; height: auto; display: block; margin: 0 auto;">
    <p style="margin-top: 15px; color: #666; font-size: 14px; font-style: italic;">
            SQL Replay Report Example Part 1 - Shows basic SQL replay overview
    </p>
  </div>
</div>

<div align="center">
  <div style="max-width: 1000px; margin: 20px auto; padding: 20px; box-shadow: 0 0 10px rgba(0,0,0,0.1); border-radius: 8px; background-color: #fff;">
    <img src="example/replay_report_p2.png" alt="replay statistics" style="width: 100%; height: auto; display: block; margin: 0 auto;">
    <p style="margin-top: 15px; color: #666; font-size: 14px; font-style: italic;">
            SQL Replay Report Example Part 2 - Shows SQL replay details
    </p>
  </div>
</div>

<div align="center">
  <div style="max-width: 1000px; margin: 20px auto;">
    <a href="https://luijianfie.github.io/sqlreplayer/example/replay_stats.html" style="display: inline-block; padding: 12px 24px; background-color: #007bff; color: white; text-decoration: none; border-radius: 4px; font-weight: bold; transition: background-color 0.3s;">
      👉 View Complete Replay Report
    </a>
  </div>
</div>

## both

"both" is combination of "analyze" and "replay"





# Quick Start

cd cmd  
make  

## Analyze Demo

### Step 1: Run Analysis Command
```bash
./sqlreplayer -config config_analyze_demo.yaml
```

You will see output like this:
```
Using configuration file: config_analyze_demo.yaml  
2025-03-06T17:18:06.335 [info]  sqlreplayer/sqlreplayer.go:492  worker 3 start.  
2025-03-06T17:18:06.335 [info]  sqlreplayer/sqlreplayer.go:492  worker 1 start.  
2025-03-06T17:18:06.335 [info]  sqlreplayer/sqlreplayer.go:492  worker 2 start.  
2025-03-06T17:18:06.335 [info]  sqlreplayer/sqlreplayer.go:722  begin to analyze general_sample.log from pos 0  
2025-03-06T17:18:06.335 [info]  sqlreplayer/sqlreplayer.go:502  worker 2 exit.  
2025-03-06T17:18:06.335 [info]  sqlreplayer/sqlreplayer.go:492  worker 0 start.  
2025-03-06T17:18:06.335 [info]  sqlreplayer/sqlreplayer.go:502  worker 0 exit.  
2025-03-06T17:18:06.335 [info]  sqlreplayer/sqlreplayer.go:722  begin to analyze general_sample_2.log from pos 0  
2025-03-06T17:18:06.340 [info]  sqlreplayer/sqlreplayer.go:539  finish parse GENLOG general_sample_2.log  
2025-03-06T17:18:06.340 [info]  sqlreplayer/sqlreplayer.go:502  worker 1 exit.  
2025-03-06T17:18:06.340 [info]  sqlreplayer/sqlreplayer.go:539  finish parse GENLOG general_sample.log  
2025-03-06T17:18:06.340 [info]  sqlreplayer/sqlreplayer.go:502  worker 3 exit.  
2025-03-06T17:18:06.346 [info]  sqlreplayer/sqlreplayer.go:1484 Raw SQL report saved to ./test/sqlreplayer_task_20250306171806/rawsql_analyze_report.html  
2025-03-06T17:18:06.346 [info]  sqlreplayer/sqlreplayer.go:595  task finished.  
2025-03-06T17:18:06.346 [info]  sqlreplayer/sqlreplayer.go:606  Memory statistic   
2025-03-06T17:18:06.346 [info]  sqlreplayer/sqlreplayer.go:607  Allocated Memory: 1655 KB  
2025-03-06T17:18:06.346 [info]  sqlreplayer/sqlreplayer.go:608  Total Allocated Memory: 4488 KB  
2025-03-06T17:18:06.346 [info]  sqlreplayer/sqlreplayer.go:609  Heap Memory: 1655 KB  
2025-03-06T17:18:06.346 [info]  sqlreplayer/sqlreplayer.go:610  Heap Memory System: 7648 KB  
2025-03-06T17:18:06.346 [info]  sqlreplayer/sqlreplayer.go:611  MaxHeapAlloc: 3128 KB  
2025-03-06T17:18:06.346 [info]  sqlreplayer/sqlreplayer.go:613  exit.  
```

## Replay Demo

### Step 1: Configure Data Sources
Modify the data source configuration in the config file as follows:
```yaml
conns:
  - "mysql:test:test:10.10.218.57:3306:test"
  - "mysql:test:test:10.10.218.57:3306:test_2"
```

### Step 2: Initialize Database
Execute the initialization script `init.sql` in the database and confirm that the `users` table is created.

### Step 3: Run Replay Command
```bash
./sqlreplayer -config config_replay_demo.yaml
```

For more usage details, please refer to the parameter descriptions below.

# Parameter Description

  Execution Type
  -exec string  
        exec type [analyze|replay|both]  
        analyze: generate raw sql from log file.  
        replay: replay raw sql in connections.  

## analyze

Time conditions for log analysis:  
  -begin string  
        filter sql according to specified begin time from log, format 2023-01-01 13:01:01 (default "0000-01-01 00:00:00")  
  -end string  
        filter sql according to specified end time from log, format 2023-01-01 13:01:01 (default "9999-12-31 23:59:59")  

Log path:  
  -filelist string  
        filename, multiple files separated by ','  

Log format:  
  -logtype string  
        log type [genlog|slowlog|csv]  

Generate raw SQL report:  
  -generate-report  
        generate report for analyze phase  

Save raw SQL in report:  
  -save-raw-sql  
        save raw sql in report  

## replay

Connection charset:  
 -charset string  
        charset of connection (default "utf8mb4")  

Database connections:  
 -conn string  
        mysql connection string, support multiple connections separated by ',' which can be used for comparison, format user1:passwd1:ip1:port1:db1[,user2:passwd2:ip2:port2:db2]  

Replay files:  
  -filelist string  
        filename, multiple files separated by ','  

Replay multiplier:  
  -m int  
        number of times a raw sql to be executed while replaying (default 1)  

SQL mode:  
  -sql-mode
        replay statement [query|dml|ddl|all], more than one type can be specified by comma, for example query,ddl, default:query

Concurrency:  
  -threads int  
        thread num while replaying (default 1)  

Save raw SQL in replay report:  
  -save-raw-sql  
        save raw sql in report  

# Contact Me

If you have any questions or suggestions while using sqlreplayer, feel free to contact me through:

- 📱 **WeChat**: `418901779`
- 📧 **Email**: [luwenhaoterry@163.com](mailto:luwenhaoterry@163.com)
- 💬 **Issues**: Welcome to submit issues on [GitHub Issues](https://github.com/luijianfie/sqlreplayer/issues)
- 🤝 **Contributions**: Pull requests are welcome to improve this project

Your feedback is very important for improving sqlreplayer!
