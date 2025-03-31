## 描述
根据金融行业监管要求写的一个失效链接排查扫描器！

## 最近更新
~~~
1.现在可以控制是否触发WAF
2.添加自定义header功能
3.增加IP封禁判断的容错
4.美化使界面更好看
~~~
## 使用方法
~~~ 
--help

  -c string
        Cookie for authentication
  -d int
        Depth of processing (default 3)
  -f int
        Function to execute (1 for default -失效链接) (default 1)
  -h value
        Headers in key=value format, comma separated
  -p int
        Number of concurrent processes (default 3)
  -thw int
        Trigger The Waf (1 for Trigger) (default 1)
  -u string
        URL to process
~~~
## 注意事项
API 扫描功能需自行改一下才能用，改了不少，懒得改回去了。
