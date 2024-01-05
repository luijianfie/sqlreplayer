[简体中文](./README_CN.md)|[English](./README.md)

# sqlreplayer
sqlreplayer能从mysql的general log，slow log以及csv文件获取raw sql，并在多个支持mysql协议的数据库上回放，得到sql执行的统计分析报告。

analyze部分支持 mysql 5.6,5.7,8.0下的general log，slow log


# analyze 

analyze部分能够从mysql的全量日志，慢日志以及csv文件中获取raw sql，并以csv格式的文件输出

> ./sqlReplayer -exec analyze -f test_general_1.log -logtype genlog  
[analyze]2023/12/28 17:20:50 begin to read genlog test_general_1.log  
[analyze]2023/12/28 17:20:50 finish reading genlog test_general_1.log  
[analyze]2023/12/28 17:20:50 raw sql save to 20231228_172050_rawsql.csv  

抓取原始sql的时候，可以增加一些条件来筛选sql，如下面的命令能够抓取10点到10点半之间的慢查询

>./sqlReplayer -exec analyze -f slow_8.0.log -logtype slowlog -begin "2024-01-01 10:00:00" -end "2024-01-01 10:30:00"



# replay 

replay对raw sql进行的回放，比如下面命令行讲raw sql在ip1:port1和ip2:port2两个数据源上进行回放，以此来比较性能差异

>./sqlReplayer -exec replay -f test.csv -conn  'user1:passwd1:ip1:port1:db1,user2:passwd2:ip2:port2:db2'  
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


# both

both模式是analyze和replay阶段结合，从日志采集到raw sql之后直接在配置的数据源下进行回放。
