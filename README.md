[简体中文](./README.md)|[English](./README_EN.md)

# sqlreplayer是什么
sqlreplayer能从mysql的general log，slow log以及csv文件获取raw sql，并在多个支持mysql协议的数据库上回放，得到sql执行的统计分析报告。

analyze部分支持 mysql 5.6,5.7,8.0下的general log，slow log

# 为什么需要sqlreplayer

这个工具使用的初衷是需要比较业务sql在多个数据库下的性能差异，并生成简单比对结果。
主要涉及到两个部分，一个是sql采集，工具支持从mysql的全量日志、慢日志、csv文件进行raw sql的采集，汇总。另一个sql回放，sql回放支持在多个数据源对raw sql进行回放，并得到比对结果。

# 安装

sqlreplayer目录下:
go build 

# 三种模式

analyze：采集日志的raw sql，也可以对这部分raw sql按照sqlid进行聚合，给出建议统计报告  
replay：将指定的raw sql在多个数据源上回放，给出在多个数据源执行结果比对  
both：analyze和replay的结合  


## analyze 

analyze部分能够从mysql的全量日志，慢日志以及csv文件中获取raw sql，并以csv格式的文件输出

> ./sqlreplayer -exec analyze -f test_general_1.log -logtype genlog  
[analyze]2023/12/28 17:20:50 begin to read genlog test_general_1.log  
[analyze]2023/12/28 17:20:50 finish reading genlog test_general_1.log  
[analyze]2023/12/28 17:20:50 raw sql save to 20231228_172050_rawsql.csv  

抓取原始sql的时候，可以增加一些条件来筛选sql，如下面的命令能够抓取10点到10点半之间的慢查询

>./sqlreplayer -exec analyze -f slow_8.0.log -logtype slowlog -begin "2024-01-01 10:00:00" -end "2024-01-01 10:30:00"


分析原始sql的时候，对sql分布按照sqlid进行简单统计

>./sqlreplayer -exec analyze -f slow.log -logtype slowlog -generate-report  
[analyze]2024/01/15 11:03:26 begin to read slowlog slow.log  
[analyze]2024/01/15 11:03:26 finish reading slowlog slow.log  
[analyze]2024/01/15 11:03:26 raw sql save to 20240115_110326_rawsql.csv  
[analyze]2024/01/15 11:03:26 raw sql save to 20240115_110326_analyze_report.csv  

## replay 

replay对raw sql进行的回放，比如下面命令行讲raw sql在ip1:port1和ip2:port2两个数据源上进行回放，以此来比较性能差异

>./sqlreplayer -exec replay -f test.csv -conn  'user1:passwd1:ip1:port1:db1,user2:passwd2:ip2:port2:db2'  
[init]2023/12/28 16:57:02 conn 0 [user1:passwd1:ip1:port1:db1]  
[init]2023/12/28 16:57:02 conn 1 [user2:passwd2:ip2:port2:db2]  
[replay]2023/12/28 16:57:08 reach the end of log file.  
[replay]2023/12/28 16:57:14 sql replay finish ,num of raw sql 3,time elasped 12.573019s  
[replay]2023/12/28 16:57:14 save replay result to 20231228_173023_replay_stats.csv

下面是test.csv文件的内容。这个文件内容可以通过analyze阶段进行生成，也可以通过手动来维护需要进行回放的sql。
>"select 1,sleep(1)"  
"select 2,sleep(2)"  
"select 3,sleep(3)"  
"select 1"  
"select 2"  
"select 3"  



回放结果如下面表格所示。raw sql会按照sqlID进行聚合，并展示在多个数据源下的一些基本性能指标。用于比较sql在不同数据源下的性能差异。

| sqlid            | sqltype | conn_0_min(ms) | conn_0_min-sql | conn_0_p99(ms) | conn_0_p99-sql | conn_0_max(ms) | conn_0_max-sql | conn_0_avg(ms) | conn_0_execution | conn_1_min(ms) | conn_1_min-sql | conn_1_p99(ms) | conn_1_p99-sql | conn_1_max(ms) | conn_1_max-sql | conn_1_avg(ms) | conn_1_execution |
|------------------|---------|----------------|----------------|----------------|----------------|----------------|----------------|----------------|------------------|----------------|----------------|----------------|----------------|----------------|----------------|----------------|------------------|
| 16219655761820A2 |         | 44             | select 1       | 44             | select 2       | 45             | select 3       | 44.33          | 3                | 44             | select 2       | 44             | select 3       | 45             | select 1       | 44.33          | 3                |
| EE3DCDA8BEC5E966 |         | 1189           | select 1,sleep(1) | 2046           | select 2,sleep(2) | 3047           | select 3,sleep(3) | 2094.00        | 3                | 1186           | select 1,sleep(1) | 2046           | select 2,sleep(2) | 3048           | select 3,sleep(3) | 2093.33        | 3                |

replay相关的其他参数

>-m: 回放倍数，每个raw sql执行次数，默认是1  
-threads: 回放并发数，默认是1  
-select-only: 是否只回访select语句，默认是false
-charset: 默认为utf8mb4


## both

both模式是analyze和replay阶段结合，从日志采集到raw sql之后直接在配置的数据源下进行回放。


# 参数说明

  执行类型
  -exec string  
        exec type [analyze|replay|both]  
        analyze:generate raw sql from log file.  
        replay:replay raw sql in connections.  

## analyze

分析日志时候加入时间条件  
  -begin string  
        filter sql according to specified begin time from log,format 2023-01-01 13:01:01 (default "0000-01-01 00:00:00")  
  -end string  
        filter sql according to specified end time from log,format 2023-01-01 13:01:01 (default "9999-12-31 23:59:59")  

分析日志路径  
  -f string  
        filename  

日志格式  
  -logtype string  
        log type [genlog|slowlog|csv]  

生成raw sql的报告，按照sqlid进行汇总，主要是针对slowlog带有运行时间的日志格式，其他格式只能进行汇总，统计次数  
  -generate-report  
        generate report for analyze phrase  

生成报告是否保存raw sql信息，可以输出各个分位值的raw sql，对于raw sql数据量很大，打开这个选项会导致内存占用过多  
  -save-raw-sql  
        save raw sql in report  





## replay
连接charset  
 -charset string  
        charset of connection (default "utf8mb4")  

数据库连接  
 -conn string  
        mysql connection string,support multiple connections seperated by ',' which can be used for comparation,format   user1:passwd1:ip1:port1:db1[,user2:passwd2:ip2:port2:db2]  

回放文件  
  -f string  
        filename  

回放倍数  
  -m int  
        number of times a raw sql to be executed while replaying (default 1)  

只回访查询语句  
  -select-only  
        replay select statement only  

并发数  
  -threads int  
        thread num while replaying (default 1)  

replay报告是否保存raw sql信息，可以输出各个分位值的raw sql，对于raw sql数据量很大，打开这个选项会导致内存占用过多
  -save-raw-sql  
        save raw sql in report  

按照sqlid绘制raw sql响应时间散点图  
  -draw-pic  
        draw elasped picture for each sqlid  

![散点图实例](example/20240123_181642_Conn0_CA6E6CCC68F8018C.png)

回放阶段不统计信息
  -dry-run  
        replay raw sql without collecting any extra info  